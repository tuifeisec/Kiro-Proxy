package router

import (
	"fmt"
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

// This fixture is entirely in-memory; dispatch is represented by assigning the
// selected candidate before RecordAutoDispatch, with no upstream requests.
func autoGroupPolicyFixture(t *testing.T) (*Router, *converter.ParsedConversation) {
	t.Helper()
	models := []store.Model{routerTestModel("first", "First"), routerTestModel("second", "Second"), routerTestModel("backup", "Backup")}
	for i := range models {
		models[i].ReasoningEffort = "high"
		models[i].ImageInputMode = store.ImageInputSupported
		models[i].ContextWindow = 128000
		models[i].MaxTokens = 8000
	}
	models[2].ContextWindow, models[2].MaxTokens = 32000, 2000
	groups := []store.ModelGroup{
		{ID: "gpt", Name: "GPT", Enabled: true, FirstByteTimeoutMS: 10000, MaxAttempts: 3, FallbackGroupID: "fallback",
			Members: []store.ModelGroupMember{{ModelID: "first", Priority: 1, MaxConcurrency: 2}, {ModelID: "second", Priority: 2, MaxConcurrency: 4}}},
		{ID: "fallback", Name: "Fallback", Enabled: true, FirstByteTimeoutMS: 20000, MaxAttempts: 2,
			Members: []store.ModelGroupMember{{ModelID: "backup", Priority: 1, MaxConcurrency: 1}}},
	}
	config := store.AutoRoutingConfig{Version: 2, Mode: store.AutoRoutingModeDutyFixed, Layout: store.AutoRoutingLayoutStandard,
		Enabled: true, DefaultModelID: "gpt", DutyModels: map[string]store.AutoDutyTarget{}}
	for _, duty := range store.AutoDutyNames(config.Layout) {
		config.DutyModels[duty] = store.AutoDutyTarget{ModelID: "gpt", ReasoningEffort: "medium"}
	}
	r := &Router{models: models, groups: groups, autoConfig: config}
	if err := ValidateAutoRoutingConfig(config, models, groups); err != nil {
		t.Fatal(err)
	}
	conv := &converter.ParsedConversation{ModelID: "auto", ConversationID: "conversation", WorkflowMetadataVersion: 2,
		WorkflowTaskType: "implementation", WorkflowExecutionID: "execution-1"}
	return r, conv
}

func TestAutoGroupDispatchRetainsStagesAndRecordsOnlyActualCandidate(t *testing.T) {
	r, conv := autoGroupPolicyFixture(t)
	route, err := r.ResolveAuto(conv)
	if err != nil || route.Group == nil || route.Group.ID != "gpt" || len(route.Candidates) != 3 || len(route.Stages) != 2 ||
		route.CandidateLimits["second"] != 4 || route.CandidateGroups["backup"].ID != "fallback" ||
		route.AutoEffort != "medium" || !route.AutoEffortFixed || route.AutoChannelIdentity != "" {
		t.Fatalf("group route = %#v, %v", route, err)
	}
	if got := r.GetAutoObservations(); len(got) != 0 {
		t.Fatalf("candidate recorded before dispatch: %#v", got)
	}
	route.Model = route.Candidates[2]
	r.RecordAutoDispatch(conv, route)
	got := r.GetAutoObservations()
	if len(got) != 1 || got[0].TargetGroupID != "gpt" || got[0].TargetGroupName != "GPT" || got[0].ModelConfigID != "backup" ||
		got[0].OutboundModel != route.Model.ModelID || got[0].ChannelIdentity != autoChannelIdentity(route.Model) {
		t.Fatalf("actual dispatch observation = %#v", got)
	}
	lock := r.autoLocks[autoExecutionLockKey(conv)]
	if lock.ModelID != "gpt" || lock.GroupID != "gpt" || lock.GroupFingerprint == "" || lock.ChannelIdentity != "" {
		t.Fatalf("group lock became member lock: %#v", lock)
	}
	r.autoConfig.DutyModels["implementation"] = store.AutoDutyTarget{ModelID: "first", ReasoningEffort: "low"}
	continued, err := r.ResolveAuto(conv)
	if err != nil || continued.Group == nil || len(continued.Candidates) != 3 || continued.AutoEffort != "medium" || continued.AutoChannelIdentity != "" {
		t.Fatalf("execution did not restore the group: %#v, %v", continued, err)
	}
	conv.WorkflowExecutionID = "execution-2"
	next, err := r.ResolveAuto(conv)
	if err != nil || next.Group != nil || next.Model.ID != "first" || next.AutoEffort != "low" {
		t.Fatalf("new execution did not load mapping: %#v, %v", next, err)
	}
}

func TestAutoGroupLocksRejectResourceAndSchedulerChanges(t *testing.T) {
	mutations := map[string]func(*Router){
		"member order": func(r *Router) {
			r.groups[0].Members[0], r.groups[0].Members[1] = r.groups[0].Members[1], r.groups[0].Members[0]
		},
		"priority":        func(r *Router) { r.groups[0].Members[0].Priority++ },
		"limit":           func(r *Router) { r.groups[0].Members[0].MaxConcurrency++ },
		"attempts":        func(r *Router) { r.groups[0].MaxAttempts++ },
		"timeout":         func(r *Router) { r.groups[1].FirstByteTimeoutMS++ },
		"group effort":    func(r *Router) { r.groups[0].ReasoningEffort = "high" },
		"fallback":        func(r *Router) { r.groups[0].FallbackGroupID = "" },
		"channel":         func(r *Router) { r.models[1].BaseURL = "https://changed.invalid" },
		"request tokens":  func(r *Router) { r.models[2].MaxTokens++ },
		"disabled member": func(r *Router) { r.models[1].Enabled = false },
		"disabled group":  func(r *Router) { r.groups[0].Enabled = false },
	}
	for name, mutate := range mutations {
		for _, scope := range []string{"execution", "tool_continuation"} {
			t.Run(name+"/"+scope, func(t *testing.T) {
				r, conv := autoGroupPolicyFixture(t)
				if scope == "tool_continuation" {
					conv.WorkflowExecutionID = ""
				}
				if _, err := r.ResolveAuto(conv); err != nil {
					t.Fatal(err)
				}
				mutate(r)
				conv.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "call"}}
				if route, err := r.ResolveAuto(conv); route != nil || err == nil {
					t.Fatalf("changed group accepted: %#v, %v", route, err)
				}
			})
		}
	}
}

