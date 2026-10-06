package proxy

import (
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

func autoTaskPreflight(t *testing.T, modelID, mode, branch, task string) *Preflight {
	t.Helper()
	req := converter.KiroRequest{
		AgentMode: mode,
		ConversationState: converter.KiroConversationState{
			ConversationID: "task-auto-conversation", AgentContinuationID: branch, AgentTaskType: task,
			CurrentMessage: converter.KiroCurrentMsg{UserInputMessage: converter.KiroUserInputMsg{ModelID: modelID, Content: "hello"}},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := converter.ParseKiroRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	return &Preflight{Body: body, Conv: conv, Protocol: "kiro", SourceModel: modelID, ConversationID: conv.ConversationID, StartTime: time.Now()}
}

func autoTaskToolFollowup(t *testing.T, pre *Preflight) *Preflight {
	t.Helper()
	var req converter.KiroRequest
	if err := json.Unmarshal(pre.Body, &req); err != nil {
		t.Fatal(err)
	}
	req.ConversationState.History = []converter.KiroHistoryMsg{{
		AssistantResponseMsg: &converter.KiroAssistantMsg{ToolUses: []converter.KiroToolUse{{ToolUseID: "auto-tool", Name: "read_file", Input: map[string]interface{}{"path": "test.txt"}}}},
	}}
	req.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext = &converter.KiroMsgContext{
		ToolResults: []converter.KiroToolResult{{ToolUseID: "auto-tool", Status: "success", Content: []converter.KiroToolContent{{Text: "done"}}}},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := converter.ParseKiroRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	pre.Body, pre.Conv = body, conv
	return pre
}

func TestTaskAutoRoutesScopesAndMetersSelectedModel(t *testing.T) {
	var callsMu sync.Mutex
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var input struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		callsMu.Lock()
		calls = append(calls, input.Model)
		callsMu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":100,\"prompt_tokens_details\":{\"cached_tokens\":600}}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "task-auto.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	models := map[string]*store.Model{}
	for index, id := range []string{"default", "fast", "coding"} {
		model := &store.Model{ID: id, Name: id, ModelID: id, Provider: "openai", BaseURL: upstream.URL, Endpoint: "/chat/completions", Enabled: true,
			InputPrice: float64(index + 1), OutputPrice: float64((index + 1) * 5), CacheInputPrice: .5, CacheOutputPrice: 1.25, RateMultiplier: 1}
		if err := db.CreateModel(model); err != nil {
			t.Fatal(err)
		}
		models[id] = model
	}
	config := store.AutoRoutingConfig{Enabled: true, DefaultModelID: "default", FastModelID: "fast", FastMaxChars: 500, LongContextMinChars: 32000,
		TaskRules: []store.AutoTaskRule{{Field: "agent_mode", Value: "context-gatherer", ModelID: "coding"}, {Field: "agent_task_type", Value: "spec", ModelID: "coding"}}}
	configJSON, _ := json.Marshal(config)
	if err := db.SetSetting(store.AutoRoutingSetting, string(configJSON)); err != nil {
		t.Fatal(err)
	}
	stats := store.NewStats(db)
	defer stats.Close()
	logPath := filepath.Join(t.TempDir(), "traffic.log")
	logger, err := NewTrafficLogger(logPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	s := &Server{db: db, stats: stats, router: router.New(db), logger: logger, conversationModels: make(map[string]conversationModelEntry)}
	fwd := NewForwarder(stats, db)
	newTask := autoTaskPreflight(t, "auto", "vibe", "", "spec")
	autoTaskToolFollowup(t, newTask)
	var newTaskRequest converter.KiroRequest
	if err := json.Unmarshal(newTask.Body, &newTaskRequest); err != nil {
		t.Fatal(err)
	}
	newTaskRequest.ConversationState.History = append(newTaskRequest.ConversationState.History, converter.KiroHistoryMsg{
		UserInputMessage: &converter.KiroUserInputMsg{Content: "done", UserInputMessageContext: newTaskRequest.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext},
	})
	newTaskRequest.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext = nil
	newTask.Body, err = json.Marshal(newTaskRequest)
	if err != nil {
		t.Fatal(err)
	}
	newTask.Conv, err = converter.ParseKiroRequest(newTask.Body)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		pre           *Preflight
		model, reason string
	}{
		{autoTaskPreflight(t, "auto", "vibe", "", ""), "fast", "fast_chat"},
		{autoTaskToolFollowup(t, autoTaskPreflight(t, "auto", "vibe", "", "spec")), "fast", "conversation_lock"},
		{autoTaskPreflight(t, "auto", "context-gatherer", "branch-1", ""), "coding", "task:agent_mode=context-gatherer"},
		{newTask, "coding", "task:agent_task_type=spec"},
	}
	var wantTotal float64
	for _, tc := range tests {
		route, err := s.resolveMainRoute(tc.pre)
		if err != nil || route == nil || route.Model.ID != tc.model || route.AutoReason != tc.reason || route.RequestedName != "auto" {
			t.Fatalf("route=%+v error=%v expected=%s/%s", route, err, tc.model, tc.reason)
		}
		s.logAutoRoute(tc.pre, route)
		tc.pre.usage = newRequestUsageRecorder(db, tc.pre.StartTime, route.Model, "/", "kiro")
		tc.pre.usage.setRoute(route)
		resp, err := fwd.Forward(httptest.NewRequest(http.MethodPost, "http://runtime.test/", strings.NewReader(string(tc.pre.Body))), tc.pre, route, logger)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		s.rememberMainModel(tc.pre, route)
		wantTotal += store.CalculateCredits(1000, 100, 600, 0, models[tc.model])
	}
	// 独立分支不覆盖主会话的 classifier 跟随目标。
	for _, tc := range []struct{ mode, branch, want string }{{"vibe", "", "coding"}, {"context-gatherer", "branch-1", "coding"}} {
		pre := autoTaskPreflight(t, "simple-task", tc.mode, tc.branch, "")
		pre.IsClassifier = true
		route := s.resolveClassifierRoute(pre)
		if route == nil || route.Model.ID != tc.want || route.Group != nil {
			t.Fatalf("classifier scope %s: route=%+v", tc.mode, route)
		}
	}
	callsMu.Lock()
	gotCalls := strings.Join(calls, ",")
	callsMu.Unlock()
	if gotCalls != "fast,fast,coding,coding" {
		t.Fatalf("upstream models=%s", gotCalls)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		page, err := db.QueryRequestUsagePage(store.RequestUsageQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == len(tests) {
			for _, item := range page.Items {
				if item.RequestedModel != "auto" || !strings.HasPrefix(item.DispatchReason, "auto:") || item.SelectedModelConfigID != item.ModelID {
					t.Fatalf("actual model/reason missing: %+v", item)
				}
				want := store.CalculateCredits(1000, 100, 600, 0, models[item.ModelID])
				if math.Abs(item.Credits-want) > 1e-12 {
					t.Fatalf("actual model credits=%v want=%v", item.Credits, want)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("request usage was not recorded")
		}
		time.Sleep(time.Millisecond)
	}
	if math.Abs(stats.Snapshot().Credits-wantTotal) > 1e-12 {
		t.Fatalf("total credits=%v want=%v", stats.Snapshot().Credits, wantTotal)
	}
	// 关闭日志刷新队列后核对实际策略记录，不输出提示正文。
	logger.Close()
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var autoLogs int
	for _, line := range strings.Split(string(logData), "\n") {
		var entry map[string]interface{}
		if json.Unmarshal([]byte(line), &entry) == nil && entry["category"] == "auto-routing" {
			autoLogs++
		}
	}
	if autoLogs != len(tests) || !strings.Contains(string(logData), "auto_route_reason") || !strings.Contains(string(logData), "context-gatherer") {
		t.Fatalf("auto route logs=%d", autoLogs)
	}
}

func TestTaskAutoManualSelectionClearsOnlyItsScope(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "manual.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"default", "coding"} {
		if err := db.CreateModel(&store.Model{ID: id, Name: id, ModelID: id, Provider: "openai", BaseURL: "https://example.invalid", Endpoint: "/chat/completions", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	config := store.AutoRoutingConfig{Enabled: true, DefaultModelID: "default", TaskRules: []store.AutoTaskRule{{Field: "agent_task_type", Value: "spec", ModelID: "coding"}}}
	raw, _ := json.Marshal(config)
	if err := db.SetSetting(store.AutoRoutingSetting, string(raw)); err != nil {
		t.Fatal(err)
	}
	s := &Server{router: router.New(db)}
	pre := autoTaskPreflight(t, "auto", "vibe", "", "")
	if route, err := s.resolveMainRoute(pre); err != nil || route.Model.ID != "default" {
		t.Fatalf("initial: %v/%v", route, err)
	}
	branch := autoTaskPreflight(t, "auto", "context-gatherer", "branch", "")
	if _, err := s.resolveMainRoute(branch); err != nil {
		t.Fatal(err)
	}
	autoTaskToolFollowup(t, branch)
	manual := autoTaskPreflight(t, "coding", "vibe", "", "")
	if route, err := s.resolveMainRoute(manual); err != nil || route.Model.ID != "coding" || route.AutoReason != "" {
		t.Fatalf("manual: %v/%v", route, err)
	}
	autoTaskToolFollowup(t, pre)
	pre.Conv.AgentTaskType = "spec"
	if route, err := s.resolveMainRoute(pre); err != nil || route.Model.ID != "coding" || route.AutoReason != "tool_continuation_unlocked:task:agent_task_type=spec" {
		t.Fatalf("reselected: %v/%v", route, err)
	}
	branch.Conv.AgentTaskType = "spec"
	if route, err := s.resolveMainRoute(branch); err != nil || route.Model.ID != "default" || route.AutoReason != "conversation_lock" {
		t.Fatalf("branch lock lost: %v/%v", route, err)
	}
	// 彩蛋走快捷响应路径，同样须清除手动改选所在的执行域。
	s.conversationModels = make(map[string]conversationModelEntry)
	egg := autoTaskPreflight(t, "GPT-8.8", "vibe", "", "")
	s.rememberEasterEggModel(egg)
	pre.Conv.AgentTaskType = ""
	if route, err := s.resolveMainRoute(pre); err != nil || route.Model.ID != "default" || route.AutoReason != "tool_continuation_unlocked:default" {
		t.Fatalf("easter egg did not clear Auto lock: %v/%v", route, err)
	}
	if route, err := s.resolveMainRoute(branch); err != nil || route.Model.ID != "default" || route.AutoReason != "conversation_lock" {
		t.Fatalf("easter egg cleared another branch: %v/%v", route, err)
	}
}

func TestTaskAutoEasterEggClassifierUsesBranchScope(t *testing.T) {
	s := &Server{conversationModels: make(map[string]conversationModelEntry)}
	main := autoTaskPreflight(t, "GPT-8.8", "vibe", "", "")
	branch := autoTaskPreflight(t, "normal", "context-gatherer", "branch", "")
	mainClassifier := autoTaskPreflight(t, "simple-task", "vibe", "", "")
	mainClassifier.IsClassifier = true
	branchClassifier := autoTaskPreflight(t, "simple-task", "context-gatherer", "branch", "")
	branchClassifier.IsClassifier = true
	s.rememberEasterEggModel(main)
	s.rememberModelName(branch, "normal")
	if !s.isEasterEggClassifier(mainClassifier) || s.isEasterEggClassifier(branchClassifier) {
		t.Fatal("main easter egg intercepted a normal branch classifier")
	}
	s.rememberModelName(main, "normal")
	s.rememberEasterEggModel(branch)
	if s.isEasterEggClassifier(mainClassifier) || !s.isEasterEggClassifier(branchClassifier) {
		t.Fatal("branch easter egg was not isolated from the normal main classifier")
	}
}
