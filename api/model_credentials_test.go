package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"kiro-proxy/codex"
	"kiro-proxy/router"
	"kiro-proxy/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newCredentialAPITestServer(t *testing.T, refresher codex.TokenRefresher) (*store.DB, *Server) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	stats := store.NewStats(db)
	manager := codex.NewCredentialManagerWithRefresher(db, refresher)
	server := New(db, router.New(db), stats, manager)
	t.Cleanup(func() {
		stats.Close()
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db, server
}

func requestModelJSON(t *testing.T, handler http.Handler, method, path string, body interface{}) (int, store.Model, map[string]string) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	var model store.Model
	var apiError map[string]string
	if recorder.Code >= 200 && recorder.Code < 300 {
		if err := json.NewDecoder(recorder.Body).Decode(&model); err != nil {
			t.Fatalf("decode model response: %v; body=%s", err, recorder.Body.String())
		}
	} else if err := json.NewDecoder(recorder.Body).Decode(&apiError); err != nil {
		t.Fatalf("decode error response: %v; body=%s", err, recorder.Body.String())
	}
	return recorder.Code, model, apiError
}

func TestPutCodexModelPersistsConfigAndUsesExistingCredentialSnapshot(t *testing.T) {
	var existingUsageRequests atomic.Int32
	var incomingBaseURLRequests atomic.Int32
	usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/usage" {
			t.Errorf("usage server received unexpected path %q", r.URL.Path)
		}
		existingUsageRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"plan_type":"pro","email":"usage@example.test","remaining":42}`)
	}))
	defer usageServer.Close()
	newBaseURLServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		incomingBaseURLRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer newBaseURLServer.Close()

	db, server := newCredentialAPITestServer(t, func(context.Context, string, string) (*codex.TokenResponse, error) {
		t.Fatal("ordinary PUT unexpectedly refreshed the existing credential")
		return nil, nil
	})

	stored := store.Model{
		ID:                   "codex-config",
		Name:                 "old-name",
		ModelID:              "old-model",
		Provider:             "codex",
		BaseURL:              usageServer.URL,
		APIKey:               "access-db-67890",
		RefreshToken:         "refresh-db-67890",
		Endpoint:             "/codex/responses",
		ContextWindow:        4096,
		MaxTokens:            128,
		ReasoningEffort:      "low",
		ImageInputMode:       "auto",
		RateMultiplier:       1,
		InputPrice:           5,
		OutputPrice:          30,
		CacheInputPrice:      0.5,
		CacheOutputPrice:     30,
		CodexAccountID:       "account-1",
		AccessTokenExpiresAt: time.Now().Add(time.Hour).Unix(),
		CodexUsageJSON:       `{"old":true}`,
		Enabled:              true,
	}
	if err := db.CreateModel(&stored); err != nil {
		t.Fatal(err)
	}

	incoming := stored
	incoming.APIKey = maskKey(stored.APIKey)
	incoming.RefreshToken = maskKey(stored.RefreshToken)
	incoming.Name = "new-name"
	incoming.ModelID = "new-model"
	incoming.BaseURL = newBaseURLServer.URL
	incoming.Endpoint = "/v2/responses"
	incoming.ProxyURL = "http://proxy.example.test:8080"
	incoming.DisableAutoSuffix = true
	incoming.ContextWindow = 16384
	incoming.MaxTokens = 512
	incoming.ReasoningEffort = "high"
	incoming.ImageInputMode = "supported"
	incoming.RateMultiplier = 1.25
	incoming.InputPrice = 7
	incoming.OutputPrice = 35
	incoming.CacheInputPrice = 0.7
	incoming.CacheOutputPrice = 31
	incoming.EnableContinuation = true
	incoming.ServiceTier = "fast"
	incoming.Enabled = false
	incoming.SortOrder = 7
	incoming.Note = "updated configuration"

	status, response, apiError := requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/"+stored.ID, incoming)
	if status != http.StatusOK {
		t.Fatalf("PUT status=%d error=%v response=%#v", status, apiError, response)
	}
	if got := existingUsageRequests.Load(); got != 1 {
		t.Fatalf("existing BaseURL usage requests=%d, want 1", got)
	}
	if got := incomingBaseURLRequests.Load(); got != 0 {
		t.Fatalf("incoming BaseURL usage requests=%d, want 0", got)
	}

	saved, err := db.GetModel(stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name != incoming.Name || saved.ModelID != incoming.ModelID || saved.BaseURL != incoming.BaseURL || saved.Endpoint != incoming.Endpoint {
		t.Fatalf("PUT config was not persisted: %#v", saved)
	}
	if saved.ProxyURL != incoming.ProxyURL || saved.DisableAutoSuffix != incoming.DisableAutoSuffix || saved.ContextWindow != incoming.ContextWindow || saved.MaxTokens != incoming.MaxTokens || saved.ReasoningEffort != incoming.ReasoningEffort || saved.ImageInputMode != incoming.ImageInputMode || saved.EnableContinuation != incoming.EnableContinuation || saved.ServiceTier != incoming.ServiceTier || saved.Enabled != incoming.Enabled || saved.SortOrder != incoming.SortOrder || saved.Note != incoming.Note {
		t.Fatalf("PUT secondary config was not persisted: %#v", saved)
	}
	if saved.RateMultiplier != incoming.RateMultiplier || saved.InputPrice != incoming.InputPrice || saved.OutputPrice != incoming.OutputPrice || saved.CacheInputPrice != incoming.CacheInputPrice || saved.CacheOutputPrice != incoming.CacheOutputPrice {
		t.Fatalf("PUT pricing config was not persisted: %#v", saved)
	}
	if saved.APIKey != stored.APIKey || saved.RefreshToken != stored.RefreshToken {
		t.Fatalf("PUT overwrote DB credential snapshot: got access=%q refresh=%q", saved.APIKey, saved.RefreshToken)
	}
	wantUsage := `{"plan_type":"pro","email":"usage@example.test","remaining":42}`
	if saved.CodexUsageJSON != wantUsage {
		t.Fatalf("usage was not persisted from existing BaseURL: got %q want %q", saved.CodexUsageJSON, wantUsage)
	}
	if saved.CodexPlanType != "pro" || saved.CodexEmail != "usage@example.test" || response.CodexPlanType != saved.CodexPlanType || response.CodexEmail != saved.CodexEmail {
		t.Fatalf("PUT plan/email disagree between API and DB: response=%#v saved=%#v", response, saved)
	}
	if response.APIKey != maskKey(stored.APIKey) || response.RefreshToken != maskKey(stored.RefreshToken) {
		t.Fatalf("PUT response credentials were not masked/preserved: %#v", response)
	}
}

func TestRefreshCodexModelPersistsRotatedTokensAndUsage(t *testing.T) {
	var usageRequests atomic.Int32
	usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/usage" {
			t.Errorf("usage server received unexpected path %q", r.URL.Path)
		}
		usageRequests.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer access-refresh-new" {
			t.Errorf("usage request authorization=%q, want refreshed token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"plan_type":"team","remaining":9}`)
	}))
	defer usageServer.Close()

	var refreshCalls atomic.Int32
	db, server := newCredentialAPITestServer(t, func(_ context.Context, refreshToken, _ string) (*codex.TokenResponse, error) {
		refreshCalls.Add(1)
		if refreshToken != "refresh-refresh-old" {
			t.Fatalf("refresher token=%q, want old DB token", refreshToken)
		}
		return &codex.TokenResponse{AccessToken: "access-refresh-new", RefreshToken: "refresh-refresh-new", ExpiresIn: 3600}, nil
	})
	model := store.Model{
		ID:                   "codex-refresh",
		Name:                 "refresh-model",
		ModelID:              "gpt-refresh",
		Provider:             "codex",
		BaseURL:              usageServer.URL,
		APIKey:               "access-refresh-old",
		RefreshToken:         "refresh-refresh-old",
		Endpoint:             "/codex/responses",
		CodexAccountID:       "account-refresh",
		AccessTokenExpiresAt: time.Now().Add(time.Hour).Unix(),
		Enabled:              true,
	}
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}

	status, response, apiError := requestModelJSON(t, server.Handler(), http.MethodPost, "/api/refresh-codex-model", map[string]string{"model_id": model.ID})
	if status != http.StatusOK {
		t.Fatalf("refresh status=%d error=%v response=%#v", status, apiError, response)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls=%d, want 1", refreshCalls.Load())
	}
	if usageRequests.Load() != 1 {
		t.Fatalf("usage requests=%d, want 1", usageRequests.Load())
	}
	saved, err := db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.APIKey != "access-refresh-new" || saved.RefreshToken != "refresh-refresh-new" {
		t.Fatalf("rotated tokens were not persisted: %#v", saved)
	}
	if saved.CodexUsageJSON != `{"plan_type":"team","remaining":9}` {
		t.Fatalf("refreshed usage was not persisted: %q", saved.CodexUsageJSON)
	}
	if saved.CodexPlanType != "team" || saved.CodexEmail != "" || response.CodexPlanType != saved.CodexPlanType || response.CodexEmail != saved.CodexEmail {
		t.Fatalf("refresh plan/email disagree between API and DB: response=%#v saved=%#v", response, saved)
	}
	if response.APIKey != maskKey(saved.APIKey) || response.RefreshToken != maskKey(saved.RefreshToken) {
		t.Fatalf("refresh response credentials were not masked: %#v", response)
	}
}

