package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/big"
	"os"
	"os/signal"
	"syscall"
	"time"

	"kiro-proxy/proxy"
	"kiro-proxy/rules"
	"kiro-proxy/store"
)

type appRunner struct {
	db      *store.DB
	server  *proxy.Server
	logFile *os.File
}

func startApp(configFile string, port int, dbPath string) (*appRunner, error) {
	logFile, err := os.OpenFile("kiro-proxy.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		log.SetOutput(io.MultiWriter(os.Stdout, logFile))
	}

	if err := ensureCA(); err != nil {
		log.Printf("警告: CA 证书生成失败: %v", err)
	}

	log.Printf("[main] BUILD: desktop-shell")
	cfg, err := rules.LoadConfig(configFile)
	if err != nil {
		log.Printf("警告: 无法加载配置文件 %s，使用默认配置: %v", configFile, err)
		cfg = rules.DefaultConfig()
	}
	if port != 0 {
		cfg.Proxy.Port = port
	}

	db, err := store.Open(dbPath)
	if err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return nil, fmt.Errorf("数据库初始化失败: %w", err)
	}

	var staticFS fs.FS = frontendFS
	srv, err := proxy.NewServer(cfg, db, staticFS)
	if err != nil {
		db.Close()
		if logFile != nil {
			logFile.Close()
		}
		return nil, fmt.Errorf("代理服务器初始化失败: %w", err)
	}
	runner := &appRunner{db: db, server: srv, logFile: logFile}

	fmt.Printf(`
╔══════════════════════════════════════════════╗
║            Kiro Proxy                        ║
╠══════════════════════════════════════════════╣
║  MITM 代理:  127.0.0.1:%-5d               ║
║  Web  UI  :  http://127.0.0.1:%-5d         ║
╚══════════════════════════════════════════════╝

Kiro 代理设置:
  HTTP_PROXY  = http://127.0.0.1:%d
  HTTPS_PROXY = http://127.0.0.1:%d

`, cfg.Proxy.Port, srv.UIPort(), cfg.Proxy.Port, cfg.Proxy.Port)

	go func() {
		if err := srv.Start(); err != nil {
			log.Printf("[main] 服务异常退出: %v", err)
			// 服务异常退出后尝试重启，避免窗口还在但端口服务消失
			for i := 1; i <= 5; i++ {
				time.Sleep(time.Duration(i) * 2 * time.Second)
				log.Printf("[main] 尝试重启服务 (第 %d 次)...", i)
				if err := srv.Start(); err != nil {
					log.Printf("[main] 重启失败: %v", err)
					continue
				}
				return
			}
			log.Printf("[main] 服务多次重启失败，放弃")
		}
	}()

	return runner, nil
}

func (r *appRunner) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", r.server.UIPort())
}

func (r *appRunner) Close() {
	if r.server != nil {
		r.server.Close()
	}
	if r.db != nil {
		if err := r.db.Close(); err != nil {
			log.Printf("[main] 数据库关闭失败: %v", err)
		}
	}
	if r.logFile != nil {
		r.logFile.Close()
	}
}

func waitForSignal() {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	fmt.Println("\n正在关闭...")
}

// ensureCA 如果 certs/ 目录下没有 CA 文件，自动生成一个
func ensureCA() error {
	if _, err := os.Stat("certs/ca.crt"); err == nil {
		return nil // 已存在
	}

	log.Println("[main] 首次运行，自动生成 CA 证书...")
	if err := os.MkdirAll("certs", 0755); err != nil {
		return err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName:   "Kiro Proxy CA",
			Organization: []string{"Kiro Proxy"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}

	certFile, err := os.Create("certs/ca.crt")
	if err != nil {
		return err
	}
	pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	certFile.Close()

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyFile, err := os.Create("certs/ca.key")
	if err != nil {
		return err
	}
	pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	keyFile.Close()

	log.Println("[main] ✓ CA 证书已生成: certs/ca.crt")
	log.Println("[main]   请在控制面板点击「安装 CA 证书」或手动安装到系统信任链")
	return nil
}
