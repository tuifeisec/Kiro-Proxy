package store

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAutoDutyTargetsAcceptExistingGroupsForBothLayouts(t *testing.T) {
	models := []Model{{ID: "member", Enabled: true}, {ID: "fallback-member", Enabled: true}, {ID: "disabled", Enabled: false}}
	groups := []ModelGroup{
		{ID: "gpt", Enabled: true, FallbackGroupID: "fallback", Members: []ModelGroupMember{{ModelID: "member"}, {ModelID: "disabled"}}},
		{ID: "fallback", Enabled: true, Members: []ModelGroupMember{{ModelID: "member"}, {ModelID: "fallback-member"}}},
	}
	for _, layout := range []string{AutoRoutingLayoutStandard, AutoRoutingLayoutExpanded} {
		config := AutoRoutingConfig{Version: 2, Mode: AutoRoutingModeDutyFixed, Enabled: true, Layout: layout,
			DefaultModelID: "gpt", DutyModels: map[string]AutoDutyTarget{},
			NodeOverrides: []AutoNodeOverride{{DefinitionID: "def", DefinitionRevision: "rev", NodePath: "node", ModelID: "gpt"}}}
		for _, duty := range AutoDutyNames(layout) {
			config.DutyModels[duty] = AutoDutyTarget{ModelID: "gpt"}
		}
		if err := ValidateAutoRoutingConfig(config, models, groups); err != nil {
			t.Fatalf("%s group references rejected: %v", layout, err)
		}
	}
	candidates, err := AutoTargetModels(models, groups, "gpt")
	if err != nil || len(candidates) != 2 || candidates[0].ID != "member" || candidates[1].ID != "fallback-member" {
		t.Fatalf("enabled fallback expansion = %#v, %v", candidates, err)
	}
	for _, item := range []struct {
		name   string
		groups []ModelGroup
		target string
		want   string
	}{
		{"missing", groups, "missing", "unknown"},
		{"disabled", []ModelGroup{{ID: "gpt", Enabled: false}}, "gpt", "disabled group"},
		{"empty", []ModelGroup{{ID: "gpt", Enabled: true, Members: []ModelGroupMember{{ModelID: "disabled"}}}}, "gpt", "no enabled candidates"},
		{"collision", []ModelGroup{{ID: "member", Enabled: true}}, "member", "conflicts"},
	} {
		t.Run(item.name, func(t *testing.T) {
			if _, err := AutoTargetModels(models, item.groups, item.target); err == nil || !strings.Contains(err.Error(), item.want) {
				t.Fatalf("target error = %v, want %q", err, item.want)
			}
		})
	}
}

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

func TestParseAutoRoutingConfigV2DutyLayoutsAreStrict(t *testing.T) {
	for _, layout := range []string{AutoRoutingLayoutStandard, AutoRoutingLayoutExpanded} {
		t.Run(layout, func(t *testing.T) {
			raw := v2AutoRoutingRaw(layout)
			config, err := ParseAutoRoutingConfig(raw)
			if err != nil {
				t.Fatal(err)
			}
			if config.Version != AutoRoutingVersion || config.Mode != AutoRoutingModeDutyFixed || config.Layout != layout {
				t.Fatalf("configuration header = %#v", config)
			}
			if got, want := len(config.DutyModels), len(AutoDutyNames(layout)); got != want {
				t.Fatalf("duty count = %d, want %d", got, want)
			}
			if err := ValidateAutoRoutingConfig(config, v2AutoRoutingModels(layout)); err != nil {
				t.Fatalf("valid config rejected: %v", err)
			}
		})
	}
}

