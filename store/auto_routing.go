package store

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const AutoRoutingSetting = "auto_routing_config"

const AutoRoutingModeStageFixed = "stage_fixed"

// AutoRoutingConfig 将五阶段固定路由配置保存在一个设置值中。
type AutoRoutingConfig struct {
	Mode           string          `json:"mode"`
	Enabled        bool            `json:"enabled"`
	DefaultModelID string          `json:"default_model_id"`
	StageModels    AutoStageModels `json:"stage_models"`
}

type AutoStageModels struct {
	Run       string `json:"run"`
	Plan      string `json:"plan"`
	Implement string `json:"implement"`
	Review    string `json:"review"`
	Finalize  string `json:"finalize"`
}

// ModelID returns the configured target for a normalized stage name.
func (m AutoStageModels) ModelID(stage string) string {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "run":
		return m.Run
	case "plan":
		return m.Plan
	case "implement":
		return m.Implement
	case "review":
		return m.Review
	case "finalize":
		return m.Finalize
	default:
		return ""
	}
}

// ModelIDs returns stage targets in the fixed run, plan, implement, review,
// finalize order. Empty entries are retained; the default target is separate.
func (m AutoStageModels) ModelIDs() []string {
	return []string{m.Run, m.Plan, m.Implement, m.Review, m.Finalize}
}

var retiredAutoRoutingFields = map[string]struct{}{
	"vision_model_id":        {},
	"reasoning_model_id":     {},
	"long_context_model_id":  {},
	"fast_model_id":          {},
	"fast_max_chars":         {},
	"long_context_min_chars": {},
	"task_rules":             {},
}

var autoRoutingFields = map[string]struct{}{
	"mode":             {},
	"enabled":          {},
	"default_model_id": {},
	"stage_models":     {},
}

var autoStageFields = map[string]struct{}{
	"run":       {},
	"plan":      {},
	"implement": {},
	"review":    {},
	"finalize":  {},
}

