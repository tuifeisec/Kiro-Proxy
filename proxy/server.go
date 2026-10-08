// server.go
// 代理服务器入口：初始化 goproxy、加载 CA、启动 MITM 代理和 Web UI

package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"kiro-proxy/api"
	"kiro-proxy/buildinfo"
	"kiro-proxy/codex"
	"kiro-proxy/license"
	"kiro-proxy/router"
	"kiro-proxy/rules"
	"kiro-proxy/store"
	"kiro-proxy/upstreamproxy"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Server 代理服务器（MITM + Web UI）
type Server struct {
	cfg                  *rules.Config
	proxy                *goproxy.ProxyHttpServer
	hub                  *Hub
	logger               *TrafficLogger
	router               *router.Router
	forwarder            *Forwarder
	stats                *store.Stats
	db                   *store.DB
	apiServer            *api.Server
	staticFS             fs.FS // 嵌入的前端静态文件
	uiServer             *http.Server
	mitmServer           *http.Server
	startMu              sync.Mutex
	serverMu             sync.Mutex
	uiRunning            atomic.Bool
	mitmRunning          atomic.Bool
	closing              atomic.Bool
	classifierMu         sync.RWMutex
	conversationModels   map[string]conversationModelEntry
	conversationPrunedAt time.Time
	lastMainModel        string
	licenseClient        licenseChecker
	responsesFallbackMu  sync.Mutex
	responsesFallback    map[string]fallbackResponseState
	closeOnce            sync.Once
}

// NewServer 创建并初始化代理服务器
// staticFS 传入嵌入的 frontend/dist 文件系统，nil 时回退到磁盘
func NewServer(cfg *rules.Config, db *store.DB, staticFS fs.FS) (*Server, error) {
	stats := store.NewStats(db)
	hub := NewHub()

	logOpts := LogOptions{DetailMode: cfg.Proxy.LogDetail, MaxDetailBytes: cfg.Proxy.LogMaxBody}
	tl, err := NewTrafficLogger(cfg.Proxy.LogFile, hub, logOpts)
	if err != nil {
		log.Printf("[server] 日志记录器初始化失败: %v，使用控制台输出", err)
		tl, _ = NewTrafficLogger("", hub, logOpts)
	}

	r := router.New(db)
	credentials := codex.NewCredentialManager(db)
	fwd := NewForwarder(stats, db, credentials)
	apiSrv := api.New(db, r, stats, credentials)
	apiSrv.SetModelHealthProvider(fwd)

	p := goproxy.NewProxyHttpServer()
	p.Verbose = cfg.Proxy.Verbose
	if rawProxyURL := strings.TrimSpace(cfg.Proxy.ForwardProxyURL); rawProxyURL != "" {
		forwardProxyURL, err := upstreamproxy.ParseProxyURL(rawProxyURL)
		if err != nil {
			return nil, fmt.Errorf("无效的放行流量出站代理地址 %q: %w", rawProxyURL, err)
		}
		p.Tr.Proxy = http.ProxyURL(forwardProxyURL)
		log.Printf("[server] 放行流量出站代理: %s://%s", forwardProxyURL.Scheme, forwardProxyURL.Host)
	}

	// 加载自定义 CA
	if err := loadCA(p, "certs/ca.crt", "certs/ca.key"); err != nil {
		log.Printf("[server] 警告: CA 加载失败 (%v)，使用内置默认 CA", err)
		log.Printf("[server]   → 请先运行: go run tools/genca/main.go")
	} else {
		log.Printf("[server] ✓ CA 已加载: certs/ca.crt")
	}

	// MITM 解密所有 HTTPS
	p.OnRequest().HandleConnect(goproxy.AlwaysMitm)

	srv := &Server{
		cfg:                cfg,
		proxy:              p,
		hub:                hub,
		logger:             tl,
		router:             r,
		forwarder:          fwd,
		stats:              stats,
		db:                 db,
		apiServer:          apiSrv,
		staticFS:           staticFS,
		conversationModels: make(map[string]conversationModelEntry),
		licenseClient:      license.NewClient(os.Getenv("KIRO_LICENSE_SERVER")),
		responsesFallback:  make(map[string]fallbackResponseState),
	}

	// 注册拦截器（在 interceptor.go 中实现）
	srv.setupInterceptor()
	srv.setupCaptureResponse()

	return srv, nil
}

