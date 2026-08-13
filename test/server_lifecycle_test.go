package test

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"kiro-proxy/proxy"
	"kiro-proxy/rules"
	"kiro-proxy/store"
)

func TestServerPassThroughUsesForwardProxy(t *testing.T) {
	forwarded := make(chan string, 2)
	forwardProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded <- r.Method + " " + r.URL.String()
		if r.Method == http.MethodConnect {
			http.Error(w, "probe complete", http.StatusBadGateway)
			return
		}
		w.Header().Set("X-Forward-Proxy", "hit")
		_, _ = io.WriteString(w, "forwarded")
	}))
	defer forwardProxy.Close()

	proxyPort, reservation := occupyPortWithFreeSuccessor(t)
	_ = reservation.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "forward-proxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := rules.DefaultConfig()
	cfg.Proxy.Port = proxyPort
	cfg.Proxy.LogFile = ""
	cfg.Proxy.ForwardProxyURL = forwardProxy.URL
	server, err := proxy.NewServer(cfg, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	startResult := make(chan error, 1)
	go func() { startResult <- server.Start() }()
	defer func() {
		server.Close()
		if err := <-startResult; err != nil {
			t.Errorf("server stopped with error: %v", err)
		}
	}()
	waitForPort(t, proxyPort)

	proxyURL := "http://127.0.0.1:" + strconv.Itoa(proxyPort)
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) {
				return url.Parse(proxyURL)
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // 测试使用临时 MITM 证书。
		},
	}
	resp, err := client.Get("http://pass-through.test/probe")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("X-Forward-Proxy") != "hit" || string(body) != "forwarded" {
		t.Fatalf("request did not use forward proxy: status=%d body=%q", resp.StatusCode, body)
	}
	select {
	case target := <-forwarded:
		if !strings.Contains(target, "GET http://pass-through.test/probe") {
			t.Fatalf("unexpected forwarded target: %s", target)
		}
	case <-time.After(time.Second):
		t.Fatal("forward proxy did not receive request")
	}

	_, _ = client.Get("https://pass-through.test/probe")
	select {
	case target := <-forwarded:
		if target != "CONNECT //pass-through.test:443" {
			t.Fatalf("unexpected HTTPS forwarded target: %s", target)
		}
	case <-time.After(time.Second):
		t.Fatal("forward proxy did not receive HTTPS CONNECT")
	}
}

func TestServerStartReleasesUIListenerWhenProxyPortIsOccupied(t *testing.T) {
	proxyPort, occupied := occupyPortWithFreeSuccessor(t)
	defer occupied.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "server-lifecycle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := rules.DefaultConfig()
	cfg.Proxy.Port = proxyPort
	cfg.Proxy.LogFile = ""
	server, err := proxy.NewServer(cfg, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	if err := server.Start(); err == nil {
		t.Fatal("expected occupied proxy port to fail startup")
	}
	uiListener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(proxyPort+1))
	if err != nil {
		t.Fatalf("UI listener was left behind after startup failure: %v", err)
	}
	_ = uiListener.Close()
}

func TestServerConcurrentStartAndCloseReleasesBothListeners(t *testing.T) {
	proxyPort, reservation := occupyPortWithFreeSuccessor(t)
	_ = reservation.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "server-close-race.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := rules.DefaultConfig()
	cfg.Proxy.Port = proxyPort
	cfg.Proxy.LogFile = ""
	server, err := proxy.NewServer(cfg, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	startResult := make(chan error, 1)
	go func() {
		startResult <- server.Start()
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		proxyConn, proxyErr := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(proxyPort), 50*time.Millisecond)
		uiConn, uiErr := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(proxyPort+1), 50*time.Millisecond)
		if proxyErr == nil && uiErr == nil {
			_ = proxyConn.Close()
			_ = uiConn.Close()
			break
		}
		if proxyConn != nil {
			_ = proxyConn.Close()
		}
		if uiConn != nil {
			_ = uiConn.Close()
		}
		if time.Now().After(deadline) {
			server.Close()
			t.Fatal("server did not open both listeners")
		}
		time.Sleep(10 * time.Millisecond)
	}

	server.Close()
	select {
	case err := <-startResult:
		if err != nil {
			t.Fatalf("Start returned an error during normal close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after Close")
	}

	proxyListener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(proxyPort))
	if err != nil {
		t.Fatalf("proxy listener was not released: %v", err)
	}
	_ = proxyListener.Close()
	uiListener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(proxyPort+1))
	if err != nil {
		t.Fatalf("UI listener was not released: %v", err)
	}
	_ = uiListener.Close()
}

func occupyPortWithFreeSuccessor(t *testing.T) (int, net.Listener) {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		candidate, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := candidate.Addr().(*net.TCPAddr).Port
		_ = candidate.Close()
		if port >= 65535 {
			continue
		}
		successor, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port+1))
		if err != nil {
			continue
		}
		_ = successor.Close()
		occupied, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err == nil {
			return port, occupied
		}
	}
	t.Fatal("did not find two consecutive free ports")
	return 0, nil
}

func waitForPort(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("port %d did not become ready", port)
}