func TestAutoGroupDefaultsMetadataAndFallbackProviderValidation(t *testing.T) {
	for _, item := range []struct{ groupEffort, memberEffort, want string }{
		{"low", "high", "low"}, {"", "high", "high"}, {"", "medium", ""},
	} {
		r, conv := autoGroupPolicyFixture(t)
		r.groups[0].ReasoningEffort = item.groupEffort
		r.models[2].ReasoningEffort = item.memberEffort
		conv.WorkflowTaskType = ""
		route, err := r.ResolveAuto(conv)
		if err != nil || route.AutoEffort != item.want || !route.AutoEffortFixed {
			t.Fatalf("default group effort = %#v, %v, want %q", route, err, item.want)
		}
	}
	r, conv := autoGroupPolicyFixture(t)
	r.autoConfig.DutyModels["implementation"] = store.AutoDutyTarget{ModelID: "gpt", ReasoningEffort: ""}
	route, err := r.ResolveAuto(conv)
	if err != nil || route.AutoEffort != "" || !route.AutoEffortFixed {
		t.Fatalf("empty duty effort=%#v %v", route, err)
	}
	r.models[2].ImageInputMode = store.ImageInputUnsupported
	metadata := r.autoModelLocked()
	if metadata == nil || metadata.ContextWindow != 32000 || metadata.MaxTokens != 2000 || metadata.ImageInputMode != store.ImageInputUnsupported {
		t.Fatalf("group-only metadata omitted fallback: %#v", metadata)
	}
	r.models[2].Provider = "anthropic"
	r.autoConfig.DutyModels["implementation"] = store.AutoDutyTarget{ModelID: "gpt", ReasoningEffort: "xhigh"}
	if err := ValidateAutoRoutingConfig(r.autoConfig, r.models, r.groups); err == nil || !strings.Contains(err.Error(), "backup") {
		t.Fatalf("fallback provider was not checked: %v", err)
	}
}

func TestForgetAutoClearsOnlyTheManualBranchExecutionLock(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), autoPolicyConfig())
	main := autoPolicyConversation()
	main.ConversationID = "shared-session"
	main.ContinuationStateKey = "shared-session"
	main.WorkflowExecutionID = "same-execution-id"
	main.WorkflowStage, main.WorkflowStageProvided = "run", true
	if route, err := r.ResolveAuto(main); err != nil || route.Model.ID != "fast" {
		t.Fatalf("main execution route=(%#v, %v)", route, err)
	}

	branch := *main
	branch.ContinuationStateKey = "branch|shared-session|code"
	branch.WorkflowStage = "implement"
	if route, err := r.ResolveAuto(&branch); err != nil || route.Model.ID != "reasoning" {
		t.Fatalf("branch execution route=(%#v, %v)", route, err)
	}
	mainExecutionKey := autoExecutionLockKey(main)
	branchExecutionKey := autoExecutionLockKey(&branch)
	if mainExecutionKey == branchExecutionKey {
		t.Fatal("execution locks for separate branches share a key")
	}

	// A manual raw request knows the session and branch metadata, but its parser
	// intentionally does not infer a workflow execution ID.
	manual := &converter.ParsedConversation{ConversationID: main.ConversationID, ContinuationStateKey: main.ContinuationStateKey}
	r.ForgetAuto(manual)
	if _, exists := r.autoLocks[mainExecutionKey]; exists {
		t.Fatal("manual model selection left its branch execution lock active")
	}
	if _, exists := r.autoLocks[branchExecutionKey]; !exists {
		t.Fatal("manual model selection cleared another branch execution lock")
	}

	config := autoPolicyConfig()
	config.StageModels.Run = "replacement"
	setAutoPolicyConfig(t, r, config)
	if route, err := r.ResolveAuto(main); err != nil || route.Model.ID != "replacement" {
		t.Fatalf("next Auto after manual model selection reused the stale execution: (%#v, %v)", route, err)
	}
}

