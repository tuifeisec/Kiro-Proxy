package router

import (
	"kiro-proxy/converter"
	"kiro-proxy/store"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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
		Enabled:             true,
		DefaultModelID:      "default",
		VisionModelID:       "vision",
		ReasoningModelID:    "reasoning",
		LongContextModelID:  "long",
		FastModelID:         "fast",
		FastMaxChars:        500,
		LongContextMinChars: 32000,
		TaskRules: []store.AutoTaskRule{
			{Field: "agent_task_type", Value: "spec", ModelID: "task"},
			{Field: "agent_mode", Value: "review", ModelID: "mode"},
			{Field: "chat_trigger_type", Value: "manual", ModelID: "trigger"},
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

func parseAutoPolicyConversation(t *testing.T, agentMode, systemPrompt, currentContent string) *converter.ParsedConversation {
	t.Helper()
	request := converter.KiroRequest{
		AgentMode: agentMode,
		ConversationState: converter.KiroConversationState{
			ConversationID: "auto-system-prompt-test",
			CurrentMessage: converter.KiroCurrentMsg{
				UserInputMessage: converter.KiroUserInputMsg{ModelID: "auto", Content: currentContent},
			},
			History: []converter.KiroHistoryMsg{{
				UserInputMessage: &converter.KiroUserInputMsg{Content: systemPrompt},
			}},
		},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := converter.ParseKiroRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	return conv
}

func TestResolveAutoCountsParsedSystemHistoryOnceAndPreservesBootstrap(t *testing.T) {
	config := autoPolicyConfig()
	models := autoPolicyModels()
	smallPrompt := "<identity>\n" + strings.Repeat("系统指令", 4250)
	current := "你好"
	longPrompt := "<identity>\n" + strings.Repeat("长", 33000)

	tests := []struct {
		name       string
		agentMode  string
		system     string
		wantSystem bool
		wantModel  string
		wantReason string
	}{
		{
			name:       "recognized system history is counted once",
			system:     smallPrompt,
			wantSystem: true,
			wantModel:  "default",
			wantReason: "default",
		},
		{
			name:       "large recognized system history remains long context",
			system:     longPrompt,
			wantSystem: true,
			wantModel:  "long",
			wantReason: "long_context",
		},
		{
			name:       "context gatherer bootstrap remains user context",
			agentMode:  "context-gatherer",
			system:     smallPrompt,
			wantSystem: false,
			wantModel:  "default",
			wantReason: "default",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conv := parseAutoPolicyConversation(t, test.agentMode, test.system, current)
			if len(conv.History) != 1 || conv.History[0].IsSystem != test.wantSystem {
				t.Fatalf("parser system classification = %#v, want IsSystem=%v", conv.History, test.wantSystem)
			}
			wantChars := utf8.RuneCountInString(test.system) + utf8.RuneCountInString(current)
			if got := conversationUnicodeChars(conv); got != wantChars {
				t.Fatalf("parsed conversation character count = %d, want %d", got, wantChars)
			}
			if test.wantModel != "long" && conversationUnicodeChars(conv) >= config.LongContextMinChars {
				t.Fatalf("test system prompt should remain below the long-context threshold: %d", conversationUnicodeChars(conv))
			}

			r := newAutoPolicyTestRouter(t, models, config)
			route, err := r.ResolveAuto(conv)
			if err != nil {
				t.Fatal(err)
			}
			if route.Model.ID != test.wantModel || route.AutoReason != test.wantReason {
				t.Fatalf("parsed Auto route = (%q, %q), want (%q, %q)", route.Model.ID, route.AutoReason, test.wantModel, test.wantReason)
			}
		})
	}
}

func TestResolveAutoPolicyPriorityAndReasons(t *testing.T) {
	config := autoPolicyConfig()
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), config)
	tests := []struct {
		name   string
		setup  func(*converter.ParsedConversation)
		model  string
		reason string
	}{
		{
			name:   "short unicode text",
			setup:  func(conv *converter.ParsedConversation) { conv.CurrentContent = strings.Repeat("字", 500) },
			model:  "fast",
			reason: "fast_chat",
		},
		{
			name:   "fast boundary exceeded",
			setup:  func(conv *converter.ParsedConversation) { conv.CurrentContent = strings.Repeat("字", 501) },
			model:  "default",
			reason: "default",
		},
		{
			name: "raw tools do not imply a task category and block fast routing",
			setup: func(conv *converter.ParsedConversation) {
				conv.CurrentContent = "short"
				conv.RawTools = []json.RawMessage{json.RawMessage(`{"name":"read_file"}`)}
			},
			model:  "default",
			reason: "default",
		},
		{
			name: "history tool activity blocks fast routing",
			setup: func(conv *converter.ParsedConversation) {
				conv.CurrentContent = "short"
				conv.History = []converter.ParsedHistoryEntry{{ToolUses: []converter.ParsedToolUse{{Name: "read_file", Input: json.RawMessage(`{"path":"README.md"}`)}}}}
			},
			model:  "default",
			reason: "default",
		},
		{
			name: "high reasoning",
			setup: func(conv *converter.ParsedConversation) {
				conv.CurrentContent = "short"
				conv.ReasoningEffort = "HIGH"
			},
			model:  "reasoning",
			reason: "high_reasoning",
		},
		{
			name: "xhigh reasoning",
			setup: func(conv *converter.ParsedConversation) {
				conv.CurrentContent = "short"
				conv.ReasoningEffort = "xhigh"
			},
			model:  "reasoning",
			reason: "high_reasoning",
		},
		{
			name: "max reasoning",
			setup: func(conv *converter.ParsedConversation) {
				conv.CurrentContent = "short"
				conv.ReasoningEffort = "max"
			},
			model:  "reasoning",
			reason: "high_reasoning",
		},
		{
			name:   "long context unicode boundary",
			setup:  func(conv *converter.ParsedConversation) { conv.CurrentContent = strings.Repeat("界", 32000) },
			model:  "long",
			reason: "long_context",
		},
		{
			name: "ordered task fields use case insensitive exact matching",
			setup: func(conv *converter.ParsedConversation) {
				conv.CurrentContent = "short"
				conv.AgentTaskType = " SPEC "
				conv.AgentMode = "REVIEW"
				conv.ChatTriggerType = "manual"
			},
			model:  "task",
			reason: "task:agent_task_type=spec",
		},
		{
			name: "task value matching is exact",
			setup: func(conv *converter.ParsedConversation) {
				conv.CurrentContent = "short"
				conv.AgentTaskType = "special"
				conv.AgentMode = "Review"
			},
			model:  "mode",
			reason: "task:agent_mode=review",
		},
		{
			name: "unknown task falls through to fast chat",
			setup: func(conv *converter.ParsedConversation) {
				conv.CurrentContent = "short"
				conv.AgentTaskType = "unknown"
			},
			model:  "fast",
			reason: "fast_chat",
		},
		{
			name: "chat trigger rule",
			setup: func(conv *converter.ParsedConversation) {
				conv.CurrentContent = "short"
				conv.ChatTriggerType = "MANUAL"
			},
			model:  "trigger",
			reason: "task:chat_trigger_type=manual",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conv := autoPolicyConversation()
			test.setup(conv)
			route, err := r.ResolveAuto(conv)
			if err != nil {
				t.Fatal(err)
			}
			if route.Model.ID != test.model || route.AutoReason != test.reason {
				t.Fatalf("route = (%q, %q), want (%q, %q)", route.Model.ID, route.AutoReason, test.model, test.reason)
			}
			if route.RequestedName != "auto" || route.Group != nil || len(route.Candidates) != 1 || route.Candidates[0].ID != test.model {
				t.Fatalf("Auto must resolve one exact model and preserve the requested name: %#v", route)
			}
		})
	}
}

