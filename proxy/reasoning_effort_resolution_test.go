package proxy

import (
	"strings"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

func reasoningTestModel(provider string, defaultEffort string, supported ...string) *store.Model {
	endpoint := "/v1/chat/completions"
	switch provider {
	case "anthropic":
		endpoint = "/v1/messages"
	case "gemini":
		endpoint = "/v1beta/models/{model}:streamGenerateContent"
	case "codex":
		endpoint = "/codex/responses"
	}
	return &store.Model{ID: provider + "-model", ModelID: provider + "-upstream", Provider: provider,
		BaseURL: "https://upstream.invalid", Endpoint: endpoint, ReasoningEffort: defaultEffort, SupportedReasoningEfforts: supported}
}

func reasoningTestPre(requestEffort, groupEffort string) *Preflight {
	return &Preflight{Protocol: "kiro", GroupReasoningEffort: groupEffort,
		Conv: &converter.ParsedConversation{ModelID: "manual", ConversationID: "effort-conv", CurrentContent: "hi", ReasoningEffort: requestEffort}}
}

func TestResolveReasoningEffortDecisionPriorityAndNearestFit(t *testing.T) {
	fixed := "xhigh"
	auto := reasoningTestPre("low", "medium")
	auto.SourceModel, auto.AutoReasoningEffort = "auto", &fixed
	cases := []struct {
		name      string
		pre       *Preflight
		model     *store.Model
		requested string
		wire      string
		source    reasoningEffortSource
	}{
		{"request wins and is kept", reasoningTestPre("high", "low"), reasoningTestModel("openai", "medium"), "high", "high", reasoningEffortSourceRequest},
		{"group xhigh fits declared anthropic high", reasoningTestPre("", "xhigh"), reasoningTestModel("anthropic", "", "none", "low", "medium", "high"), "xhigh", "high", reasoningEffortSourceGroup},
		{"request max fits declared tiers", reasoningTestPre("max", ""), reasoningTestModel("openai", "", "low", "medium"), "max", "medium", reasoningEffortSourceRequest},
		{"request none fits gemini low", reasoningTestPre("none", ""), reasoningTestModel("gemini", ""), "none", "low", reasoningEffortSourceRequest},
		{"model default", reasoningTestPre("", ""), reasoningTestModel("openai", "low"), "low", "low", reasoningEffortSourceModel},
		{"nothing configured sends nothing", reasoningTestPre("", ""), reasoningTestModel("openai", ""), "", "", ""},
		{"auto fixed overrides request and group", auto, reasoningTestModel("anthropic", "low", "none", "low", "medium", "high"), "xhigh", "high", reasoningEffortSourceAuto},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := resolveReasoningEffortDecision(tc.pre, tc.model)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Requested != tc.requested || decision.Wire != tc.wire || decision.Source != tc.source {
				t.Fatalf("decision = %+v, want requested=%q wire=%q source=%q", decision, tc.requested, tc.wire, tc.source)
			}
			if got := resolveReasoningEffort(tc.pre, tc.model); got != tc.wire {
				t.Fatalf("resolveReasoningEffort = %q, want wire %q", got, tc.wire)
			}
		})
	}
}

func TestResolveReasoningEffortClassifierUsesLowestEffectiveTier(t *testing.T) {
	cases := []struct {
		model *store.Model
		wire  string
	}{
		{reasoningTestModel("openai", "high"), "none"},
		{reasoningTestModel("anthropic", "high"), "none"},
		{reasoningTestModel("gemini", "high"), "low"},
		{reasoningTestModel("openai", "high", "medium", "high"), "medium"},
		{reasoningTestModel("openai", ""), ""},
	}
	for _, tc := range cases {
		pre := reasoningTestPre("max", "xhigh")
		pre.IsClassifier, pre.Conv.IsClassifier = true, true
		decision, err := resolveReasoningEffortDecision(pre, tc.model)
		if err != nil {
			t.Fatal(err)
		}
		wantSource := reasoningEffortSourceClassifier
		if tc.wire == "" {
			wantSource = ""
		}
		if decision.Wire != tc.wire || decision.Requested != tc.wire || decision.Source != wantSource {
			t.Fatalf("%s default=%q supported=%v: decision=%+v, want wire %q", tc.model.Provider, tc.model.ReasoningEffort, tc.model.SupportedReasoningEfforts, decision, tc.wire)
		}
	}
}

