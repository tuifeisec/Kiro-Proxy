package router

import (
	"kiro-proxy/converter"
	"kiro-proxy/store"
	"strings"
	"testing"
	"time"
)

func stagePolicyConfig() store.AutoRoutingConfig {
	c := autoPolicyConfig()
	c.Mode = store.AutoRoutingModeStageFixed
	c.StageModels = store.AutoStageModels{Run: "fast", Plan: "task", Implement: "reasoning", Review: "mode", Finalize: "long"}
	return c
}

func stageConversation(stage string) *converter.ParsedConversation {
	c := autoPolicyConversation()
	c.ConversationID = "stage-conversation"
	c.ContinuationStateKey = "stage-conversation"
	c.WorkflowStage = stage
	c.WorkflowStageProvided = stage != ""
	c.WorkflowStageSource = "request_header"
	return c
}

func TestStageFixedUsesOnlySynchronousStage(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), stagePolicyConfig())
	for _, tc := range []struct{ stage, model, reason string }{
		{"run", "fast", "stage:run"}, {"plan", "task", "stage:plan"},
		{"implement", "reasoning", "stage:implement"}, {"review", "mode", "stage:review"},
		{"finalize", "long", "stage:finalize"}, {"", "default", "stage_unavailable"},
		{"future", "default", "stage_unknown:future"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			conv := stageConversation(tc.stage)
			conv.AgentTaskType, conv.AgentMode, conv.ChatTriggerType = "spec", "review", "manual"
			conv.ReasoningEffort = "max"
			conv.CurrentContent = strings.Repeat("x", 33000)
			route, err := r.ResolveAuto(conv)
			if err != nil {
				t.Fatal(err)
			}
			if route.Model.ID != tc.model || route.AutoReason != tc.reason || route.AutoMode != store.AutoRoutingModeStageFixed {
				t.Fatalf("unexpected route: %+v", route)
			}
			if len(route.Candidates) != 1 || route.Group != nil || len(route.AutoChannelIdentity) != 64 {
				t.Fatalf("stage route is not a single auditable target: %+v", route)
			}
		})
	}

	missing := stageConversation("plan")
	missing.WorkflowStageProvided = false
	route, err := r.ResolveAuto(missing)
	if err != nil || route.Model.ID != "default" || route.AutoReason != "stage_unavailable" || route.AutoStage != "" {
		t.Fatalf("unprovided stage metadata affected routing: route=%+v err=%v", route, err)
	}
}

func TestStageFixedLegacyFieldsDoNotAffectSelectionOrMetadata(t *testing.T) {
	models := autoPolicyModels()
	models[7].ContextWindow = 1000
	models[7].MaxTokens = 100
	r := newAutoTestRouter(t, models, nil)
	raw := `{"mode":"stage_fixed","enabled":true,"default_model_id":"default","stage_models":{"run":"fast","plan":"task","implement":"reasoning","review":"mode","finalize":"long"},"vision_model_id":"vision","reasoning_model_id":"trigger","long_context_model_id":"trigger","fast_model_id":"trigger","fast_max_chars":1,"long_context_min_chars":2,"task_rules":[{"field":"agent_task_type","value":"spec","model_id":"trigger"}]}`
	if err := r.db.SetSetting(store.AutoRoutingSetting, raw); err != nil {
		t.Fatal(err)
	}
	r.Reload()

	conv := stageConversation("plan")
	conv.AgentTaskType = "spec"
	conv.ReasoningEffort = "max"
	conv.CurrentContent = strings.Repeat("x", 33000)
	route, err := r.ResolveAuto(conv)
	if err != nil || route.Model.ID != "task" || route.AutoReason != "stage:plan" {
		t.Fatalf("retired fields changed fixed stage selection: route=%#v err=%v", route, err)
	}

	model := r.AutoModel()
	if model == nil || model.Name != "Auto（五阶段固定模型）" || model.ContextWindow != 16000 || model.MaxTokens != 2000 ||
		model.ImageInputMode != store.ImageInputUnsupported || model.ReasoningEffort != "" {
		t.Fatalf("retired fields affected Auto metadata: %#v", model)
	}
}

func TestStageFixedImageInputFailsWithoutChangingTarget(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), stagePolicyConfig())
	c := stageConversation("plan")
	c.CurrentImages = []converter.KiroImage{{Source: converter.KiroImageSource{Bytes: "aW1hZ2U="}}}
	if route, err := r.ResolveAuto(c); route != nil || err == nil || !strings.Contains(err.Error(), "does not support image input") {
		t.Fatalf("image mismatch must fail, not select vision: %+v %v", route, err)
	}
}

