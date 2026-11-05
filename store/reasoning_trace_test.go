package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestReasoningTraceRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "reasoning.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	want := &ReasoningTrace{
		StateKey: "state", TurnKey: "turn", Protocol: "responses", Text: "think",
		ItemID: "item_1", Status: "completed", WireField: "reasoning_text", UpdatedAt: time.Now(),
	}
	if err := db.UpsertReasoningTrace(want); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListReasoningTraces("state")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != want.Text || got[0].ItemID != want.ItemID || got[0].WireField != want.WireField {
		t.Fatalf("stored traces = %+v", got)
	}
}

func TestReasoningTraceBlocksRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "reasoning.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	blocks := []ReasoningTraceBlock{
		{Type: "thinking", Text: "first", Signature: "sig-1"},
		{Type: "redacted_thinking", Data: "sealed"},
		{Type: "thinking", Signature: "sig-2"},
	}
	if err := db.UpsertReasoningTrace(&ReasoningTrace{StateKey: "state", TurnKey: "turn", Protocol: "anthropic", Blocks: blocks, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListReasoningTraces("state")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Blocks) != len(blocks) {
		t.Fatalf("stored traces = %+v", got)
	}
	for i := range blocks {
		if got[0].Blocks[i] != blocks[i] {
			t.Fatalf("block %d = %+v, want %+v", i, got[0].Blocks[i], blocks[i])
		}
	}
}

func TestReasoningTraceLegacyRowsSurviveBlocksMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacyDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// 加 blocks_json 之前的表结构与一行旧数据
	if _, err := legacyDB.Exec(`
CREATE TABLE reasoning_traces (
    state_key    TEXT NOT NULL,
    turn_key     TEXT NOT NULL,
    protocol     TEXT NOT NULL DEFAULT '',
    text         TEXT NOT NULL DEFAULT '',
    signature    TEXT NOT NULL DEFAULT '',
    item_id      TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT '',
    wire_field   TEXT NOT NULL DEFAULT '',
    updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (state_key, turn_key)
);
INSERT INTO reasoning_traces (state_key, turn_key, protocol, text, signature, updated_at)
VALUES ('state', 'turn', 'anthropic', 'legacy thought', 'legacy-sig', ?);
`, time.Now().Format(time.RFC3339Nano)); err != nil {
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
	got, err := db.ListReasoningTraces("state")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "legacy thought" || got[0].Signature != "legacy-sig" || len(got[0].Blocks) != 0 {
		t.Fatalf("legacy trace after migration = %+v", got)
	}
}
