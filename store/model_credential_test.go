package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateModelPreserveCodexCredentialIsAtomic(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	model := Model{
		ID: "model-a", Name: "A", ModelID: "gpt-test", Provider: "codex",
		BaseURL: "https://example.invalid", Endpoint: "/codex/responses",
		APIKey: "access-old", RefreshToken: "refresh-old", CodexAccountID: "acct",
		AccessTokenExpiresAt: time.Now().Add(time.Hour).Unix(), Enabled: true,
	}
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	rotated := model
	rotated.APIKey = "access-new"
	rotated.RefreshToken = "refresh-new"
	rotated.AccessTokenExpiresAt = time.Now().Add(time.Hour).Unix()
	if err := db.UpdateCodexTokenAndUsage(&rotated); err != nil {
		t.Fatal(err)
	}
	configEdit := model
	configEdit.Note = "edited"
	configEdit.APIKey = "stale-access"
	configEdit.RefreshToken = "stale-refresh"
	if err := db.UpdateModelPreserveCodexCredential(&configEdit); err != nil {
		t.Fatal(err)
	}
	saved, err := db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Note != "edited" || saved.APIKey != "access-new" || saved.RefreshToken != "refresh-new" {
		t.Fatalf("config update overwrote credentials: %#v", saved)
	}
}

func TestCodexCredentialBatchConflictRollsBackAllRows(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "batch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := Model{ID: "a", Provider: "codex", Endpoint: "/codex/responses", APIKey: "old-a", RefreshToken: "old-refresh", CodexAccountID: "account"}
	b := a
	b.ID, b.APIKey = "b", "old-b"
	for _, model := range []*Model{&a, &b} {
		if err := db.CreateModel(model); err != nil {
			t.Fatal(err)
		}
	}
	updatedA, updatedB := a, b
	updatedA.APIKey, updatedB.APIKey = "rotated", "rotated"
	manual := b
	manual.RefreshToken = "manual-import"
	if err := db.UpdateModel(&manual); err != nil {
		t.Fatal(err)
	}
	_, err = db.UpdateCodexTokenBatchCAS([]CodexCredentialUpdate{{Original: a, Updated: updatedA}, {Original: b, Updated: updatedB}})
	if !errors.Is(err, ErrCodexCredentialConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	gotA, err := db.GetModel(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := db.GetModel(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotA.APIKey != a.APIKey || gotB.RefreshToken != manual.RefreshToken {
		t.Fatal("batch partly committed or overwrote manual import")
	}
}
