package test

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"kiro-proxy/license"
	"kiro-proxy/proxy"
	"kiro-proxy/rules"
	"kiro-proxy/store"
)

func TestLocalCompatFallbackAffinityDoesNotBypassPrimaryGroup(t *testing.T) {
	licenseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(license.CheckResult{
			Authorized:   true,
			Registered:   true,
			VersionValid: true,
		})
	}))
	defer licenseServer.Close()
	t.Setenv("KIRO_LICENSE_SERVER", licenseServer.URL)

	var primaryCalls atomic.Int32
	primaryUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if primaryCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary upstream failure"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"primary"}}]}`))
	}))
	defer primaryUpstream.Close()

	var fallbackCalls atomic.Int32
	fallbackUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"fallback"}}]}`))
	}))
	defer fallbackUpstream.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "local-compat-group-stages.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	primary := createLocalCompatModel(t, db, "Primary", primaryUpstream.URL)
	fallback := createLocalCompatModel(t, db, "Fallback", fallbackUpstream.URL)
	fallbackGroup := &store.ModelGroup{
		Name: "Fallback Group", Enabled: true,
		Members: []store.ModelGroupMember{{ModelID: fallback.ID}},
	}
	if err := db.CreateModelGroup(fallbackGroup); err != nil {
		t.Fatal(err)
	}
	primaryGroup := &store.ModelGroup{
		Name: "Primary Group", Enabled: true, FallbackGroupID: fallbackGroup.ID,
		Members: []store.ModelGroupMember{{ModelID: primary.ID}},
	}
	if err := db.CreateModelGroup(primaryGroup); err != nil {
		t.Fatal(err)
	}

	proxyPort, reservation := occupyPortWithFreeSuccessor(t)
	_ = reservation.Close()
	cfg := rules.DefaultConfig()
	cfg.Proxy.Port = proxyPort
	cfg.Proxy.LogFile = ""
	server := proxy.NewServer(cfg, db, nil)
	startResult := make(chan error, 1)
	go func() { startResult <- server.Start() }()
	defer func() {
		server.Close()
		select {
		case err := <-startResult:
			if err != nil {
				t.Errorf("server stopped with error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	}()
	waitForTCPPort(t, proxyPort+1)

	endpoint := "http://127.0.0.1:" + strconv.Itoa(proxyPort+1) + "/v1/chat/completions"
	requestBody := []byte(`{"model":"Primary Group","messages":[{"role":"user","content":"hello"}]}`)
	for i := 0; i < 2; i++ {
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(requestBody))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Session-ID", "stable-session")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d returned %d", i+1, resp.StatusCode)
		}
	}

	if got := primaryCalls.Load(); got != 2 {
		t.Fatalf("primary calls = %d, want 2; fallback affinity bypassed the primary stage", got)
	}
	if got := fallbackCalls.Load(); got != 1 {
		t.Fatalf("fallback calls = %d, want 1", got)
	}
}

func createLocalCompatModel(t *testing.T, db *store.DB, name, baseURL string) *store.Model {
	t.Helper()
	model := &store.Model{
		Name: name, ModelID: name, Provider: "openai", BaseURL: baseURL,
		APIKey: "test-key", Endpoint: "/v1/chat/completions", Enabled: true,
	}
	if err := db.CreateModel(model); err != nil {
		t.Fatal(err)
	}
	return model
}

func waitForTCPPort(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("port %d did not open", port)
}
