package test

import (
	"bytes"
	"encoding/json"
	"io"
	"kiro-proxy/api"
	"kiro-proxy/router"
	"kiro-proxy/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUpstreamModelsOpenAIPreview(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer openai-secret" {
			t.Errorf("unexpected authorization header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"z-model"},{"id":"a-model"},{"id":"z-model"}]}`)
	}))
	defer upstream.Close()

	handler, closeDB := newUpstreamModelsAPI(t)
	defer closeDB()

	response := postUpstreamModels(t, handler, map[string]interface{}{
		"provider": "openai",
		"base_url": upstream.URL + "/v1",
		"api_key":  "openai-secret",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	assertUpstreamModelIDs(t, response.Body.Bytes(), []string{"a-model", "z-model"})
}

func TestUpstreamModelsAnthropicUsesSavedMaskedKeyAndPaginates(t *testing.T) {
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "anthropic-secret" {
			t.Errorf("unexpected api key: %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("unexpected anthropic version: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after_id") == "" {
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-z"}],"has_more":true,"last_id":"claude-z"}`)
			return
		}
		if got := r.URL.Query().Get("after_id"); got != "claude-z" {
			t.Errorf("unexpected after_id: %q", got)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-a"}],"has_more":false,"last_id":"claude-a"}`)
	}))
	defer upstream.Close()

	handler, db, closeDB := newUpstreamModelsAPIWithDB(t)
	defer closeDB()
	model := &store.Model{
		Name:     "Anthropic saved",
		ModelID:  "claude-existing",
		Provider: "anthropic",
		BaseURL:  upstream.URL,
		APIKey:   "anthropic-secret",
		Endpoint: "/messages",
		Enabled:  true,
	}
	if err := db.CreateModel(model); err != nil {
		t.Fatal(err)
	}

	response := postUpstreamModels(t, handler, map[string]interface{}{
		"source_id": model.ID,
		"provider":  "anthropic",
		"base_url":  upstream.URL,
		"api_key":   "anth****cret",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	if requests != 2 {
		t.Fatalf("expected two upstream requests, got %d", requests)
	}
	assertUpstreamModelIDs(t, response.Body.Bytes(), []string{"claude-a", "claude-z"})
}

func TestUpstreamModelsGeminiUsesQueryKeyAndPaginates(t *testing.T) {
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1beta/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("key"); got != "gemini-secret" {
			t.Errorf("unexpected Gemini key: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("pageToken") == "" {
			_, _ = io.WriteString(w, `{"models":[{"name":"models/gemini-z"}],"nextPageToken":"next"}`)
			return
		}
		_, _ = io.WriteString(w, `{"models":[{"name":"models/gemini-a"}]}`)
	}))
	defer upstream.Close()

	handler, closeDB := newUpstreamModelsAPI(t)
	defer closeDB()
	response := postUpstreamModels(t, handler, map[string]interface{}{
		"provider": "gemini",
		"base_url": upstream.URL,
		"api_key":  "gemini-secret",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	if requests != 2 {
		t.Fatalf("expected two upstream requests, got %d", requests)
	}
	assertUpstreamModelIDs(t, response.Body.Bytes(), []string{"gemini-a", "gemini-z"})
}

func TestUpstreamModelsDoesNotExposeUpstreamErrorBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"credential secret from upstream"}`)
	}))
	defer upstream.Close()

	handler, closeDB := newUpstreamModelsAPI(t)
	defer closeDB()
	response := postUpstreamModels(t, handler, map[string]interface{}{
		"provider": "openai",
		"base_url": upstream.URL,
		"api_key":  "openai-secret",
	})
	if response.Code != http.StatusBadGateway {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "credential secret") {
		t.Fatalf("upstream response body leaked: %s", response.Body.String())
	}
}

func newUpstreamModelsAPI(t *testing.T) (http.Handler, func()) {
	handler, _, closeDB := newUpstreamModelsAPIWithDB(t)
	return handler, closeDB
}

func newUpstreamModelsAPIWithDB(t *testing.T) (http.Handler, *store.DB, func()) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "upstream-models.db"))
	if err != nil {
		t.Fatal(err)
	}
	routes := router.New(db)
	server := api.New(db, routes, store.NewStats(db))
	return server.Handler(), db, func() { _ = db.Close() }
}

func postUpstreamModels(t *testing.T, handler http.Handler, payload map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/upstream-models", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertUpstreamModelIDs(t *testing.T, body []byte, expected []string) {
	t.Helper()
	var response struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(response.Models, expected) {
		t.Fatalf("unexpected models: got %v want %v", response.Models, expected)
	}
}
