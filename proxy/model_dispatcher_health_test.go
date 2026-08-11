package proxy

import (
	"strconv"
	"testing"
	"time"

	"kiro-proxy/store"
)

func TestModelHealthNeutralReleasesHalfOpenSlot(t *testing.T) {
	tracker := newModelHealthTracker()
	model := &store.Model{ID: "model-1", UpdatedAt: "2026-07-28T00:00:00Z"}
	now := time.Now()
	tracker.failure(model, modelFailureCredential, 0, now)

	allowed, state := tracker.allow(model, now.Add(modelOpenDuration))
	if !allowed || state != "half-open" {
		t.Fatalf("expected first half-open probe, got allowed=%v state=%q", allowed, state)
	}
	tracker.neutral(model)

	allowed, state = tracker.allow(model, now.Add(modelOpenDuration))
	if !allowed || state != "half-open" {
		t.Fatalf("expected cancelled probe slot to be reusable, got allowed=%v state=%q", allowed, state)
	}
}

func TestModelAffinityStorePrunesAndCapsEntries(t *testing.T) {
	store := newModelAffinityStore()
	now := time.Now()
	store.set("group", "expired", "model", now.Add(-modelAffinityTTL-time.Second))
	store.set("group", "current", "model", now)
	if len(store.entries) != 1 {
		t.Fatalf("expired affinity was not pruned: %d entries", len(store.entries))
	}

	for i := 0; i < modelAffinityMaxItems; i++ {
		store.set("group", "conversation-"+strconv.Itoa(i), "model", now.Add(time.Duration(i+1)*time.Nanosecond))
	}
	if len(store.entries) != modelAffinityMaxItems {
		t.Fatalf("affinity capacity exceeded: %d", len(store.entries))
	}
	if got := store.get("group", "current", now); got != "" {
		t.Fatalf("oldest affinity was not evicted: %q", got)
	}
}

func TestConversationModelEntriesArePrunedAndCapped(t *testing.T) {
	now := time.Now()
	server := &Server{conversationModels: make(map[string]conversationModelEntry)}
	server.conversationModels["expired"] = conversationModelEntry{modelName: "old", lastSeen: now.Add(-conversationModelTTL - time.Second)}
	server.pruneConversationModelsLocked(now)
	if len(server.conversationModels) != 0 {
		t.Fatal("expired conversation model was not pruned")
	}

	for i := 0; i < conversationModelMaxItems; i++ {
		server.conversationModels["conversation-"+strconv.Itoa(i)] = conversationModelEntry{
			modelName: "model",
			lastSeen:  now.Add(time.Duration(i) * time.Nanosecond),
		}
	}
	server.rememberModelName(&Preflight{ConversationID: "latest"}, "model")
	if len(server.conversationModels) != conversationModelMaxItems {
		t.Fatalf("conversation model capacity exceeded: %d", len(server.conversationModels))
	}
	if _, exists := server.conversationModels["conversation-0"]; exists {
		t.Fatal("oldest conversation model was not evicted")
	}
}

func TestModelHealthSyncRemovesStaleModelRevisions(t *testing.T) {
	tracker := newModelHealthTracker()
	now := time.Now()
	oldModel := &store.Model{ID: "model-1", UpdatedAt: "2026-07-27T00:00:00Z"}
	currentModel := &store.Model{ID: "model-1", UpdatedAt: "2026-07-28T00:00:00Z"}
	deletedModel := &store.Model{ID: "model-2", UpdatedAt: "2026-07-28T00:00:00Z"}
	tracker.failure(oldModel, modelFailureCredential, 0, now)
	tracker.failure(currentModel, modelFailureCredential, 0, now)
	tracker.failure(deletedModel, modelFailureCredential, 0, now)

	tracker.syncModels([]store.Model{*currentModel})

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.states) != 1 {
		t.Fatalf("expected one current health state, got %d", len(tracker.states))
	}
	if _, exists := tracker.states[modelHealthKey(currentModel)]; !exists {
		t.Fatalf("current model health state was removed")
	}
}

func TestCandidateFirstByteTimeoutUsesRemainingBudget(t *testing.T) {
	now := time.Now()
	deadline := now.Add(5 * time.Second)
	if got := candidateFirstByteTimeout(60*time.Second, deadline, now); got != 5*time.Second {
		t.Fatalf("expected remaining budget, got %v", got)
	}
	if got := candidateFirstByteTimeout(2*time.Second, deadline, now); got != 2*time.Second {
		t.Fatalf("expected configured timeout, got %v", got)
	}
	if candidateHasBudget(deadline, deadline) {
		t.Fatal("deadline must not allow another candidate")
	}
}

func TestObservedFirstByteTimeoutAddsGraceBeforeDegradation(t *testing.T) {
	now := time.Now()
	deadline := now.Add(2 * time.Minute)
	if got := observedFirstByteTimeout(10*time.Second, deadline, now, false); got != 20*time.Second {
		t.Fatalf("expected observation grace, got %v", got)
	}
	if got := observedFirstByteTimeout(3*time.Minute, now.Add(10*time.Minute), now, false); got != firstByteGraceLimit {
		t.Fatalf("expected grace cap, got %v", got)
	}
	if got := observedFirstByteTimeout(10*time.Second, deadline, now, true); got != 10*time.Second {
		t.Fatalf("degraded model should use configured timeout, got %v", got)
	}
}
