package router

import (
	"kiro-proxy/converter"
	"kiro-proxy/store"
	"strings"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

func newAutoPolicyTestRouter(t *testing.T, models []store.Model, config store.AutoRoutingConfig) *Router {
	t.Helper()
	r := newAutoTestRouter(t, models, nil)
	setAutoPolicyConfig(t, r, config)
	return r
}

func setAutoPolicyConfig(t *testing.T, r *Router, config store.AutoRoutingConfig) {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.db.SetSetting(store.AutoRoutingSetting, string(raw)); err != nil {
		t.Fatal(err)
	}
	r.Reload()
}

func autoPolicyConfig() store.AutoRoutingConfig {
	return store.AutoRoutingConfig{
		Mode:           store.AutoRoutingModeStageFixed,
		Enabled:        true,
		DefaultModelID: "default",
		StageModels: store.AutoStageModels{
			Run: "fast", Plan: "task", Implement: "reasoning", Review: "mode", Finalize: "long",
		},
	}
}

func autoPolicyModels() []store.Model {
	models := []store.Model{
		routerTestModel("default", "Default"),
		routerTestModel("vision", "Vision"),
		routerTestModel("reasoning", "Reasoning"),
		routerTestModel("long", "Long context"),
		routerTestModel("fast", "Fast"),
		routerTestModel("task", "Task"),
		routerTestModel("mode", "Mode"),
		routerTestModel("trigger", "Trigger"),
		routerTestModel("replacement", "Replacement"),
	}
	for i := range models {
		models[i].ContextWindow = 128000
		models[i].MaxTokens = 8000
	}
	models[1].ContextWindow = 90000
	models[1].MaxTokens = 6000
	models[1].ImageInputMode = store.ImageInputSupported
	models[2].ContextWindow = 100000
	models[3].ContextWindow = 200000
	models[3].MaxTokens = 12000
	models[4].ContextWindow = 16000
	models[4].MaxTokens = 2000
	models[5].ContextWindow = 64000
	models[5].MaxTokens = 4000
	models[6].ContextWindow = 32000
	models[6].MaxTokens = 3000
	models[7].ContextWindow = 50000
	models[7].MaxTokens = 3500
	models[8].ContextWindow = 11000
	models[8].MaxTokens = 1200
	return models
}

func autoPolicyConversation() *converter.ParsedConversation {
	return &converter.ParsedConversation{ModelID: "auto"}
}

func TestResolveAutoLocksStageTargetAndForgetClearsScope(t *testing.T) {
	config := autoPolicyConfig()
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), config)
	main := autoPolicyConversation()
	main.ConversationID = "conversation"
	main.ContinuationStateKey = "conversation"
	main.WorkflowStage = "run"
	main.WorkflowStageProvided = true

	first, err := r.ResolveAuto(main)
	if err != nil || first.Model.ID != "fast" || first.AutoReason != "stage:run" {
		t.Fatalf("first main request = (%#v, %v)", first, err)
	}

	config.StageModels.Run = "replacement"
	setAutoPolicyConfig(t, r, config)
	main.WorkflowStage = "review"
	main.CurrentToolResults = []converter.ParsedToolResult{{Content: "tool response"}}
	second, err := r.ResolveAuto(main)
	if err != nil || second.Model.ID != "fast" || second.AutoStage != "run" ||
		second.AutoMode != store.AutoRoutingModeStageFixed || second.AutoReason != "conversation_lock:stage:run" {
		t.Fatalf("config reload switched the active tool chain: (%#v, %v)", second, err)
	}

	main.CurrentToolResults = nil
	main.WorkflowStage = "implement"
	newTask, err := r.ResolveAuto(main)
	if err != nil || newTask.Model.ID != "reasoning" || newTask.AutoReason != "stage:implement" {
		t.Fatalf("new task did not use the current stage configuration: (%#v, %v)", newTask, err)
	}

	branch := *main
	branch.ContinuationStateKey = "branch|conversation|review"
	branch.WorkflowStage = "review"
	branchRoute, err := r.ResolveAuto(&branch)
	if err != nil || branchRoute.Model.ID != "mode" {
		t.Fatalf("independent branch shared the main lock: (%#v, %v)", branchRoute, err)
	}

	r.ForgetAuto(main)
	main.WorkflowStage = "plan"
	manualFollowup, err := r.ResolveAuto(main)
	if err != nil || manualFollowup.Model.ID != "task" || manualFollowup.AutoReason != "stage:plan" {
		t.Fatalf("ForgetAuto did not clear the current scope: (%#v, %v)", manualFollowup, err)
	}
	if exact := r.ResolveExact("upstream-fast"); exact == nil || exact.Model.ID != "fast" || exact.AutoReason != "" {
		t.Fatalf("manual exact routing changed: %#v", exact)
	}
}