func TestResolveAutoCountsAllUnicodeTextSources(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*converter.ParsedConversation)
		reason string
	}{
		{name: "system text", setup: func(conv *converter.ParsedConversation) { conv.SystemText = "系統內容" }},
		{name: "history user text", setup: func(conv *converter.ParsedConversation) {
			conv.History = []converter.ParsedHistoryEntry{{UserContent: "歷史使用者"}}
		}},
		{name: "history assistant text", setup: func(conv *converter.ParsedConversation) {
			conv.History = []converter.ParsedHistoryEntry{{AssistantContent: "助手回答"}}
		}},
		{name: "assistant tool input", setup: func(conv *converter.ParsedConversation) {
			conv.History = []converter.ParsedHistoryEntry{{ToolUses: []converter.ParsedToolUse{{Input: json.RawMessage(`"输入内容"`)}}}}
		}},
		{name: "history tool result", setup: func(conv *converter.ParsedConversation) {
			conv.History = []converter.ParsedHistoryEntry{{ToolResults: []converter.ParsedToolResult{{Content: "工具结果"}}}}
		}},
		{name: "current tool result", setup: func(conv *converter.ParsedConversation) {
			conv.CurrentToolResults = []converter.ParsedToolResult{{Content: "当前结果"}}
		}, reason: "tool_continuation_unlocked:long_context"},
		{name: "raw tools", setup: func(conv *converter.ParsedConversation) {
			conv.RawTools = []json.RawMessage{json.RawMessage(`"工具定义"`)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := autoPolicyConfig()
			config.FastMaxChars = 2
			config.LongContextMinChars = 5
			r := newAutoPolicyTestRouter(t, autoPolicyModels(), config)
			conv := autoPolicyConversation()
			conv.CurrentContent = "x"
			test.setup(conv)
			route, err := r.ResolveAuto(conv)
			if err != nil {
				t.Fatal(err)
			}
			wantReason := test.reason
			if wantReason == "" {
				wantReason = "long_context"
			}
			if route.Model.ID != "long" || route.AutoReason != wantReason {
				t.Fatalf("Unicode character count omitted %s: route=(%q, %q), count=%d", test.name, route.Model.ID, route.AutoReason, conversationUnicodeChars(conv))
			}
		})
	}
}

