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
	"kiro-proxy/license"
	"kiro-proxy/router"
	"kiro-proxy/rules"
	"kiro-proxy/store"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Server 代理服务器（MITM + Web UI）
type Server struct {
	cfg                *rules.Config
	proxy              *goproxy.ProxyHttpServer
	hub                *Hub
	logger             *TrafficLogger
	router             *router.Router
	forwarder          *Forwarder
	stats              *store.Stats
	db                 *store.DB
	apiServer          *api.Server
	staticFS           fs.FS // 嵌入的前端静态文件
	uiServer           *http.Server
	mitmServer         *http.Server
	classifierMu       sync.RWMutex
	conversationModels map[string]string
	lastMainModel      string
	licenseClient      *license.Client
	closeOnce          sync.Once
}

// NewServer 创建并初始化代理服务器
// staticFS 传入嵌入的 frontend/dist 文件系统，nil 时回退到磁盘
func NewServer(cfg *rules.Config, db *store.DB, staticFS fs.FS) *Server {
	stats := store.NewStats(db)
	hub := NewHub()

	logOpts := LogOptions{DetailMode: cfg.Proxy.LogDetail, MaxDetailBytes: cfg.Proxy.LogMaxBody}
	tl, err := NewTrafficLogger(cfg.Proxy.LogFile, hub, logOpts)
	if err != nil {
		log.Printf("[server] 日志记录器初始化失败: %v，使用控制台输出", err)
		tl, _ = NewTrafficLogger("", hub, logOpts)
	}

	r := router.New(db)
	fwd := NewForwarder(stats, db)
	apiSrv := api.New(db, r, stats)

	p := goproxy.NewProxyHttpServer()
	p.Verbose = cfg.Proxy.Verbose

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
		conversationModels: make(map[string]string),
		licenseClient:      license.NewClient(os.Getenv("KIRO_LICENSE_SERVER")),
	}

	// 注册拦截器（在 interceptor.go 中实现）
	srv.setupInterceptor()
	srv.setupCaptureResponse()

	return srv
}

// Start 启动两个服务：
//   - :Port      MITM 代理
//   - :Port+1    Web UI (HTTP + WebSocket)
func (s *Server) Start() error {
	log.Printf("[server] BUILD VERSION: 2026-05-30-v10-clean")

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

	uiAddr := fmt.Sprintf(":%d", s.UIPort())
	mitmAddr := fmt.Sprintf(":%d", s.cfg.Proxy.Port)
	s.uiServer = &http.Server{Addr: uiAddr, Handler: mux}
	s.mitmServer = &http.Server{Addr: mitmAddr, Handler: s.proxy}

	errCh := make(chan error, 2)
	go func() {
		log.Printf("[server] Web UI: http://127.0.0.1%s", uiAddr)
		if err := s.uiServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("Web UI 启动失败: %w", err)
		}
	}()

	go func() {
		log.Printf("[server] MITM 代理: http://127.0.0.1%s", mitmAddr)
		if err := s.mitmServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("MITM 代理启动失败: %w", err)
		}
	}()

	return <-errCh
}

// UIPort 返回 Web UI/API 监听端口。
func (s *Server) UIPort() int {
	return s.cfg.Proxy.Port + 1
}

// Close 释放资源，并关闭 MITM 与 Web UI HTTP server。
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		if s.uiServer != nil {
			if err := s.uiServer.Shutdown(ctx); err != nil {
				log.Printf("[server] Web UI 关闭失败: %v", err)
			}
		}
		if s.mitmServer != nil {
			if err := s.mitmServer.Shutdown(ctx); err != nil {
				log.Printf("[server] MITM 代理关闭失败: %v", err)
			}
		}

		s.stats.Flush() // 退出前持久化统计
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

	// 定时 ping，检测连接是否还活着（窗口关闭后 write 会失败）
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.hub.writeMu.Lock()
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				err := conn.WriteMessage(websocket.PingMessage, nil)
				s.hub.writeMu.Unlock()
				if err != nil {
					conn.Close()
					return
				}
			case <-done:
				return
			}
		}
	}()

	// 读循环：检测客户端断开
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
	close(done)
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
