package test

import (
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"kiro-proxy/proxy"
	"kiro-proxy/rules"
	"kiro-proxy/store"
)

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
	server := proxy.NewServer(cfg, db, nil)
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
	server := proxy.NewServer(cfg, db, nil)
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