func TestStageFixedToolLockRejectsNewUnsupportedImage(t *testing.T) {
	config := stagePolicyConfig()
	config.StageModels.Review = "vision"
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), config)
	c := stageConversation("plan")
	first, err := r.ResolveAuto(c)
	if err != nil || first.Model.ID != "task" {
		t.Fatalf("initial stage target = %+v, error=%v", first, err)
	}

	c.WorkflowStage = "plan"
	c.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "tool", Content: "done"}}
	c.CurrentImages = []converter.KiroImage{{Source: converter.KiroImageSource{Bytes: "aW1hZ2U="}}}
	if route, err := r.ResolveAuto(c); route != nil || err == nil ||
		!strings.Contains(err.Error(), `locked to model "task"`) || !strings.Contains(err.Error(), "does not support image input") {
		t.Fatalf("unsupported image should fail the locked target without switching to review's image model: route=%+v err=%v", route, err)
	}
	if lock := r.autoLocks[c.ContinuationStateKey]; lock.ModelID != first.Model.ID || lock.ChannelIdentity != first.AutoChannelIdentity {
		t.Fatalf("image error changed the active stage lock: %+v", lock)
	}

	c.CurrentImages = nil
	if route, err := r.ResolveAuto(c); err != nil || route.Model.ID != first.Model.ID ||
		route.AutoReason != "conversation_lock:stage:plan" {
		t.Fatalf("valid continuation after image failure lost the original target: route=%+v err=%v", route, err)
	}
}

func TestStageFixedToolLockAndNewTaskConfiguration(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), stagePolicyConfig())
	c := stageConversation("plan")
	first, err := r.ResolveAuto(c)
	if err != nil {
		t.Fatal(err)
	}
	config := stagePolicyConfig()
	config.StageModels.Plan = "replacement"
	setAutoPolicyConfig(t, r, config)
	c.WorkflowStage = "plan"
	c.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "tool-1", Content: "done"}}
	locked, err := r.ResolveAuto(c)
	if err != nil || locked.Model.ID != first.Model.ID || locked.AutoStage != "plan" || locked.AutoReason != "conversation_lock:stage:plan" {
		t.Fatalf("tool turn must retain initial stage and target: %+v %v", locked, err)
	}
	branch := *c
	branch.ContinuationStateKey = "independent-branch"
	branch.WorkflowStage = "review"
	branchRoute, err := r.ResolveAuto(&branch)
	if err != nil || branchRoute.Model.ID != "mode" {
		t.Fatalf("branch shared lock: %+v %v", branchRoute, err)
	}
	c.CurrentToolResults = nil
	c.WorkflowStage = "plan"
	c.History = []converter.ParsedHistoryEntry{{ToolResults: []converter.ParsedToolResult{{ToolUseID: "old"}}}}
	next, err := r.ResolveAuto(c)
	if err != nil || next.Model.ID != "replacement" {
		t.Fatalf("new task did not load config: %+v %v", next, err)
	}
}

func TestStageFixedToolContinuationFollowsAllFiveStageBoundaries(t *testing.T) {
	for _, raw := range []bool{false, true} {
		r := newAutoPolicyTestRouter(t, autoPolicyModels(), stagePolicyConfig())
		resolve := r.ResolveAuto
		if raw {
			resolve = r.ResolveStageAuto
		}
		c := stageConversation("run")
		c.WorkflowID = "workflow-1"
		for index, step := range []struct{ stage, model string }{
			{"run", "fast"}, {"plan", "task"}, {"implement", "reasoning"}, {"review", "mode"}, {"finalize", "long"},
		} {
			c.WorkflowStage = step.stage
			if index > 0 {
				c.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "previous-stage-tool", Content: "done"}}
			}
			route, err := resolve(c)
			if err != nil || route.Model.ID != step.model || route.AutoStage != step.stage || strings.HasPrefix(route.AutoReason, "conversation_lock:") {
				t.Fatalf("阶段边界未重新选模 raw=%t stage=%s route=%+v err=%v", raw, step.stage, route, err)
			}
			c.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "current-stage-tool", Content: "done"}}
			locked, err := resolve(c)
			if err != nil || locked.Model.ID != step.model || locked.AutoChannelIdentity != route.AutoChannelIdentity || locked.AutoReason != "conversation_lock:stage:"+step.stage {
				t.Fatalf("同阶段续轮未保持渠道 raw=%t stage=%s route=%+v err=%v", raw, step.stage, locked, err)
			}
		}
	}
}

