package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"kiro-proxy/codex"
	"kiro-proxy/store"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func canonicalModel(url string) store.Model {
	return store.Model{ID: "canonical", Name: "before", ModelID: "test", Provider: "codex", BaseURL: url, Endpoint: "/codex/responses", APIKey: "old-access", RefreshToken: "old-refresh", CodexAccountID: "account", AccessTokenExpiresAt: time.Now().Add(time.Hour).Unix()}
}

func TestProviderCanonicalizationPrecedesCredentialBranches(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		status   int
	}{
		{"codex", " CoDeX ", 200}, {"codex", "CODEX", 200},
		{"codex", " OPENAI ", 400}, {"codex", "Anthropic", 400}, {"codex", " GEMINI ", 400},
		{"openai", " CODEX ", 400}, {"anthropic", "CoDeX", 400}, {"gemini", " codex ", 400},
	} {
		t.Run(tc.from+"-"+tc.to, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(w, `{"plan_type":"team"}`)
			}))
			defer upstream.Close()
			db, server := newCredentialAPITestServer(t, func(context.Context, string, string) (*codex.TokenResponse, error) {
				t.Error("unexpected refresh")
				return nil, fmt.Errorf("unexpected refresh")
			})
			model := canonicalModel(upstream.URL)
			model.Provider = tc.from
			if err := db.CreateModel(&model); err != nil {
				t.Fatal(err)
			}
			incoming := model
			incoming.Provider = tc.to
			incoming.APIKey = maskKey(model.APIKey)
			incoming.RefreshToken = maskKey(model.RefreshToken)
			status, _, apiErr := requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/"+model.ID, incoming)
			if status != tc.status {
				t.Fatalf("status=%d err=%v", status, apiErr)
			}
			saved, err := db.GetModel(model.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Provider != model.Provider || saved.APIKey != model.APIKey || saved.RefreshToken != model.RefreshToken {
				t.Fatal("provider normalization bypassed credential boundary")
			}
			if tc.status == 400 && calls.Load() != 0 {
				t.Fatal("rejected provider switch contacted upstream")
			}
			if tc.status == 200 && calls.Load() != 1 {
				t.Fatal("canonical Codex skipped Codex usage precheck")
			}
		})
	}
}

func TestWrappedSameRefreshRejectsChangedAccess(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	db, server := newCredentialAPITestServer(t, func(context.Context, string, string) (*codex.TokenResponse, error) {
		calls.Add(1)
		return nil, fmt.Errorf("must reject before refresh")
	})
	model := canonicalModel(upstream.URL)
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	incoming := model
	incoming.APIKey = maskKey(model.APIKey)
	incoming.RefreshToken = `{"refresh_token":"old-refresh","access_token":"another-access"}`
	status, _, _ := requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/"+model.ID, incoming)
	if status != 400 || calls.Load() != 0 {
		t.Fatalf("incomplete wrapped bundle accepted: status=%d calls=%d", status, calls.Load())
	}
	saved, err := db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.APIKey != model.APIKey || saved.RefreshToken != model.RefreshToken {
		t.Fatal("rejected bundle changed DB")
	}
}

func TestWrappedSameRefreshSharesAutomaticFlight(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseFirst := func() { once.Do(func() { close(release) }) }
	defer releaseFirst()
	var calls atomic.Int32
	db, server := newCredentialAPITestServer(t, func(context.Context, string, string) (*codex.TokenResponse, error) {
		if calls.Add(1) != 1 {
			return nil, fmt.Errorf("same token refreshed twice")
		}
		close(started)
		<-release
		return &codex.TokenResponse{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 3600}, nil
	})
	model := canonicalModel("http://127.0.0.1:1")
	model.AccessTokenExpiresAt = 1
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	autoDone := make(chan error, 1)
	go func() { _, err := server.credentials.Ensure(context.Background(), model); autoDone <- err }()
	<-started
	manual := model
	manual.RefreshToken = `{ "refresh_token": "old-refresh" }`
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := server.refreshAndFetchCodexUsage(ctx, &manual, true, &model)
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatalf("manual did not join same-generation flight: %v calls=%d", err, calls.Load())
	}
	releaseFirst()
	if err := <-autoDone; err != nil {
		t.Fatal(err)
	}
}

