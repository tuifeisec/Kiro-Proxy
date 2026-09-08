package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResponsesSessionGenerationRejectsOutOfOrderCompletion(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := "test"
	_, a, err := db.BeginResponsesSession(key)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := db.BeginResponsesSession(key)
	if err != nil || b <= a {
		t.Fatalf("a=%d b=%d err=%v", a, b, err)
	}
	ok, err := db.CommitResponsesSession(&ResponsesSession{StateKey: key, ResponseID: "new", InputJSON: `[{"role":"user"}]`, ItemCount: 1}, b)
	if err != nil || !ok {
		t.Fatalf("new commit=%v err=%v", ok, err)
	}
	ok, err = db.CommitResponsesSession(&ResponsesSession{StateKey: key, ResponseID: "old", InputJSON: `[]`}, a)
	if err != nil || ok {
		t.Fatalf("stale commit=%v err=%v", ok, err)
	}
	if err := db.InvalidateResponsesSession(key, a); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetResponsesSession(key)
	if err != nil || got.ResponseID != "new" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestResponsesSessionReadTTLAndOversizedSnapshot(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.UpsertResponsesSession(&ResponsesSession{StateKey: "expired", ResponseID: "old", InputJSON: `[]`, UpdatedAt: time.Now().Add(-ResponsesSessionTTL - time.Second)}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetResponsesSession("expired")
	if err != nil || got != nil {
		t.Fatalf("expired=%+v err=%v", got, err)
	}
	db.responsesSessionCleanupAt.Store(0)
	_, gen, err := db.BeginResponsesSession("large")
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM responses_sessions WHERE conversation_id='expired'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("runtime cleanup count=%d err=%v", count, err)
	}
	ok, err := db.CommitResponsesSession(&ResponsesSession{StateKey: "large", ResponseID: "large", InputJSON: strings.Repeat("x", MaxResponsesSessionBytes+1)}, gen)
	if err != nil || ok {
		t.Fatalf("oversized=%v err=%v", ok, err)
	}
}

func TestResponsesSessionDeletionDoesNotReuseInFlightGeneration(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, old, err := db.BeginResponsesSession("key")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteResponsesSession("key"); err != nil {
		t.Fatal(err)
	}
	_, current, err := db.BeginResponsesSession("key")
	if err != nil || current <= old {
		t.Fatalf("old=%d current=%d err=%v", old, current, err)
	}
	if ok, err := db.CommitResponsesSession(&ResponsesSession{StateKey: "key", ResponseID: "new", InputJSON: `[]`}, current); err != nil || !ok {
		t.Fatalf("new write after deletion: %v %v", ok, err)
	}
	if ok, err := db.CommitResponsesSession(&ResponsesSession{StateKey: "key", ResponseID: "old", InputJSON: `[]`}, old); err != nil || ok {
		t.Fatalf("stale write after deletion: %v %v", ok, err)
	}
	if err := db.InvalidateResponsesSession("key", old); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetResponsesSession("key")
	if err != nil || got == nil || got.ResponseID != "new" {
		t.Fatalf("stale invalidation after deletion: %+v %v", got, err)
	}
}