func TestParseAutoRoutingConfigV2RejectsUnknownDuplicateAndUnstableBindings(t *testing.T) {
	base := v2AutoRoutingRaw(AutoRoutingLayoutStandard)
	for name, raw := range map[string]string{
		"unknown top-level":          strings.Replace(base, `"node_overrides":[]`, `"node_overrides":[],"future":true`, 1),
		"duplicate top-level":        strings.Replace(base, `"enabled":true`, `"enabled":true,"enabled":true`, 1),
		"unknown duty":               strings.Replace(base, `"duty_models":{`, `"duty_models":{"future":{"model_id":"m1","reasoning_effort":""},`, 1),
		"duplicate node binding":     strings.Replace(base, `"node_bindings":[]`, `"node_bindings":[{"definition_id":"d","definition_revision":"1","node_path":"a","duty":"design"},{"definition_id":"d","definition_revision":"1","node_path":"a","duty":"design"}]`, 1),
		"missing stable revision":    strings.Replace(base, `"node_bindings":[]`, `"node_bindings":[{"definition_id":"d","definition_revision":" ","node_path":"a","duty":"design"}]`, 1),
		"unsupported effort":         strings.Replace(base, `"reasoning_effort":""`, `"reasoning_effort":"ultra"`, 1),
		"wrong version":              strings.Replace(base, `"version":2`, `"version":3`, 1),
		"unexpected stage schema":    strings.Replace(base, `"layout":"standard"`, `"layout":"unknown"`, 1),
		"v2 unknown field in target": strings.Replace(base, `"model_id":"m1","reasoning_effort":""`, `"model_id":"m1","reasoning_effort":"","effort":"high"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAutoRoutingConfig(raw); err == nil {
				t.Fatalf("invalid v2 configuration unexpectedly parsed: %s", raw)
			}
		})
	}
}

func TestValidateAutoRoutingConfigV2ReferencesEveryActiveTarget(t *testing.T) {
	config, err := ParseAutoRoutingConfig(v2AutoRoutingRaw(AutoRoutingLayoutStandard))
	if err != nil {
		t.Fatal(err)
	}
	models := v2AutoRoutingModels(AutoRoutingLayoutStandard)
	if err := ValidateAutoRoutingConfig(config, models); err != nil {
		t.Fatal(err)
	}

	config.DutyModels["design"] = AutoDutyTarget{ModelID: "missing", ReasoningEffort: "high"}
	if err := ValidateAutoRoutingConfig(config, models); err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("unknown duty model error = %v", err)
	}
	config, err = ParseAutoRoutingConfig(v2AutoRoutingRaw(AutoRoutingLayoutStandard))
	if err != nil {
		t.Fatal(err)
	}
	delete(config.DutyModels, "design")
	if err := ValidateAutoRoutingConfig(config, models); err == nil || !strings.Contains(err.Error(), "duty_models.design") {
		t.Fatalf("missing enabled duty error = %v", err)
	}

	draft := AutoRoutingConfig{Version: AutoRoutingVersion, Mode: AutoRoutingModeDutyFixed, Layout: AutoRoutingLayoutStandard,
		DutyModels: map[string]AutoDutyTarget{"design": {ReasoningEffort: ""}}, NodeBindings: []AutoNodeBinding{}, NodeOverrides: []AutoNodeOverride{}}
	if err := ValidateAutoRoutingConfig(draft, models); err != nil {
		t.Fatalf("disabled empty draft rejected: %v", err)
	}
}

func TestValidateAutoRoutingConfigV2RequiresOverrideModelOnlyWhenEnabled(t *testing.T) {
	raw := strings.Replace(v2AutoRoutingRaw(AutoRoutingLayoutStandard), `"node_overrides":[]`,
		`"node_overrides":[{"definition_id":"d","definition_revision":"1","node_path":"a","model_id":"","reasoning_effort":""}]`, 1)
	config, err := ParseAutoRoutingConfig(raw)
	if err != nil {
		t.Fatalf("empty override model is a valid draft schema: %v", err)
	}
	models := v2AutoRoutingModels(AutoRoutingLayoutStandard)
	if err := ValidateAutoRoutingConfig(config, models); err == nil || !strings.Contains(err.Error(), "node_overrides[0].model_id") {
		t.Fatalf("enabled override without a model was accepted: %v", err)
	}
	config.Enabled = false
	if err := ValidateAutoRoutingConfig(config, models); err != nil {
		t.Fatalf("disabled override draft was rejected: %v", err)
	}
}

func TestParseAutoRoutingConfigV2PreservesMaximumStableIdentityWithoutTrimming(t *testing.T) {
	config, err := ParseAutoRoutingConfig(v2AutoRoutingRaw(AutoRoutingLayoutStandard))
	if err != nil {
		t.Fatal(err)
	}
	config.NodeBindings = []AutoNodeBinding{{DefinitionID: strings.Repeat("d", 1024), DefinitionRevision: strings.Repeat("r", 256),
		NodePath: strings.Repeat("p", 1024), Duty: "investigation"}}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseAutoRoutingConfig(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	got := parsed.NodeBindings[0]
	if got.DefinitionID != config.NodeBindings[0].DefinitionID || got.DefinitionRevision != config.NodeBindings[0].DefinitionRevision || got.NodePath != config.NodeBindings[0].NodePath {
		t.Fatal("maximum stable identity was changed during config parsing")
	}

	config.NodeBindings[0].NodePath = " steps/investigate"
	encoded, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAutoRoutingConfig(string(encoded)); err == nil || !strings.Contains(err.Error(), "leading or trailing whitespace") {
		t.Fatalf("unstable whitespace identity error = %v", err)
	}
}

func v2AutoRoutingRaw(layout string) string {
	targets := make(map[string]AutoDutyTarget)
	for _, duty := range AutoDutyNames(layout) {
		targets[duty] = AutoDutyTarget{ModelID: "m1", ReasoningEffort: ""}
	}
	config := AutoRoutingConfig{Version: AutoRoutingVersion, Mode: AutoRoutingModeDutyFixed, Layout: layout,
		Enabled: true, DefaultModelID: "m1", DutyModels: targets, NodeBindings: []AutoNodeBinding{}, NodeOverrides: []AutoNodeOverride{}}
	encoded, err := json.Marshal(config)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func v2AutoRoutingModels(layout string) []Model {
	return []Model{{ID: "m1", Enabled: true, Provider: "openai"}}
}

func TestSetSettingsWithAutoRoutingMigrationRollsBackBothValuesOnBackupFailure(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "auto-routing-backup.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	legacy := `{"mode":"stage_fixed","enabled":true,"default_model_id":"m1","stage_models":{"run":"m1","plan":"m1","implement":"m1","review":"m1","finalize":"m1"}}`
	if err := db.SetSetting(AutoRoutingSetting, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`CREATE TRIGGER reject_auto_routing_backup BEFORE INSERT ON settings WHEN NEW.key = 'auto_routing_config_stage_fixed_backup' BEGIN SELECT RAISE(ABORT, 'backup blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSettingsWithAutoRoutingMigration(map[string]string{AutoRoutingSetting: v2AutoRoutingRaw(AutoRoutingLayoutStandard)}); err == nil {
		t.Fatal("migration unexpectedly succeeded despite the backup trigger")
	}
	stored, err := db.GetSetting(AutoRoutingSetting)
	if err != nil || stored != legacy {
		t.Fatalf("failed migration changed the old config: %q (%v)", stored, err)
	}
	backup, err := db.GetSetting(AutoRoutingStageFixedBackupSetting)
	if err != nil || backup != "" {
		t.Fatalf("failed migration left a backup: %q (%v)", backup, err)
	}
}
