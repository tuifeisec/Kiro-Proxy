package store

import (
	"path/filepath"
	"testing"
	"time"
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
