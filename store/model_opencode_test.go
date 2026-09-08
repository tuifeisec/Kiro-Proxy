package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestModelOpenCodeSessionLegacyMigrationAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.db")
	legacyDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacyDB.Exec(`
CREATE TABLE models (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    model_id TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT 'openai',
    base_url TEXT NOT NULL,
    api_key TEXT NOT NULL,
    refresh_token TEXT NOT NULL DEFAULT '',
    proxy_url TEXT NOT NULL DEFAULT '',
    endpoint TEXT NOT NULL DEFAULT '',
    disable_auto_suffix INTEGER NOT NULL DEFAULT 0,
    context_window INTEGER NOT NULL DEFAULT 0,
    max_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_effort TEXT NOT NULL DEFAULT '',
    image_input_mode TEXT NOT NULL DEFAULT 'auto',
    rate_multiplier REAL NOT NULL DEFAULT 1.0,
    input_price REAL NOT NULL DEFAULT 5.0,
    output_price REAL NOT NULL DEFAULT 30.0,
    cache_input_price REAL NOT NULL DEFAULT 0.5,
    cache_output_price REAL NOT NULL DEFAULT 30.0,
    enable_continuation INTEGER NOT NULL DEFAULT 0,
    codex_email TEXT NOT NULL DEFAULT '',
    codex_plan_type TEXT NOT NULL DEFAULT '',
    codex_account_id TEXT NOT NULL DEFAULT '',
    access_token_expires_at INTEGER NOT NULL DEFAULT 0,
    codex_usage_json TEXT NOT NULL DEFAULT '',
    service_tier TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    sort_order INTEGER NOT NULL DEFAULT 0,
    note TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO models (id, name, model_id, base_url, api_key, endpoint)
VALUES ('legacy', 'legacy-name', 'legacy-model', 'https://legacy.example.test', 'legacy-key', '/v1/chat/completions');
`)
	if err != nil {
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

	var columnType, defaultValue string
	var notNull int
	if err := db.conn.QueryRow(`
SELECT type, dflt_value, "notnull" FROM pragma_table_info('models') WHERE name = ?
`, "enable_opencode_session").Scan(&columnType, &defaultValue, &notNull); err != nil {
		t.Fatal(err)
	}
	if columnType != "INTEGER" || defaultValue != "0" || notNull != 1 {
		t.Fatalf("unexpected enable_opencode_session schema: type=%q default=%q notnull=%d", columnType, defaultValue, notNull)
	}

	legacy, err := db.GetModel("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.EnableOpenCodeSession {
		t.Fatal("legacy model must default enable_opencode_session to false")
	}

	model := Model{
		ID:                    "opencode-roundtrip",
		Name:                  "OpenCode Roundtrip",
		ModelID:               "roundtrip-model",
		Provider:              "openai",
		BaseURL:               "https://example.test",
		APIKey:                "api-key",
		Endpoint:              "/v1/chat/completions",
		EnableOpenCodeSession: true,
		Enabled:               true,
	}
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.EnableOpenCodeSession {
		t.Fatal("GetModel lost enable_opencode_session=true")
	}
	models, err := db.GetModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || !models[1].EnableOpenCodeSession {
		t.Fatalf("GetModels lost enable_opencode_session=true: %#v", models)
	}
	byID, err := db.FindByModelID(model.ModelID)
	if err != nil {
		t.Fatal(err)
	}
	if !byID.EnableOpenCodeSession {
		t.Fatal("FindByModelID lost enable_opencode_session=true")
	}
	byName, err := db.FindModelByName(model.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !byName.EnableOpenCodeSession {
		t.Fatal("FindModelByName lost enable_opencode_session=true")
	}

	model.EnableOpenCodeSession = false
	if err := db.UpdateModel(&model); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EnableOpenCodeSession {
		t.Fatal("UpdateModel failed to persist enable_opencode_session=false")
	}

	codexModel := Model{
		ID:                    "opencode-codex",
		Name:                  "OpenCode Codex",
		ModelID:               "codex-model",
		Provider:              "codex",
		BaseURL:               "https://codex.example.test",
		APIKey:                "access-token",
		RefreshToken:          "refresh-token",
		CodexAccountID:        "account-id",
		Endpoint:              "/codex/responses",
		EnableOpenCodeSession: true,
		Enabled:               true,
	}
	if err := db.CreateModel(&codexModel); err != nil {
		t.Fatal(err)
	}
	codexEdit := codexModel
	codexEdit.APIKey = "stale-access-token"
	codexEdit.RefreshToken = "stale-refresh-token"
	codexEdit.EnableOpenCodeSession = false
	if err := db.UpdateModelPreserveCodexCredential(&codexEdit); err != nil {
		t.Fatal(err)
	}
	savedCodex, err := db.GetModel(codexModel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if savedCodex.EnableOpenCodeSession || savedCodex.APIKey != codexModel.APIKey || savedCodex.RefreshToken != codexModel.RefreshToken {
		t.Fatalf("preserve-credential update did not round-trip flag while preserving credentials: %#v", savedCodex)
	}
}