func TestResolveAutoHistoryImagePrecedesTaskRules(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), autoPolicyConfig())
	conv := autoPolicyConversation()
	conv.CurrentContent = "short"
	conv.AgentTaskType = "spec"
	conv.History = []converter.ParsedHistoryEntry{{Images: []converter.KiroImage{{Source: converter.KiroImageSource{Bytes: "aGVsbG8="}}}}}

	route, err := r.ResolveAuto(conv)
	if err != nil {
		t.Fatal(err)
	}
	if route.Model.ID != "vision" || route.AutoReason != "vision_required" {
		t.Fatalf("history image did not select the explicit vision model: (%q, %q)", route.Model.ID, route.AutoReason)
	}
}

func TestResolveAutoImageTargetErrorsAreExplicit(t *testing.T) {
	models := autoPolicyModels()
	unsupported := routerTestModel("unsupported-vision", "Unsupported vision")
	unsupported.ImageInputMode = store.ImageInputUnsupported
	models = append(models, unsupported)

	config := autoPolicyConfig()
	config.VisionModelID = ""
	r := newAutoPolicyTestRouter(t, models, config)
	conv := autoPolicyConversation()
	conv.CurrentImages = []converter.KiroImage{{Source: converter.KiroImageSource{Bytes: "aGVsbG8="}}}
	if route, err := r.ResolveAuto(conv); err == nil || route != nil || !strings.Contains(err.Error(), "vision model") {
		t.Fatalf("missing vision target should fail explicitly: route=%#v err=%v", route, err)
	}

	config.VisionModelID = "unsupported-vision"
	r = newAutoPolicyTestRouter(t, models, config)
	if route, err := r.ResolveAuto(conv); err == nil || route != nil || !strings.Contains(err.Error(), "supported image input") {
		t.Fatalf("unsupported configured vision target should fail explicitly: route=%#v err=%v", route, err)
	}
}

