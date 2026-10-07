package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kiro-proxy/converter"
	"kiro-proxy/store"
)

func TestDutyAutoGroupRecordsActualMemberAfterCapacityDispatch(t *testing.T) {
	env := newAutoStageIntegrationEnv(t)
	group := &store.ModelGroup{ID: "duty-group", Name: "Duty GPT group", Enabled: true, MaxAttempts: 2, FirstByteTimeoutMS: 1000,
		Members: []store.ModelGroupMember{{ModelID: "run", Priority: 0, MaxConcurrency: 1}, {ModelID: "manual", Priority: 1, MaxConcurrency: 1}}}
	if err := env.db.CreateModelGroup(group); err != nil {
		t.Fatal(err)
	}
	installDutyIntegrationConfig(t, env, group.ID)
	pre := dutyIntegrationPre("design", "group-execution", "low")
	route, err := env.server.resolveMainRoute(pre)
	if err != nil {
		t.Fatal(err)
	}
	if route.Group == nil || route.Group.ID != group.ID || len(route.Candidates) != 2 || route.AutoChannelIdentity != "" {
		t.Fatalf("group resolved as an actual first member: %+v", route)
	}
	if len(env.server.router.GetAutoObservations()) != 0 {
		t.Fatal("first candidate was observed before dispatch")
	}
	lease, ok := env.forward.load.acquire("run", 1)
	if !ok {
		t.Fatal("cannot occupy first candidate")
	}
	defer lease.release()
	requests := 0
	env.forward.clients[""] = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"model":"upstream-manual"`) {
			t.Fatalf("wrong actual member: %s", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Request: req,
			Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))}, nil
	})}
	req := httptest.NewRequest(http.MethodPost, "http://runtime.local/generateAssistantResponse", nil)
	resp, err := env.forward.Forward(req, pre, route, env.logger)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	env.server.router.RecordAutoDispatch(pre.Conv, route)
	env.server.logAutoRoute(pre, route)
	observations := env.server.router.GetAutoObservations()
	if requests != 1 || route.Model.ID != "manual" || len(observations) != 1 {
		t.Fatalf("actual dispatch not recorded: %+v", observations)
	}
	observed := observations[0]
	if observed.TargetGroupID != group.ID || observed.TargetGroupName != group.Name || observed.ModelConfigID != "manual" || observed.OutboundModel != "upstream-manual" || observed.EffectiveEffort != "high" || observed.ChannelIdentity == "" {
		t.Fatalf("group observation does not describe the actual member: %+v", observed)
	}
}

func installDutyIntegrationConfig(t *testing.T, env *autoStageIntegrationEnv, designTarget string) {
	t.Helper()
	duties := []string{"orchestration", "routine_support", "investigation", "requirements", "design", "implementation_plan",
		"implementation", "iterative_execution", "experiment", "design_review", "code_review", "semantic_review",
		"review_synthesis", "review_verdict", "validation", "delivery_audit", "operations", "pr_lifecycle"}
	targets := make(map[string]interface{}, len(duties))
	for _, duty := range duties {
		targets[duty] = map[string]string{"model_id": "default", "reasoning_effort": "medium"}
	}
	targets["design"] = map[string]string{"model_id": designTarget, "reasoning_effort": "high"}
	targets["implementation"] = map[string]string{"model_id": "implement", "reasoning_effort": "low"}
	config := map[string]interface{}{"version": 2, "mode": "duty_fixed", "enabled": true, "layout": "standard",
		"default_model_id": "default", "duty_models": targets, "node_bindings": []interface{}{}, "node_overrides": []interface{}{}}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.db.SetSetting(store.AutoRoutingSetting, string(encoded)); err != nil {
		t.Fatal(err)
	}
	env.server.router.Reload()
}

func dutyIntegrationPre(duty, execution, requestedEffort string) *Preflight {
	return &Preflight{Protocol: "kiro", SourceModel: "auto", Conv: &converter.ParsedConversation{
		ModelID: "auto", ConversationID: "duty-conversation", ContinuationStateKey: "duty-conversation|code",
		WorkflowMetadataVersion: 2, WorkflowStageProvided: true, WorkflowID: "workflow-duty",
		WorkflowDefinitionID: "definition-duty", WorkflowDefinitionRevision: "revision-1", WorkflowNodePath: "steps/design",
		WorkflowNodeID: "design", WorkflowTaskType: duty, WorkflowExecutionID: execution,
		ReasoningEffort: requestedEffort, CurrentContent: "do the declared work", SystemText: "stable instructions",
	}}
}

func TestDutyAutoFixesEffortAndReusesExecutionWithoutToolResults(t *testing.T) {
	env := newAutoStageIntegrationEnv(t)
	installDutyIntegrationConfig(t, env, "shared-responses")
	first := dutyIntegrationPre("design", "step-session-1", "low")
	route, err := env.server.resolveMainRoute(first)
	if err != nil {
		t.Fatal(err)
	}
	if route.Model.ID != "shared-responses" || route.AutoDuty != "design" || route.AutoLockScope != "execution" ||
		!route.AutoEffortFixed || resolveReasoningEffort(first, route.Model) != "high" {
		t.Fatalf("fixed execution target not applied: %+v", route)
	}
	body, _, err := convertRequest(first, route.Model)
	if err != nil {
		t.Fatal(err)
	}
	var outgoing struct {
		Model     string `json:"model"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(body, &outgoing); err != nil {
		t.Fatal(err)
	}
	if outgoing.Model != "upstream-shared-responses" || outgoing.Reasoning.Effort != "high" {
		t.Fatalf("wrong provider target or request-level effort leaked: %+v", outgoing)
	}
	installDutyIntegrationConfig(t, env, "implement-v2")
	continued := dutyIntegrationPre("design", "step-session-1", "max")
	continuedRoute, err := env.server.resolveMainRoute(continued)
	if err != nil {
		t.Fatal(err)
	}
	if continuedRoute.Model.ID != "shared-responses" || resolveReasoningEffort(continued, continuedRoute.Model) != "high" {
		t.Fatal("same node execution changed target after config reload or client effort change")
	}
	next := dutyIntegrationPre("design", "step-session-2", "medium")
	nextRoute, err := env.server.resolveMainRoute(next)
	if err != nil {
		t.Fatal(err)
	}
	if nextRoute.Model.ID != "implement-v2" {
		t.Fatal("new node execution did not read updated configuration")
	}
}

