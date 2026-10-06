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

func newAutoRoutingAPITestServer(t *testing.T) (*store.DB, *Server, *router.Router) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "auto-routing.db"))
	if err != nil {
		t.Fatal(err)
	}
	r := router.New(db)
	server := New(db, r, nil)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db, server, r
}

func putAutoRoutingSettings(t *testing.T, handler http.Handler, body map[string]string) (int, map[string]string) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	var response map[string]string
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	return recorder.Code, response
}

func TestSettingsPutValidatesBeforeWritingAndReloadsRouter(t *testing.T) {
	db, server, r := newAutoRoutingAPITestServer(t)
	model := store.Model{ID: "auto-route-model", Name: "Auto Route Model", ModelID: "upstream-model", Provider: "openai", BaseURL: "http://127.0.0.1", APIKey: "test", Endpoint: "/v1/chat/completions", Enabled: true, ImageInputMode: "supported"}
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	if r.Resolve(model.ID) != nil {
		t.Fatal("router unexpectedly saw model created after initial load")
	}
	if err := db.SetSetting("otherkey", "preserve me"); err != nil {
		t.Fatal(err)
	}
	config := `{"enabled":true,"default_model_id":" auto-route-model ","fast_max_chars":0,"long_context_min_chars":0,"task_rules":null}`
	status, response := putAutoRoutingSettings(t, server.Handler(), map[string]string{
		store.AutoRoutingSetting: config,
	})
	if status != http.StatusOK {
		t.Fatalf("status=%d response=%v", status, response)
	}
	stored, err := db.GetSetting(store.AutoRoutingSetting)
	if err != nil {
		t.Fatalf("read stored config: %v", err)
	}
	var saved map[string]interface{}
	if err := json.Unmarshal([]byte(stored), &saved); err != nil {
		t.Fatalf("stored config is not valid JSON %q: %v", stored, err)
	}
	if saved["default_model_id"] != model.ID || saved["fast_max_chars"] != float64(500) || saved["long_context_min_chars"] != float64(32000) {
		t.Fatalf("stored config was not canonicalized: %#v", saved)
	}
	rules, ok := saved["task_rules"].([]interface{})
	if !ok || rules == nil || len(rules) != 0 {
		t.Fatalf("stored task_rules = %#v, want an empty array", saved["task_rules"])
	}
	parsed, err := store.ParseAutoRoutingConfig(stored)
	if err != nil {
		t.Fatalf("saved config cannot be parsed: %v", err)
	}
	stable, err := json.Marshal(parsed)
	if err != nil || string(stable) != stored {
		t.Fatalf("saved config is not stable after parse: %q, error=%v", stable, err)
	}
	other, err := db.GetSetting("otherkey")
	if err != nil || other != "preserve me" {
		t.Fatalf("unrelated setting changed: value=%q error=%v", other, err)
	}
	if route := r.Resolve(model.ID); route == nil || route.Model == nil || route.Model.ID != model.ID {
		t.Fatalf("router was not reloaded after save: %#v", route)
	}
}

func TestSettingsPutInvalidAutoConfigDoesNotWriteAnySetting(t *testing.T) {
	db, server, _ := newAutoRoutingAPITestServer(t)
	if err := db.SetSetting("unrelated_setting", "before"); err != nil {
		t.Fatal(err)
	}
	status, response := putAutoRoutingSettings(t, server.Handler(), map[string]string{
		store.AutoRoutingSetting: `{"enabled":true,"default_model_id":"missing-model"}`,
		"unrelated_setting":      "after",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d response=%v", status, response)
	}
	value, err := db.GetSetting("unrelated_setting")
	if err != nil || value != "before" {
		t.Fatalf("invalid config partially wrote unrelated setting: value=%q error=%v", value, err)
	}
	value, err = db.GetSetting(store.AutoRoutingSetting)
	if err != nil || value != "" {
		t.Fatalf("invalid config was persisted: value=%q error=%v", value, err)
	}
}
