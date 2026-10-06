package proxy

import (
	"path/filepath"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"
)

func classifierScopeServer(t *testing.T) *Server {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "classifier-scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, id := range []string{"classifier-fallback", "main-model", "branch-model", "other-model", "preset-model"} {
		if err := db.CreateModel(&store.Model{
			ID: id, Name: id, ModelID: id, Provider: "openai",
			BaseURL: "https://example.invalid", Endpoint: "/chat/completions", Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	return &Server{
		db: db, router: router.New(db),
		conversationModels: make(map[string]conversationModelEntry),
	}
}

func classifierScopePreflight(scope string) *Preflight {
	return &Preflight{
		SourceModel: "classifier-fallback", IsClassifier: true,
		ConversationID: "shared-conversation",
		Conv: &converter.ParsedConversation{
			ConversationID: "shared-conversation", ContinuationStateKey: scope,
		},
	}
}

func TestClassifierMemoryDoesNotCrossScopes(t *testing.T) {
	s := classifierScopeServer(t)
	s.rememberModelName(classifierScopePreflight("main"), "GPT-8.8")
	s.rememberModelName(classifierScopePreflight("branch"), "branch-model")

	mainClassifier := classifierScopePreflight("main")
	branchClassifier := classifierScopePreflight("branch")
	newBranchClassifier := classifierScopePreflight("new-branch")

	if !s.isEasterEggClassifier(mainClassifier) {
		t.Fatal("main-scope GPT-8.8 memory was not recognized")
	}
	if s.isEasterEggClassifier(branchClassifier) || s.isEasterEggClassifier(newBranchClassifier) {
		t.Fatal("GPT-8.8 memory leaked into another or uninitialized scope")
	}

	if route := s.resolveClassifierRoute(branchClassifier); route == nil || route.Model.ID != "branch-model" {
		t.Fatalf("branch classifier route=%v, want branch-model", route)
	}
	if route := s.resolveClassifierRoute(newBranchClassifier); route == nil || route.Model.ID != "classifier-fallback" {
		t.Fatalf("uninitialized branch classifier route=%v, want classifier-fallback", route)
	}

	// 更新全局兼容记忆后，已知与未知 scope 仍只读取各自分区。
	s.rememberModelName(classifierScopePreflight("other"), "other-model")
	if route := s.resolveClassifierRoute(branchClassifier); route == nil || route.Model.ID != "branch-model" {
		t.Fatalf("other-scope model leaked into branch classifier: %v", route)
	}
	if route := s.resolveClassifierRoute(newBranchClassifier); route == nil || route.Model.ID != "classifier-fallback" {
		t.Fatalf("other-scope model leaked into uninitialized branch: %v", route)
	}
	if got := s.mainModelForConversation(""); got != "other-model" {
		t.Fatalf("empty-scope compatibility lookup=%q, want other-model", got)
	}
}

func TestFirstClassifierRequestDoesNotInheritAnotherScope(t *testing.T) {
	tests := []struct {
		name        string
		memoryScope string
		model       string
		targetScope string
	}{
		{name: "main easter egg does not intercept new branch", memoryScope: "main", model: "GPT-8.8", targetScope: "new-branch"},
		{name: "branch easter egg does not intercept new main", memoryScope: "branch", model: "GPT-8.8", targetScope: "main"},
		{name: "main model does not route new branch", memoryScope: "main", model: "main-model", targetScope: "new-branch"},
		{name: "branch model does not route new main", memoryScope: "branch", model: "branch-model", targetScope: "main"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := classifierScopeServer(t)
			s.rememberModelName(classifierScopePreflight(tc.memoryScope), tc.model)
			pre := classifierScopePreflight(tc.targetScope)

			if got := s.mainModelForConversation(tc.targetScope); got != "" {
				t.Fatalf("first lookup for target scope=%q, want empty", got)
			}
			if s.isEasterEggClassifier(pre) {
				t.Fatal("first classifier request inherited another scope's GPT-8.8 state")
			}
			if route := s.resolveClassifierRoute(pre); route == nil || route.Model.ID != "classifier-fallback" {
				t.Fatalf("first classifier route=%v, want classifier-fallback", route)
			}
		})
	}
}

func TestClassifierExpiredScopeDoesNotInheritGlobalModel(t *testing.T) {
	s := classifierScopeServer(t)
	s.lastMainModel = "GPT-8.8"
	s.conversationModels["expired-branch"] = conversationModelEntry{
		modelName: "branch-model", lastSeen: time.Now().Add(-conversationModelTTL - time.Second),
	}
	pre := classifierScopePreflight("expired-branch")

	if got := s.mainModelForConversation("expired-branch"); got != "" {
		t.Fatalf("expired scoped lookup=%q, want empty", got)
	}
	if _, exists := s.conversationModels["expired-branch"]; exists {
		t.Fatal("expired entry was not removed")
	}
	if s.isEasterEggClassifier(pre) {
		t.Fatal("expired scope inherited global GPT-8.8 memory")
	}
	if route := s.resolveClassifierRoute(pre); route == nil || route.Model.ID != "classifier-fallback" {
		t.Fatalf("expired branch classifier route=%v, want classifier-fallback", route)
	}
}

func TestClassifierEvictedScopeDoesNotInheritGlobalModel(t *testing.T) {
	s := classifierScopeServer(t)
	now := time.Now()
	s.conversationPrunedAt = now
	s.conversationModels["evicted-branch"] = conversationModelEntry{
		modelName: "branch-model", lastSeen: now.Add(-time.Minute),
	}
	for i := 0; len(s.conversationModels) < conversationModelMaxItems; i++ {
		s.conversationModels["scope-"+time.Duration(i).String()] = conversationModelEntry{
			modelName: "other-model", lastSeen: now,
		}
	}
	s.lastMainModel = "GPT-8.8"

	s.rememberModelName(classifierScopePreflight("new-scope"), "other-model")
	if _, exists := s.conversationModels["evicted-branch"]; exists {
		t.Fatal("capacity insertion did not evict the oldest scoped entry")
	}
	pre := classifierScopePreflight("evicted-branch")
	if got := s.mainModelForConversation("evicted-branch"); got != "" {
		t.Fatalf("evicted scoped lookup=%q, want empty", got)
	}
	if s.isEasterEggClassifier(pre) {
		t.Fatal("evicted scope inherited global GPT-8.8 memory")
	}
	if route := s.resolveClassifierRoute(pre); route == nil || route.Model.ID != "classifier-fallback" {
		t.Fatalf("evicted branch classifier route=%v, want classifier-fallback", route)
	}
}

func TestClassifierPresetModelKeepsPriorityOverScopedMemory(t *testing.T) {
	s := classifierScopeServer(t)
	if err := s.db.SetSetting("classifier_model_id", "preset-model"); err != nil {
		t.Fatal(err)
	}
	s.rememberModelName(classifierScopePreflight("branch"), "branch-model")

	route := s.resolveClassifierRoute(classifierScopePreflight("branch"))
	if route == nil || route.Model.ID != "preset-model" {
		t.Fatalf("classifier preset route=%v, want preset-model", route)
	}
}