func TestResolveReasoningEffortDecisionReportsUnusableConfig(t *testing.T) {
	cases := map[string]*store.Model{
		"only none declared":     reasoningTestModel("openai", "", "none"),
		"stored invalid tier":    reasoningTestModel("gemini", "", "max"),
		"stored invalid default": reasoningTestModel("openai", "turbo"),
	}
	for name, model := range cases {
		pre := reasoningTestPre("high", "")
		if name == "stored invalid default" {
			pre = reasoningTestPre("", "")
		}
		if _, err := resolveReasoningEffortDecision(pre, model); err == nil {
			t.Fatalf("%s: expected an explicit error", name)
		}
		if got := resolveReasoningEffort(pre, model); got != "" {
			t.Fatalf("%s: wire must be empty on error, got %q", name, got)
		}
		if _, _, err := convertRequest(pre, model); err == nil {
			t.Fatalf("%s: conversion must return the resolution error", name)
		}
	}
}

func TestConvertRequestUsesWireEffortAndHonorsDisabledField(t *testing.T) {
	cases := []struct {
		name     string
		model    *store.Model
		effort   string
		contains string
		absent   []string
	}{
		{"anthropic xhigh maps to declared high budget", reasoningTestModel("anthropic", "", "none", "low", "medium", "high"), "xhigh", `"thinking":{"type":"enabled","budget_tokens":8192}`, nil},
		{"gemini none maps to low budget", reasoningTestModel("gemini", ""), "none", `"thinkingConfig":{"thinkingBudget":1024}`, nil},
		{"openai chat declared tiers", reasoningTestModel("openai", "", "low", "medium"), "max", `"reasoning_effort":"medium"`, nil},
		{"codex declared tiers", reasoningTestModel("codex", "", "high"), "low", `"effort":"high"`, nil},
	}
	disabled := []*store.Model{reasoningTestModel("anthropic", "high"), reasoningTestModel("gemini", "high"), reasoningTestModel("openai", "high"), reasoningTestModel("codex", "high")}
	for _, model := range disabled {
		model.DisableReasoningField = true
		cases = append(cases, struct {
			name     string
			model    *store.Model
			effort   string
			contains string
			absent   []string
		}{"disabled " + model.Provider, model, "max", "", []string{"thinking", "reasoning", "reasoning_effort", "thinkingConfig"}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _, err := convertRequest(reasoningTestPre(tc.effort, ""), tc.model)
			if err != nil {
				t.Fatal(err)
			}
			if tc.contains != "" && !strings.Contains(string(body), tc.contains) {
				t.Fatalf("body missing %s: %s", tc.contains, body)
			}
			var root map[string]json.RawMessage
			if err := json.Unmarshal(body, &root); err != nil {
				t.Fatal(err)
			}
			for _, field := range tc.absent {
				if _, ok := root[field]; ok {
					t.Fatalf("disabled model still sent %q: %s", field, body)
				}
			}
			if config := root["generationConfig"]; tc.absent != nil && strings.Contains(string(config), "thinking") {
				t.Fatalf("disabled gemini still sent thinkingConfig: %s", body)
			}
		})
	}
}

func TestBindUsageReasoningEffortRecordsRequestedWireAndSource(t *testing.T) {
	usage := newRequestUsageRecorder(nil, time.Now(), nil, "/", "kiro")
	model := reasoningTestModel("anthropic", "", "none", "low", "medium", "high")
	bindUsageReasoningEffort(usage, reasoningTestPre("", "xhigh"), model)
	if usage.item.ReasoningEffort != "high" || usage.item.RequestedReasoningEffort != "xhigh" || usage.item.ReasoningEffortSource != "group" {
		t.Fatalf("usage decision = wire %q requested %q source %q", usage.item.ReasoningEffort, usage.item.RequestedReasoningEffort, usage.item.ReasoningEffortSource)
	}
	model.DisableReasoningField = true
	bindUsageReasoningEffort(usage, reasoningTestPre("", "xhigh"), model)
	if usage.item.ReasoningEffort != "" || usage.item.RequestedReasoningEffort != "xhigh" {
		t.Fatalf("disabled field must record empty wire with the requested tier: %+v", usage.item)
	}
	meta := reasoningEffortLogMeta(reasoningEffortDecision{Requested: "xhigh", Wire: "", Source: reasoningEffortSourceGroup, FieldDisabled: true})
	if meta["reasoning_effort_requested"] != "xhigh" || meta["reasoning_effort_wire"] != "" || meta["reasoning_effort_field_disabled"] != true {
		t.Fatalf("log metadata = %v", meta)
	}
	if reasoningEffortLogMeta(reasoningEffortDecision{}) != nil {
		t.Fatal("no reasoning information must not emit log metadata")
	}
}
