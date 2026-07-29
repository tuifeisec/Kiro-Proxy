package test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"kiro-proxy/store"

	_ "modernc.org/sqlite"
)

func TestModelGroupMigrationPreservesLegacyData(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-model-groups.db")
	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
		CREATE TABLE model_groups (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		INSERT INTO model_groups (id, name, enabled) VALUES ('legacy-group', 'Legacy Group', 1);
		CREATE TABLE request_usage (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			request_date TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT ''
		);
	`)
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	group, err := db.GetModelGroup("legacy-group")
	if err != nil {
		t.Fatal(err)
	}
	if group.Name != "Legacy Group" || group.FirstByteTimeoutMS != 10000 || group.MaxAttempts != 4 {
		t.Fatalf("legacy group was not preserved with dispatch defaults: %+v", group)
	}

	for _, column := range []string{
		"requested_model",
		"route_group_id",
		"selected_model_config_id",
		"failover_count",
		"dispatch_reason",
		"failover_reason",
		"capacity_wait_ms",
	} {
		if !sqliteColumnExists(t, dbPath, "request_usage", column) {
			t.Fatalf("request_usage migration did not add column %q", column)
		}
	}
}

func TestModelImageInputModePersistsAndDefaultsToAuto(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "model-image-capability.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	auto := &store.Model{
		Name: "Auto", ModelID: "auto", Provider: "openai", BaseURL: "https://example.test",
		APIKey: "test", Endpoint: "/v1/responses", Enabled: true,
	}
	if err := db.CreateModel(auto); err != nil {
		t.Fatal(err)
	}
	loadedAuto, err := db.GetModel(auto.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedAuto.ImageInputMode != store.ImageInputAuto {
		t.Fatalf("empty image mode should normalize to auto, got %q", loadedAuto.ImageInputMode)
	}

	loadedAuto.ImageInputMode = store.ImageInputUnsupported
	if err := db.UpdateModel(loadedAuto); err != nil {
		t.Fatal(err)
	}
	loadedUnsupported, err := db.GetModel(auto.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedUnsupported.ImageInputMode != store.ImageInputUnsupported {
		t.Fatalf("image mode was not persisted: %q", loadedUnsupported.ImageInputMode)
	}
	if !sqliteColumnExists(t, dbPath, "models", "image_input_mode") {
		t.Fatal("models migration did not add image_input_mode")
	}
}

func sqliteColumnExists(t *testing.T, dbPath, table, column string) bool {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	rows, err := conn.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return false
}