func TestDutyAutoUnknownIsVisibleAndManualEffortRemainsManual(t *testing.T) {
	env := newAutoStageIntegrationEnv(t)
	installDutyIntegrationConfig(t, env, "shared-responses")
	unknown := dutyIntegrationPre("future_duty", "future-session", "max")
	route, err := env.server.resolveMainRoute(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if route.Model.ID != "default" || route.AutoUnknownReason == "" || !route.AutoEffortFixed || resolveReasoningEffort(unknown, route.Model) != "" {
		t.Fatal("unknown declaration was guessed or client effort escaped the fixed default target")
	}
	manual := dutyIntegrationPre("design", "manual-session", "high")
	manual.SourceModel, manual.Conv.ModelID = "manual", "manual"
	manualRoute, err := env.server.resolveMainRoute(manual)
	if err != nil {
		t.Fatal(err)
	}
	if manualRoute.AutoEffortFixed || manual.AutoReasoningEffort != nil || resolveReasoningEffort(manual, manualRoute.Model) != "high" {
		t.Fatal("manual model selection was changed by the Auto duty policy")
	}
}

func TestFixedAutoEffortOverridesRawBodyAndKeepsOtherReasoningFields(t *testing.T) {
	model := &store.Model{ModelID: "gpt-fixed", Provider: "openai", ReasoningEffort: "low"}
	for _, fixed := range []string{"high", ""} {
		pre := &Preflight{AutoReasoningEffort: &fixed, Conv: &converter.ParsedConversation{ReasoningEffort: "max"}}
		body, err := prepareRawResponsesBody([]byte(`{"model":"auto","input":[],"reasoning":{"effort":"max","summary":"auto"},"prompt_cache_key":"stable-key"}`), pre, model)
		if err != nil {
			t.Fatal(err)
		}
		var outgoing struct {
			Model     string            `json:"model"`
			Reasoning map[string]string `json:"reasoning"`
			CacheKey  string            `json:"prompt_cache_key"`
		}
		if err := json.Unmarshal(body, &outgoing); err != nil {
			t.Fatal(err)
		}
		if outgoing.Model != "gpt-fixed" || outgoing.Reasoning["effort"] != fixed || outgoing.Reasoning["summary"] != "auto" || outgoing.CacheKey != "stable-key" {
			t.Fatalf("raw fixed effort or stable fields were changed: %s", body)
		}
		if _, present := outgoing.Reasoning["effort"]; fixed == "" && present {
			t.Fatal("fixed upstream default still sent a client effort")
		}
	}
}
