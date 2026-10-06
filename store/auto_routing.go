package store

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	json "github.com/goccy/go-json"
)

const AutoRoutingSetting = "auto_routing_config"

const (
	defaultFastMaxChars        = 500
	defaultLongContextMinChars = 32000
)

// AutoRoutingConfig 将本地 Auto 任务路由策略保存在一个设置值中。
type AutoRoutingConfig struct {
	Enabled             bool           `json:"enabled"`
	DefaultModelID      string         `json:"default_model_id"`
	VisionModelID       string         `json:"vision_model_id"`
	ReasoningModelID    string         `json:"reasoning_model_id"`
	LongContextModelID  string         `json:"long_context_model_id"`
	FastModelID         string         `json:"fast_model_id"`
	FastMaxChars        int            `json:"fast_max_chars"`
	LongContextMinChars int            `json:"long_context_min_chars"`
	TaskRules           []AutoTaskRule `json:"task_rules"`
}

type AutoTaskRule struct {
	Field   string `json:"field"`
	Value   string `json:"value"`
	ModelID string `json:"model_id"`
}

// ParseAutoRoutingConfig 解析并规范化持久化的设置值。
func ParseAutoRoutingConfig(raw string) (AutoRoutingConfig, error) {
	config := AutoRoutingConfig{
		FastMaxChars:        defaultFastMaxChars,
		LongContextMinChars: defaultLongContextMinChars,
		TaskRules:           []AutoTaskRule{},
	}
	if strings.TrimSpace(raw) == "" {
		return config, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return AutoRoutingConfig{}, fmt.Errorf("configuration must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return AutoRoutingConfig{}, fmt.Errorf("invalid configuration: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return AutoRoutingConfig{}, fmt.Errorf("configuration must contain only one JSON value")
		}
		return AutoRoutingConfig{}, fmt.Errorf("invalid trailing data: %w", err)
	}
	if config.FastMaxChars == 0 {
		config.FastMaxChars = defaultFastMaxChars
	}
	if config.LongContextMinChars == 0 {
		config.LongContextMinChars = defaultLongContextMinChars
	}
	if config.FastMaxChars < 0 {
		return AutoRoutingConfig{}, fmt.Errorf("fast_max_chars must not be negative")
	}
	if config.LongContextMinChars < 0 {
		return AutoRoutingConfig{}, fmt.Errorf("long_context_min_chars must not be negative")
	}
	if config.FastMaxChars >= config.LongContextMinChars {
		return AutoRoutingConfig{}, fmt.Errorf("fast_max_chars must be less than long_context_min_chars")
	}
	config.DefaultModelID = strings.TrimSpace(config.DefaultModelID)
	config.VisionModelID = strings.TrimSpace(config.VisionModelID)
	config.ReasoningModelID = strings.TrimSpace(config.ReasoningModelID)
	config.LongContextModelID = strings.TrimSpace(config.LongContextModelID)
	config.FastModelID = strings.TrimSpace(config.FastModelID)
	for i := range config.TaskRules {
		rule := &config.TaskRules[i]
		rule.Field = strings.ToLower(strings.TrimSpace(rule.Field))
		rule.Value = strings.TrimSpace(rule.Value)
		rule.ModelID = strings.TrimSpace(rule.ModelID)
		if !validAutoTaskField(rule.Field) {
			return AutoRoutingConfig{}, fmt.Errorf("task_rules[%d].field is invalid", i)
		}
		if rule.Value == "" {
			return AutoRoutingConfig{}, fmt.Errorf("task_rules[%d].value is required", i)
		}
		if rule.ModelID == "" {
			return AutoRoutingConfig{}, fmt.Errorf("task_rules[%d].model_id is required", i)
		}
		for previous := 0; previous < i; previous++ {
			prior := config.TaskRules[previous]
			if prior.Field == rule.Field && strings.EqualFold(prior.Value, rule.Value) {
				return AutoRoutingConfig{}, fmt.Errorf("task_rules[%d] duplicates task_rules[%d]", i, previous)
			}
		}
	}
	if config.TaskRules == nil {
		config.TaskRules = []AutoTaskRule{}
	}
	return config, nil
}

// ValidateAutoRoutingConfig 根据实际启用的模型校验所有已配置目标。
func ValidateAutoRoutingConfig(config AutoRoutingConfig, models []Model) error {
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
		{"vision_model_id", config.VisionModelID},
		{"reasoning_model_id", config.ReasoningModelID},
		{"long_context_model_id", config.LongContextModelID},
		{"fast_model_id", config.FastModelID},
	}
	for _, rule := range config.TaskRules {
		if !validAutoTaskField(strings.ToLower(strings.TrimSpace(rule.Field))) {
			return fmt.Errorf("task rule field is invalid: %q", rule.Field)
		}
		if strings.TrimSpace(rule.Value) == "" {
			return fmt.Errorf("task rule value is required")
		}
		targets = append(targets, struct {
			name    string
			modelID string
		}{fmt.Sprintf("task_rules[%s=%s].model_id", strings.TrimSpace(rule.Field), strings.TrimSpace(rule.Value)), rule.ModelID})
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
		if target.name == "vision_model_id" && !strings.EqualFold(strings.TrimSpace(model.ImageInputMode), "supported") {
			return fmt.Errorf("vision_model_id requires a model with supported image input")
		}
	}
	return nil
}

func validAutoTaskField(field string) bool {
	switch field {
	case "agent_task_type", "agent_mode", "chat_trigger_type":
		return true
	default:
		return false
	}
}