// ParseAutoRoutingConfig 解析并规范化持久化的设置值。
func ParseAutoRoutingConfig(raw string) (AutoRoutingConfig, error) {
	config := AutoRoutingConfig{Mode: AutoRoutingModeStageFixed}
	if strings.TrimSpace(raw) == "" {
		return config, nil
	}

	fields := make(map[string]json.RawMessage)
	decoder := json.NewDecoder(strings.NewReader(raw))
	if err := decoder.Decode(&fields); err != nil {
		return AutoRoutingConfig{}, fmt.Errorf("invalid configuration: %w", err)
	}
	if fields == nil {
		return AutoRoutingConfig{}, fmt.Errorf("configuration must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return AutoRoutingConfig{}, fmt.Errorf("configuration must contain only one JSON value")
	} else if err != io.EOF {
		return AutoRoutingConfig{}, fmt.Errorf("invalid trailing data: %w", err)
	}

	modeRaw, hasMode := fields["mode"]
	if !hasMode {
		return AutoRoutingConfig{}, fmt.Errorf("legacy Auto configuration is no longer supported; configure and save stage_fixed with five stage models")
	}
	var mode string
	if err := json.Unmarshal(modeRaw, &mode); err != nil {
		return AutoRoutingConfig{}, fmt.Errorf("mode must be a string: %w", err)
	}
	config.Mode = strings.ToLower(strings.TrimSpace(mode))
	if config.Mode == "" || config.Mode == "task_rules" {
		return AutoRoutingConfig{}, fmt.Errorf("legacy Auto configuration is no longer supported; configure and save stage_fixed with five stage models")
	}
	if config.Mode != AutoRoutingModeStageFixed {
		return AutoRoutingConfig{}, fmt.Errorf("mode must be %q", AutoRoutingModeStageFixed)
	}
	for key := range fields {
		if _, ok := autoRoutingFields[key]; ok {
			continue
		}
		if _, retired := retiredAutoRoutingFields[key]; retired {
			continue
		}
		return AutoRoutingConfig{}, fmt.Errorf("unknown configuration field %q", key)
	}

	if enabledRaw, ok := fields["enabled"]; ok {
		if strings.TrimSpace(string(enabledRaw)) == "null" {
			return AutoRoutingConfig{}, fmt.Errorf("enabled must be a boolean")
		}
		if err := json.Unmarshal(enabledRaw, &config.Enabled); err != nil {
			return AutoRoutingConfig{}, fmt.Errorf("enabled must be a boolean: %w", err)
		}
	}
	if defaultRaw, ok := fields["default_model_id"]; ok {
		if strings.TrimSpace(string(defaultRaw)) == "null" {
			return AutoRoutingConfig{}, fmt.Errorf("default_model_id must be a string")
		}
		if err := json.Unmarshal(defaultRaw, &config.DefaultModelID); err != nil {
			return AutoRoutingConfig{}, fmt.Errorf("default_model_id must be a string: %w", err)
		}
	}
	config.DefaultModelID = strings.TrimSpace(config.DefaultModelID)

	stageRaw, ok := fields["stage_models"]
	if !ok {
		return AutoRoutingConfig{}, fmt.Errorf("stage_models is required")
	}
	stageFields := make(map[string]json.RawMessage)
	if err := json.Unmarshal(stageRaw, &stageFields); err != nil {
		return AutoRoutingConfig{}, fmt.Errorf("stage_models must be an object: %w", err)
	}
	if stageFields == nil {
		return AutoRoutingConfig{}, fmt.Errorf("stage_models must be an object")
	}
	for key := range stageFields {
		if _, ok := autoStageFields[key]; !ok {
			return AutoRoutingConfig{}, fmt.Errorf("unknown stage_models field %q", key)
		}
	}
	stageTargets := []struct {
		name   string
		target *string
	}{
		{"run", &config.StageModels.Run},
		{"plan", &config.StageModels.Plan},
		{"implement", &config.StageModels.Implement},
		{"review", &config.StageModels.Review},
		{"finalize", &config.StageModels.Finalize},
	}
	for _, stage := range stageTargets {
		value, ok := stageFields[stage.name]
		if !ok {
			return AutoRoutingConfig{}, fmt.Errorf("stage_models.%s is required", stage.name)
		}
		if strings.TrimSpace(string(value)) == "null" {
			return AutoRoutingConfig{}, fmt.Errorf("stage_models.%s must be a string", stage.name)
		}
		if err := json.Unmarshal(value, stage.target); err != nil {
			return AutoRoutingConfig{}, fmt.Errorf("stage_models.%s must be a string: %w", stage.name, err)
		}
		*stage.target = strings.TrimSpace(*stage.target)
	}
	return config, nil
}

// ValidateAutoRoutingConfig 根据实际启用的模型校验所有已配置目标。
func ValidateAutoRoutingConfig(config AutoRoutingConfig, models []Model) error {
	if strings.ToLower(strings.TrimSpace(config.Mode)) != AutoRoutingModeStageFixed {
		return fmt.Errorf("mode must be %q", AutoRoutingModeStageFixed)
	}
	if config.Enabled && strings.TrimSpace(config.DefaultModelID) == "" {
		return fmt.Errorf("default_model_id is required when auto routing is enabled")
	}
	byID := make(map[string]Model, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}
	targets := []struct {
		name    string
		modelID string
	}{
		{"default_model_id", config.DefaultModelID},
	}
	for i, modelID := range config.StageModels.ModelIDs() {
		name := []string{"run", "plan", "implement", "review", "finalize"}[i]
		if config.Enabled && strings.TrimSpace(modelID) == "" {
			return fmt.Errorf("stage_models.%s is required when auto routing is enabled", name)
		}
		targets = append(targets, struct {
			name    string
			modelID string
		}{"stage_models." + name, modelID})
	}
	for _, target := range targets {
		modelID := strings.TrimSpace(target.modelID)
		if modelID == "" {
			continue
		}
		model, ok := byID[modelID]
		if !ok {
			return fmt.Errorf("%s references unknown model %q", target.name, modelID)
		}
		if !model.Enabled {
			return fmt.Errorf("%s references disabled model %q", target.name, modelID)
		}
	}
	return nil
}