func TestWrappedSameRefreshLateImportReadsNewGenerationAndPreservesConfig(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"plan_type":"team"}`) }))
	defer upstream.Close()
	var consumed []string
	db, server := newCredentialAPITestServer(t, func(_ context.Context, refresh, _ string) (*codex.TokenResponse, error) {
		consumed = append(consumed, refresh)
		n := len(consumed)
		return &codex.TokenResponse{AccessToken: fmt.Sprintf("access-%d", n), RefreshToken: fmt.Sprintf("refresh-%d", n), ExpiresIn: 3600}, nil
	})
	model := canonicalModel(upstream.URL)
	model.AccessTokenExpiresAt = 1
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	if _, err := server.credentials.Ensure(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	manual := model
	manual.Name = "edited"
	manual.RefreshToken = `{"refresh_token":"old-refresh"}`
	if err := server.refreshAndFetchCodexUsage(context.Background(), &manual, true, &model); err != nil {
		t.Fatal(err)
	}
	if len(consumed) != 2 || consumed[0] != "old-refresh" || consumed[1] != "refresh-1" || manual.Name != "edited" || manual.RefreshToken != "refresh-2" {
		t.Fatalf("late import used stale token/config: consumed=%v name=%q", consumed, manual.Name)
	}
}

func TestPutMissingModelReturnsNotFoundBeforeCredentialIO(t *testing.T) {
	var calls atomic.Int32
	db, server := newCredentialAPITestServer(t, func(context.Context, string, string) (*codex.TokenResponse, error) {
		calls.Add(1)
		return nil, fmt.Errorf("unexpected")
	})
	_ = db
	status, _, _ := requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/missing", canonicalModel("http://127.0.0.1:1"))
	if status != 404 || calls.Load() != 0 {
		t.Fatalf("missing PUT did IO: status=%d calls=%d", status, calls.Load())
	}
}

func TestLegacyProviderCorrectionRequiresExplicitCredential(t *testing.T) {
	for _, legacy := range []string{"CoDeX", " codex "} {
		t.Run(legacy, func(t *testing.T) {
			var requests, refreshes atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "Bearer corrected-access" {
					t.Error("legacy key reused for corrected provider")
				}
				_, _ = io.WriteString(w, `{"plan_type":"team"}`)
			}))
			defer upstream.Close()
			db, server := newCredentialAPITestServer(t, func(_ context.Context, refresh, _ string) (*codex.TokenResponse, error) {
				refreshes.Add(1)
				if refresh != "explicit-new-refresh" {
					t.Errorf("unexpected refresh credential %q", refresh)
				}
				return &codex.TokenResponse{AccessToken: "corrected-access", RefreshToken: "corrected-refresh", ExpiresIn: 3600}, nil
			})
			model := canonicalModel(upstream.URL)
			model.Provider = legacy
			if err := db.CreateModel(&model); err != nil {
				t.Fatal(err)
			}
			incoming := model
			incoming.Provider = " CODEX "
			incoming.APIKey, incoming.RefreshToken = maskKey(model.APIKey), maskKey(model.RefreshToken)
			status, _, _ := requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/"+model.ID, incoming)
			if status != 400 || requests.Load() != 0 || refreshes.Load() != 0 {
				t.Fatalf("masked legacy correction did IO: status=%d", status)
			}
			saved, err := db.GetModel(model.ID)
			if err != nil || saved.Provider != legacy || saved.APIKey != model.APIKey {
				t.Fatalf("rejected correction changed DB: %v", err)
			}
			incoming.RefreshToken = "explicit-new-refresh"
			status, _, apiErr := requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/"+model.ID, incoming)
			if status != 200 {
				t.Fatalf("explicit correction failed: %d %v", status, apiErr)
			}
			saved, err = db.GetModel(model.ID)
			if err != nil || saved.Provider != "codex" || saved.APIKey != "corrected-access" || saved.RefreshToken != "corrected-refresh" || saved.CodexPlanType != "team" {
				t.Fatalf("correction not persisted: %v", err)
			}
			if requests.Load() != 1 || refreshes.Load() != 1 {
				t.Fatal("correction did not use one refresh and usage request")
			}
		})
	}
}