func TestNativeContinuationLocksGenerationAndIgnoresLateActivityExecution(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), autoPolicyConfig())
	conv := autoPolicyConversation()
	conv.ConversationID = "native-session"
	conv.AgentMode = "vibe"
	conv.AgentContinuationID = "native-continuation-1"
	conv.AgentContinuationProvenance = converter.AgentContinuationProvenanceNativeRaw
	// The Responses cache keeps its existing conversation-only key for vibe mode.
	conv.ContinuationStateKey = "native-session"
	conv.WorkflowStage, conv.WorkflowStageProvided = "run", true
	conv.WorkflowExecutionID = "observed-execution-1"

	lockKey := autoNativeContinuationLockKey(conv)
	if lockKey == "" || autoExecutionLockKey(conv) != "" || autoScopeKey(conv) != lockKey {
		t.Fatalf("native scope was not separated from activity execution: scope=%q execution=%q", autoScopeKey(conv), autoExecutionLockKey(conv))
	}
	first, err := r.ResolveAuto(conv)
	if err != nil || first.Model.ID != "fast" || first.AutoLockScope != "native_continuation" {
		t.Fatalf("first native continuation route=%+v err=%v", first, err)
	}
	if lock, ok := r.autoLocks[lockKey]; !ok || lock.ScopeKey != lockKey || lock.ExecutionID != "" {
		t.Fatalf("native continuation snapshot was not stored independently: %#v, exists=%t", lock, ok)
	}
	if first.AutoExecutionID != "observed-execution-1" {
		t.Fatalf("activity execution ID was not kept as observation data: route=%+v", first)
	}

	conv.WorkflowStage = "implement"
	conv.WorkflowExecutionID = "observed-execution-2"
	lock := r.autoLocks[lockKey]
	lock.LastUsed = time.Now().Add(-autoLockIdleLimit - time.Minute)
	r.autoLocks[lockKey] = lock
	if got := autoNativeContinuationLockKey(conv); got != lockKey {
		t.Fatalf("late activity changed native key: got %q want %q", got, lockKey)
	}
	continued, err := r.ResolveAuto(conv)
	if err != nil || continued.Model.ID != "fast" || continued.AutoLockScope != "native_continuation" ||
		continued.AutoStage != first.AutoStage || continued.AutoSelectionSource != first.AutoSelectionSource || continued.AutoExecutionID != "observed-execution-2" {
		t.Fatalf("late activity changed locked generation: route=%+v err=%v", continued, err)
	}

	next := *conv
	next.AgentContinuationID = "native-continuation-2"
	next.WorkflowStage = "implement"
	next.WorkflowExecutionID = "observed-execution-3"
	nextRoute, err := r.ResolveAuto(&next)
	if err != nil || nextRoute.Model.ID != "reasoning" || nextRoute.AutoLockScope != "native_continuation" {
		t.Fatalf("new native continuation did not select from current metadata: route=%+v err=%v", nextRoute, err)
	}
	if autoNativeContinuationLockKey(&next) == lockKey {
		t.Fatal("different native continuations shared a lock key")
	}
	otherMode := *conv
	otherMode.AgentMode = "plan"
	if autoNativeContinuationLockKey(&otherMode) == lockKey {
		t.Fatal("different agent modes shared a lock key")
	}
	otherConversation := *conv
	otherConversation.ConversationID = "another-session"
	if autoNativeContinuationLockKey(&otherConversation) == lockKey {
		t.Fatal("different conversations shared a lock key")
	}
	emptyNative := *conv
	emptyNative.AgentContinuationID = ""
	if autoNativeContinuationLockKey(&emptyNative) != "" || autoExecutionLockKey(&emptyNative) != "" {
		t.Fatal("empty native continuation was replaced with an activity execution lock")
	}
	legacyBridge := *conv
	legacyBridge.AgentContinuationProvenance = converter.AgentContinuationProvenanceProxyBridge
	if autoExecutionLockKey(&legacyBridge) == "" {
		t.Fatal("legacy bridge execution-lock behavior was not preserved")
	}

	manual := &converter.ParsedConversation{ConversationID: conv.ConversationID, AgentMode: conv.AgentMode,
		AgentContinuationID: conv.AgentContinuationID, AgentContinuationProvenance: converter.AgentContinuationProvenanceNativeRaw}
	r.ForgetAuto(manual)
	if _, exists := r.autoLocks[lockKey]; exists {
		t.Fatal("manual model selection left the native continuation lock active")
	}
}