func TestResolveAutoRejectsInvalidConfiguredTarget(t *testing.T) {
	config := autoPolicyConfig()
	config.DefaultModelID = "missing-model"
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), config)

	route, err := r.ResolveAuto(autoPolicyConversation())
	if err == nil || route != nil || !strings.Contains(err.Error(), `unknown model "missing-model"`) {
		t.Fatalf("invalid target should fail closed: route=%#v err=%v", route, err)
	}
	if model := r.AutoModel(); model != nil {
		t.Fatalf("invalid config must not publish default-route metadata: %#v", model)
	}
}

func TestResolveAutoMissingOptionalRoleUsesDefault(t *testing.T) {
	config := autoPolicyConfig()
	config.FastModelID = ""
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), config)
	conv := autoPolicyConversation()
	conv.CurrentContent = "short"

	route, err := r.ResolveAuto(conv)
	if err != nil || route.Model.ID != "default" || route.AutoReason != "default" {
		t.Fatalf("empty optional fast role should use the default target: (%#v, %v)", route, err)
	}
}

func TestResolveAutoLocksMainAndBranchAndForgetClearsManualScope(t *testing.T) {
	config := autoPolicyConfig()
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), config)
	main := autoPolicyConversation()
	main.ConversationID = "conversation"
	main.ContinuationStateKey = "conversation"
	main.CurrentContent = "short"

	first, err := r.ResolveAuto(main)
	if err != nil || first.Model.ID != "fast" || first.AutoReason != "fast_chat" {
		t.Fatalf("first main request = (%#v, %v)", first, err)
	}

	config.FastModelID = "replacement"
	setAutoPolicyConfig(t, r, config)
	main.AgentTaskType = "spec"
	main.ReasoningEffort = "high"
	main.CurrentContent = strings.Repeat("x", config.LongContextMinChars+1)
	main.CurrentToolResults = []converter.ParsedToolResult{{Content: "tool response"}}
	second, err := r.ResolveAuto(main)
	if err != nil || second.Model.ID != "fast" || second.AutoReason != "conversation_lock" {
		t.Fatalf("config reload switched the active main conversation: (%#v, %v)", second, err)
	}

	main.CurrentToolResults = nil
	main.History = []converter.ParsedHistoryEntry{{
		ToolUses:    []converter.ParsedToolUse{{Name: "read_file"}},
		ToolResults: []converter.ParsedToolResult{{Content: "previous tool result"}},
	}}
	newTask, err := r.ResolveAuto(main)
	if err != nil || newTask.Model.ID != "task" || newTask.AutoReason != "task:agent_task_type=spec" {
		t.Fatalf("new task did not reevaluate rules after old tool activity: (%#v, %v)", newTask, err)
	}

	branch := *main
	branch.ContinuationStateKey = "branch|conversation|review"
	branch.AgentTaskType = "unknown"
	branch.AgentMode = "review"
	branchRoute, err := r.ResolveAuto(&branch)
	if err != nil || branchRoute.Model.ID != "mode" || branchRoute.AutoReason != "task:agent_mode=review" {
		t.Fatalf("independent branch did not use its own task policy: (%#v, %v)", branchRoute, err)
	}

	r.ForgetAuto(main)
	manualFollowup, err := r.ResolveAuto(main)
	if err != nil || manualFollowup.Model.ID != "task" || manualFollowup.AutoReason != "task:agent_task_type=spec" {
		t.Fatalf("ForgetAuto did not clear the current scope: (%#v, %v)", manualFollowup, err)
	}
	if exact := r.ResolveExact("upstream-fast"); exact == nil || exact.Model.ID != "fast" || exact.AutoReason != "" {
		t.Fatalf("manual exact routing changed: %#v", exact)
	}
}