// Start 启动两个服务：
//   - :Port      MITM 代理
//   - :Port+1    Web UI (HTTP + WebSocket)
func (s *Server) Start() error {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.closing.Load() {
		return nil
	}

	build := buildinfo.Current()
	log.Printf("[server] BUILD revision=%s time=%s go=%s os=%s arch=%s", build.Revision, build.Time, build.GoVersion, build.OS, build.Arch)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.handleLocalOpenAIChat)
	mux.HandleFunc("/v1/responses", s.handleLocalOpenAIResponses)
	mux.HandleFunc("/v1/messages", s.handleLocalAnthropicMessages)
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/api/ca-status", s.handleCAStatus)
	mux.HandleFunc("/api/install-ca", s.handleInstallCA)
	mux.HandleFunc("/api/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("/api/service/status", s.handleServiceStatus)
	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/api/auto-routing/history", s.handleAutoNodeHistory)
	mux.HandleFunc("/api/proxy-mode", s.handleProxyMode)
	mux.HandleFunc("/api/proxy-access-mode", s.handleProxyAccessMode)
	mux.HandleFunc("/api/log-detail", s.handleLogDetail)
	mux.HandleFunc("/api/kiro-proxy", s.handleKiroProxy)
	mux.HandleFunc("/api/kiro-restart", s.handleKiroRestart)
	// 直接注册 api 路由到同一个 mux，避免子 mux 路径匹配问题
	s.apiServer.RegisterTo(mux)

	// 静态文件：优先用嵌入的 FS，否则回退到磁盘
	var staticHandler http.Handler
	if s.staticFS != nil {
		sub, err := fs.Sub(s.staticFS, "frontend/dist")
		if err != nil {
			log.Printf("[server] 嵌入 FS 子目录失败: %v，回退到磁盘", err)
		} else {
			// 验证 index.html 存在
			if f, err := sub.Open("index.html"); err != nil {
				log.Printf("[server] 嵌入 FS 中找不到 index.html: %v，回退到磁盘", err)
			} else {
				f.Close()
				log.Printf("[server] ✓ 使用嵌入的前端文件")
				staticHandler = http.FileServer(http.FS(sub))
			}
		}
	}
	if staticHandler == nil {
		log.Printf("[server] 使用磁盘前端文件: frontend/dist")
		staticHandler = http.FileServer(http.Dir("frontend/dist"))
	}
	mux.Handle("/", staticHandler)

	bindHost := s.proxyBindHost()
	uiAddr := fmt.Sprintf("%s:%d", bindHost, s.UIPort())
	mitmAddr := fmt.Sprintf("%s:%d", bindHost, s.cfg.Proxy.Port)
	uiListener, err := net.Listen("tcp", uiAddr)
	if err != nil {
		return fmt.Errorf("Web UI 启动失败: %w", err)
	}
	mitmListener, err := net.Listen("tcp", mitmAddr)
	if err != nil {
		_ = uiListener.Close()
		return fmt.Errorf("MITM 代理启动失败: %w", err)
	}
	if s.closing.Load() {
		_ = uiListener.Close()
		_ = mitmListener.Close()
		return nil
	}

	uiServer := newHTTPServer(uiAddr, mux)
	mitmServer := newHTTPServer(mitmAddr, s.proxy)
	s.serverMu.Lock()
	if s.closing.Load() {
		s.serverMu.Unlock()
		_ = uiListener.Close()
		_ = mitmListener.Close()
		return nil
	}
	s.uiServer = uiServer
	s.mitmServer = mitmServer
	s.uiRunning.Store(true)
	s.mitmRunning.Store(true)
	s.serverMu.Unlock()

	type serveResult struct {
		name string
		err  error
	}
	resultCh := make(chan serveResult, 2)
	go func() {
		log.Printf("[server] Web UI: http://%s:%d", s.proxyDisplayHost(), s.UIPort())
		err := uiServer.Serve(uiListener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.uiRunning.Store(false)
		resultCh <- serveResult{name: "Web UI", err: err}
	}()
	go func() {
		log.Printf("[server] MITM 代理: http://%s:%d", s.proxyDisplayHost(), s.cfg.Proxy.Port)
		err := mitmServer.Serve(mitmListener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.mitmRunning.Store(false)
		resultCh <- serveResult{name: "MITM 代理", err: err}
	}()

	first := <-resultCh
	shutdownHTTPServers(uiServer, mitmServer)
	second := <-resultCh

	s.serverMu.Lock()
	if s.uiServer == uiServer {
		s.uiServer = nil
	}
	if s.mitmServer == mitmServer {
		s.mitmServer = nil
	}
	s.serverMu.Unlock()
	if s.closing.Load() {
		return nil
	}
	if first.err != nil {
		return fmt.Errorf("%s 异常退出: %w", first.name, first.err)
	}
	if second.err != nil {
		return fmt.Errorf("%s 异常退出: %w", second.name, second.err)
	}
	return fmt.Errorf("服务监听器意外停止")
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

func shutdownHTTPServers(servers ...*http.Server) {
	var wg sync.WaitGroup
	for _, server := range servers {
		if server == nil {
			continue
		}
		wg.Add(1)
		go func(server *http.Server) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := server.Shutdown(ctx); err != nil {
				_ = server.Close()
			}
		}(server)
	}
	wg.Wait()
}

// UIPort 返回 Web UI/API 监听端口。
func (s *Server) UIPort() int {
	return s.cfg.Proxy.Port + 1
}

// Close 释放资源，并关闭 MITM 与 Web UI HTTP server。
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.closing.Store(true)
		s.serverMu.Lock()
		uiServer := s.uiServer
		mitmServer := s.mitmServer
		s.serverMu.Unlock()
		s.hub.Close()
		shutdownHTTPServers(uiServer, mitmServer)
		s.forwarder.CloseIdleConnections()

		s.stats.Close()
		s.logger.Close()
	})
}

// handleWS 处理 WebSocket 升级
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.requireTrafficLogPermission(w) {
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[server] WS 升级失败: %v", err)
		return
	}
	s.hub.Register(conn)
	defer func() {
		s.hub.Unregister(conn)
		conn.Close()
	}()

	// 读循环：检测客户端断开
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}

// loadCA 将 PEM 格式的 CA 证书和私钥注入 goproxy
func loadCA(p *goproxy.ProxyHttpServer, certFile, keyFile string) error {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return err
	}

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("解析证书失败: %w", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("无法解析 PEM 块")
	}
	x509Cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("解析 x509 失败: %w", err)
	}

	tlsCert.Leaf = x509Cert
	goproxy.GoproxyCa = tlsCert
	return nil
}

// isKiroHost 判断是否是需要拦截的 Kiro 相关域名（用于流量日志过滤）
func isKiroHost(host string) bool {
	suffixes := []string{
		"codewhisperer.us-east-1.amazonaws.com",
		"codewhisperer.eu-central-1.amazonaws.com",
		"q.us-east-1.amazonaws.com",
		"q.eu-central-1.amazonaws.com",
		"kiro.dev",
	}
	for _, s := range suffixes {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}