func TestAutoGroupImageRoutingAllowsCompatibleOrUnknownMembers(t *testing.T) {
	for _, mode := range []string{store.ImageInputSupported, store.ImageInputAuto, ""} {
		r, conv := autoGroupPolicyFixture(t)
		for i := range r.models {
			r.models[i].ImageInputMode = store.ImageInputUnsupported
		}
		r.models[2].ImageInputMode = mode
		conv.RawResponsesHasImages = true
		if route, err := r.ResolveAuto(conv); err != nil || route.Group == nil || len(route.Candidates) != 3 {
			t.Fatalf("mixed image candidates rejected for %q: %#v, %v", mode, route, err)
		}
	}
	r, conv := autoGroupPolicyFixture(t)
	for i := range r.models {
		r.models[i].ImageInputMode = store.ImageInputUnsupported
	}
	conv.RawResponsesHasImages = true
	if route, err := r.ResolveAuto(conv); route != nil || err == nil {
		t.Fatalf("all unsupported accepted: %#v, %v", route, err)
	}
}

func TestAutoGroupStableOverrideAndToolSnapshotSurviveMappingChanges(t *testing.T) {
	r, conv := autoGroupPolicyFixture(t)
	conv.WorkflowExecutionID = ""
	conv.WorkflowDefinitionID, conv.WorkflowDefinitionRevision, conv.WorkflowNodePath = "def", "rev", "steps/build"
	r.autoConfig.NodeOverrides = []store.AutoNodeOverride{{DefinitionID: "def", DefinitionRevision: "rev", NodePath: "steps/build",
		ModelID: "gpt", ReasoningEffort: ""}}
	route, err := r.ResolveAuto(conv)
	if err != nil || route.Group == nil || route.AutoSelectionSource != "node_override" || route.AutoEffort != "" || !route.AutoEffortFixed {
		t.Fatalf("group override = %#v, %v", route, err)
	}
	r.autoConfig.NodeOverrides[0].ModelID = "first"
	r.autoConfig.NodeOverrides[0].ReasoningEffort = "low"
	r.autoConfig.Enabled = false
	r.autoConfigErr = fmt.Errorf("unrelated draft is invalid")
	conv.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "call"}}
	continued, err := r.ResolveAuto(conv)
	if err != nil || continued.Group == nil || continued.Group.ID != "gpt" || continued.AutoEffort != "" || len(continued.Candidates) != 3 {
		t.Fatalf("existing tool group snapshot = %#v, %v", continued, err)
	}
	conv.WorkflowNodePath = "steps/other"
	if next, err := r.ResolveAuto(conv); next != nil || err == nil {
		t.Fatalf("new boundary ignored invalid config: %#v, %v", next, err)
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

func autoDutyPolicyConfig() store.AutoRoutingConfig {
	targets := make(map[string]store.AutoDutyTarget)
	for _, duty := range store.AutoDutyNames(store.AutoRoutingLayoutStandard) {
		targets[duty] = store.AutoDutyTarget{ModelID: "task", ReasoningEffort: "high"}
	}
	targets["investigation"] = store.AutoDutyTarget{ModelID: "fast", ReasoningEffort: "low"}
	targets["design"] = store.AutoDutyTarget{ModelID: "mode", ReasoningEffort: "medium"}
	targets["implementation"] = store.AutoDutyTarget{ModelID: "task", ReasoningEffort: "high"}
	return store.AutoRoutingConfig{Version: store.AutoRoutingVersion, Mode: store.AutoRoutingModeDutyFixed,
		Layout: store.AutoRoutingLayoutStandard, Enabled: true, DefaultModelID: "default", DutyModels: targets,
		NodeBindings: []store.AutoNodeBinding{{DefinitionID: "workflow", DefinitionRevision: "rev-1", NodePath: "steps/investigate", Duty: "investigation"},
			{DefinitionID: "workflow", DefinitionRevision: "rev-1", NodePath: "steps/other", Duty: "design"}},
		NodeOverrides: []store.AutoNodeOverride{{DefinitionID: "workflow", DefinitionRevision: "rev-1", NodePath: "steps/investigate", ModelID: "replacement", ReasoningEffort: "max"}}}
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
	main.WorkflowStage = "run"
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
	fallback.WorkflowStage = "run"
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

func TestDutyFixedUsesExactBindingOverridePriorityAndExplicitTaskType(t *testing.T) {
	models := autoPolicyModels()
	models[0].ReasoningEffort = "medium"
	r := newAutoPolicyTestRouter(t, models, autoDutyPolicyConfig())

	conv := autoPolicyConversation()
	conv.WorkflowMetadataVersion = 2
	conv.WorkflowDefinitionID, conv.WorkflowDefinitionRevision, conv.WorkflowNodePath = "workflow", "rev-1", "steps/investigate"
	conv.WorkflowNodeID = "node-name-is-not-a-duty"
	conv.WorkflowTaskType = "design"
	conv.ReasoningEffort = "low"
	route, err := r.ResolveAuto(conv)
	if err != nil || route.Model.ID != "replacement" || route.AutoSelectionSource != "node_override" ||
		route.AutoDuty != "investigation" || route.AutoEffort != "max" || !route.AutoEffortFixed {
		t.Fatalf("exact node override did not win: route=%+v err=%v", route, err)
	}

	config := autoDutyPolicyConfig()
	config.NodeOverrides = []store.AutoNodeOverride{}
	setAutoPolicyConfig(t, r, config)
	route, err = r.ResolveAuto(conv)
	if err != nil || route.Model.ID != "fast" || route.AutoSelectionSource != "node_binding" ||
		route.AutoDuty != "investigation" || route.AutoEffort != "low" {
		t.Fatalf("stable node binding did not beat taskType: route=%+v err=%v", route, err)
	}

	bareNode := autoPolicyConversation()
	bareNode.WorkflowMetadataVersion = 2
	bareNode.WorkflowNodeID = "investigate"
	route, err = r.ResolveAuto(bareNode)
	if err != nil || route.Model.ID != "default" || route.AutoSelectionSource != "default" ||
		route.AutoUnknownReason != "stable_identity_unavailable" {
		t.Fatalf("bare node id was guessed into a duty: route=%+v err=%v", route, err)
	}
	observations := r.GetAutoObservations()
	foundExact := false
	for _, observation := range observations {
		if observation.DefinitionID == "workflow" && observation.NodePath == "steps/investigate" &&
			observation.ModelConfigID == "fast" && observation.EffectiveEffort == "low" {
			foundExact = true
		}
	}
	if !foundExact {
		t.Fatalf("stable node identity or effective effort missing from observations: %#v", observations)
	}
	longIdentity := autoPolicyConversation()
	longIdentity.WorkflowMetadataVersion = 2
	longIdentity.WorkflowDefinitionID = strings.Repeat("d", 1024)
	longIdentity.WorkflowDefinitionRevision = strings.Repeat("r", 256)
	longIdentity.WorkflowNodePath = strings.Repeat("p", 1024)
	longIdentity.WorkflowTaskType = "implementation"
	if _, err := r.ResolveAuto(longIdentity); err != nil {
		t.Fatal(err)
	}
	observations = r.GetAutoObservations()
	foundLongIdentity := false
	for _, observation := range observations {
		if observation.DefinitionID == longIdentity.WorkflowDefinitionID &&
			observation.DefinitionRevision == longIdentity.WorkflowDefinitionRevision && observation.NodePath == longIdentity.WorkflowNodePath {
			foundLongIdentity = true
		}
	}
	if !foundLongIdentity {
		t.Fatal("observation shortened a valid stable identity")
	}
}

func TestDutyFixedExecutionSnapshotSurvivesMappingReloadAndIdleButChecksResources(t *testing.T) {
	models := autoPolicyModels()
	models[0].ReasoningEffort = "medium"
	config := autoDutyPolicyConfig()
	r := newAutoPolicyTestRouter(t, models, config)
	conv := autoPolicyConversation()
	conv.WorkflowMetadataVersion = 2
	conv.WorkflowTaskType = "implementation"
	conv.WorkflowExecutionID = "execution-1"
	first, err := r.ResolveAuto(conv)
	if err != nil || first.Model.ID != "task" || first.AutoEffort != "high" || first.AutoLockScope != "execution" {
		t.Fatalf("initial execution route=%+v err=%v", first, err)
	}

	lockKey := autoExecutionLockKey(conv)
	lock := r.autoLocks[lockKey]
	lock.LastUsed = time.Now().Add(-autoLockIdleLimit - time.Minute)
	r.autoLocks[lockKey] = lock
	config.DutyModels["implementation"] = store.AutoDutyTarget{ModelID: "replacement", ReasoningEffort: "max"}
	setAutoPolicyConfig(t, r, config)
	conv.ReasoningEffort = "low"
	locked, err := r.ResolveAuto(conv)
	if err != nil || locked.Model.ID != "task" || locked.AutoEffort != "high" || locked.AutoEffortFixed != true {
		t.Fatalf("active execution did not retain its idle target snapshot: route=%+v err=%v", locked, err)
	}

	next := *conv
	next.WorkflowExecutionID = "execution-2"
	newRoute, err := r.ResolveAuto(&next)
	if err != nil || newRoute.Model.ID != "replacement" || newRoute.AutoEffort != "max" {
		t.Fatalf("new execution did not load the updated mapping: route=%+v err=%v", newRoute, err)
	}

	model, err := r.db.GetModel("task")
	if err != nil {
		t.Fatal(err)
	}
	model.BaseURL = "https://changed.example.invalid"
	if err := r.db.UpdateModel(model); err != nil {
		t.Fatal(err)
	}
	r.Reload()
	if got, err := r.ResolveAuto(conv); got != nil || err == nil || !strings.Contains(err.Error(), "changed channel identity") {
		t.Fatalf("changed channel identity did not fail the active execution: route=%+v err=%v", got, err)
	}
}

func TestDutyFixedToolLockDoesNotLeakIntoNewExecutionOrDifferentDeclaredBoundary(t *testing.T) {
	models := autoPolicyModels()
	models[0].ReasoningEffort = "medium"
	config := autoDutyPolicyConfig()
	config.NodeOverrides = []store.AutoNodeOverride{}
	r := newAutoPolicyTestRouter(t, models, config)

	toolTurn := autoPolicyConversation()
	toolTurn.ConversationID = "tool-scope"
	toolTurn.ContinuationStateKey = "tool-scope"
	toolTurn.WorkflowMetadataVersion = 2
	toolTurn.WorkflowTaskType = "investigation"
	toolTurn.WorkflowDefinitionID, toolTurn.WorkflowDefinitionRevision, toolTurn.WorkflowNodePath = "workflow", "rev-1", "steps/investigate"
	first, err := r.ResolveAuto(toolTurn)
	if err != nil || first.Model.ID != "fast" || first.AutoLockScope != "tool_continuation" {
		t.Fatalf("tool-scope route=%+v err=%v", first, err)
	}
	toolTurn.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "call-1"}}
	if _, err := r.ResolveAuto(toolTurn); err != nil {
		t.Fatal(err)
	}

	newExecution := *toolTurn
	newExecution.WorkflowExecutionID = "fresh-execution"
	newExecution.WorkflowTaskType = "design"
	newExecution.WorkflowDefinitionID = ""
	newExecution.WorkflowDefinitionRevision = ""
	newExecution.WorkflowNodePath = ""
	newExecution.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "call-2"}}
	route, err := r.ResolveAuto(&newExecution)
	if err != nil || route.Model.ID != "mode" || route.AutoLockScope != "execution" {
		t.Fatalf("new execution reused an older tool lock: route=%+v err=%v", route, err)
	}

	continued := autoPolicyConversation()
	continued.ConversationID = "changed-duty"
	continued.ContinuationStateKey = "changed-duty"
	continued.WorkflowMetadataVersion = 2
	continued.WorkflowTaskType = "investigation"
	continued.WorkflowDefinitionID, continued.WorkflowDefinitionRevision, continued.WorkflowNodePath = "workflow", "rev-1", "steps/unbound"
	if _, err := r.ResolveAuto(continued); err != nil {
		t.Fatal(err)
	}
	continued.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "call-3"}}
	continued.WorkflowTaskType = "design"
	continuedRoute, err := r.ResolveAuto(continued)
	if err != nil || continuedRoute.Model.ID != "mode" || continuedRoute.AutoDuty != "design" {
		t.Fatalf("explicit duty change inherited the previous tool lock: route=%+v err=%v", continuedRoute, err)
	}
}