func TestResolveAutoNoScopeAndConversationIDFallback(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), autoPolicyConfig())
	first := autoPolicyConversation()
	first.CurrentContent = "short"
	if route, err := r.ResolveAuto(first); err != nil || route.Model.ID != "fast" {
		t.Fatalf("unscoped first route = (%#v, %v)", route, err)
	}
	second := autoPolicyConversation()
	second.AgentTaskType = "spec"
	if route, err := r.ResolveAuto(second); err != nil || route.Model.ID != "task" {
		t.Fatalf("unscoped requests should not share a lock: (%#v, %v)", route, err)
	}
	if len(r.autoLocks) != 0 {
		t.Fatalf("empty conversation scope created locks: %#v", r.autoLocks)
	}

	fallback := autoPolicyConversation()
	fallback.ConversationID = "conversation-id-fallback"
	fallback.CurrentContent = "short"
	if _, err := r.ResolveAuto(fallback); err != nil {
		t.Fatal(err)
	}
	fallback.AgentTaskType = "spec"
	fallback.CurrentToolResults = []converter.ParsedToolResult{{Content: "tool response"}}
	if route, err := r.ResolveAuto(fallback); err != nil || route.Model.ID != "fast" || route.AutoReason != "conversation_lock" {
		t.Fatalf("ConversationID fallback did not retain the model lock: (%#v, %v)", route, err)
	}
}

func TestResolveAutoExpiredLockAndUnlockedToolContinuation(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), autoPolicyConfig())
	conv := autoPolicyConversation()
	conv.ContinuationStateKey = "expired"
	conv.AgentTaskType = "spec"
	conv.CurrentToolResults = []converter.ParsedToolResult{{ToolUseID: "expired-tool", Content: "done"}}
	r.autoLocks = map[string]autoModelLock{"expired": {ModelID: "fast", LastUsed: time.Now().Add(-2 * time.Hour)}}

	route, err := r.ResolveAuto(conv)
	if err != nil || route.Model.ID != "task" || route.AutoReason != "tool_continuation_unlocked:task:agent_task_type=spec" {
		t.Fatalf("expired lock was reused: (%#v, %v)", route, err)
	}

	restarted := autoPolicyConversation()
	restarted.ConversationID = "restarted"
	restarted.ContinuationStateKey = "restarted"
	restarted.CurrentContent = "short"
	restarted.History = []converter.ParsedHistoryEntry{{ToolResults: []converter.ParsedToolResult{{Content: "previous result"}}}}
	restarted.CurrentToolResults = []converter.ParsedToolResult{{Content: "current result"}}
	route, err = r.ResolveAuto(restarted)
	if err != nil || route.Model.ID != "default" || route.AutoReason != "tool_continuation_unlocked:default" {
		t.Fatalf("cold tool continuation should use explicit default with a reason: (%#v, %v)", route, err)
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
			conv.CurrentContent = "short"
			conv.CurrentToolResults = []converter.ParsedToolResult{{Content: "tool response"}}
			if index%2 == 0 {
				conv.AgentTaskType = "spec"
			}
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
			conv.CurrentContent = "short"
			if route, err := r.ResolveAuto(conv); err != nil || route.Model.ID != "fast" {
				t.Fatalf("initial lock route = (%#v, %v)", route, err)
			}

			config.FastModelID = "replacement"
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

func TestResolveAutoLockedModelRejectsNewUnsupportedImage(t *testing.T) {
	config := autoPolicyConfig()
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), config)
	conv := autoPolicyConversation()
	conv.ContinuationStateKey = "image-lock"
	conv.CurrentContent = "short"
	if route, err := r.ResolveAuto(conv); err != nil || route.Model.ID != "fast" {
		t.Fatalf("initial route = (%#v, %v)", route, err)
	}

	model := getAutoPolicyModel(t, r, "fast")
	model.ImageInputMode = store.ImageInputUnsupported
	if err := r.db.UpdateModel(&model); err != nil {
		t.Fatal(err)
	}
	config.FastModelID = "replacement"
	setAutoPolicyConfig(t, r, config)
	r.Reload()

	conv.CurrentToolResults = []converter.ParsedToolResult{{Content: "tool response"}}
	conv.CurrentImages = []converter.KiroImage{{Source: converter.KiroImageSource{Bytes: "aGVsbG8="}}}
	if route, err := r.ResolveAuto(conv); err == nil || route != nil || !strings.Contains(err.Error(), "does not support image input") {
		t.Fatalf("locked model image mismatch should fail without switching: route=%#v err=%v", route, err)
	}
}

