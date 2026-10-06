package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const AutoRoutingSetting = "auto_routing_config"
const AutoRoutingStageFixedBackupSetting = "auto_routing_config_stage_fixed_backup"

// SetSettingsWithAutoRoutingMigration saves a v2 configuration together with
// the first exact legacy stage_fixed value in one settings transaction.
func (db *DB) SetSettingsWithAutoRoutingMigration(settings map[string]string) error {
	if _, supplied := settings[AutoRoutingStageFixedBackupSetting]; supplied {
		return fmt.Errorf("%s is reserved for the server", AutoRoutingStageFixedBackupSetting)
	}
	values := make(map[string]string, len(settings)+1)
	for key, value := range settings {
		values[key] = value
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, writesAuto := values[AutoRoutingSetting]; writesAuto {
		var previousRaw string
		err := tx.QueryRow(`SELECT value FROM settings WHERE key = ?`, AutoRoutingSetting).Scan(&previousRaw)
		if err == nil {
			previous, parseErr := ParseAutoRoutingConfig(previousRaw)
			if parseErr == nil && previous.Mode == AutoRoutingModeStageFixed {
				var existingBackup string
				backupErr := tx.QueryRow(`SELECT value FROM settings WHERE key = ?`, AutoRoutingStageFixedBackupSetting).Scan(&existingBackup)
				if errors.Is(backupErr, sql.ErrNoRows) {
					values[AutoRoutingStageFixedBackupSetting] = previousRaw
				} else if backupErr != nil {
					return backupErr
				}
			} else if parseErr != nil {
				return fmt.Errorf("read existing Auto configuration before migration: %w", parseErr)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	const upsertSQL = `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at
	`
	for _, key := range keys {
		if _, err := tx.Exec(upsertSQL, key, values[key], time.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const (
	AutoRoutingModeStageFixed = "stage_fixed"
	AutoRoutingModeDutyFixed  = "duty_fixed"
	AutoRoutingVersion        = 2
	AutoRoutingLayoutStandard = "standard"
	AutoRoutingLayoutExpanded = "expanded"
)

// AutoRoutingConfig supports the read-only five-stage compatibility format and
// the strict v2 fixed-duty format.
type AutoRoutingConfig struct {
	Version        int                       `json:"version,omitempty"`
	Mode           string                    `json:"mode"`
	Layout         string                    `json:"layout,omitempty"`
	Enabled        bool                      `json:"enabled"`
	DefaultModelID string                    `json:"default_model_id"`
	StageModels    AutoStageModels           `json:"stage_models,omitempty"`
	DutyModels     map[string]AutoDutyTarget `json:"duty_models,omitempty"`
	NodeBindings   []AutoNodeBinding         `json:"node_bindings,omitempty"`
	NodeOverrides  []AutoNodeOverride        `json:"node_overrides,omitempty"`
}

type AutoDutyTarget struct {
	ModelID         string `json:"model_id"`
	ReasoningEffort string `json:"reasoning_effort"`
}

type AutoNodeBinding struct {
	DefinitionID       string `json:"definition_id"`
	DefinitionRevision string `json:"definition_revision"`
	NodePath           string `json:"node_path"`
	Duty               string `json:"duty"`
}

type AutoNodeOverride struct {
	DefinitionID       string `json:"definition_id"`
	DefinitionRevision string `json:"definition_revision"`
	NodePath           string `json:"node_path"`
	ModelID            string `json:"model_id"`
	ReasoningEffort    string `json:"reasoning_effort"`
}

type AutoStageModels struct {
	Run       string `json:"run"`
	Plan      string `json:"plan"`
	Implement string `json:"implement"`
	Review    string `json:"review"`
	Finalize  string `json:"finalize"`
}

// MarshalJSON emits one exact schema for each supported mode. In particular,
// v2 must retain empty arrays because their presence is part of its contract.
func (c AutoRoutingConfig) MarshalJSON() ([]byte, error) {
	switch c.Mode {
	case AutoRoutingModeStageFixed:
		return json.Marshal(struct {
			Mode           string          `json:"mode"`
			Enabled        bool            `json:"enabled"`
			DefaultModelID string          `json:"default_model_id"`
			StageModels    AutoStageModels `json:"stage_models"`
		}{c.Mode, c.Enabled, c.DefaultModelID, c.StageModels})
	case AutoRoutingModeDutyFixed:
		dutyModels := c.DutyModels
		if dutyModels == nil {
			dutyModels = map[string]AutoDutyTarget{}
		}
		bindings := c.NodeBindings
		if bindings == nil {
			bindings = []AutoNodeBinding{}
		}
		overrides := c.NodeOverrides
		if overrides == nil {
			overrides = []AutoNodeOverride{}
		}
		return json.Marshal(struct {
			Version        int                       `json:"version"`
			Mode           string                    `json:"mode"`
			Layout         string                    `json:"layout"`
			Enabled        bool                      `json:"enabled"`
			DefaultModelID string                    `json:"default_model_id"`
			DutyModels     map[string]AutoDutyTarget `json:"duty_models"`
			NodeBindings   []AutoNodeBinding         `json:"node_bindings"`
			NodeOverrides  []AutoNodeOverride        `json:"node_overrides"`
		}{c.Version, c.Mode, c.Layout, c.Enabled, c.DefaultModelID, dutyModels, bindings, overrides})
	default:
		type configAlias AutoRoutingConfig
		return json.Marshal(configAlias(c))
	}
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

// ModelIDs returns legacy stage targets in run, plan, implement, review,
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

var legacyAutoRoutingFields = map[string]struct{}{
	"mode":             {},
	"enabled":          {},
	"default_model_id": {},
	"stage_models":     {},
}

var dutyAutoRoutingFields = map[string]struct{}{
	"version":          {},
	"mode":             {},
	"layout":           {},
	"enabled":          {},
	"default_model_id": {},
	"duty_models":      {},
	"node_bindings":    {},
	"node_overrides":   {},
}

var autoStageFields = map[string]struct{}{
	"run":       {},
	"plan":      {},
	"implement": {},
	"review":    {},
	"finalize":  {},
}

var autoTargetFields = map[string]struct{}{
	"model_id":         {},
	"reasoning_effort": {},
}

var autoNodeBindingFields = map[string]struct{}{
	"definition_id":       {},
	"definition_revision": {},
	"node_path":           {},
	"duty":                {},
}

var autoNodeOverrideFields = map[string]struct{}{
	"definition_id":       {},
	"definition_revision": {},
	"node_path":           {},
	"model_id":            {},
	"reasoning_effort":    {},
}

// AutoDutyNames returns the exact set of configuration keys for a v2 layout.
func AutoDutyNames(layout string) []string {
	standard := []string{
		"orchestration", "routine_support", "investigation", "requirements", "design",
		"implementation_plan", "implementation", "iterative_execution", "experiment",
		"design_review", "code_review", "semantic_review", "review_synthesis",
		"review_verdict", "validation", "delivery_audit", "operations", "pr_lifecycle",
	}
	switch layout {
	case AutoRoutingLayoutStandard:
		return standard
	case AutoRoutingLayoutExpanded:
		return []string{
			"orchestration", "preparation", "finalization", "investigation", "requirements", "design",
			"implementation_plan", "implementation", "iterative_execution", "experiment",
			"design_review", "code_review", "semantic_review", "review_synthesis",
			"review_verdict", "validation", "delivery_audit", "operations", "pr_submit", "pr_feedback",
		}
	default:
		return nil
	}
}

// ParseAutoRoutingConfig parses and canonicalizes a stored configuration.
func ParseAutoRoutingConfig(raw string) (AutoRoutingConfig, error) {
	if strings.TrimSpace(raw) == "" {
		return AutoRoutingConfig{Mode: AutoRoutingModeStageFixed}, nil
	}
	if err := validateUniqueJSONKeys([]byte(raw)); err != nil {
		return AutoRoutingConfig{}, fmt.Errorf("invalid configuration: %w", err)
	}
	fields, err := decodeObject([]byte(raw), "configuration")
	if err != nil {
		return AutoRoutingConfig{}, err
	}
	modeRaw, hasMode := fields["mode"]
	if !hasMode {
		return AutoRoutingConfig{}, fmt.Errorf("legacy Auto configuration has no mode; stage_fixed is read-only compatibility and duty_fixed v2 must be saved explicitly")
	}
	mode, err := decodeString(modeRaw, "mode")
	if err != nil {
		return AutoRoutingConfig{}, err
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return AutoRoutingConfig{}, fmt.Errorf("legacy Auto configuration has no supported mode; stage_fixed is read-only compatibility and duty_fixed v2 must be saved explicitly")
	}
	switch mode {
	case AutoRoutingModeStageFixed:
		return parseLegacyStageConfig(fields)
	case AutoRoutingModeDutyFixed:
		return parseDutyConfig(fields)
	case "task_rules":
		return AutoRoutingConfig{}, fmt.Errorf("legacy task_rules Auto configuration is unsupported; stage_fixed is read-only compatibility and duty_fixed v2 is supported")
	default:
		return AutoRoutingConfig{}, fmt.Errorf("mode must be %q or %q", AutoRoutingModeStageFixed, AutoRoutingModeDutyFixed)
	}
}

func parseLegacyStageConfig(fields map[string]json.RawMessage) (AutoRoutingConfig, error) {
	config := AutoRoutingConfig{Mode: AutoRoutingModeStageFixed}
	for key := range fields {
		if _, ok := legacyAutoRoutingFields[key]; ok {
			continue
		}
		if _, retired := retiredAutoRoutingFields[key]; retired {
			continue
		}
		return AutoRoutingConfig{}, fmt.Errorf("unknown configuration field %q", key)
	}
	if raw, ok := fields["enabled"]; ok {
		if err := json.Unmarshal(raw, &config.Enabled); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return AutoRoutingConfig{}, fmt.Errorf("enabled must be a boolean")
		}
	}
	if raw, ok := fields["default_model_id"]; ok {
		value, err := decodeString(raw, "default_model_id")
		if err != nil {
			return AutoRoutingConfig{}, err
		}
		config.DefaultModelID = strings.TrimSpace(value)
	}
	stageRaw, ok := fields["stage_models"]
	if !ok {
		return AutoRoutingConfig{}, fmt.Errorf("stage_models is required")
	}
	stageFields, err := decodeObject(stageRaw, "stage_models")
	if err != nil {
		return AutoRoutingConfig{}, err
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
		{"run", &config.StageModels.Run}, {"plan", &config.StageModels.Plan},
		{"implement", &config.StageModels.Implement}, {"review", &config.StageModels.Review},
		{"finalize", &config.StageModels.Finalize},
	}
	for _, stage := range stageTargets {
		raw, ok := stageFields[stage.name]
		if !ok {
			return AutoRoutingConfig{}, fmt.Errorf("stage_models.%s is required", stage.name)
		}
		value, err := decodeString(raw, "stage_models."+stage.name)
		if err != nil {
			return AutoRoutingConfig{}, err
		}
		*stage.target = strings.TrimSpace(value)
	}
	return config, nil
}

func parseDutyConfig(fields map[string]json.RawMessage) (AutoRoutingConfig, error) {
	for key := range fields {
		if _, ok := dutyAutoRoutingFields[key]; !ok {
			return AutoRoutingConfig{}, fmt.Errorf("unknown configuration field %q", key)
		}
	}
	for _, name := range []string{"version", "mode", "layout", "enabled", "default_model_id", "duty_models", "node_bindings", "node_overrides"} {
		if _, ok := fields[name]; !ok {
			return AutoRoutingConfig{}, fmt.Errorf("%s is required", name)
		}
	}
	var config AutoRoutingConfig
	versionRaw := bytes.TrimSpace(fields["version"])
	if bytes.Equal(versionRaw, []byte("null")) || json.Unmarshal(versionRaw, &config.Version) != nil || config.Version != AutoRoutingVersion {
		return AutoRoutingConfig{}, fmt.Errorf("version must be %d", AutoRoutingVersion)
	}
	config.Mode = AutoRoutingModeDutyFixed
	layout, err := decodeString(fields["layout"], "layout")
	if err != nil {
		return AutoRoutingConfig{}, err
	}
	config.Layout = strings.ToLower(strings.TrimSpace(layout))
	if len(AutoDutyNames(config.Layout)) == 0 {
		return AutoRoutingConfig{}, fmt.Errorf("layout must be %q or %q", AutoRoutingLayoutStandard, AutoRoutingLayoutExpanded)
	}
	if bytes.Equal(bytes.TrimSpace(fields["enabled"]), []byte("null")) || json.Unmarshal(fields["enabled"], &config.Enabled) != nil {
		return AutoRoutingConfig{}, fmt.Errorf("enabled must be a boolean")
	}
	defaultModel, err := decodeString(fields["default_model_id"], "default_model_id")
	if err != nil {
		return AutoRoutingConfig{}, err
	}
	config.DefaultModelID = strings.TrimSpace(defaultModel)

	dutyFields, err := decodeObject(fields["duty_models"], "duty_models")
	if err != nil {
		return AutoRoutingConfig{}, err
	}
	validDuties := make(map[string]struct{})
	for _, duty := range AutoDutyNames(config.Layout) {
		validDuties[duty] = struct{}{}
	}
	config.DutyModels = make(map[string]AutoDutyTarget, len(dutyFields))
	for duty, raw := range dutyFields {
		if _, ok := validDuties[duty]; !ok {
			return AutoRoutingConfig{}, fmt.Errorf("unknown duty_models key %q for layout %q", duty, config.Layout)
		}
		targetFields, err := decodeObject(raw, "duty_models."+duty)
		if err != nil {
			return AutoRoutingConfig{}, err
		}
		if len(targetFields) != len(autoTargetFields) {
			return AutoRoutingConfig{}, fmt.Errorf("duty_models.%s must contain model_id and reasoning_effort", duty)
		}
		for key := range targetFields {
			if _, ok := autoTargetFields[key]; !ok {
				return AutoRoutingConfig{}, fmt.Errorf("unknown duty_models.%s field %q", duty, key)
			}
		}
		modelID, ok := targetFields["model_id"]
		if !ok {
			return AutoRoutingConfig{}, fmt.Errorf("duty_models.%s.model_id is required", duty)
		}
		effort, ok := targetFields["reasoning_effort"]
		if !ok {
			return AutoRoutingConfig{}, fmt.Errorf("duty_models.%s.reasoning_effort is required", duty)
		}
		modelIDValue, err := decodeString(modelID, "duty_models."+duty+".model_id")
		if err != nil {
			return AutoRoutingConfig{}, err
		}
		effortValue, err := decodeEffort(effort, "duty_models."+duty+".reasoning_effort")
		if err != nil {
			return AutoRoutingConfig{}, err
		}
		config.DutyModels[duty] = AutoDutyTarget{ModelID: strings.TrimSpace(modelIDValue), ReasoningEffort: effortValue}
	}

	config.NodeBindings, err = parseNodeBindings(fields["node_bindings"])
	if err != nil {
		return AutoRoutingConfig{}, err
	}
	config.NodeOverrides, err = parseNodeOverrides(fields["node_overrides"])
	if err != nil {
		return AutoRoutingConfig{}, err
	}
	return config, nil
}

func parseNodeBindings(raw json.RawMessage) ([]AutoNodeBinding, error) {
	items, err := decodeArray(raw, "node_bindings")
	if err != nil {
		return nil, err
	}
	result := make([]AutoNodeBinding, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		fields, err := decodeObject(item, fmt.Sprintf("node_bindings[%d]", index))
		if err != nil {
			return nil, err
		}
		if len(fields) != len(autoNodeBindingFields) {
			return nil, fmt.Errorf("node_bindings[%d] must contain definition_id, definition_revision, node_path, and duty", index)
		}
		for key := range fields {
			if _, ok := autoNodeBindingFields[key]; !ok {
				return nil, fmt.Errorf("unknown node_bindings[%d] field %q", index, key)
			}
		}
		itemValue, err := readIdentityFields(fields, fmt.Sprintf("node_bindings[%d]", index))
		if err != nil {
			return nil, err
		}
		duty, err := decodeString(fields["duty"], fmt.Sprintf("node_bindings[%d].duty", index))
		if err != nil {
			return nil, err
		}
		itemValue.Duty = strings.ToLower(strings.TrimSpace(duty))
		key := autoNodeIdentity(itemValue.DefinitionID, itemValue.DefinitionRevision, itemValue.NodePath)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate node_bindings identity at index %d", index)
		}
		seen[key] = struct{}{}
		result = append(result, itemValue)
	}
	return result, nil
}

func parseNodeOverrides(raw json.RawMessage) ([]AutoNodeOverride, error) {
	items, err := decodeArray(raw, "node_overrides")
	if err != nil {
		return nil, err
	}
	result := make([]AutoNodeOverride, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		fields, err := decodeObject(item, fmt.Sprintf("node_overrides[%d]", index))
		if err != nil {
			return nil, err
		}
		if len(fields) != len(autoNodeOverrideFields) {
			return nil, fmt.Errorf("node_overrides[%d] must contain identity fields, model_id, and reasoning_effort", index)
		}
		for key := range fields {
			if _, ok := autoNodeOverrideFields[key]; !ok {
				return nil, fmt.Errorf("unknown node_overrides[%d] field %q", index, key)
			}
		}
		identity, err := readIdentityFields(fields, fmt.Sprintf("node_overrides[%d]", index))
		if err != nil {
			return nil, err
		}
		modelID, err := decodeString(fields["model_id"], fmt.Sprintf("node_overrides[%d].model_id", index))
		if err != nil {
			return nil, err
		}
		effort, err := decodeEffort(fields["reasoning_effort"], fmt.Sprintf("node_overrides[%d].reasoning_effort", index))
		if err != nil {
			return nil, err
		}
		itemValue := AutoNodeOverride{DefinitionID: identity.DefinitionID, DefinitionRevision: identity.DefinitionRevision,
			NodePath: identity.NodePath, ModelID: strings.TrimSpace(modelID), ReasoningEffort: effort}
		key := autoNodeIdentity(itemValue.DefinitionID, itemValue.DefinitionRevision, itemValue.NodePath)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate node_overrides identity at index %d", index)
		}
		seen[key] = struct{}{}
		result = append(result, itemValue)
	}
	return result, nil
}

func readIdentityFields(fields map[string]json.RawMessage, path string) (AutoNodeBinding, error) {
	var result AutoNodeBinding
	for name, target := range map[string]*string{
		"definition_id": &result.DefinitionID, "definition_revision": &result.DefinitionRevision, "node_path": &result.NodePath,
	} {
		raw, ok := fields[name]
		if !ok {
			return AutoNodeBinding{}, fmt.Errorf("%s.%s is required", path, name)
		}
		value, err := decodeString(raw, path+"."+name)
		if err != nil {
			return AutoNodeBinding{}, err
		}
		*target = value
		if *target == "" {
			return AutoNodeBinding{}, fmt.Errorf("%s.%s must be non-empty for a stable node identity", path, name)
		}
		if *target != strings.TrimSpace(*target) {
			return AutoNodeBinding{}, fmt.Errorf("%s.%s must not have leading or trailing whitespace", path, name)
		}
		limit := map[string]int{"definition_id": 1024, "definition_revision": 256, "node_path": 1024}[name]
		if len(*target) > limit || strings.ContainsAny(*target, "\x00\r\n") {
			return AutoNodeBinding{}, fmt.Errorf("%s.%s exceeds the stable identity limit", path, name)
		}
	}
	return result, nil
}

func decodeArray(raw json.RawMessage, name string) ([]json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("%s must be an array", name)
	}
	var result []json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil || result == nil {
		return nil, fmt.Errorf("%s must be an array", name)
	}
	return result, nil
}

func decodeObject(raw json.RawMessage, name string) (map[string]json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil || result == nil {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	return result, nil
}

func decodeString(raw json.RawMessage, name string) (string, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fmt.Errorf("%s must be a string", name)
	}
	var result string
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return result, nil
}

func decodeEffort(raw json.RawMessage, name string) (string, error) {
	value, err := decodeString(raw, name)
	if err != nil {
		return "", err
	}
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "none", "low", "medium", "high", "xhigh", "max":
		return value, nil
	default:
		return "", fmt.Errorf("%s has unsupported reasoning effort %q", name, value)
	}
}

func autoNodeIdentity(definitionID, revision, path string) string {
	return definitionID + "\x00" + revision + "\x00" + path
}

// ValidateAutoRoutingConfig validates references and the selected layout.
func ValidateAutoRoutingConfig(config AutoRoutingConfig, models []Model) error {
	byID := make(map[string]Model, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}
	switch config.Mode {
	case AutoRoutingModeStageFixed:
		if config.Version != 0 {
			return fmt.Errorf("stage_fixed is a read-only legacy configuration")
		}
		if config.Enabled && strings.TrimSpace(config.DefaultModelID) == "" {
			return fmt.Errorf("default_model_id is required when auto routing is enabled")
		}
		for i, modelID := range config.StageModels.ModelIDs() {
			name := []string{"run", "plan", "implement", "review", "finalize"}[i]
			if config.Enabled && strings.TrimSpace(modelID) == "" {
				return fmt.Errorf("stage_models.%s is required when auto routing is enabled", name)
			}
			if err := validateAutoModelTarget(byID, "stage_models."+name, modelID); err != nil {
				return err
			}
		}
		return validateAutoModelTarget(byID, "default_model_id", config.DefaultModelID)
	case AutoRoutingModeDutyFixed:
		if config.Version != AutoRoutingVersion {
			return fmt.Errorf("version must be %d", AutoRoutingVersion)
		}
		if len(AutoDutyNames(config.Layout)) == 0 {
			return fmt.Errorf("layout must be %q or %q", AutoRoutingLayoutStandard, AutoRoutingLayoutExpanded)
		}
		if config.Enabled && strings.TrimSpace(config.DefaultModelID) == "" {
			return fmt.Errorf("default_model_id is required when auto routing is enabled")
		}
		if err := validateAutoModelTarget(byID, "default_model_id", config.DefaultModelID); err != nil {
			return err
		}
		validDuties := make(map[string]struct{})
		for _, duty := range AutoDutyNames(config.Layout) {
			validDuties[duty] = struct{}{}
			target, ok := config.DutyModels[duty]
			if config.Enabled && !ok {
				return fmt.Errorf("duty_models.%s is required when auto routing is enabled", duty)
			}
			if !ok {
				continue
			}
			if config.Enabled && strings.TrimSpace(target.ModelID) == "" {
				return fmt.Errorf("duty_models.%s.model_id is required when auto routing is enabled", duty)
			}
			if _, err := normalizeAutoEffort(target.ReasoningEffort); err != nil {
				return fmt.Errorf("duty_models.%s.%w", duty, err)
			}
			if err := validateAutoModelTarget(byID, "duty_models."+duty+".model_id", target.ModelID); err != nil {
				return err
			}
		}
		for duty := range config.DutyModels {
			if _, ok := validDuties[duty]; !ok {
				return fmt.Errorf("unknown duty_models key %q for layout %q", duty, config.Layout)
			}
		}
		seenBindings := make(map[string]struct{}, len(config.NodeBindings))
		for index, binding := range config.NodeBindings {
			key := autoNodeIdentity(binding.DefinitionID, binding.DefinitionRevision, binding.NodePath)
			if binding.DefinitionID == "" || binding.DefinitionRevision == "" || binding.NodePath == "" {
				return fmt.Errorf("node_bindings[%d] must have a complete stable identity", index)
			}
			if _, exists := seenBindings[key]; exists {
				return fmt.Errorf("duplicate node_bindings identity at index %d", index)
			}
			seenBindings[key] = struct{}{}
			if _, ok := validDuties[binding.Duty]; !ok {
				return fmt.Errorf("node_bindings[%d].duty %q is not in layout %q", index, binding.Duty, config.Layout)
			}
		}
		seenOverrides := make(map[string]struct{}, len(config.NodeOverrides))
		for index, override := range config.NodeOverrides {
			key := autoNodeIdentity(override.DefinitionID, override.DefinitionRevision, override.NodePath)
			if override.DefinitionID == "" || override.DefinitionRevision == "" || override.NodePath == "" {
				return fmt.Errorf("node_overrides[%d] must have a complete stable identity", index)
			}
			if _, exists := seenOverrides[key]; exists {
				return fmt.Errorf("duplicate node_overrides identity at index %d", index)
			}
			seenOverrides[key] = struct{}{}
			if config.Enabled && strings.TrimSpace(override.ModelID) == "" {
				return fmt.Errorf("node_overrides[%d].model_id is required when auto routing is enabled", index)
			}
			if _, err := normalizeAutoEffort(override.ReasoningEffort); err != nil {
				return fmt.Errorf("node_overrides[%d].%w", index, err)
			}
			if err := validateAutoModelTarget(byID, fmt.Sprintf("node_overrides[%d].model_id", index), override.ModelID); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("mode must be %q or %q", AutoRoutingModeStageFixed, AutoRoutingModeDutyFixed)
	}
}

func validateAutoModelTarget(byID map[string]Model, name, modelID string) error {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	model, ok := byID[modelID]
	if !ok {
		return fmt.Errorf("%s references unknown model %q", name, modelID)
	}
	if !model.Enabled {
		return fmt.Errorf("%s references disabled model %q", name, modelID)
	}
	return nil
}

func normalizeAutoEffort(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "none", "low", "medium", "high", "xhigh", "max":
		return value, nil
	default:
		return "", fmt.Errorf("has unsupported reasoning effort %q", value)
	}
}

func validateUniqueJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("configuration must contain only one JSON value")
		}
		return fmt.Errorf("invalid trailing data: %w", err)
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key must be a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}