func TestDutyFixedToolLockWithoutNodeIdentityKeepsSnapshotWhenMetadataDisappears(t *testing.T) {
	models := autoPolicyModels()
	models[0].ReasoningEffort = "medium"
	config := autoDutyPolicyConfig()
	config.DutyModels["requirements"] = store.AutoDutyTarget{ModelID: "vision", ReasoningEffort: "medium"}
	r := newAutoPolicyTestRouter(t, models, config)

	conv := autoPolicyConversation()
	conv.ConversationID = "metadata-disappeared"
	conv.ContinuationStateKey = "metadata-disappeared"
	conv.WorkflowMetadataVersion = 2
	conv.WorkflowTaskType = "investigation"
	first, err := r.ResolveAuto(conv)
	if err != nil || first.Model.ID != "fast" || first.AutoDuty != "investigation" || first.AutoLockScope != "tool_continuation" {
		t.Fatalf("initial tool target=%+v err=%v", first, err)
	}
	lock := r.autoLocks[conv.ContinuationStateKey]
	if lock.NodeIdentity != "" || lock.TaskType != "investigation" {
		t.Fatalf("test requires a taskType-only tool lock: %+v", lock)
	}

	config.DutyModels["investigation"] = store.AutoDutyTarget{ModelID: "mode", ReasoningEffort: "medium"}
	setAutoPolicyConfig(t, r, config)
	disableAutoPolicyModel(t, r, "vision")
	conv.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "tool-call"}}
	conv.WorkflowTaskType = ""
	locked, err := r.ResolveAuto(conv)
	if err != nil || locked.Model.ID != first.Model.ID || locked.AutoDuty != first.AutoDuty ||
		locked.AutoEffort != first.AutoEffort || locked.AutoReason != first.AutoReason {
		t.Fatalf("missing metadata or mapping reload changed the active tool snapshot: route=%+v err=%v", locked, err)
	}
}