func TestResolveAutoScopeAndConversationIDFallback(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), autoPolicyConfig())
	first := autoPolicyConversation()
	first.WorkflowStage, first.WorkflowStageProvided = "run", true
	if route, err := r.ResolveAuto(first); err != nil || route.Model.ID != "fast" {
		t.Fatalf("unscoped first route = (%#v, %v)", route, err)
	}
	second := autoPolicyConversation()
	second.WorkflowStage, second.WorkflowStageProvided = "plan", true
	if route, err := r.ResolveAuto(second); err != nil || route.Model.ID != "task" {
		t.Fatalf("unscoped requests should not share a lock: (%#v, %v)", route, err)
	}
	if len(r.autoLocks) != 0 {
		t.Fatalf("empty conversation scope created locks: %#v", r.autoLocks)
	}

	fallback := autoPolicyConversation()
	fallback.ConversationID = "conversation-id-fallback"
	fallback.WorkflowStage, fallback.WorkflowStageProvided = "run", true
	if _, err := r.ResolveAuto(fallback); err != nil {
		t.Fatal(err)
	}
	fallback.WorkflowStage = "plan"
	fallback.CurrentToolResults = []converter.ParsedToolResult{{Content: "tool response"}}
	if route, err := r.ResolveAuto(fallback); err != nil || route.Model.ID != "fast" ||
		route.AutoReason != "conversation_lock:stage:run" {
		t.Fatalf("ConversationID fallback did not retain the stage lock: (%#v, %v)", route, err)
	}
}

func TestResolveAutoExpiredLockAndUnlockedToolContinuation(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), autoPolicyConfig())
	conv := autoPolicyConversation()
	conv.ContinuationStateKey = "expired"
	conv.WorkflowStage, conv.WorkflowStageProvided = "plan", true
	conv.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "expired-tool", Content: "done"}}
	r.autoLocks = map[string]autoModelLock{"expired": {ModelID: "fast", LastUsed: time.Now().Add(-2 * time.Hour)}}

	route, err := r.ResolveAuto(conv)
	if err != nil || route.Model.ID != "task" || route.AutoReason != "tool_continuation_unlocked:stage:plan" {
		t.Fatalf("expired lock was reused: (%#v, %v)", route, err)
	}

	restarted := autoPolicyConversation()
	restarted.ConversationID = "restarted"
	restarted.ContinuationStateKey = "restarted"
	restarted.CurrentToolResults = []converter.ParsedToolResult{{Content: "current result"}}
	route, err = r.ResolveAuto(restarted)
	if err != nil || route.Model.ID != "default" || route.AutoReason != "tool_continuation_unlocked:stage_unavailable" {
		t.Fatalf("cold tool continuation should use the fixed target: (%#v, %v)", route, err)
	}
}

func TestResolveAutoSerializesFirstChoiceWithinOneScope(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), autoPolicyConfig())
	const requestCount = 12
	start := make(chan struct{})
	results := make(chan *Route, requestCount)
	errors := make(chan error, requestCount)
	var workers sync.WaitGroup
	for i := 0; i < requestCount; i++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			conv := autoPolicyConversation()
			conv.ContinuationStateKey = "shared"
			conv.WorkflowStage = "run"
			if index%2 == 0 {
				conv.WorkflowStage = "plan"
			}
			conv.WorkflowStageProvided = true
			conv.CurrentToolResults = []converter.ParsedToolResult{{Content: "tool response"}}
			<-start
			route, err := r.ResolveAuto(conv)
			if err != nil {
				errors <- err
				return
			}
			results <- route
		}(i)
	}
	close(start)
	workers.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	var firstModel string
	for route := range results {
		if firstModel == "" {
			firstModel = route.Model.ID
			continue
		}
		if route.Model.ID != firstModel {
			t.Fatalf("same-scope concurrent requests selected different models: %q and %q", firstModel, route.Model.ID)
		}
	}
	if firstModel == "" {
		t.Fatal("no concurrent routes returned")
	}
}