func TestResolveAutoNewTaskWithHistoricalToolsReevaluatesForImages(t *testing.T) {
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), autoPolicyConfig())
	conv := autoPolicyConversation()
	conv.ContinuationStateKey = "new-task-image"
	conv.CurrentContent = "short"
	if route, err := r.ResolveAuto(conv); err != nil || route.Model.ID != "fast" {
		t.Fatalf("initial Auto task = (%#v, %v)", route, err)
	}

	conv.CurrentContent = "new image task"
	conv.History = []converter.ParsedHistoryEntry{{
		ToolUses: []converter.ParsedToolUse{{Name: "read_file"}},
		ToolResults: []converter.ParsedToolResult{{
			Content: "result from the previous task",
		}},
	}}
	conv.CurrentImages = []converter.KiroImage{{Source: converter.KiroImageSource{Bytes: "aGVsbG8="}}}
	route, err := r.ResolveAuto(conv)
	if err != nil || route.Model.ID != "vision" || route.AutoReason != "vision_required" {
		t.Fatalf("new task did not reselect a compatible vision model after historical tool activity: (%#v, %v)", route, err)
	}
}

func TestResolveAutoDisabledUsesLegacyRouteAndClearsLock(t *testing.T) {
	models := autoPolicyModels()
	group := autoTestGroup("legacy-auto", "Auto", "default", "fast")
	r := newAutoTestRouter(t, models, []store.ModelGroup{group})
	config := autoPolicyConfig()
	setAutoPolicyConfig(t, r, config)
	conv := autoPolicyConversation()
	conv.ContinuationStateKey = "disabled-policy"
	conv.CurrentContent = "short"
	if route, err := r.ResolveAuto(conv); err != nil || route.Model.ID != "fast" {
		t.Fatalf("initial policy route = (%#v, %v)", route, err)
	}

	config.Enabled = false
	setAutoPolicyConfig(t, r, config)
	route, err := r.ResolveAuto(conv)
	if err != nil || route.AutoReason != "policy_disabled" || route.Group == nil || len(route.Candidates) != 2 {
		t.Fatalf("disabled policy should retain legacy group routing: (%#v, %v)", route, err)
	}
	if _, ok := r.autoLocks["disabled-policy"]; ok {
		t.Fatal("disabled policy retained an old Auto lock")
	}
	if model := r.AutoModel(); model == nil || model.Name != "Auto（默认路由）" || model.Note != model.Name {
		t.Fatalf("disabled Auto metadata has the wrong label: %#v", model)
	}
}