func TestDutyFixedToolLockSurvivesDisabledOrChangedRoutingConfigUntilBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
	}{
		{name: "disabled"},
		{name: "mode changed", mode: store.AutoRoutingModeStageFixed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			models := autoPolicyModels()
			config := autoDutyPolicyConfig()
			r := newAutoPolicyTestRouter(t, models, config)
			conv := autoPolicyConversation()
			conv.ConversationID, conv.ContinuationStateKey = "config-change", "config-change"
			conv.WorkflowMetadataVersion, conv.WorkflowTaskType = 2, "investigation"
			first, err := r.ResolveAuto(conv)
			if err != nil || first.Model.ID != "fast" {
				t.Fatalf("initial route=%+v err=%v", first, err)
			}

			if tc.mode == store.AutoRoutingModeStageFixed {
				stageConfig := stagePolicyConfig()
				stageConfig.StageModels.Plan = "replacement"
				setAutoPolicyConfig(t, r, stageConfig)
			} else {
				config.Enabled = false
				setAutoPolicyConfig(t, r, config)
			}
			conv.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "tool-call"}}
			locked, err := r.ResolveAuto(conv)
			if err != nil || locked.Model.ID != first.Model.ID || locked.AutoDuty != first.AutoDuty {
				t.Fatalf("configuration change replaced a matching active snapshot: route=%+v err=%v", locked, err)
			}

			conv.WorkflowTaskType = "design"
			if tc.mode == store.AutoRoutingModeStageFixed {
				conv.WorkflowMetadataVersion = 1
				conv.WorkflowStage, conv.WorkflowStageProvided = "plan", true
			}
			next, err := r.ResolveAuto(conv)
			if tc.mode == store.AutoRoutingModeStageFixed {
				if err != nil || next.Model.ID != "replacement" || next.AutoReason != "tool_continuation_unlocked:stage:plan" {
					t.Fatalf("new boundary did not use the active stage configuration: route=%+v err=%v", next, err)
				}
			} else if err != nil || next.Model.ID != "default" || next.AutoReason != "policy_disabled" {
				t.Fatalf("new boundary did not honor the disabled policy: route=%+v err=%v", next, err)
			}
		})
	}
}