func TestPutCodexModelManualCredentialImportUsesIncomingCredentials(t *testing.T) {
	var authorization string
	var usageRequests atomic.Int32
	usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/usage" {
			t.Errorf("usage server received unexpected path %q", r.URL.Path)
		}
		authorization = r.Header.Get("Authorization")
		usageRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"plan_type":"plus","remaining":3}`)
	}))
	defer usageServer.Close()

	newAccess := testAccessToken("account-new", "", "new-plan")
	db, server := newCredentialAPITestServer(t, func(_ context.Context, refreshToken, _ string) (*codex.TokenResponse, error) {
		if refreshToken != "refresh-manual-new" {
			t.Fatalf("manual refresher token=%q, want new input token", refreshToken)
		}
		return &codex.TokenResponse{AccessToken: newAccess, RefreshToken: "refresh-manual-new", ExpiresIn: 3600}, nil
	})
	model := store.Model{
		ID:                   "codex-manual",
		Name:                 "manual-model",
		ModelID:              "gpt-manual",
		Provider:             "codex",
		BaseURL:              "https://old.example.test",
		APIKey:               "access-manual-old",
		RefreshToken:         "refresh-manual-old",
		Endpoint:             "/codex/responses",
		CodexAccountID:       "account-old",
		AccessTokenExpiresAt: time.Now().Add(time.Hour).Unix(),
		Enabled:              true,
	}
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	incoming := model
	incoming.Name = "manual-imported"
	incoming.BaseURL = usageServer.URL
	incoming.APIKey = "access-manual-new"
	incoming.RefreshToken = "refresh-manual-new"
	incoming.CodexAccountID = "account-new"
	incoming.CodexUsageJSON = `{"old":true}`

	status, _, apiError := requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/"+model.ID, incoming)
	if status != http.StatusOK {
		t.Fatalf("manual PUT status=%d error=%v", status, apiError)
	}
	if usageRequests.Load() != 1 || authorization != "Bearer "+newAccess {
		t.Fatalf("manual import did not use incoming access token: requests=%d authorization=%q", usageRequests.Load(), authorization)
	}
	saved, err := db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.APIKey != newAccess || saved.RefreshToken != incoming.RefreshToken || saved.CodexAccountID != "account-new" || saved.Name != incoming.Name || saved.BaseURL != incoming.BaseURL {
		t.Fatalf("manual incoming credential/config was not persisted: %#v", saved)
	}
	if saved.CodexUsageJSON != `{"plan_type":"plus","remaining":3}` {
		t.Fatalf("manual import usage was not persisted: %q", saved.CodexUsageJSON)
	}
}

func testAccessToken(accountID, email, plan string) string {
	claims, _ := json.Marshal(map[string]interface{}{
		"exp": time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]string{
			"chatgpt_account_id": accountID,
			"chatgpt_plan_type":  plan,
		},
		"https://api.openai.com/profile": map[string]string{"email": email},
	})
	return "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
}

func TestPutCodexManualRefreshOnlyImportsFreshBundle(t *testing.T) {
	tests := []struct {
		name    string
		refresh string
	}{
		{name: "plain refresh token", refresh: "refresh-manual-plain-new"},
		{name: "auth JSON refresh only", refresh: `{"refresh_token":"refresh-manual-json-new"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var usageAuthorization string
			var usageAccountID string
			usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/codex/usage" {
					t.Errorf("usage server received unexpected path %q", r.URL.Path)
				}
				usageAuthorization = r.Header.Get("Authorization")
				usageAccountID = r.Header.Get("ChatGPT-Account-Id")
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"plan_type":"usage-plan-new","email":"usage-new@example.test","remaining":5}`)
			}))
			defer usageServer.Close()

			newAccess := testAccessToken("account-manual-new", "", "token-plan-new")
			var refresherToken string
			db, server := newCredentialAPITestServer(t, func(_ context.Context, refreshToken, _ string) (*codex.TokenResponse, error) {
				refresherToken = refreshToken
				return &codex.TokenResponse{AccessToken: newAccess, RefreshToken: "refresh-output-new", ExpiresIn: 3600}, nil
			})
			model := store.Model{
				ID:                   "codex-manual-bundle",
				Name:                 "manual-bundle",
				ModelID:              "gpt-manual",
				Provider:             "codex",
				BaseURL:              "https://old.example.test",
				APIKey:               "access-manual-old",
				RefreshToken:         "refresh-manual-old",
				Endpoint:             "/codex/responses",
				CodexAccountID:       "account-manual-old",
				CodexEmail:           "old@example.test",
				CodexPlanType:        "old-plan",
				AccessTokenExpiresAt: time.Now().Add(time.Hour).Unix(),
				CodexUsageJSON:       `{"old":true}`,
				Enabled:              true,
			}
			if err := db.CreateModel(&model); err != nil {
				t.Fatal(err)
			}
			incoming := model
			incoming.BaseURL = usageServer.URL
			incoming.APIKey = maskKey(model.APIKey)
			incoming.RefreshToken = tc.refresh
			incoming.CodexAccountID = model.CodexAccountID
			incoming.CodexEmail = model.CodexEmail
			incoming.CodexPlanType = model.CodexPlanType

			status, response, apiError := requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/"+model.ID, incoming)
			if status != http.StatusOK {
				t.Fatalf("manual refresh-only PUT status=%d error=%v response=%#v", status, apiError, response)
			}
			wantRefresh := tc.refresh
			if strings.HasPrefix(wantRefresh, "{") {
				wantRefresh = "refresh-manual-json-new"
			}
			if refresherToken != wantRefresh {
				t.Fatalf("refresher token=%q, want %q", refresherToken, wantRefresh)
			}
			if usageAuthorization != "Bearer "+newAccess || usageAccountID != "account-manual-new" {
				t.Fatalf("usage used stale bundle: authorization=%q account=%q", usageAuthorization, usageAccountID)
			}
			saved, err := db.GetModel(model.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.APIKey != newAccess || saved.RefreshToken != "refresh-output-new" || saved.CodexAccountID != "account-manual-new" || saved.CodexEmail != "usage-new@example.test" || saved.CodexPlanType != "usage-plan-new" {
				t.Fatalf("manual refresh-only bundle was mixed with old profile: %#v", saved)
			}
			if saved.CodexUsageJSON != `{"plan_type":"usage-plan-new","email":"usage-new@example.test","remaining":5}` || strings.Contains(saved.CodexUsageJSON, "old") {
				t.Fatalf("manual refresh-only usage was stale: %q", saved.CodexUsageJSON)
			}
			if response.CodexAccountID != saved.CodexAccountID || response.CodexEmail != saved.CodexEmail || response.CodexPlanType != saved.CodexPlanType || response.CodexUsageJSON != saved.CodexUsageJSON {
				t.Fatalf("manual API response disagrees with DB: response=%#v saved=%#v", response, saved)
			}
		})
	}
}

func TestPutCodexModelRejectsAccessOnlyCredentialChange(t *testing.T) {
	db, server := newCredentialAPITestServer(t, func(context.Context, string, string) (*codex.TokenResponse, error) {
		t.Fatal("access-only credential change must be rejected before refresh")
		return nil, nil
	})
	model := store.Model{
		ID:                   "codex-access-only",
		Name:                 "access-only",
		ModelID:              "gpt-access-only",
		Provider:             "codex",
		BaseURL:              "https://example.invalid",
		APIKey:               "access-access-old",
		RefreshToken:         "refresh-access-old",
		Endpoint:             "/codex/responses",
		CodexAccountID:       "account-access-old",
		AccessTokenExpiresAt: time.Now().Add(time.Hour).Unix(),
		CodexUsageJSON:       `{"old":true}`,
		Enabled:              true,
	}
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	incoming := model
	incoming.APIKey = "access-access-new"
	incoming.RefreshToken = model.RefreshToken

	status, _, apiError := requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/"+model.ID, incoming)
	if status != http.StatusBadRequest || !strings.Contains(apiError["error"], "refresh_token") {
		t.Fatalf("access-only change status=%d error=%v, want explicit incomplete-credential rejection", status, apiError)
	}
	saved, err := db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.APIKey != model.APIKey || saved.RefreshToken != model.RefreshToken || saved.CodexUsageJSON != model.CodexUsageJSON {
		t.Fatalf("rejected access-only change modified DB: %#v", saved)
	}
}

func TestCodexUsageCASRejectsOrdinaryPutAccountReplacement(t *testing.T) {
	var replace func()
	var replaced atomic.Bool
	usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if replaced.CompareAndSwap(false, true) && replace != nil {
			replace()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"plan_type":"old-usage","email":"old-usage@example.test","remaining":1}`)
	}))
	defer usageServer.Close()
	db, server := newCredentialAPITestServer(t, func(context.Context, string, string) (*codex.TokenResponse, error) {
		t.Fatal("ordinary PUT should use valid existing token without refresh")
		return nil, nil
	})
	model := store.Model{
		ID:                   "codex-put-race",
		Name:                 "put-race",
		ModelID:              "gpt-put-race",
		Provider:             "codex",
		BaseURL:              usageServer.URL,
		APIKey:               "access-put-old",
		RefreshToken:         "refresh-put-old",
		Endpoint:             "/codex/responses",
		CodexAccountID:       "account-put-old",
		CodexEmail:           "old@example.test",
		CodexPlanType:        "old-plan",
		AccessTokenExpiresAt: time.Now().Add(time.Hour).Unix(),
		CodexUsageJSON:       `{"old":true}`,
		Enabled:              true,
	}
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	replacement := model
	replacement.Name = "replacement-account"
	replacement.APIKey = "access-put-new"
	replacement.RefreshToken = "refresh-put-new"
	replacement.CodexAccountID = "account-put-new"
	replacement.CodexEmail = "new@example.test"
	replacement.CodexPlanType = "new-plan"
	replacement.CodexUsageJSON = `{"new":true}`
	replace = func() {
		if err := db.UpdateModel(&replacement); err != nil {
			t.Errorf("replace account during usage: %v", err)
		}
	}
	incoming := model
	incoming.Name = "incoming-config"
	incoming.APIKey = maskKey(model.APIKey)
	incoming.RefreshToken = maskKey(model.RefreshToken)
	incoming.BaseURL = "https://new-config.example.test"

	status, _, apiError := requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/"+model.ID, incoming)
	if status != http.StatusConflict || !strings.Contains(apiError["error"], "codex credential CAS conflict") {
		t.Fatalf("ordinary PUT race status=%d error=%v, want credential conflict", status, apiError)
	}
	saved, err := db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.APIKey != replacement.APIKey || saved.RefreshToken != replacement.RefreshToken || saved.CodexAccountID != replacement.CodexAccountID || saved.CodexEmail != replacement.CodexEmail || saved.CodexPlanType != replacement.CodexPlanType || saved.CodexUsageJSON != replacement.CodexUsageJSON || saved.Name != replacement.Name {
		t.Fatalf("ordinary PUT race overwrote replacement account: %#v", saved)
	}
}

