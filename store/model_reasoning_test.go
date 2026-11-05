package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"
)

// legacyModelsDDL 是新增思考档位列之前的 models 表结构。
const legacyModelsDDL = `
CREATE TABLE models (
    id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', model_id TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT 'openai', base_url TEXT NOT NULL, api_key TEXT NOT NULL,
    endpoint TEXT NOT NULL DEFAULT '', context_window INTEGER NOT NULL DEFAULT 0,
    max_tokens INTEGER NOT NULL DEFAULT 0, reasoning_effort TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1, sort_order INTEGER NOT NULL DEFAULT 0,
    note TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')), updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO models (id, name, model_id, base_url, api_key, endpoint, reasoning_effort)
VALUES ('legacy', 'legacy-name', 'legacy-model', 'https://legacy.example.test', 'legacy-key', '/v1/chat/completions', 'high');
`

func TestModelReasoningColumnsLegacyMigrationDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.db")
	legacyDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDB.Exec(legacyModelsDDL); err != nil {
		_ = legacyDB.Close()
		t.Fatal(err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	legacy, err := db.GetModel("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.SupportedReasoningEfforts == nil || len(legacy.SupportedReasoningEfforts) != 0 ||
		legacy.DisableReasoningField || legacy.ThinkingStyle != "" || legacy.ReasoningEffort != "high" {
		t.Fatalf("legacy model must keep its effort and default the new fields: %#v", legacy)
	}
	for _, column := range []string{"supported_reasoning_efforts", "disable_reasoning_field", "thinking_style"} {
		var notNull int
		if err := db.conn.QueryRow(`SELECT "notnull" FROM pragma_table_info('models') WHERE name = ?`, column).Scan(&notNull); err != nil || notNull != 1 {
			t.Fatalf("column %s missing or nullable: notnull=%d err=%v", column, notNull, err)
		}
	}
}

func TestModelReasoningFieldsRoundTripThroughAllWritePaths(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	model := Model{
		ID: "reasoning-roundtrip", Name: "Reasoning", ModelID: "reasoning-model", Provider: "anthropic",
		BaseURL: "https://example.test", APIKey: "key", Endpoint: "/messages", Enabled: true,
		ReasoningEffort: "medium", ThinkingStyle: "budget",
		SupportedReasoningEfforts: []string{"low", "medium"}, DisableReasoningField: true,
	}
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	assertReasoningFields := func(label string, got *Model, efforts []string, disabled bool, style string) {
		t.Helper()
		if !reflect.DeepEqual(got.SupportedReasoningEfforts, efforts) || got.DisableReasoningField != disabled || got.ThinkingStyle != style {
			t.Fatalf("%s: efforts=%v disabled=%v style=%q", label, got.SupportedReasoningEfforts, got.DisableReasoningField, got.ThinkingStyle)
		}
	}
	got, err := db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertReasoningFields("GetModel", got, []string{"low", "medium"}, true, "budget")
	byModelID, err := db.FindByModelID(model.ModelID)
	if err != nil {
		t.Fatal(err)
	}
	assertReasoningFields("FindByModelID", byModelID, []string{"low", "medium"}, true, "budget")
	byName, err := db.FindModelByName(model.Name)
	if err != nil {
		t.Fatal(err)
	}
	assertReasoningFields("FindModelByName", byName, []string{"low", "medium"}, true, "budget")

	model.SupportedReasoningEfforts, model.DisableReasoningField, model.ThinkingStyle = nil, false, ""
	if err := db.UpdateModel(&model); err != nil {
		t.Fatal(err)
	}
	models, err := db.GetModels()
	if err != nil || len(models) != 1 {
		t.Fatalf("GetModels = %v, %v", models, err)
	}
	assertReasoningFields("UpdateModel", &models[0], []string{}, false, "")

	codexModel := Model{
		ID: "codex-reasoning", Name: "Codex", ModelID: "gpt-codex", Provider: "codex",
		BaseURL: "https://chatgpt.example", APIKey: "token", RefreshToken: "refresh", Endpoint: "/codex/responses", Enabled: true,
	}
	if err := db.CreateModel(&codexModel); err != nil {
		t.Fatal(err)
	}
	codexModel.SupportedReasoningEfforts, codexModel.DisableReasoningField = []string{"high", "max"}, true
	if err := db.UpdateModelPreserveCodexCredential(&codexModel); err != nil {
		t.Fatal(err)
	}
	gotCodex, err := db.GetModel(codexModel.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertReasoningFields("UpdateModelPreserveCodexCredential", gotCodex, []string{"high", "max"}, true, "")
}

func TestRequestUsageStoresRequestedWireAndSource(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InsertRequestUsage(&RequestUsage{
		ModelID: "claude", Provider: "anthropic", StatusCode: 200, Success: true,
		ReasoningEffort: "high", RequestedReasoningEffort: "xhigh", ReasoningEffortSource: "group",
	}); err != nil {
		t.Fatal(err)
	}
	items, _, err := db.QueryRequestUsage("", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ReasoningEffort != "high" || items[0].RequestedReasoningEffort != "xhigh" || items[0].ReasoningEffortSource != "group" {
		t.Fatalf("stored reasoning decision = %+v", items)
	}
}
