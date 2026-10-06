package store

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseAutoRoutingConfigDefaultsAndCanonicalizes(t *testing.T) {
	config, err := ParseAutoRoutingConfig(" \n ")
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != AutoRoutingModeStageFixed || config.Enabled || config.DefaultModelID != "" || config.StageModels != (AutoStageModels{}) {
		t.Fatalf("empty configuration = %#v", config)
	}

	config, err = ParseAutoRoutingConfig(`{"mode":" STAGE_FIXED ","enabled":false,"default_model_id":" model-1 ","stage_models":{"run":" run ","plan":"plan","implement":"implement","review":"review","finalize":"finalize"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != AutoRoutingModeStageFixed || config.DefaultModelID != "model-1" || config.StageModels.Run != "run" {
		t.Fatalf("configuration was not canonicalized: %#v", config)
	}
	if got := config.StageModels.ModelID("  IMPLEMENT "); got != "implement" {
		t.Fatalf("ModelID normalized lookup = %q", got)
	}
	if got := config.StageModels.ModelID("other"); got != "" {
		t.Fatalf("ModelID unknown stage = %q", got)
	}
	if got, want := config.StageModels.ModelIDs(), []string{"run", "plan", "implement", "review", "finalize"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ModelIDs() = %#v, want %#v", got, want)
	}
}

func TestParseAutoRoutingConfigRejectsLegacyAndInvalidInput(t *testing.T) {
	for name, raw := range map[string]string{
		"missing mode":                    `{"enabled":false}`,
		"missing mode with unknown field": `{"unexpected":true}`,
		"empty mode":                      `{"mode":" "}`,
		"task rules mode":                 `{"mode":"task_rules"}`,
		"unknown mode":                    `{"mode":"other"}`,
		"non-string mode":                 `{"mode":1}`,
		"unknown field":                   `{"mode":"stage_fixed","unexpected":true}`,
		"invalid JSON":                    `{"mode":`,
		"non-object":                      `[]`,
		"multiple JSON values":            `{"mode":"stage_fixed"} {}`,
		"enabled null":                    `{"mode":"stage_fixed","enabled":null,"stage_models":{"run":"r","plan":"p","implement":"i","review":"v","finalize":"f"}}`,
		"enabled string":                  `{"mode":"stage_fixed","enabled":"false","stage_models":{"run":"r","plan":"p","implement":"i","review":"v","finalize":"f"}}`,
		"default null":                    `{"mode":"stage_fixed","default_model_id":null,"stage_models":{"run":"r","plan":"p","implement":"i","review":"v","finalize":"f"}}`,
		"missing stage models":            `{"mode":"stage_fixed"}`,
		"stage models null":               `{"mode":"stage_fixed","stage_models":null}`,
		"missing stage key":               `{"mode":"stage_fixed","stage_models":{"run":"r","plan":"p","implement":"i","review":"v"}}`,
		"unknown stage key":               `{"mode":"stage_fixed","stage_models":{"run":"r","plan":"p","implement":"i","review":"v","finalize":"f","other":"x"}}`,
		"stage wrong type":                `{"mode":"stage_fixed","stage_models":{"run":null,"plan":"p","implement":"i","review":"v","finalize":"f"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAutoRoutingConfig(raw)
			if err == nil {
				t.Fatalf("ParseAutoRoutingConfig(%q) unexpectedly succeeded", raw)
			}
			if name == "missing mode" || name == "missing mode with unknown field" || name == "empty mode" || name == "task rules mode" {
				if !strings.Contains(err.Error(), "legacy Auto configuration") || !strings.Contains(err.Error(), "stage_fixed") {
					t.Fatalf("migration error = %v", err)
				}
			}
		})
	}
}

func TestParseAutoRoutingConfigDropsRetiredFieldsAtBoundary(t *testing.T) {
	raw := `{"mode":"stage_fixed","enabled":false,"default_model_id":" default ","stage_models":{"run":" run ","plan":"plan","implement":"implement","review":"review","finalize":"finalize"},"vision_model_id":{"ignored":true},"reasoning_model_id":null,"long_context_model_id":7,"fast_model_id":[1],"fast_max_chars":"ignored","long_context_min_chars":false,"task_rules":{"ignored":true}}`
	config, err := ParseAutoRoutingConfig(raw)
	if err != nil {
		t.Fatalf("stage config with retired fields rejected: %v", err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	want := []string{"mode", "enabled", "default_model_id", "stage_models"}
	if len(saved) != len(want) {
		t.Fatalf("canonical fields = %#v", saved)
	}
	for _, key := range want {
		if _, ok := saved[key]; !ok {
			t.Fatalf("canonical config lacks %q: %#v", key, saved)
		}
	}
}

func TestValidateAutoRoutingConfigStageFixedTargets(t *testing.T) {
	models := []Model{{ID: "enabled", Enabled: true}, {ID: "disabled", Enabled: false}}
	valid := AutoRoutingConfig{
		Mode: AutoRoutingModeStageFixed, Enabled: true, DefaultModelID: " enabled ",
		StageModels: AutoStageModels{Run: "enabled", Plan: "enabled", Implement: "enabled", Review: "enabled", Finalize: "enabled"},
	}
	if err := ValidateAutoRoutingConfig(valid, models); err != nil {
		t.Fatalf("valid configuration with repeated targets rejected: %v", err)
	}

	for _, stage := range []string{"run", "plan", "implement", "review", "finalize"} {
		t.Run("missing "+stage, func(t *testing.T) {
			config := valid
			switch stage {
			case "run":
				config.StageModels.Run = ""
			case "plan":
				config.StageModels.Plan = ""
			case "implement":
				config.StageModels.Implement = ""
			case "review":
				config.StageModels.Review = ""
			case "finalize":
				config.StageModels.Finalize = ""
			}
			if err := ValidateAutoRoutingConfig(config, models); err == nil || !strings.Contains(err.Error(), "stage_models."+stage) {
				t.Fatalf("missing stage error = %v", err)
			}
		})
	}

	for name, mutate := range map[string]func(*AutoRoutingConfig){
		"missing default": func(c *AutoRoutingConfig) { c.DefaultModelID = "" },
		"unknown target":  func(c *AutoRoutingConfig) { c.StageModels.Review = "missing" },
		"disabled target": func(c *AutoRoutingConfig) { c.StageModels.Review = "disabled" },
	} {
		t.Run(name, func(t *testing.T) {
			config := valid
			mutate(&config)
			if err := ValidateAutoRoutingConfig(config, models); err == nil {
				t.Fatal("invalid enabled configuration unexpectedly succeeded")
			}
		})
	}
	if err := ValidateAutoRoutingConfig(AutoRoutingConfig{}, models); err == nil {
		t.Fatal("configuration with empty mode unexpectedly succeeded")
	}

	if err := ValidateAutoRoutingConfig(AutoRoutingConfig{Mode: AutoRoutingModeStageFixed}, models); err != nil {
		t.Fatalf("empty disabled draft rejected: %v", err)
	}
	if err := ValidateAutoRoutingConfig(AutoRoutingConfig{Mode: AutoRoutingModeStageFixed, StageModels: AutoStageModels{Run: "missing"}}, models); err == nil {
		t.Fatal("disabled draft with invalid configured target unexpectedly succeeded")
	}
}