func TestResolveAutoErrorsWhenLockedModelBecomesUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, *Router)
		want   string
	}{
		{
			name: "disabled",
			change: func(t *testing.T, r *Router) {
				model := getAutoPolicyModel(t, r, "fast")
				model.Enabled = false
				if err := r.db.UpdateModel(&model); err != nil {
					t.Fatal(err)
				}
			},
			want: "disabled",
		},
		{
			name: "deleted",
			change: func(t *testing.T, r *Router) {
				if err := r.db.DeleteModel("fast"); err != nil {
					t.Fatal(err)
				}
			},
			want: "no longer exists",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := autoPolicyConfig()
			r := newAutoPolicyTestRouter(t, autoPolicyModels(), config)
			conv := autoPolicyConversation()
			conv.ContinuationStateKey = "locked"
			conv.WorkflowStage, conv.WorkflowStageProvided = "run", true
			if route, err := r.ResolveAuto(conv); err != nil || route.Model.ID != "fast" {
				t.Fatalf("initial lock route = (%#v, %v)", route, err)
			}

			config.StageModels.Run = "replacement"
			setAutoPolicyConfig(t, r, config)
			test.change(t, r)
			r.Reload()
			conv.CurrentToolResults = []converter.ParsedToolResult{{Content: "tool response"}}
			if route, err := r.ResolveAuto(conv); err == nil || route != nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unavailable locked target should fail without switching: route=%#v err=%v", route, err)
			}
		})
	}
}

func TestResolveAutoDisabledUsesDefaultRouteAndClearsLock(t *testing.T) {
	models := autoPolicyModels()
	group := autoTestGroup("legacy-auto", "Auto", "default", "fast")
	r := newAutoTestRouter(t, models, []store.ModelGroup{group})
	config := autoPolicyConfig()
	setAutoPolicyConfig(t, r, config)
	conv := autoPolicyConversation()
	conv.ContinuationStateKey = "disabled-policy"
	conv.WorkflowStage, conv.WorkflowStageProvided = "run", true
	if route, err := r.ResolveAuto(conv); err != nil || route.Model.ID != "fast" {
		t.Fatalf("initial policy route = (%#v, %v)", route, err)
	}

	config.Enabled = false
	setAutoPolicyConfig(t, r, config)
	route, err := r.ResolveAuto(conv)
	if err != nil || route.AutoReason != "policy_disabled" || route.Group == nil || len(route.Candidates) != 2 {
		t.Fatalf("disabled policy should retain default group routing: (%#v, %v)", route, err)
	}
	if _, ok := r.autoLocks["disabled-policy"]; ok {
		t.Fatal("disabled policy retained an old Auto lock")
	}
	if model := r.AutoModel(); model == nil || model.Name != "Auto（默认路由）" || model.Note != model.Name {
		t.Fatalf("disabled Auto metadata has the wrong label: %#v", model)
	}
}

func TestResolveAutoKeepsMalformedPolicyErrorWhenDisabled(t *testing.T) {
	r := newAutoTestRouter(t, autoPolicyModels(), []store.ModelGroup{
		autoTestGroup("legacy-auto", "Auto", "default", "fast"),
	})
	if err := r.db.SetSetting(store.AutoRoutingSetting, "{"); err != nil {
		t.Fatal(err)
	}
	r.Reload()

	route, err := r.ResolveAuto(autoPolicyConversation())
	if err == nil || route != nil || !strings.Contains(err.Error(), "auto routing configuration is invalid") {
		t.Fatalf("malformed Auto JSON should remain an explicit routing error: route=%#v err=%v", route, err)
	}
	if model := r.AutoModel(); model != nil {
		t.Fatalf("malformed Auto JSON must not publish Auto metadata: %#v", model)
	}
}

func TestResolveStageAutoRequiresEnabledStageConfigWithoutLock(t *testing.T) {
	config := autoPolicyConfig()
	config.Enabled = false
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), config)
	if route, err := r.ResolveStageAuto(autoPolicyConversation()); err == nil || route != nil ||
		!strings.Contains(err.Error(), "requires enabled stage_fixed mode") {
		t.Fatalf("raw stage Auto must reject a disabled config: route=%#v err=%v", route, err)
	}
}

func TestAutoModelReturnsNilWhenNoTargetIsAvailable(t *testing.T) {
	r := newAutoTestRouter(t, nil, nil)
	config := autoPolicyConfig()
	config.DefaultModelID = "missing"
	config.StageModels = store.AutoStageModels{Run: "missing", Plan: "missing", Implement: "missing", Review: "missing", Finalize: "missing"}
	setAutoPolicyConfig(t, r, config)
	if model := r.AutoModel(); model != nil {
		t.Fatalf("invalid target metadata should not be published: %#v", model)
	}
}

func getAutoPolicyModel(t *testing.T, r *Router, id string) store.Model {
	t.Helper()
	models, err := r.db.GetModels()
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range models {
		if model.ID == id {
			return model
		}
	}
	t.Fatalf("model %q not found", id)
	return store.Model{}
}