func TestStageFixedWorkflowBoundaryAndMissingStage(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), stagePolicyConfig())
	c := stageConversation("plan")
	c.WorkflowID = "workflow-1"
	first, err := r.ResolveAuto(c)
	if err != nil {
		t.Fatal(err)
	}
	config := stagePolicyConfig()
	config.StageModels.Plan = "replacement"
	setAutoPolicyConfig(t, r, config)
	c.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "tool", Content: "done"}}
	c.WorkflowStageProvided = false
	missing, err := r.ResolveAuto(c)
	if err != nil || missing.Model.ID != first.Model.ID || missing.AutoReason != "conversation_lock:stage:plan" {
		t.Fatalf("阶段缺失时不应猜测切换: route=%+v err=%v", missing, err)
	}
	c.WorkflowStageProvided = true
	c.WorkflowID = "workflow-2"
	next, err := r.ResolveAuto(c)
	if err != nil || next.Model.ID != "replacement" || next.AutoReason != "tool_continuation_unlocked:stage:plan" {
		t.Fatalf("新 workflow 应重新读取阶段配置: route=%+v err=%v", next, err)
	}
}

func TestStageFixedLockSurvivesDisabledConfigUntilNextTask(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  bool
	}{{"sdk", false}, {"raw", true}} {
		t.Run(tc.name, func(t *testing.T) {
			config := stagePolicyConfig()
			r := newAutoPolicyTestRouter(t, autoPolicyModels(), config)
			resolve := r.ResolveAuto
			if tc.raw {
				resolve = r.ResolveStageAuto
			}
			c := stageConversation("plan")
			first, err := resolve(c)
			if err != nil {
				t.Fatal(err)
			}
			config.Enabled = false
			setAutoPolicyConfig(t, r, config)
			c.WorkflowStage = "plan"
			c.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "tool", Content: "done"}}
			locked, err := resolve(c)
			if err != nil || locked.Model.ID != first.Model.ID || locked.AutoStage != "plan" ||
				locked.AutoMode != store.AutoRoutingModeStageFixed || locked.AutoReason != "conversation_lock:stage:plan" ||
				locked.AutoChannelIdentity != first.AutoChannelIdentity {
				t.Fatalf("disabling config changed a valid tool chain: route=%+v err=%v", locked, err)
			}

			c.CurrentToolResults = nil
			next, err := resolve(c)
			if tc.raw {
				if next != nil || err == nil || !strings.Contains(err.Error(), "requires enabled stage_fixed mode") {
					t.Fatalf("new raw task should reject disabled stage config: route=%+v err=%v", next, err)
				}
				return
			}
			if err != nil || next.AutoReason != "policy_disabled" || next.AutoMode != "" {
				t.Fatalf("new SDK task should use the disabled-policy default route: route=%+v err=%v", next, err)
			}
			if _, ok := r.autoLocks[c.ContinuationStateKey]; ok {
				t.Fatal("new disabled SDK task retained the old stage lock")
			}
		})
	}
}

func TestStageFixedLockSurvivesLegacyConfigErrorUntilNextTask(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), stagePolicyConfig())
	c := stageConversation("plan")
	if _, err := r.ResolveAuto(c); err != nil {
		t.Fatal(err)
	}
	if err := r.db.SetSetting(store.AutoRoutingSetting, `{}`); err != nil {
		t.Fatal(err)
	}
	r.Reload()

	c.WorkflowStage = "plan"
	c.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "tool"}}
	route, err := r.ResolveStageAuto(c)
	if err != nil || route.Model.ID != "task" || route.AutoStage != "plan" ||
		route.AutoMode != store.AutoRoutingModeStageFixed || route.AutoReason != "conversation_lock:stage:plan" {
		t.Fatalf("legacy config error broke an active stage tool chain: route=%+v err=%v", route, err)
	}

	c.CurrentToolResults = nil
	if route, err := r.ResolveAuto(c); err == nil || route != nil || !strings.Contains(err.Error(), "auto routing configuration is invalid") {
		t.Fatalf("new task should report the stored legacy config: route=%+v err=%v", route, err)
	}
}