func TestCodexUsageCASRejectsRefreshAccountReplacement(t *testing.T) {
	var replace func()
	var replaced atomic.Bool
	usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if replaced.CompareAndSwap(false, true) && replace != nil {
			replace()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"plan_type":"old-usage","email":"old-usage@example.test","remaining":1}`)
	}))
	defer usageServer.Close()
	db, server := newCredentialAPITestServer(t, func(_ context.Context, refreshToken, _ string) (*codex.TokenResponse, error) {
		if refreshToken != "refresh-refresh-race-old" {
			t.Fatalf("refresh token=%q, want old token", refreshToken)
		}
		return &codex.TokenResponse{AccessToken: "access-refresh-race-new", RefreshToken: "refresh-refresh-race-new", ExpiresIn: 3600}, nil
	})
	model := store.Model{
		ID:                   "codex-refresh-race",
		Name:                 "refresh-race",
		ModelID:              "gpt-refresh-race",
		Provider:             "codex",
		BaseURL:              usageServer.URL,
		APIKey:               "access-refresh-race-old",
		RefreshToken:         "refresh-refresh-race-old",
		Endpoint:             "/codex/responses",
		CodexAccountID:       "account-refresh-race-old",
		CodexEmail:           "old@example.test",
		CodexPlanType:        "old-plan",
		AccessTokenExpiresAt: time.Now().Add(time.Hour).Unix(),
		CodexUsageJSON:       `{"old":true}`,
		Enabled:              true,
	}
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	replacement := model
	replacement.Name = "replacement-refresh-account"
	replacement.APIKey = "access-refresh-race-replacement"
	replacement.RefreshToken = "refresh-refresh-race-replacement"
	replacement.CodexAccountID = "account-refresh-race-replacement"
	replacement.CodexEmail = "replacement@example.test"
	replacement.CodexPlanType = "replacement-plan"
	replacement.CodexUsageJSON = `{"replacement":true}`
	replace = func() {
		if err := db.UpdateModel(&replacement); err != nil {
			t.Errorf("replace account during refresh usage: %v", err)
		}
	}

	status, _, apiError := requestModelJSON(t, server.Handler(), http.MethodPost, "/api/refresh-codex-model", map[string]string{"model_id": model.ID})
	if status != http.StatusConflict || !strings.Contains(apiError["error"], "codex credential CAS conflict") {
		t.Fatalf("refresh race status=%d error=%v, want credential conflict", status, apiError)
	}
	saved, err := db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.APIKey != replacement.APIKey || saved.RefreshToken != replacement.RefreshToken || saved.CodexAccountID != replacement.CodexAccountID || saved.CodexEmail != replacement.CodexEmail || saved.CodexPlanType != replacement.CodexPlanType || saved.CodexUsageJSON != replacement.CodexUsageJSON || saved.Name != replacement.Name {
		t.Fatalf("refresh race overwrote replacement account: %#v", saved)
	}
}

func TestPutCodexProviderChangeRequiresNewCredential(t *testing.T) {
	providers := []string{"openai", "anthropic", "gemini"}
	for _, provider := range providers {
		t.Run("codex-to-"+provider, func(t *testing.T) {
			db, server := newCredentialAPITestServer(t, nil)
			model := store.Model{
				ID:                   "codex-to-provider-" + provider,
				Name:                 "codex-to-" + provider,
				ModelID:              "gpt-provider",
				Provider:             "codex",
				BaseURL:              "https://example.invalid",
				APIKey:               "access-codex-old",
				RefreshToken:         "refresh-codex-old",
				Endpoint:             "/codex/responses",
				CodexAccountID:       "account-codex-old",
				CodexEmail:           "old@example.test",
				CodexPlanType:        "old-plan",
				CodexUsageJSON:       `{"old":true}`,
				AccessTokenExpiresAt: time.Now().Add(time.Hour).Unix(),
				Enabled:              true,
			}
			if err := db.CreateModel(&model); err != nil {
				t.Fatal(err)
			}
			invalid := model
			invalid.Provider = provider
			invalid.APIKey = maskKey(model.APIKey)
			status, _, apiError := requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/"+model.ID, invalid)
			if status != http.StatusBadRequest || !strings.Contains(apiError["error"], "api_key") {
				t.Fatalf("masked %s key status=%d error=%v", provider, status, apiError)
			}
			saved, err := db.GetModel(model.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Provider != model.Provider || saved.APIKey != model.APIKey || saved.CodexUsageJSON != model.CodexUsageJSON {
				t.Fatalf("invalid provider switch modified DB: %#v", saved)
			}
		})

		t.Run(provider+"-to-codex", func(t *testing.T) {
			db, server := newCredentialAPITestServer(t, func(context.Context, string, string) (*codex.TokenResponse, error) {
				return &codex.TokenResponse{AccessToken: "access-switched-codex", RefreshToken: "refresh-switched-codex", ExpiresIn: 3600}, nil
			})
			model := store.Model{
				ID:             "provider-to-codex-" + provider,
				Name:           provider + "-to-codex",
				ModelID:        "gpt-provider",
				Provider:       provider,
				BaseURL:        "https://example.invalid",
				APIKey:         "api-key-old",
				RefreshToken:   "",
				Endpoint:       "/v1/chat/completions",
				CodexEmail:     "foreign@example.test",
				CodexPlanType:  "foreign-plan",
				CodexAccountID: "foreign-account",
				CodexUsageJSON: `{"foreign":true}`,
				Enabled:        true,
			}
			if err := db.CreateModel(&model); err != nil {
				t.Fatal(err)
			}
			invalid := model
			invalid.Provider = "codex"
			invalid.APIKey = maskKey(model.APIKey)
			invalid.RefreshToken = "****"
			status, _, apiError := requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/"+model.ID, invalid)
			if status != http.StatusBadRequest || !strings.Contains(apiError["error"], "refresh_token") {
				t.Fatalf("masked Codex refresh status=%d error=%v", status, apiError)
			}
			saved, err := db.GetModel(model.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Provider != model.Provider || saved.APIKey != model.APIKey || saved.CodexUsageJSON != model.CodexUsageJSON {
				t.Fatalf("invalid reverse provider switch modified DB: %#v", saved)
			}
		})
	}
}

func TestPutCodexProviderChangeWithExplicitCredential(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "gemini"} {
		t.Run("codex-to-"+provider, func(t *testing.T) {
			db, server := newCredentialAPITestServer(t, nil)
			model := store.Model{
				ID:                   "codex-valid-to-" + provider,
				Name:                 "codex-valid-to-" + provider,
				ModelID:              "gpt-provider",
				Provider:             "codex",
				BaseURL:              "https://example.invalid",
				APIKey:               "access-codex-old",
				RefreshToken:         "refresh-codex-old",
				Endpoint:             "/codex/responses",
				CodexAccountID:       "account-codex-old",
				CodexEmail:           "old@example.test",
				CodexPlanType:        "old-plan",
				CodexUsageJSON:       `{"old":true}`,
				AccessTokenExpiresAt: time.Now().Add(time.Hour).Unix(),
				Enabled:              true,
			}
			if err := db.CreateModel(&model); err != nil {
				t.Fatal(err)
			}
			incoming := model
			incoming.Provider = provider
			incoming.APIKey = "api-key-" + provider + "-new"
			incoming.RefreshToken = ""
			status, _, apiError := requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/"+model.ID, incoming)
			if status != http.StatusOK {
				t.Fatalf("explicit %s key switch status=%d error=%v", provider, status, apiError)
			}
			saved, err := db.GetModel(model.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Provider != provider || saved.APIKey != incoming.APIKey || saved.RefreshToken != "" || saved.CodexAccountID != "" || saved.CodexEmail != "" || saved.CodexPlanType != "" || saved.CodexUsageJSON != "" || saved.AccessTokenExpiresAt != 0 {
				t.Fatalf("provider switch retained old Codex fields: %#v", saved)
			}
		})

		t.Run(provider+"-to-codex", func(t *testing.T) {
			usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/codex/usage" {
					t.Errorf("usage server received unexpected path %q", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"plan_type":"switched-plan","email":"switched@example.test"}`)
			}))
			defer usageServer.Close()
			newAccess := testAccessToken("account-switched", "", "token-plan")
			db, server := newCredentialAPITestServer(t, func(_ context.Context, refreshToken, _ string) (*codex.TokenResponse, error) {
				if refreshToken != "refresh-switched-new" {
					t.Fatalf("switch refresher token=%q", refreshToken)
				}
				return &codex.TokenResponse{AccessToken: newAccess, RefreshToken: "refresh-switched-output", ExpiresIn: 3600}, nil
			})
			model := store.Model{
				ID:             "provider-valid-to-codex-" + provider,
				Name:           provider + "-valid-to-codex",
				ModelID:        "gpt-provider",
				Provider:       provider,
				BaseURL:        "https://example.invalid",
				APIKey:         "api-key-old",
				Endpoint:       "/v1/chat/completions",
				CodexEmail:     "foreign@example.test",
				CodexPlanType:  "foreign-plan",
				CodexAccountID: "foreign-account",
				CodexUsageJSON: `{"foreign":true}`,
				Enabled:        true,
			}
			if err := db.CreateModel(&model); err != nil {
				t.Fatal(err)
			}
			incoming := model
			incoming.Provider = "codex"
			incoming.APIKey = ""
			incoming.RefreshToken = "refresh-switched-new"
			incoming.BaseURL = usageServer.URL
			incoming.Endpoint = "/codex/responses"
			status, _, apiError := requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/"+model.ID, incoming)
			if status != http.StatusOK {
				t.Fatalf("explicit Codex switch status=%d error=%v", status, apiError)
			}
			saved, err := db.GetModel(model.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Provider != "codex" || saved.APIKey != newAccess || saved.RefreshToken != "refresh-switched-output" || saved.CodexAccountID != "account-switched" || saved.CodexEmail != "switched@example.test" || saved.CodexPlanType != "switched-plan" || strings.Contains(saved.CodexUsageJSON, "foreign") {
				t.Fatalf("explicit reverse provider switch was not fresh: %#v", saved)
			}
		})
	}
}

