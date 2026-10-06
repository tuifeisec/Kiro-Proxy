package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"
)

type autoStageUpstreamObservation struct {
	Path                 string
	Model                string
	PromptCacheKey       string
	PreviousResponseID   string
	WorkflowHeaderValues []string
}

type autoStageIntegrationEnv struct {
	db       *store.DB
	server   *Server
	forward  *Forwarder
	logger   *TrafficLogger
	models   map[string]*store.Model
	requests chan autoStageUpstreamObservation
}

func newAutoStageIntegrationEnv(t *testing.T) *autoStageIntegrationEnv {
	t.Helper()
	requests := make(chan autoStageUpstreamObservation, 64)
	var responseID atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, "read request", http.StatusBadRequest)
			return
		}
		var metadata struct {
			Model              string `json:"model"`
			PromptCacheKey     string `json:"prompt_cache_key"`
			PreviousResponseID string `json:"previous_response_id"`
		}
		if err := json.Unmarshal(body, &metadata); err != nil {
			http.Error(w, "decode request", http.StatusBadRequest)
			return
		}
		requests <- autoStageUpstreamObservation{
			Path: req.URL.Path, Model: metadata.Model, PromptCacheKey: metadata.PromptCacheKey,
			PreviousResponseID:   metadata.PreviousResponseID,
			WorkflowHeaderValues: append([]string(nil), req.Header.Values(converter.WorkflowStageHeader)...),
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(req.URL.Path, "/responses") {
			id := fmt.Sprintf("response-%d", responseID.Add(1))
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":%q}}\n\n", id)
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1000,\"output_tokens\":100,\"input_tokens_details\":{\"cached_tokens\":600}}}}\n\n", id)
			return
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":100,\"prompt_tokens_details\":{\"cached_tokens\":600}}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)

	db, err := store.Open(filepath.Join(t.TempDir(), "auto-stage-integration.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	models := make(map[string]*store.Model)
	modelIDs := []string{"default", "run", "shared-responses", "implement", "implement-v2", "finalize", "manual", "classifier"}
	for i, id := range modelIDs {
		endpoint := "/v1/chat/completions"
		if id == "shared-responses" {
			endpoint = "/v1/responses"
		}
		model := &store.Model{
			ID: id, Name: id, ModelID: "upstream-" + id, Provider: "openai",
			BaseURL: upstream.URL, Endpoint: endpoint, Enabled: true,
			InputPrice: float64(i + 1), OutputPrice: float64(i + 2),
			CacheInputPrice: float64(i+1) / 10, CacheOutputPrice: 1.25, RateMultiplier: 1,
		}
		if err := db.CreateModel(model); err != nil {
			t.Fatal(err)
		}
		models[id] = model
	}
	config := store.AutoRoutingConfig{
		Mode: store.AutoRoutingModeStageFixed, Enabled: true, DefaultModelID: "default",
		StageModels: store.AutoStageModels{
			Run: "run", Plan: "shared-responses", Implement: "implement",
			Review: "shared-responses", Finalize: "finalize",
		},
	}
	setAutoStageIntegrationConfig(t, db, config)
	if err := db.SetSetting("classifier_model_id", "classifier"); err != nil {
		t.Fatal(err)
	}

	stats := store.NewStats(db)
	t.Cleanup(stats.Close)
	logger, err := NewTrafficLogger(filepath.Join(t.TempDir(), "auto-stage.log"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logger.Close)
	forward := NewForwarder(stats, db)
	t.Cleanup(forward.CloseIdleConnections)
	return &autoStageIntegrationEnv{
		db: db, server: &Server{
			db: db, stats: stats, router: router.New(db), logger: logger,
			conversationModels: make(map[string]conversationModelEntry),
		},
		forward: forward, logger: logger, models: models, requests: requests,
	}
}

func setAutoStageIntegrationConfig(t *testing.T, db *store.DB, config store.AutoRoutingConfig) {
	t.Helper()
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(store.AutoRoutingSetting, string(encoded)); err != nil {
		t.Fatal(err)
	}
}

func autoStageIntegrationRequest(t *testing.T, modelID, conversationID, content string, stage *string, toolResult bool) *http.Request {
	t.Helper()
	current := converter.KiroUserInputMsg{ModelID: modelID, Content: content}
	history := []converter.KiroHistoryMsg(nil)
	if toolResult {
		history = []converter.KiroHistoryMsg{{AssistantResponseMsg: &converter.KiroAssistantMsg{
			ToolUses: []converter.KiroToolUse{{ToolUseID: "stage-tool", Name: "read_file", Input: map[string]interface{}{"path": "sample.txt"}}},
		}}}
		current.UserInputMessageContext = &converter.KiroMsgContext{ToolResults: []converter.KiroToolResult{{
			ToolUseID: "stage-tool", Status: "success", Content: []converter.KiroToolContent{{Text: "tool result"}},
		}}}
	}
	body, err := json.Marshal(converter.KiroRequest{
		AgentMode: "vibe",
		ConversationState: converter.KiroConversationState{
			ConversationID: conversationID,
			CurrentMessage: converter.KiroCurrentMsg{UserInputMessage: current},
			History:        history,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://runtime.test/generateAssistantResponse", bytes.NewReader(body))
	if stage != nil {
		header, err := json.Marshal(map[string]interface{}{
			"version": 1, "stage": *stage, "conversationId": conversationID,
			"agentMode": "vibe", "agentContinuationId": "",
		})
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set(converter.WorkflowStageHeader, string(header))
	}
	return request
}

func (env *autoStageIntegrationEnv) routeForward(t *testing.T, modelID, conversationID, content string, stage *string, toolResult bool) (*Preflight, *router.Route, autoStageUpstreamObservation) {
	t.Helper()
	req := autoStageIntegrationRequest(t, modelID, conversationID, content, stage, toolResult)
	pre, err := env.server.parseRequest(req, "kiro", nil)
	if err != nil {
		t.Fatalf("parseRequest: %v", err)
	}
	var route *router.Route
	if pre.IsClassifier {
		route = env.server.resolveClassifierRoute(pre)
	} else {
		route, err = env.server.resolveMainRoute(pre)
	}
	if err != nil || route == nil || route.Model == nil {
		t.Fatalf("resolve route: route=%+v error=%v", route, err)
	}
	pre.usage = newRequestUsageRecorder(env.db, pre.StartTime, route.Model, req.URL.Path, pre.Protocol)
	pre.usage.setRoute(route)
	resp, err := env.forward.Forward(req, pre, route, env.logger)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("upstream status=%d body=%q", resp.StatusCode, body)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		_ = resp.Body.Close()
		t.Fatalf("read Kiro response through EOF: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close Kiro response: %v", err)
	}
	var observation autoStageUpstreamObservation
	select {
	case observation = <-env.requests:
	case <-time.After(2 * time.Second):
		t.Fatal("local upstream did not receive the forwarded request")
	}
	if len(observation.WorkflowHeaderValues) != 0 {
		t.Fatalf("proxy-private workflow header leaked upstream: %v", observation.WorkflowHeaderValues)
	}
	return pre, route, observation
}

func TestStageFixedRouteForwardLockFallbackAndUsageIntegration(t *testing.T) {
	env := newAutoStageIntegrationEnv(t)
	stage := func(value string) *string { return &value }
	expectStage := func(input, conversationID, modelID, reason string) (*Preflight, autoStageUpstreamObservation) {
		t.Helper()
		pre, route, observation := env.routeForward(t, "auto", conversationID, "request-"+conversationID, stage(input), false)
		if route.Model.ID != modelID || route.AutoReason != reason || route.AutoMode != store.AutoRoutingModeStageFixed ||
			route.AutoStage != input || route.AutoChannelIdentity == "" {
			t.Fatalf("stage %s route=%+v, want model=%s reason=%s", input, route, modelID, reason)
		}
		if observation.Model != route.Model.ModelID {
			t.Fatalf("upstream model=%q, selected target=%q", observation.Model, route.Model.ModelID)
		}
		return pre, observation
	}

	// The five configured stages all traverse parseRequest, route selection, Forward, and EOF.
	expectStage("run", "stage-run", "run", "stage:run")
	planPre, planObservation := expectStage("plan", "cache-scope", "shared-responses", "stage:plan")
	if planObservation.Path != "/v1/responses" || planObservation.PromptCacheKey == "" {
		t.Fatalf("Responses stage request missing cache identity: %+v", planObservation)
	}

	implementPre, implementRoute, implementObservation := env.routeForward(t, "auto", "locked-scope", "first tool task", stage("implement"), false)
	if implementRoute.Model.ID != "implement" || implementRoute.AutoReason != "stage:implement" || implementObservation.Model != "upstream-implement" {
		t.Fatalf("initial implement route=%+v upstream=%+v", implementRoute, implementObservation)
	}

	reviewPre, reviewRoute, reviewObservation := env.routeForward(t, "auto", "cache-scope", "request-cache-scope", stage("review"), false)
	if reviewRoute.Model.ID != "shared-responses" || reviewRoute.AutoReason != "stage:review" || reviewObservation.Path != "/v1/responses" {
		t.Fatalf("review route=%+v upstream=%+v", reviewRoute, reviewObservation)
	}
	if reviewObservation.PromptCacheKey != planObservation.PromptCacheKey {
		t.Fatalf("stage changed Responses prompt_cache_key: plan=%q review=%q", planObservation.PromptCacheKey, reviewObservation.PromptCacheKey)
	}
	if planPre.Conv.ContinuationStateKey != reviewPre.Conv.ContinuationStateKey {
		t.Fatalf("stage changed conversation continuation key: plan=%q review=%q", planPre.Conv.ContinuationStateKey, reviewPre.Conv.ContinuationStateKey)
	}
	planContinuationKey := responsesContinuationStateKeyFor(planPre, env.models["shared-responses"], "")
	reviewContinuationKey := responsesContinuationStateKeyFor(reviewPre, env.models["shared-responses"], "")
	if planContinuationKey == "" || planContinuationKey != reviewContinuationKey {
		t.Fatalf("stage changed Responses continuation key: plan=%q review=%q", planContinuationKey, reviewContinuationKey)
	}

	expectStage("finalize", "stage-finalize", "finalize", "stage:finalize")

	// 同一执行域进入真实的新阶段，即使携带工具结果也选择新阶段目标。
	followupPre, followupRoute, followupObservation := env.routeForward(t, "auto", "locked-scope", "tool continuation", stage("review"), true)
	if !followupPre.Conv.WorkflowStageProvided || followupRoute.Model.ID != "shared-responses" || followupRoute.AutoStage != "review" ||
		followupRoute.AutoReason != "tool_continuation_unlocked:stage:review" || followupRoute.AutoChannelIdentity == implementRoute.AutoChannelIdentity ||
		followupObservation.Model != "upstream-shared-responses" {
		t.Fatalf("新阶段未选择 review 目标: pre=%+v route=%+v upstream=%+v", followupPre.Conv, followupRoute, followupObservation)
	}

	// A new task without current tool results rereads the refreshed mapping in the same conversation.
	config := store.AutoRoutingConfig{
		Mode: store.AutoRoutingModeStageFixed, Enabled: true, DefaultModelID: "default",
		StageModels: store.AutoStageModels{Run: "run", Plan: "shared-responses", Implement: "implement-v2", Review: "shared-responses", Finalize: "finalize"},
	}
	setAutoStageIntegrationConfig(t, env.db, config)
	env.server.router.Reload()
	newTaskPre, newTaskRoute, newTaskObservation := env.routeForward(t, "auto", "locked-scope", "new task", stage("implement"), false)
	if newTaskPre.Conv.ContinuationStateKey != implementPre.Conv.ContinuationStateKey || newTaskRoute.Model.ID != "implement-v2" ||
		newTaskRoute.AutoReason != "stage:implement" || newTaskObservation.Model != "upstream-implement-v2" {
		t.Fatalf("new task did not use the refreshed stage target: route=%+v upstream=%+v", newTaskRoute, newTaskObservation)
	}

	// Missing and unknown stages both use the configured fixed default, with distinct reasons.
	unavailablePre, unavailableRoute, unavailableObservation := env.routeForward(t, "auto", "stage-unavailable", "request-stage-unavailable", nil, false)
	if unavailablePre.Conv.WorkflowStageProvided || unavailableRoute.Model.ID != "default" || unavailableRoute.AutoReason != "stage_unavailable" || unavailableObservation.Model != "upstream-default" {
		t.Fatalf("missing stage route=%+v upstream=%+v", unavailableRoute, unavailableObservation)
	}
	unknownPre, unknownRoute, unknownObservation := env.routeForward(t, "auto", "stage-unknown", "request-stage-unknown", stage("future_stage"), false)
	if !unknownPre.Conv.WorkflowStageProvided || unknownRoute.Model.ID != "default" || unknownRoute.AutoReason != "stage_unknown:future_stage" || unknownObservation.Model != "upstream-default" {
		t.Fatalf("unknown stage route=%+v upstream=%+v", unknownRoute, unknownObservation)
	}

	// A manually selected model and a classifier remain outside stage routing.
	manualPre, manualRoute, _ := env.routeForward(t, "manual", "manual-scope", "manual request", stage("run"), false)
	if manualPre.Conv.WorkflowStageProvided || manualRoute.Model.ID != "manual" || manualRoute.AutoMode != "" || manualRoute.AutoReason != "" {
		t.Fatalf("manual request entered stage routing: pre=%+v route=%+v", manualPre.Conv, manualRoute)
	}
	classifierPre, classifierRoute, _ := env.routeForward(t, "simple-task", "classifier-scope", "intent classifier\nRespond ONLY with a raw JSON object", stage("finalize"), false)
	if !classifierPre.IsClassifier || classifierPre.Conv.WorkflowStageProvided || classifierRoute.Model.ID != "classifier" || classifierRoute.AutoMode != "" {
		t.Fatalf("classifier entered stage routing: pre=%+v route=%+v", classifierPre, classifierRoute)
	}

	// EOF usage rows must account for cache reads and credits using the selected stage target.
	page, err := env.db.QueryRequestUsagePage(store.RequestUsageQuery{PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"run|auto:stage:run":                                            1,
		"shared-responses|auto:stage:plan":                              1,
		"implement|auto:stage:implement":                                1,
		"shared-responses|auto:stage:review":                            1,
		"finalize|auto:stage:finalize":                                  1,
		"shared-responses|auto:tool_continuation_unlocked:stage:review": 1,
		"implement-v2|auto:stage:implement":                             1,
		"default|auto:stage_unavailable":                                1,
		"default|auto:stage_unknown:future_stage":                       1,
	}
	var autoRows int
	for _, item := range page.Items {
		if item.RequestedModel != "auto" {
			continue
		}
		autoRows++
		key := item.SelectedModelConfigID + "|" + item.DispatchReason
		if want[key] != 1 {
			t.Fatalf("unexpected or duplicate auto usage row %q: %+v", key, item)
		}
		want[key]--
		model := env.models[item.SelectedModelConfigID]
		if model == nil || item.ModelID != model.Name || item.StatusCode != http.StatusOK || !item.Success ||
			item.InputTokens != 1000 || item.OutputTokens != 100 || item.CacheReadTokens != 600 || item.CacheWriteTokens != 0 {
			t.Fatalf("usage did not reflect selected target/cache read: %+v", item)
		}
		credits := store.CalculateCredits(1000, 100, 600, 0, model)
		if item.Credits != credits {
			t.Fatalf("usage credits=%v want=%v for selected model %s", item.Credits, credits, model.ID)
		}
	}
	if autoRows != len(want) {
		t.Fatalf("auto usage rows=%d want=%d", autoRows, len(want))
	}
	for key, count := range want {
		if count != 0 {
			t.Fatalf("missing auto usage row %q", key)
		}
	}

	// The local mock saw every request and never received the proxy-only header.
	if got := len(env.requests); got != 0 {
		t.Fatalf("unconsumed upstream observations: %d", got)
	}
	if implementPre.Conv.WorkflowStage != "implement" || implementRoute.AutoStage != "implement" || followupRoute.AutoStage != "review" {
		t.Fatal("阶段切换应保留原请求记录，并为新请求记录 review 阶段")
	}
}
