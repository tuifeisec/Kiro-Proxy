package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"kiro-proxy/router"
	"kiro-proxy/store"
)

func newTypesafeSettingsTestServer(t *testing.T) (*store.DB, *Server) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "typesafe-settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	server := New(db, router.New(db), nil)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db, server
}

func putTypesafeSettings(t *testing.T, server *Server, config string, additional map[string]string) int {
	t.Helper()
	body := make(map[string]string, len(additional)+1)
	for key, value := range additional {
		body[key] = value
	}
	body[store.TypesafeClassifierSetting] = config
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	return recorder.Code
}

func getTypesafeSettings(t *testing.T, server *Server) (int, map[string]string, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode settings response %q: %v", recorder.Body.String(), err)
	}
	return recorder.Code, response, recorder.Body.String()
}

func TestTypesafeSettingsGetReturnsNormalizedMaskedConfig(t *testing.T) {
	db, server := newTypesafeSettingsTestServer(t)
	if err := db.SetSetting(store.TypesafeClassifierSetting, `{"api_key":"secret"}`); err != nil {
		t.Fatal(err)
	}
	status, response, body := getTypesafeSettings(t, server)
	if status != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", status, body)
	}
	var config store.TypesafeClassifierConfig
	if err := json.Unmarshal([]byte(response[store.TypesafeClassifierSetting]), &config); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if config.APIKey != store.TypesafeAPIKeyMask || config.BaseURL != "https://api.typesafe.ai" || config.Model != "jev-1.13.0" || config.TimeoutSeconds != 10 || config.InputPricePerMillion != 0.042 {
		t.Fatalf("GET config = %#v", config)
	}
	if bytes.Contains([]byte(body), []byte("secret")) {
		t.Fatalf("GET response leaked API key: %s", body)
	}
}

func TestTypesafeSettingsPutPreservesUpdatesAndClearsAPIKey(t *testing.T) {
	db, server := newTypesafeSettingsTestServer(t)
	if err := db.SetSetting(store.TypesafeClassifierSetting, `{"api_key":"old-secret"}`); err != nil {
		t.Fatal(err)
	}

	if status := putTypesafeSettings(t, server, `{"api_key":"****","timeout_seconds":20}`, nil); status != http.StatusOK {
		t.Fatalf("masked-key PUT status=%d", status)
	}
	stored, err := db.GetSetting(store.TypesafeClassifierSetting)
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.ParseTypesafeClassifierConfig(stored)
	if err != nil || config.APIKey != "old-secret" || config.TimeoutSeconds != 20 {
		t.Fatalf("masked-key save = %#v, error=%v", config, err)
	}

	if status := putTypesafeSettings(t, server, `{"api_key":"new-secret"}`, nil); status != http.StatusOK {
		t.Fatalf("new-key PUT status=%d", status)
	}
	stored, err = db.GetSetting(store.TypesafeClassifierSetting)
	if err != nil {
		t.Fatal(err)
	}
	config, err = store.ParseTypesafeClassifierConfig(stored)
	if err != nil || config.APIKey != "new-secret" {
		t.Fatalf("new-key save = %#v, error=%v", config, err)
	}

	if status := putTypesafeSettings(t, server, `{"api_key":""}`, nil); status != http.StatusOK {
		t.Fatalf("clear-key PUT status=%d", status)
	}
	stored, err = db.GetSetting(store.TypesafeClassifierSetting)
	if err != nil {
		t.Fatal(err)
	}
	config, err = store.ParseTypesafeClassifierConfig(stored)
	if err != nil || config.APIKey != "" {
		t.Fatalf("clear-key save = %#v, error=%v", config, err)
	}
}

func TestTypesafeSettingsPutRejectsMaskWithoutStoredKey(t *testing.T) {
	db, server := newTypesafeSettingsTestServer(t)
	if err := db.SetSetting("unrelated_setting", "before"); err != nil {
		t.Fatal(err)
	}
	status := putTypesafeSettings(t, server, `{"api_key":"****"}`, map[string]string{"unrelated_setting": "after"})
	if status != http.StatusBadRequest {
		t.Fatalf("masked-key PUT status=%d, want %d", status, http.StatusBadRequest)
	}
	value, err := db.GetSetting("unrelated_setting")
	if err != nil || value != "before" {
		t.Fatalf("invalid config partially wrote unrelated setting: value=%q error=%v", value, err)
	}
	value, err = db.GetSetting(store.TypesafeClassifierSetting)
	if err != nil || value != "" {
		t.Fatalf("invalid config was persisted: value=%q error=%v", value, err)
	}
}

func TestTypesafeSettingsPutValidatesBeforeWritingAnySetting(t *testing.T) {
	db, server := newTypesafeSettingsTestServer(t)
	if err := db.SetSetting("unrelated_setting", "before"); err != nil {
		t.Fatal(err)
	}
	status := putTypesafeSettings(t, server, `{"enabled":true,"api_key":""}`, map[string]string{"unrelated_setting": "after"})
	if status != http.StatusBadRequest {
		t.Fatalf("invalid config PUT status=%d, want %d", status, http.StatusBadRequest)
	}
	value, err := db.GetSetting("unrelated_setting")
	if err != nil || value != "before" {
		t.Fatalf("invalid config partially wrote unrelated setting: value=%q error=%v", value, err)
	}
	value, err = db.GetSetting(store.TypesafeClassifierSetting)
	if err != nil || value != "" {
		t.Fatalf("invalid config was persisted: value=%q error=%v", value, err)
	}
}

func TestTypesafeSettingsErrorsDoNotEchoKeysOrProxyCredentials(t *testing.T) {
	_, server := newTypesafeSettingsTestServer(t)
	requestBody, err := json.Marshal(map[string]string{
		store.TypesafeClassifierSetting: `{"api_key":"secret-value","proxy_url":"http://secret-value@%"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("PUT status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("secret-value")) {
		t.Fatalf("configuration error leaked a key or proxy credential: %s", recorder.Body.String())
	}
}
