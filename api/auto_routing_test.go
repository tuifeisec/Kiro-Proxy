package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func TestSettingsPutV2ValidatesNormalizesReloadsAndRoundTrips(t *testing.T) {
	db, server, r := newAutoRoutingAPITestServer(t)
	model := apiAutoRoutingModel("auto-route-model", "upstream-model")
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	if r.Resolve(model.ID) != nil {
		t.Fatal("router unexpectedly saw model created after initial load")
	}
	if err := db.SetSetting("otherkey", "preserve me"); err != nil {
		t.Fatal(err)
	}
	raw := apiAutoRoutingV2Raw(model.ID, "")
	status, response := putAutoRoutingSettings(t, server.Handler(), map[string]string{store.AutoRoutingSetting: raw})
	if status != http.StatusOK {
		t.Fatalf("status=%d response=%v", status, response)
	}
	stored, err := db.GetSetting(store.AutoRoutingSetting)
	if err != nil {
		t.Fatalf("read stored config: %v", err)
	}
	parsed, err := store.ParseAutoRoutingConfig(stored)
	if err != nil {
		t.Fatalf("saved config cannot be parsed: %v", err)
	}
	if parsed.Mode != store.AutoRoutingModeDutyFixed || parsed.Version != 2 || len(parsed.DutyModels) != 18 {
		t.Fatalf("saved config = %#v", parsed)
	}
	stable, err := json.Marshal(parsed)
	if err != nil || string(stable) != stored {
		t.Fatalf("saved config is not stable after parse: %q, error=%v", stable, err)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stored), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 8 {
		t.Fatalf("v2 schema has %d fields, want 8: %#v", len(saved), saved)
	}
	for _, field := range []string{"version", "mode", "layout", "enabled", "default_model_id", "duty_models", "node_bindings", "node_overrides"} {
		if _, ok := saved[field]; !ok {
			t.Fatalf("saved v2 config omitted %q", field)
		}
	}
	var bindings, overrides []json.RawMessage
	if err := json.Unmarshal(saved["node_bindings"], &bindings); err != nil || bindings == nil {
		t.Fatalf("empty node_bindings did not round trip as an array: %s (%v)", saved["node_bindings"], err)
	}
	if err := json.Unmarshal(saved["node_overrides"], &overrides); err != nil || overrides == nil {
		t.Fatalf("empty node_overrides did not round trip as an array: %s (%v)", saved["node_overrides"], err)
	}
	other, err := db.GetSetting("otherkey")
	if err != nil || other != "preserve me" {
		t.Fatalf("unrelated setting changed: value=%q error=%v", other, err)
	}
	if !r.StageRoutingEnabled() {
		t.Fatal("router was not reloaded with enabled v2 routing")
	}
}

