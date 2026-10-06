package proxy

import (
	"encoding/json"
	"testing"

	"kiro-proxy/converter"
	"kiro-proxy/store"
)

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