func TestPostCodexModelWithClientIDUsesEphemeralPrecheck(t *testing.T) {
	usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/usage" {
			t.Errorf("usage server received unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"plan_type":"post-plan","email":"post@example.test"}`)
	}))
	defer usageServer.Close()
	db, server := newCredentialAPITestServer(t, func(context.Context, string, string) (*codex.TokenResponse, error) {
		t.Fatal("new POST with valid access token unexpectedly refreshed")
		return nil, nil
	})
	model := store.Model{
		ID:                   "post-client-id",
		Name:                 "post-client-model",
		ModelID:              "gpt-post",
		Provider:             "codex",
		BaseURL:              usageServer.URL,
		APIKey:               "access-post-valid",
		RefreshToken:         "refresh-post-valid",
		Endpoint:             "/codex/responses",
		AccessTokenExpiresAt: time.Now().Add(time.Hour).Unix(),
		Enabled:              true,
	}
	status, response, apiError := requestModelJSON(t, server.Handler(), http.MethodPost, "/api/models", model)
	if status != http.StatusCreated {
		t.Fatalf("POST status=%d error=%v response=%#v", status, apiError, response)
	}
	saved, err := db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != model.ID || saved.CodexUsageJSON != `{"plan_type":"post-plan","email":"post@example.test"}` || saved.CodexPlanType != "post-plan" || saved.CodexEmail != "post@example.test" {
		t.Fatalf("POST with client ID did not persist precheck result: %#v", saved)
	}
}
