package store

import (
	"strings"
	"testing"
)

func TestParseAutoRoutingConfigDefaultsAndCanonicalizes(t *testing.T) {
	config, err := ParseAutoRoutingConfig(`{"enabled":true,"default_model_id":" model-1 ","task_rules":[{"field":" AGENT_MODE ","value":"  agent  ","model_id":" model-2 "}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if config.FastMaxChars != 500 || config.LongContextMinChars != 32000 {
		t.Fatalf("threshold defaults = %d/%d", config.FastMaxChars, config.LongContextMinChars)
	}
	if config.DefaultModelID != "model-1" || len(config.TaskRules) != 1 || config.TaskRules[0] != (AutoTaskRule{Field: "agent_mode", Value: "agent", ModelID: "model-2"}) {
		t.Fatalf("configuration was not canonicalized: %#v", config)
	}
	empty, err := ParseAutoRoutingConfig(" \n ")
	if err != nil || empty.Enabled || empty.FastMaxChars != 500 || empty.LongContextMinChars != 32000 {
		t.Fatalf("empty configuration = %#v, error=%v", empty, err)
	}
}

func TestParseAutoRoutingConfigRejectsInvalidInput(t *testing.T) {
	for _, raw := range []string{
		`{"enabled":false,"unexpected":true}`,
		`{"enabled":`,
		`[]`,
		`{} {}`,
		`{"fast_max_chars":-1}`,
		`{"long_context_min_chars":-1}`,
		`{"fast_max_chars":32000,"long_context_min_chars":32000}`,
		`{"task_rules":[{"field":"other","value":"x","model_id":"m"}]}`,
		`{"task_rules":[{"field":"agent_mode","value":"  ","model_id":"m"}]}`,
		`{"task_rules":[{"field":"agent_mode","value":"x","model_id":" "}]}`,
		`{"task_rules":[{"field":"agent_mode","value":"x","model_id":"m"},{"field":"AGENT_MODE","value":" X ","model_id":"n"}]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseAutoRoutingConfig(raw); err == nil {
				t.Fatalf("ParseAutoRoutingConfig(%q) unexpectedly succeeded", raw)
			}
		})
	}
	if _, err := ParseAutoRoutingConfig(`{"fast_max_chars":100,"long_context_min_chars":100}`); err == nil || !strings.Contains(err.Error(), "less than") {
		t.Fatalf("equal thresholds error = %v", err)
	}
}

func TestValidateAutoRoutingConfigChecksEveryTarget(t *testing.T) {
	models := []Model{
		{ID: "enabled", Enabled: true, ImageInputMode: "supported"},
		{ID: "no-images", Enabled: true, ImageInputMode: "auto"},
		{ID: "disabled", Enabled: false, ImageInputMode: "supported"},
	}
	valid := AutoRoutingConfig{
		Enabled: true, DefaultModelID: "enabled", VisionModelID: "enabled",
		ReasoningModelID: "enabled", LongContextModelID: "enabled", FastModelID: "enabled",
		TaskRules: []AutoTaskRule{{Field: "agent_task_type", Value: "coding", ModelID: "enabled"}},
	}
	if err := ValidateAutoRoutingConfig(valid, models); err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}
	for name, mutate := range map[string]func(*AutoRoutingConfig){
		"enabled requires default": func(c *AutoRoutingConfig) { c.DefaultModelID = "" },
		"unknown default":          func(c *AutoRoutingConfig) { c.DefaultModelID = "missing" },
		"disabled model":           func(c *AutoRoutingConfig) { c.FastModelID = "disabled" },
		"vision capability":        func(c *AutoRoutingConfig) { c.VisionModelID = "no-images" },
		"rule target":              func(c *AutoRoutingConfig) { c.TaskRules[0].ModelID = "missing" },
	} {
		t.Run(name, func(t *testing.T) {
			config := valid
			config.TaskRules = append([]AutoTaskRule(nil), valid.TaskRules...)
			mutate(&config)
			if err := ValidateAutoRoutingConfig(config, models); err == nil {
				t.Fatal("invalid configuration unexpectedly succeeded")
			}
		})
	}
	// Disabled drafts still validate configured targets.
	draft := AutoRoutingConfig{FastModelID: "disabled"}
	if err := ValidateAutoRoutingConfig(draft, models); err == nil {
		t.Fatal("disabled draft with disabled target unexpectedly succeeded")
	}
}