func TestStageFixedIdentityChangesFailOnlyDuringToolContinuation(t *testing.T) {
	changes := []struct {
		name   string
		mutate func(*store.Model)
	}{
		{"model", func(m *store.Model) { m.ModelID = "changed" }},
		{"provider", func(m *store.Model) { m.Provider = "anthropic" }},
		{"base", func(m *store.Model) { m.BaseURL = "https://other.invalid" }},
		{"endpoint", func(m *store.Model) { m.Endpoint = "/v1/responses" }},
		{"account", func(m *store.Model) { m.APIKey = "new-secret" }},
		{"proxy", func(m *store.Model) { m.ProxyURL = "http://proxy.invalid:8080" }},
		{"tier", func(m *store.Model) { m.ServiceTier = "fast" }},
		{"continuation", func(m *store.Model) { m.EnableContinuation = true }},
		{"layout", func(m *store.Model) { m.DisableAutoSuffix = true }},
	}
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			r := newAutoPolicyTestRouter(t, autoPolicyModels(), stagePolicyConfig())
			c := stageConversation("plan")
			first, err := r.ResolveAuto(c)
			if err != nil {
				t.Fatal(err)
			}
			updated := *first.Model
			tc.mutate(&updated)
			if err := r.db.UpdateModel(&updated); err != nil {
				t.Fatal(err)
			}
			r.Reload()
			c.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "tool"}}
			if got, err := r.ResolveAuto(c); got != nil || err == nil || !strings.Contains(err.Error(), "changed channel identity") {
				t.Fatalf("channel mutation silently used: %+v %v", got, err)
			}
			c.CurrentToolResults = nil
			next, err := r.ResolveAuto(c)
			if err != nil || next.AutoChannelIdentity == first.AutoChannelIdentity {
				t.Fatalf("new task did not accept updated target: %+v %v", next, err)
			}
		})
	}
}

func TestStageFixedIdentityIgnoresPriceAndCodexAccessTokenRefresh(t *testing.T) {
	m := routerTestModel("account", "Account")
	m.Provider, m.APIKey, m.CodexAccountID = "codex", "initial-access-token", "account-1"
	before := autoChannelIdentity(&m)
	m.APIKey, m.InputPrice, m.OutputPrice, m.Note = "refreshed-access-token", 12, 30, "new note"
	if autoChannelIdentity(&m) != before {
		t.Fatal("price/display/token refresh changed account identity")
	}
	m.CodexAccountID = "account-2"
	if autoChannelIdentity(&m) == before {
		t.Fatal("different Codex account was not isolated")
	}
}

func TestStageFixedExpiredLockReevaluatesAndManualSelectionClears(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), stagePolicyConfig())
	c := stageConversation("plan")
	if _, err := r.ResolveAuto(c); err != nil {
		t.Fatal(err)
	}
	lock := r.autoLocks[c.ContinuationStateKey]
	lock.LastUsed = time.Now().Add(-autoLockIdleLimit - time.Second)
	r.autoLocks[c.ContinuationStateKey] = lock
	c.WorkflowStage = "review"
	c.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "tool"}}
	got, err := r.ResolveAuto(c)
	if err != nil || got.Model.ID != "mode" || got.AutoReason != "tool_continuation_unlocked:stage:review" {
		t.Fatalf("expiry did not reselect: %+v %v", got, err)
	}
	r.ForgetAuto(c)
	c.WorkflowStage = "run"
	got, err = r.ResolveAuto(c)
	if err != nil || got.Model.ID != "fast" {
		t.Fatalf("manual selection did not clear lock: %+v %v", got, err)
	}
}

func TestStageFixedAutoMetadataUsesOnlyActiveTargetsAndKnownLimits(t *testing.T) {
	models := autoPolicyModels()
	config := stagePolicyConfig()
	config.StageModels = store.AutoStageModels{Run: "task", Plan: "task", Implement: "task", Review: "task", Finalize: "task"}
	r := newAutoPolicyTestRouter(t, models, config)
	m := r.AutoModel()
	if m == nil || m.Name != "Auto（五阶段固定模型）" || m.ContextWindow != 64000 || m.MaxTokens != 4000 ||
		m.AutoRoutingMode != store.AutoRoutingModeStageFixed || m.ImageInputMode != store.ImageInputUnsupported || m.ReasoningEffort != "" {
		t.Fatalf("inactive legacy candidates affected metadata: %+v", m)
	}

	for _, id := range []string{"default", "task"} {
		target, err := r.db.GetModel(id)
		if err != nil {
			t.Fatal(err)
		}
		target.ImageInputMode = store.ImageInputSupported
		if err := r.db.UpdateModel(target); err != nil {
			t.Fatal(err)
		}
	}
	r.Reload()
	if m = r.AutoModel(); m == nil || m.ImageInputMode != store.ImageInputSupported {
		t.Fatalf("all active targets support images but Auto omitted IMAGE: %+v", m)
	}
	target, err := r.db.GetModel("task")
	if err != nil {
		t.Fatal(err)
	}
	target.ContextWindow, target.MaxTokens = 0, 0
	if err := r.db.UpdateModel(target); err != nil {
		t.Fatal(err)
	}
	r.Reload()
	m = r.AutoModel()
	if m.ContextWindow != 0 || m.MaxTokens != 0 {
		t.Fatalf("unknown capacity fabricated: %+v", m)
	}
}