func TestResolveAutoDisabledPolicyIgnoresUnavailableRoleTargets(t *testing.T) {
	targetStates := []struct {
		name   string
		mutate func(*testing.T, *Router)
		want   string
	}{
		{
			name: "disabled target",
			mutate: func(t *testing.T, r *Router) {
				model := getAutoPolicyModel(t, r, "task")
				model.Enabled = false
				if err := r.db.UpdateModel(&model); err != nil {
					t.Fatal(err)
				}
			},
			want: "disabled model",
		},
		{
			name: "deleted target",
			mutate: func(t *testing.T, r *Router) {
				if err := r.db.DeleteModel("task"); err != nil {
					t.Fatal(err)
				}
			},
			want: "unknown model",
		},
	}

	for _, targetState := range targetStates {
		for _, enabled := range []bool{false, true} {
			policyName := "disabled"
			if enabled {
				policyName = "enabled"
			}
			name := targetState.name + "/policy-" + policyName
			t.Run(name, func(t *testing.T) {
				group := autoTestGroup("legacy-auto", "Auto", "default", "fast")
				r := newAutoTestRouter(t, autoPolicyModels(), []store.ModelGroup{group})
				config := autoPolicyConfig()
				config.Enabled = enabled
				setAutoPolicyConfig(t, r, config)
				targetState.mutate(t, r)
				r.Reload()

				route, err := r.ResolveAuto(autoPolicyConversation())
				if enabled {
					if err == nil || route != nil || !strings.Contains(err.Error(), targetState.want) {
						t.Fatalf("enabled policy should reject the unavailable role target: route=%#v err=%v", route, err)
					}
					if model := r.AutoModel(); model != nil {
						t.Fatalf("enabled policy with an invalid target must not publish Auto metadata: %#v", model)
					}
					return
				}

				if err != nil || route == nil || route.AutoReason != "policy_disabled" || route.Group == nil || len(route.Candidates) != 2 {
					t.Fatalf("disabled policy should keep the legacy Auto route: route=%#v err=%v", route, err)
				}
				if legacy := r.Resolve("auto"); legacy == nil || legacy.Group == nil || len(legacy.Candidates) != 2 {
					t.Fatalf("legacy Resolve(auto) route was blocked by an unavailable role target: %#v", legacy)
				}
				if model := r.AutoModel(); model == nil || model.Name != "Auto（默认路由）" || model.Note != model.Name {
					t.Fatalf("disabled policy should publish legacy Auto metadata: %#v", model)
				}
			})
		}
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

func TestAutoModelAggregatesConfiguredTargets(t *testing.T) {
	config := autoPolicyConfig()
	r := newAutoPolicyTestRouter(t, autoPolicyModels(), config)
	model := r.AutoModel()
	if model == nil || model.ID != "auto" || model.Name != "Auto（按任务选模）" || model.Note != model.Name {
		t.Fatalf("enabled Auto metadata has the wrong identity or label: %#v", model)
	}
	if model.ContextWindow != 16000 || model.MaxTokens != 2000 {
		t.Fatalf("Auto metadata did not use the safe candidate minima: context=%d output=%d", model.ContextWindow, model.MaxTokens)
	}
	if model.ImageInputMode != store.ImageInputSupported {
		t.Fatalf("configured supported vision target was not advertised: %q", model.ImageInputMode)
	}

	config.VisionModelID = ""
	setAutoPolicyConfig(t, r, config)
	model = r.AutoModel()
	if model == nil || model.ImageInputMode != store.ImageInputUnsupported {
		t.Fatalf("unconfigured vision support was advertised: %#v", model)
	}
}

func TestAutoModelReturnsNilWhenNoTargetIsAvailable(t *testing.T) {
	r := newAutoTestRouter(t, nil, nil)
	setAutoPolicyConfig(t, r, store.AutoRoutingConfig{Enabled: true, DefaultModelID: "missing"})
	if model := r.AutoModel(); model != nil {
		t.Fatalf("invalid target metadata should not be published: %#v", model)
	}
}

func TestToolResultUnicodeCharsDoesNotCountTextTwice(t *testing.T) {
	result := converter.ParsedToolResult{
		Content: "abc",
		Blocks:  []converter.ParsedToolResultBlock{{Text: "abc"}},
	}
	if got := toolResultUnicodeChars([]converter.ParsedToolResult{result}); got != 3 {
		t.Fatalf("tool result text counted more than once: got %d, want 3", got)
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