func TestSettingsPutV2MigrationBacksUpExactStageFixedRawAndHidesReservedKey(t *testing.T) {
	db, server, r := newAutoRoutingAPITestServer(t)
	model := apiAutoRoutingModel("stage-model", "upstream-stage-model")
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	legacy := `{"mode":"stage_fixed","enabled":true,"default_model_id":"stage-model","stage_models":{"run":"stage-model","plan":"stage-model","implement":"stage-model","review":"stage-model","finalize":"stage-model"},"vision_model_id":"inactive-missing","task_rules":[{"field":"invalid"}]}`
	if err := db.SetSetting(store.AutoRoutingSetting, legacy); err != nil {
		t.Fatal(err)
	}
	r.Reload()
	status, response := putAutoRoutingSettings(t, server.Handler(), map[string]string{store.AutoRoutingSetting: apiAutoRoutingV2Raw(model.ID, "")})
	if status != http.StatusOK {
		t.Fatalf("status=%d response=%v", status, response)
	}
	backup, err := db.GetSetting(store.AutoRoutingStageFixedBackupSetting)
	if err != nil || backup != legacy {
		t.Fatalf("stage_fixed backup = %q, error=%v", backup, err)
	}
	if !r.StageRoutingEnabled() {
		t.Fatal("router did not reload duty_fixed config")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	var settings map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if _, visible := settings[store.AutoRoutingStageFixedBackupSetting]; visible {
		t.Fatal("server-owned backup key was exposed to settings clients")
	}
}

func TestSettingsPutInvalidV2DoesNotWriteAnySetting(t *testing.T) {
	db, server, _ := newAutoRoutingAPITestServer(t)
	if err := db.SetSetting("unrelated_setting", "before"); err != nil {
		t.Fatal(err)
	}
	invalid := apiAutoRoutingV2Raw("missing-model", "")
	status, response := putAutoRoutingSettings(t, server.Handler(), map[string]string{
		store.AutoRoutingSetting: invalid,
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

func TestSettingsPutRejectsEffortUnsupportedByLocalProviderConverter(t *testing.T) {
	_, server, _ := newAutoRoutingAPITestServer(t)
	model := apiAutoRoutingModel("anthropic-model", "upstream-claude")
	model.Provider = "anthropic"
	if err := server.db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	status, response := putAutoRoutingSettings(t, server.Handler(), map[string]string{
		store.AutoRoutingSetting: apiAutoRoutingV2Raw(model.ID, "max"),
	})
	if status != http.StatusBadRequest || !strings.Contains(response["error"], `provider "anthropic"`) {
		t.Fatalf("unsupported provider effort status=%d response=%v", status, response)
	}
}

func TestSettingsPutRejectsLegacyWritesAndReservedBackupOverride(t *testing.T) {
	for name, body := range map[string]map[string]string{
		"legacy stage write": {store.AutoRoutingSetting: `{"mode":"stage_fixed","enabled":false,"default_model_id":"","stage_models":{"run":"","plan":"","implement":"","review":"","finalize":""}}`},
		"backup override":    {store.AutoRoutingStageFixedBackupSetting: "client value"},
	} {
		t.Run(name, func(t *testing.T) {
			db, server, _ := newAutoRoutingAPITestServer(t)
			if err := db.SetSetting(store.AutoRoutingSetting, `{"mode":"stage_fixed","enabled":false,"default_model_id":"","stage_models":{"run":"","plan":"","implement":"","review":"","finalize":""}}`); err != nil {
				t.Fatal(err)
			}
			status, response := putAutoRoutingSettings(t, server.Handler(), body)
			if status != http.StatusBadRequest || response["error"] == "" {
				t.Fatalf("status=%d response=%v", status, response)
			}
			stored, err := db.GetSetting(store.AutoRoutingSetting)
			if err != nil || !strings.Contains(stored, `"mode":"stage_fixed"`) {
				t.Fatalf("rejected request changed the old setting: %q (%v)", stored, err)
			}
		})
	}
}

func TestSettingsGetPreservesUnmigratedRawConfigWithoutBackup(t *testing.T) {
	db, server, _ := newAutoRoutingAPITestServer(t)
	legacy := `{"mode":"stage_fixed","enabled":false,"default_model_id":"","stage_models":{"run":"","plan":"","implement":"","review":"","finalize":""},"vision_model_id":"kept in original"}`
	if err := db.SetSetting(store.AutoRoutingSetting, legacy); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(store.AutoRoutingStageFixedBackupSetting, "server backup"); err != nil {
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
		t.Fatal(err)
	}
	if settings[store.AutoRoutingSetting] != legacy {
		t.Fatalf("GET normalized legacy config: got %q, want %q", settings[store.AutoRoutingSetting], legacy)
	}
	if _, visible := settings[store.AutoRoutingStageFixedBackupSetting]; visible {
		t.Fatal("server-owned backup key was exposed")
	}
	backup, err := db.GetSetting(store.AutoRoutingStageFixedBackupSetting)
	if err != nil || backup != "server backup" {
		t.Fatalf("GET changed backup: %q (%v)", backup, err)
	}
}

func TestAutoRoutingObservationsEndpointReturnsArray(t *testing.T) {
	_, server, r := newAutoRoutingAPITestServer(t)
	if got := r.GetAutoObservations(); len(got) != 0 || got == nil {
		t.Fatalf("empty observations = %#v", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auto-routing/observations", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "[]\n" {
		t.Fatalf("GET observations status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/auto-routing/observations", nil)
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST observations status=%d", recorder.Code)
	}
}

func apiAutoRoutingModel(id, upstream string) store.Model {
	return store.Model{ID: id, Name: id, ModelID: upstream, Provider: "openai", BaseURL: "http://127.0.0.1",
		APIKey: "test", Endpoint: "/v1/chat/completions", Enabled: true, ImageInputMode: "supported"}
}

func TestSettingsV2ExistingGroupTargetsSaveReloadAndRejectDisabledAtomically(t *testing.T) {
	db, server, r := newAutoRoutingAPITestServer(t)
	model := apiAutoRoutingModel("group-member", "gpt-upstream")
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	group := store.ModelGroup{ID: "gpt-group", Name: "GPT", Enabled: true,
		Members: []store.ModelGroupMember{{ModelID: model.ID, Priority: 1, MaxConcurrency: 2}}}
	if err := db.CreateModelGroup(&group); err != nil {
		t.Fatal(err)
	}
	status, response := putAutoRoutingSettings(t, server.Handler(), map[string]string{store.AutoRoutingSetting: apiAutoRoutingV2Raw(group.ID, "high")})
	if status != http.StatusOK {
		t.Fatalf("group settings rejected: %d %#v", status, response)
	}
	stored, err := db.GetSetting(store.AutoRoutingSetting)
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.ParseAutoRoutingConfig(stored)
	if err != nil || config.DefaultModelID != group.ID || config.DutyModels["implementation"].ModelID != group.ID {
		t.Fatalf("group IDs were not preserved: %#v, %v", config, err)
	}
	if !r.StageRoutingEnabled() || r.AutoModel() == nil {
		t.Fatal("reload failed to expose group-only Auto")
	}
	group.Enabled = false
	if err := db.UpdateModelGroup(&group); err != nil {
		t.Fatal(err)
	}
	status, response = putAutoRoutingSettings(t, server.Handler(), map[string]string{store.AutoRoutingSetting: apiAutoRoutingV2Raw(group.ID, "low"), "group-save-sibling": "must not save"})
	if status != http.StatusBadRequest {
		t.Fatalf("disabled group accepted: %d %#v", status, response)
	}
	current, err := db.GetSetting(store.AutoRoutingSetting)
	if err != nil || current != stored {
		t.Fatalf("failed save changed previous config: %q %v", current, err)
	}
	if sibling, _ := db.GetSetting("group-save-sibling"); sibling != "" {
		t.Fatalf("failed group save wrote sibling: %q", sibling)
	}
}

func apiAutoRoutingV2Raw(modelID, effort string) string {
	targets := make(map[string]store.AutoDutyTarget)
	for _, duty := range store.AutoDutyNames(store.AutoRoutingLayoutStandard) {
		targets[duty] = store.AutoDutyTarget{ModelID: modelID, ReasoningEffort: effort}
	}
	config := store.AutoRoutingConfig{Version: store.AutoRoutingVersion, Mode: store.AutoRoutingModeDutyFixed,
		Layout: store.AutoRoutingLayoutStandard, Enabled: true, DefaultModelID: modelID, DutyModels: targets,
		NodeBindings: []store.AutoNodeBinding{}, NodeOverrides: []store.AutoNodeOverride{}}
	encoded, err := json.Marshal(config)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
