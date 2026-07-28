package test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"kiro-proxy/proxy"
	"kiro-proxy/router"
	"kiro-proxy/store"
)

func TestModelGroupConcurrencyLimitAppliesToStandaloneRoute(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "model-group-global-concurrency.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	model := &store.Model{
		Name: "Limited Account", ModelID: "limited-account", Provider: "openai",
		BaseURL: upstream.URL, APIKey: "test-key", Endpoint: "/v1/responses", Enabled: true,
	}
	if err := db.CreateModel(model); err != nil {
		t.Fatal(err)
	}
	group := &store.ModelGroup{
		Name: "Limited Group", Enabled: true,
		Members: []store.ModelGroupMember{{ModelID: model.ID, MaxConcurrency: 1}},
	}
	if err := db.CreateModelGroup(group); err != nil {
		t.Fatal(err)
	}

	routes := router.New(db)
	standalone := routes.ResolveExact(model.Name)
	if standalone.CandidateLimits[model.ID] != 1 {
		t.Fatalf("standalone route lost global concurrency limit: %+v", standalone.CandidateLimits)
	}

	forwarder := proxy.NewForwarder(store.NewStats(db), db)
	logger, err := proxy.NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	request := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/responses", nil)
	}
	preflight := func() *proxy.Preflight {
		return &proxy.Preflight{Body: []byte(`{"model":"Limited Account","input":"hello"}`), Protocol: "responses"}
	}

	first, err := forwarder.Forward(request(), preflight(), routes.ResolveExact(model.Name), logger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := forwarder.Forward(request(), preflight(), routes.ResolveExact(model.Name), logger); err == nil {
		t.Fatal("expected the second standalone request to hit the shared concurrency limit")
	}

	_, _ = io.Copy(io.Discard, first.Body)
	if err := first.Body.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := forwarder.Forward(request(), preflight(), routes.ResolveExact(model.Name), logger)
	if err != nil {
		t.Fatalf("concurrency lease was not released: %v", err)
	}
	third.Body.Close()

	group.Enabled = false
	if err := db.UpdateModelGroup(group); err != nil {
		t.Fatal(err)
	}
	routes.Reload()
	standalone = routes.ResolveExact(model.Name)
	if standalone.CandidateLimits[model.ID] != 0 {
		t.Fatalf("disabled group must not constrain standalone requests: %+v", standalone.CandidateLimits)
	}
}
