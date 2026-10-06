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
	config := `{"mode":"stage_fixed","enabled":true,"default_model_id":" auto-route-model ","stage_models":{"run":"auto-route-model","plan":" auto-route-model ","implement":"auto-route-model","review":"auto-route-model","finalize":"auto-route-model"}}`
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
	if saved["mode"] != store.AutoRoutingModeStageFixed || saved["default_model_id"] != model.ID {
		t.Fatalf("stored config was not canonicalized: %#v", saved)
	}
	if len(saved) != 4 {
		t.Fatalf("stored config must contain only four fields: %#v", saved)
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

func TestSettingsPutStageFixedConfigSavesAndReloads(t *testing.T) {
	db, server, r := newAutoRoutingAPITestServer(t)
	model := store.Model{ID: "stage-model", Name: "Stage Model", ModelID: "upstream-stage-model", Provider: "openai", BaseURL: "http://127.0.0.1", APIKey: "test", Endpoint: "/v1/chat/completions", Enabled: true}
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	config := `{"mode":"stage_fixed","enabled":true,"default_model_id":"stage-model","stage_models":{"run":" stage-model ","plan":"stage-model","implement":"stage-model","review":"stage-model","finalize":"stage-model"},"vision_model_id":"inactive-missing","task_rules":[{"field":"invalid"}]}`
	status, response := putAutoRoutingSettings(t, server.Handler(), map[string]string{store.AutoRoutingSetting: config})
	if status != http.StatusOK {
		t.Fatalf("status=%d response=%v", status, response)
	}
	stored, err := db.GetSetting(store.AutoRoutingSetting)
	if err != nil {
		t.Fatalf("read stored config: %v", err)
	}
	parsed, err := store.ParseAutoRoutingConfig(stored)
	if err != nil {
		t.Fatalf("saved stage config cannot be parsed: %v", err)
	}
	if parsed.Mode != store.AutoRoutingModeStageFixed || parsed.StageModels.Run != model.ID || parsed.StageModels.Finalize != model.ID {
		t.Fatalf("saved stage config = %#v", parsed)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stored), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 4 {
		t.Fatalf("saved stage config retained retired fields: %#v", saved)
	}
	if route := r.Resolve(model.ID); route == nil || route.Model == nil || route.Model.ID != model.ID {
		t.Fatalf("router was not reloaded after stage config save: %#v", route)
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
	if status != http.StatusBadRequest || response["error"] == "" {
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

func TestSettingsPutInvalidStageFixedConfigDoesNotWriteAnySetting(t *testing.T) {
	for name, config := range map[string]string{
		"unknown mode":  `{"mode":"stage-fix","enabled":true,"default_model_id":"stage-model"}`,
		"missing stage": `{"mode":"stage_fixed","enabled":true,"default_model_id":"stage-model","stage_models":{"run":"stage-model","plan":"stage-model","implement":"stage-model","review":"stage-model"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			db, server, _ := newAutoRoutingAPITestServer(t)
			model := store.Model{ID: "stage-model", Name: "Stage Model", ModelID: "upstream-stage-model", Provider: "openai", BaseURL: "http://127.0.0.1", APIKey: "test", Endpoint: "/v1/chat/completions", Enabled: true}
			if err := db.CreateModel(&model); err != nil {
				t.Fatal(err)
			}
			if err := db.SetSetting("unrelated_setting", "before"); err != nil {
				t.Fatal(err)
			}
			status, response := putAutoRoutingSettings(t, server.Handler(), map[string]string{
				store.AutoRoutingSetting: config,
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
		})
	}
}

func TestSettingsPutRejectsLegacyAutoConfigWithoutPartialWrite(t *testing.T) {
	for name, config := range map[string]string{
		"missing mode":    `{"enabled":true,"default_model_id":"old-model"}`,
		"task rules mode": `{"mode":"task_rules","enabled":true,"default_model_id":"old-model","task_rules":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			db, server, _ := newAutoRoutingAPITestServer(t)
			if err := db.SetSetting(store.AutoRoutingSetting, `{"mode":"task_rules","persisted":"original"}`); err != nil {
				t.Fatal(err)
			}
			if err := db.SetSetting("unrelated_setting", "before"); err != nil {
				t.Fatal(err)
			}
			status, response := putAutoRoutingSettings(t, server.Handler(), map[string]string{
				store.AutoRoutingSetting: config,
				"unrelated_setting":      "after",
			})
			if status != http.StatusBadRequest || response["error"] == "" {
				t.Fatalf("status=%d response=%v", status, response)
			}
			value, err := db.GetSetting("unrelated_setting")
			if err != nil || value != "before" {
				t.Fatalf("legacy config partially wrote unrelated setting: value=%q error=%v", value, err)
			}
			value, err = db.GetSetting(store.AutoRoutingSetting)
			if err != nil || value != `{"mode":"task_rules","persisted":"original"}` {
				t.Fatalf("legacy config was persisted: value=%q error=%v", value, err)
			}
		})
	}
}

func TestSettingsGetPreservesLegacyAutoJSONWithoutNormalizing(t *testing.T) {
	db, server, _ := newAutoRoutingAPITestServer(t)
	legacy := `{"enabled":true,"default_model_id":"old-model","vision_model_id":"legacy-target","task_rules":[{"field":"agent_mode","value":"agent","model_id":"old-model"}]}`
	if err := db.SetSetting(store.AutoRoutingSetting, legacy); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var settings map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &settings); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if settings[store.AutoRoutingSetting] != legacy {
		t.Fatalf("GET normalized legacy config: got %q, want %q", settings[store.AutoRoutingSetting], legacy)
	}
	stored, err := db.GetSetting(store.AutoRoutingSetting)
	if err != nil || stored != legacy {
		t.Fatalf("GET changed persisted legacy config: value=%q error=%v", stored, err)
	}
}