func TestDutyFixedNewToolBoundariesRejectInvalidConfiguration(t *testing.T) {
	for _, boundary := range []string{"task type", "node identity", "execution id"} {
		t.Run(boundary, func(t *testing.T) {
			models := autoPolicyModels()
			config := autoDutyPolicyConfig()
			config.DutyModels["requirements"] = store.AutoDutyTarget{ModelID: "vision", ReasoningEffort: "medium"}
			r := newAutoPolicyTestRouter(t, models, config)
			conv := autoPolicyConversation()
			conv.ConversationID, conv.ContinuationStateKey = "invalid-config-"+strings.ReplaceAll(boundary, " ", "-"), "invalid-config-"+strings.ReplaceAll(boundary, " ", "-")
			conv.WorkflowMetadataVersion, conv.WorkflowTaskType = 2, "investigation"
			if boundary == "node identity" {
				conv.WorkflowDefinitionID, conv.WorkflowDefinitionRevision, conv.WorkflowNodePath = "workflow", "rev-1", "steps/unbound"
			}
			if first, err := r.ResolveAuto(conv); err != nil || first.Model.ID != "fast" {
				t.Fatalf("initial route=%+v err=%v", first, err)
			}
			disableAutoPolicyModel(t, r, "vision")
			conv.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "tool-call"}}
			switch boundary {
			case "task type":
				conv.WorkflowTaskType = "design"
			case "node identity":
				conv.WorkflowNodePath = "steps/changed"
			case "execution id":
				conv.WorkflowExecutionID = "fresh-execution"
			}
			if route, err := r.ResolveAuto(conv); route != nil || err == nil || !strings.Contains(err.Error(), "auto routing configuration is invalid") {
				t.Fatalf("new %s boundary did not reject invalid configuration: route=%+v err=%v", boundary, route, err)
			}
			if boundary != "execution id" {
				if _, ok := r.autoLocks[conv.ContinuationStateKey]; ok {
					t.Fatalf("new %s boundary kept the old tool lock", boundary)
				}
			}
		})
	}
}

