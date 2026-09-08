package proxy

import (
	"context"
	"errors"
	"io"
	"kiro-proxy/codex"
	"kiro-proxy/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestForwarderCodexAuthorizationUsesSharedCredentialManager(t *testing.T) {
	var refreshCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-from-manager" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acct" {
			t.Errorf("ChatGPT-Account-Id = %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	manager := codex.NewCredentialManagerWithRefresher(nil, func(context.Context, string, string) (*codex.TokenResponse, error) {
		refreshCalls.Add(1)
		return &codex.TokenResponse{AccessToken: "access-from-manager", RefreshToken: "refresh-rotated", ExpiresIn: 3600}, nil
	})
	forwarder := NewForwarder(nil, nil, manager)
	model := expiredCodexModelForIntegration()
	resp, err := forwarder.doRequest(context.Background(), upstream.URL, []byte(`{"model":"test"}`), &model, "session", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls.Load())
	}
	if model.APIKey != "old-access" {
		t.Fatalf("forwarder mutated route model API key: %q", model.APIKey)
	}
}

func expiredCodexModelForIntegration() store.Model {
	return store.Model{
		ID:                   "integration-model",
		ModelID:              "gpt-test",
		Provider:             "codex",
		Endpoint:             "/codex/responses",
		APIKey:               "old-access",
		RefreshToken:         "refresh-original",
		CodexAccountID:       "acct",
		AccessTokenExpiresAt: 1,
	}
}

func TestForwarderCodexChangedRouteDoesNotSendCredentials(t *testing.T) {
	for _, change := range []string{"provider", "base_url", "endpoint", "proxy", "account", "during_refresh", "deleted", "deleted_during_refresh"} {
		t.Run(change, func(t *testing.T) {
			var sent atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { sent.Add(1) }))
			defer upstream.Close()
			db, err := store.Open(filepath.Join(t.TempDir(), "route.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			model := expiredCodexModelForIntegration()
			model.BaseURL = upstream.URL
			if err := db.CreateModel(&model); err != nil {
				t.Fatal(err)
			}
			changed := model
			switch change {
			case "provider":
				changed.Provider = "openai"
			case "base_url", "during_refresh":
				changed.BaseURL = upstream.URL + "/another-target"
			case "endpoint":
				changed.Endpoint = "/another-endpoint"
			case "proxy":
				changed.ProxyURL = "http://127.0.0.1:1"
			case "account":
				changed.CodexAccountID = "other-account"
			}
			if change == "deleted" {
				if err := db.DeleteModel(model.ID); err != nil {
					t.Fatal(err)
				}
			} else if change != "during_refresh" && change != "deleted_during_refresh" {
				if err := db.UpdateModel(&changed); err != nil {
					t.Fatal(err)
				}
			}
			manager := codex.NewCredentialManagerWithRefresher(db, func(context.Context, string, string) (*codex.TokenResponse, error) {
				if change != "during_refresh" && change != "deleted_during_refresh" {
					t.Error("stale route attempted refresh")
				}
				var err error
				if change == "deleted_during_refresh" {
					err = db.DeleteModel(model.ID)
				} else {
					err = db.UpdateModel(&changed)
				}
				if err != nil {
					return nil, err
				}
				return &codex.TokenResponse{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 3600}, nil
			})
			forwarder := NewForwarder(nil, nil, manager)
			defer forwarder.CloseIdleConnections()
			resp, err := forwarder.doRequest(context.Background(), upstream.URL, []byte(`{"model":"test"}`), &model, "session", nil)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if !errors.Is(err, store.ErrCodexCredentialConflict) || sent.Load() != 0 {
				t.Fatalf("stale route sent credentials: sent=%d err=%v", sent.Load(), err)
			}
		})
	}
}