func TestDutyFixedToolLockStillValidatesLockedTargetResources(t *testing.T) {
	for _, mutation := range []string{"disabled target", "changed channel"} {
		t.Run(mutation, func(t *testing.T) {
			r := newAutoPolicyTestRouter(t, autoPolicyModels(), autoDutyPolicyConfig())
			conv := autoPolicyConversation()
			conv.ConversationID, conv.ContinuationStateKey = "resource-check", "resource-check"
			conv.WorkflowMetadataVersion, conv.WorkflowTaskType = 2, "investigation"
			if first, err := r.ResolveAuto(conv); err != nil || first.Model.ID != "fast" {
				t.Fatalf("initial route=%+v err=%v", first, err)
			}
			model := getAutoPolicyModel(t, r, "fast")
			if mutation == "disabled target" {
				model.Enabled = false
			} else {
				model.BaseURL = "https://changed.example.invalid"
			}
			if err := r.db.UpdateModel(&model); err != nil {
				t.Fatal(err)
			}
			r.Reload()
			conv.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "tool-call"}}
			route, err := r.ResolveAuto(conv)
			want := "changed channel identity"
			if mutation == "disabled target" {
				want = "disabled"
			}
			if route != nil || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("invalid locked target resource was accepted: route=%+v err=%v", route, err)
			}
		})
	}
}

func disableAutoPolicyModel(t *testing.T, r *Router, id string) {
	t.Helper()
	model := getAutoPolicyModel(t, r, id)
	model.Enabled = false
	if err := r.db.UpdateModel(&model); err != nil {
		t.Fatal(err)
	}
	r.Reload()
	if r.autoConfigErr == nil {
		t.Fatalf("disabling mapped model %q did not invalidate the configuration", id)
	}
}

func TestLegacyStageMapsOnlyExplicitMainOrchestrationToRun(t *testing.T) {
	for _, tc := range []struct {
		name, taskType, workflowID, modelID, stage, source, reason, unknown string
	}{
		{name: "explicit orchestration", taskType: "orchestration", modelID: "fast", stage: "run", source: "legacy_orchestration", reason: "stage:run"},
		{name: "missing declaration", modelID: "default", source: "legacy_default", reason: "stage_unavailable", unknown: "legacy_node_identity_unavailable"},
		{name: "other declaration", taskType: "requirements", modelID: "default", source: "legacy_default", reason: "stage_unavailable", unknown: "legacy_node_identity_unavailable"},
		{name: "orchestration inside workflow", taskType: "orchestration", workflowID: "workflow-1", modelID: "default", source: "legacy_default", reason: "stage_unavailable", unknown: "legacy_node_identity_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newAutoPolicyTestRouter(t, autoPolicyModels(), autoPolicyConfig())
			conv := autoPolicyConversation()
			conv.WorkflowMetadataVersion = 2
			conv.WorkflowTaskType = tc.taskType
			conv.WorkflowID = tc.workflowID
			route, err := r.ResolveAuto(conv)
			if err != nil || route.Model.ID != tc.modelID || route.AutoStage != tc.stage ||
				route.AutoSelectionSource != tc.source || route.AutoReason != tc.reason || route.AutoUnknownReason != tc.unknown {
				t.Fatalf("legacy v2 selection did not match its declaration boundary: route=%+v err=%v", route, err)
			}
		})
	}
}

func TestDutyFixedUnknownV2MetadataUsesDefaultAndIgnoresRequestEffort(t *testing.T) {
	models := autoPolicyModels()
	models[0].ReasoningEffort = "medium"
	r := newAutoPolicyTestRouter(t, models, autoDutyPolicyConfig())
	conv := autoPolicyConversation()
	conv.WorkflowMetadataVersion = 1
	conv.WorkflowStage, conv.WorkflowStageProvided = "review", true
	conv.WorkflowTaskType = "design"
	conv.ReasoningEffort = "max"
	route, err := r.ResolveAuto(conv)
	if err != nil || route.Model.ID != "default" || route.AutoUnknownReason != "metadata_v1" ||
		route.AutoEffort != "medium" || !route.AutoEffortFixed {
		t.Fatalf("v1 metadata or request effort changed duty default: route=%+v err=%v", route, err)
	}
}
