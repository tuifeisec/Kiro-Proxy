package router

import (
	"fmt"
	"kiro-proxy/converter"
	"kiro-proxy/store"
	"strings"
	"time"
)

const autoLockIdleLimit = time.Hour

type autoModelLock struct {
	ModelID         string
	LastUsed        time.Time
	Mode            string
	Duty            string
	Effort          string
	EffortFixed     bool
	Stage           string
	WorkflowID      string
	NodeIdentity    string
	TaskType        string
	ExecutionID     string
	LockScope       string
	Reason          string
	SelectionSource string
	UnknownReason   string
	ChannelIdentity string
}

type autoSelection struct {
	ModelID         string
	Duty            string
	Effort          string
	EffortFixed     bool
	UseModelDefault bool
	Stage           string
	Source          string
	UnknownReason   string
	Reason          string
}

// ValidateAutoRoutingConfig validates stored references and the local provider
// conversion capability. It does not claim that the remote model accepts an effort.
func ValidateAutoRoutingConfig(config store.AutoRoutingConfig, models []store.Model) error {
	if err := store.ValidateAutoRoutingConfig(config, models); err != nil {
		return err
	}
	if config.Mode != store.AutoRoutingModeDutyFixed {
		return nil
	}
	byID := make(map[string]store.Model, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}
	validateEffort := func(name, modelID, rawEffort string, useModelDefault bool) error {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			return nil
		}
		model, ok := byID[modelID]
		if !ok {
			return nil // the store validator returns the reference error
		}
		effort := rawEffort
		if useModelDefault {
			effort = model.ReasoningEffort
		}
		normalized, err := converter.NormalizeReasoningEffort(effort)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := converter.ValidateProviderReasoningEffort(model.Provider, normalized); err != nil {
			return fmt.Errorf("%s for provider %q: %w", name, model.Provider, err)
		}
		return nil
	}
	if err := validateEffort("default_model_id reasoning_effort", config.DefaultModelID, "", true); err != nil {
		return err
	}
	for duty, target := range config.DutyModels {
		if err := validateEffort("duty_models."+duty+".reasoning_effort", target.ModelID, target.ReasoningEffort, false); err != nil {
			return err
		}
	}
	for index, override := range config.NodeOverrides {
		if err := validateEffort(fmt.Sprintf("node_overrides[%d].reasoning_effort", index), override.ModelID, override.ReasoningEffort, false); err != nil {
			return err
		}
	}
	return nil
}

// ResolveAuto routes a Kiro main generation request.
func (r *Router) ResolveAuto(conv *converter.ParsedConversation) (*Route, error) {
	return r.resolveAuto(conv, false)
}

// ResolveStageAuto is retained for native Responses callers and accepts either
// enabled fixed-routing mode.
func (r *Router) ResolveStageAuto(conv *converter.ParsedConversation) (*Route, error) {
	return r.resolveAuto(conv, true)
}

func (r *Router) resolveAuto(conv *converter.ParsedConversation, requireEnabledAuto bool) (*Route, error) {
	if conv == nil {
		return nil, fmt.Errorf("auto routing requires a parsed conversation")
	}
	if conv.IsClassifier || !strings.EqualFold(conv.ModelID, "auto") {
		return nil, fmt.Errorf("auto routing requires a Kiro main request with modelId=auto")
	}

	now := time.Now()
	executionKey := autoExecutionLockKey(conv)
	toolKey := autoScopeKey(conv)
	r.autoMu.Lock()
	defer r.autoMu.Unlock()
	r.expireAutoLocksLocked(now)
	r.mu.RLock()
	defer r.mu.RUnlock()

	if executionKey != "" {
		if lock, ok := r.autoLocks[executionKey]; ok {
			return r.resolveExistingExecutionLocked(conv, lock, executionKey, now)
		}
	}
	if executionKey == "" && toolKey != "" && hasToolContinuation(conv) {
		if lock, ok := r.autoLocks[toolKey]; ok {
			if lock.Mode == store.AutoRoutingModeStageFixed {
				if (r.autoConfigErr != nil || r.autoConfig.Mode == store.AutoRoutingModeStageFixed) && autoContinuationMatchesStage(conv, lock) {
					return r.resolveAutoContinuationLocked(conv, lock, toolKey, now)
				}
				if r.autoConfigErr == nil && r.autoConfig.Mode != store.AutoRoutingModeStageFixed {
					return nil, fmt.Errorf("auto tool continuation routing mode changed")
				}
			}
			if lock.Mode == store.AutoRoutingModeDutyFixed {
				if autoDutyContinuationMatches(conv, lock) {
					return r.resolveExistingDutyLockLocked(conv, lock, toolKey, now)
				}
				delete(r.autoLocks, toolKey)
			}
		}
	}

	if r.autoConfigErr != nil {
		return nil, fmt.Errorf("auto routing configuration is invalid: %w", r.autoConfigErr)
	}
	if requireEnabledAuto && (!r.autoConfig.Enabled || !isFixedAutoMode(r.autoConfig.Mode)) {
		return nil, fmt.Errorf("raw Kiro Responses Auto routing requires enabled stage_fixed mode or duty_fixed mode")
	}
	if !r.autoConfig.Enabled {
		if toolKey != "" {
			delete(r.autoLocks, toolKey)
		}
		route := r.resolveAutoLocked(conv.ModelID)
		if route == nil {
			return nil, fmt.Errorf("auto default route has no enabled target")
		}
		route.AutoReason = "policy_disabled"
		return route, nil
	}
	if r.autoConfig.Mode == store.AutoRoutingModeStageFixed {
		return r.resolveLegacyStageAutoLocked(conv, executionKey, toolKey, now)
	}
	if r.autoConfig.Mode == store.AutoRoutingModeDutyFixed {
		selection := selectDutyTarget(conv, r.autoConfig)
		return r.resolveDutyAutoLocked(conv, selection, executionKey, toolKey, now)
	}
	return nil, fmt.Errorf("auto routing mode %q is unsupported", r.autoConfig.Mode)
}

func isFixedAutoMode(mode string) bool {
	return mode == store.AutoRoutingModeStageFixed || mode == store.AutoRoutingModeDutyFixed
}

func (r *Router) resolveExistingExecutionLocked(conv *converter.ParsedConversation, lock autoModelLock, key string, now time.Time) (*Route, error) {
	if lock.Mode == store.AutoRoutingModeDutyFixed {
		return r.resolveExistingDutyLockLocked(conv, lock, key, now)
	}
	if lock.Mode == store.AutoRoutingModeStageFixed {
		selection := autoSelection{ModelID: lock.ModelID, Stage: lock.Stage, Source: lock.SelectionSource,
			UnknownReason: lock.UnknownReason, Reason: lock.Reason}
		return r.resolveLockedAutoRoute(conv, lock, selection, key, now)
	}
	return nil, fmt.Errorf("auto execution %q has unsupported locked mode %q", lock.ExecutionID, lock.Mode)
}

func (r *Router) resolveExistingDutyLockLocked(conv *converter.ParsedConversation, lock autoModelLock, key string, now time.Time) (*Route, error) {
	route, err := r.routeForLockedAutoModel(lock.ModelID, conv.ModelID)
	if err != nil {
		return nil, fmt.Errorf("auto locked target %q is unavailable: %w", lock.ModelID, err)
	}
	if lock.ChannelIdentity != autoChannelIdentity(route.Model) {
		return nil, fmt.Errorf("auto %s target %q has changed channel identity; start a new execution before using the updated configuration", lock.LockScope, lock.ModelID)
	}
	if hasNonEmptyImages(conv) && !modelSupportsImages(route.Model) {
		return nil, fmt.Errorf("auto conversation is locked to model %q, which does not support image input", lock.ModelID)
	}
	lock.LastUsed = now
	r.autoLocks[key] = lock
	selection := autoSelection{ModelID: lock.ModelID, Duty: lock.Duty, Effort: lock.Effort, EffortFixed: lock.EffortFixed,
		Source: lock.SelectionSource, UnknownReason: lock.UnknownReason, Reason: lock.Reason}
	applyAutoSelection(route, selection, lock.ExecutionID, lock.LockScope, lock.Effort)
	route.AutoMode = lock.Mode
	route.AutoChannelIdentity = lock.ChannelIdentity
	r.recordAutoObservationLocked(conv, route, lock.LockScope)
	return route, nil
}

func (r *Router) resolveLegacyStageAutoLocked(conv *converter.ParsedConversation, executionKey, toolKey string, now time.Time) (*Route, error) {
	selection := selectLegacyStageTarget(conv, r.autoConfig)
	if selection.ModelID == "" {
		return nil, fmt.Errorf("auto default route has no enabled target")
	}
	route, err := r.routeForLockedAutoModel(selection.ModelID, conv.ModelID)
	if err != nil {
		return nil, fmt.Errorf("auto routing target %q for %s is unavailable: %w", selection.ModelID, selection.Reason, err)
	}
	if hasNonEmptyImages(conv) && !modelSupportsImages(route.Model) {
		return nil, fmt.Errorf("auto routing target %q does not support image input", selection.ModelID)
	}
	selectionReason := selection.Reason
	if hasToolContinuation(conv) {
		selection.Reason = "tool_continuation_unlocked:" + selectionReason
	}
	lockKey, lockScope := toolKey, "tool_continuation"
	if executionKey != "" {
		lockKey, lockScope = executionKey, "execution"
	}
	selection.EffortFixed = false // legacy stage_fixed preserves legacy request-level effort behavior
	applyAutoSelection(route, selection, conv.WorkflowExecutionID, lockScope, "")
	route.AutoMode = store.AutoRoutingModeStageFixed
	route.AutoStage = selection.Stage
	route.AutoChannelIdentity = autoChannelIdentity(route.Model)
	if lockKey != "" {
		r.storeAutoLock(lockKey, autoModelLock{ModelID: route.Model.ID, LastUsed: now, Mode: store.AutoRoutingModeStageFixed,
			Stage: selection.Stage, WorkflowID: conv.WorkflowID, NodeIdentity: conversationNodeIdentity(conv), ExecutionID: conv.WorkflowExecutionID,
			TaskType:  conv.WorkflowTaskType,
			LockScope: lockScope, Reason: selectionReason, SelectionSource: selection.Source, UnknownReason: selection.UnknownReason,
			ChannelIdentity: route.AutoChannelIdentity})
	}
	r.recordAutoObservationLocked(conv, route, lockScope)
	return route, nil
}

func (r *Router) resolveDutyAutoLocked(conv *converter.ParsedConversation, selection autoSelection, executionKey, toolKey string, now time.Time) (*Route, error) {
	if selection.ModelID == "" {
		return nil, fmt.Errorf("auto duty routing has no fixed default target")
	}
	route, err := r.routeForLockedAutoModel(selection.ModelID, conv.ModelID)
	if err != nil {
		return nil, fmt.Errorf("auto routing target %q for %s is unavailable: %w", selection.ModelID, selection.Reason, err)
	}
	if selection.UseModelDefault {
		selection.Effort, err = converter.NormalizeReasoningEffort(route.Model.ReasoningEffort)
		if err != nil {
			return nil, fmt.Errorf("auto default model %q reasoning_effort is invalid: %w", selection.ModelID, err)
		}
	}
	if err := converter.ValidateProviderReasoningEffort(route.Model.Provider, selection.Effort); err != nil {
		return nil, fmt.Errorf("auto target %q cannot express reasoning_effort %q: %w", selection.ModelID, selection.Effort, err)
	}
	if hasNonEmptyImages(conv) && !modelSupportsImages(route.Model) {
		return nil, fmt.Errorf("auto routing target %q does not support image input", selection.ModelID)
	}
	lockKey, lockScope := toolKey, "tool_continuation"
	if executionKey != "" {
		lockKey, lockScope = executionKey, "execution"
	}
	applyAutoSelection(route, selection, conv.WorkflowExecutionID, lockScope, selection.Effort)
	route.AutoChannelIdentity = autoChannelIdentity(route.Model)
	if lockKey != "" {
		r.storeAutoLock(lockKey, autoModelLock{ModelID: route.Model.ID, LastUsed: now, Mode: store.AutoRoutingModeDutyFixed,
			Duty: selection.Duty, Effort: selection.Effort, EffortFixed: true, NodeIdentity: conversationNodeIdentity(conv),
			TaskType:    conv.WorkflowTaskType,
			ExecutionID: conv.WorkflowExecutionID, LockScope: lockScope, Reason: selection.Reason,
			SelectionSource: selection.Source, UnknownReason: selection.UnknownReason, ChannelIdentity: route.AutoChannelIdentity})
	}
	r.recordAutoObservationLocked(conv, route, lockScope)
	return route, nil
}

func (r *Router) resolveLockedAutoRoute(conv *converter.ParsedConversation, lock autoModelLock, selection autoSelection, key string, now time.Time) (*Route, error) {
	route, err := r.routeForLockedAutoModel(lock.ModelID, conv.ModelID)
	if err != nil {
		return nil, err
	}
	if lock.ChannelIdentity != autoChannelIdentity(route.Model) {
		return nil, fmt.Errorf("auto execution target %q has changed channel identity", lock.ModelID)
	}
	if hasNonEmptyImages(conv) && !modelSupportsImages(route.Model) {
		return nil, fmt.Errorf("auto conversation is locked to model %q, which does not support image input", lock.ModelID)
	}
	lock.LastUsed = now
	r.autoLocks[key] = lock
	applyAutoSelection(route, selection, lock.ExecutionID, lock.LockScope, lock.Effort)
	route.AutoMode = lock.Mode
	route.AutoStage = lock.Stage
	route.AutoChannelIdentity = lock.ChannelIdentity
	r.recordAutoObservationLocked(conv, route, lock.LockScope)
	return route, nil
}

func (r *Router) storeAutoLock(key string, lock autoModelLock) {
	if key == "" {
		return
	}
	if r.autoLocks == nil {
		r.autoLocks = make(map[string]autoModelLock)
	}
	r.autoLocks[key] = lock
}

func applyAutoSelection(route *Route, selection autoSelection, executionID, lockScope, effectiveEffort string) {
	route.AutoMode = store.AutoRoutingModeDutyFixed
	route.AutoDuty = selection.Duty
	route.AutoEffort = effectiveEffort
	route.AutoEffortFixed = selection.EffortFixed
	route.AutoExecutionID = executionID
	route.AutoLockScope = lockScope
	route.AutoSelectionSource = selection.Source
	route.AutoUnknownReason = selection.UnknownReason
	route.AutoReason = selection.Reason
}

// ForgetAuto clears Auto locks associated with a manually selected session/branch.
func (r *Router) ForgetAuto(conv *converter.ParsedConversation) {
	if conv == nil {
		return
	}
	r.autoMu.Lock()
	delete(r.autoLocks, autoScopeKey(conv))
	if executionKey := autoExecutionLockKey(conv); executionKey != "" {
		delete(r.autoLocks, executionKey)
	}
	r.autoMu.Unlock()
}

func autoScopeKey(conv *converter.ParsedConversation) string {
	if conv == nil {
		return ""
	}
	if conv.ContinuationStateKey != "" {
		return conv.ContinuationStateKey
	}
	return conv.ConversationID
}

func autoExecutionLockKey(conv *converter.ParsedConversation) string {
	if conv == nil || strings.TrimSpace(conv.WorkflowExecutionID) == "" {
		return ""
	}
	return "execution:" + strings.TrimSpace(conv.WorkflowExecutionID)
}

func (r *Router) expireAutoLocksLocked(now time.Time) {
	for key, lock := range r.autoLocks {
		if lock.LockScope == "execution" {
			continue
		}
		if now.Sub(lock.LastUsed) >= autoLockIdleLimit {
			delete(r.autoLocks, key)
		}
	}
}

func autoDutyContinuationMatches(conv *converter.ParsedConversation, lock autoModelLock) bool {
	identity := conversationNodeIdentity(conv)
	currentTaskType := strings.ToLower(strings.TrimSpace(conv.WorkflowTaskType))
	lockedTaskType := strings.ToLower(strings.TrimSpace(lock.TaskType))
	if currentTaskType != "" && currentTaskType != lockedTaskType {
		return false
	}
	if identity != "" || lock.NodeIdentity != "" {
		if identity == "" {
			return true // Missing metadata keeps the tool-scoped snapshot after explicit changes were checked.
		}
		return identity == lock.NodeIdentity
	}
	return true
}

func (r *Router) routeForLockedAutoModel(modelID, requestedName string) (*Route, error) {
	for i := range r.models {
		model := &r.models[i]
		if model.ID != modelID {
			continue
		}
		if !model.Enabled {
			return nil, fmt.Errorf("model %q is disabled", modelID)
		}
		return r.routeForModelLocked(model, requestedName), nil
	}
	return nil, fmt.Errorf("model %q no longer exists", modelID)
}

func selectLegacyStageTarget(conv *converter.ParsedConversation, config store.AutoRoutingConfig) autoSelection {
	stage := ""
	source := "legacy_stage"
	unknown := ""
	available := false
	if conv != nil {
		if conv.WorkflowMetadataVersion >= 2 {
			stage = strings.ToLower(strings.TrimSpace(conv.WorkflowNodeID))
			source = "legacy_node_stage"
			if conv.WorkflowMetadataVersion == 2 && stage == "" && conv.WorkflowTaskType == "orchestration" && conv.WorkflowID == "" {
				stage = "run"
				source = "legacy_orchestration"
				available = true
			} else {
				available = stage != ""
				if stage == "" {
					unknown = "legacy_node_identity_unavailable"
				}
			}
		} else if conv.WorkflowStageProvided {
			stage = strings.ToLower(strings.TrimSpace(conv.WorkflowStage))
			available = true
		} else {
			unknown = "legacy_stage_unavailable"
		}
	}
	if modelID := config.StageModels.ModelID(stage); modelID != "" {
		return autoSelection{ModelID: modelID, Stage: stage, Source: source, Reason: "stage:" + stage}
	}
	reason := "stage_unavailable"
	if available {
		reason = "stage_unknown:" + stage
		if unknown == "" {
			unknown = "legacy_node_unknown"
		}
	}
	return autoSelection{ModelID: config.DefaultModelID, Stage: stage, Source: "legacy_default", UnknownReason: unknown,
		Reason: reason}
}

func selectDutyTarget(conv *converter.ParsedConversation, config store.AutoRoutingConfig) autoSelection {
	selection := autoSelection{ModelID: config.DefaultModelID, UseModelDefault: true, EffortFixed: true,
		Source: "default", UnknownReason: "task_type_unavailable", Reason: "default:task_type_unavailable"}
	if conv == nil {
		return selection
	}
	if conv.WorkflowMetadataVersion == 1 {
		selection.UnknownReason = "metadata_v1"
		selection.Reason = "default:metadata_v1"
		return selection
	}
	identity := conversationNodeIdentity(conv)
	if identity != "" {
		for _, override := range config.NodeOverrides {
			if identity == autoNodeIdentity(override.DefinitionID, override.DefinitionRevision, override.NodePath) {
				selection.ModelID = override.ModelID
				selection.Effort = override.ReasoningEffort
				selection.UseModelDefault = false
				selection.Duty = declaredDutyForObservation(conv, config)
				selection.Source = "node_override"
				selection.UnknownReason = ""
				selection.Reason = "node_override"
				return selection
			}
		}
		for _, binding := range config.NodeBindings {
			if identity == autoNodeIdentity(binding.DefinitionID, binding.DefinitionRevision, binding.NodePath) {
				return dutySelectionForName(binding.Duty, "node_binding", config)
			}
		}
	}
	if strings.TrimSpace(conv.WorkflowTaskType) != "" {
		return dutySelectionForTaskType(conv.WorkflowTaskType, conv.WorkflowNodeID, config)
	}
	if identity == "" {
		selection.UnknownReason = "stable_identity_unavailable"
		selection.Reason = "default:stable_identity_unavailable"
	}
	return selection
}

func declaredDutyForObservation(conv *converter.ParsedConversation, config store.AutoRoutingConfig) string {
	if identity := conversationNodeIdentity(conv); identity != "" {
		for _, binding := range config.NodeBindings {
			if identity == autoNodeIdentity(binding.DefinitionID, binding.DefinitionRevision, binding.NodePath) {
				return binding.Duty
			}
		}
	}
	if conv != nil && strings.TrimSpace(conv.WorkflowTaskType) != "" {
		return resolveTaskTypeDuty(conv.WorkflowTaskType, conv.WorkflowNodeID, config.Layout).duty
	}
	return ""
}

func dutySelectionForTaskType(taskType, nodeID string, config store.AutoRoutingConfig) autoSelection {
	resolved := resolveTaskTypeDuty(taskType, nodeID, config.Layout)
	if resolved.duty == "" {
		return autoSelection{ModelID: config.DefaultModelID, UseModelDefault: true, EffortFixed: true, Source: "default",
			UnknownReason: resolved.reason, Reason: "default:" + resolved.reason}
	}
	return dutySelectionForName(resolved.duty, "task_type", config)
}

func dutySelectionForName(duty, source string, config store.AutoRoutingConfig) autoSelection {
	target, ok := config.DutyModels[duty]
	if !ok || strings.TrimSpace(target.ModelID) == "" {
		return autoSelection{ModelID: config.DefaultModelID, Duty: duty, UseModelDefault: true, EffortFixed: true, Source: "default",
			UnknownReason: "duty_target_missing", Reason: "default:duty_target_missing:" + duty}
	}
	return autoSelection{ModelID: target.ModelID, Duty: duty, Effort: target.ReasoningEffort, EffortFixed: true,
		Source: source, Reason: source + ":" + duty}
}

type resolvedAutoDuty struct {
	duty   string
	reason string
}

func resolveTaskTypeDuty(taskType, nodeID, layout string) resolvedAutoDuty {
	taskType = strings.ToLower(strings.TrimSpace(taskType))
	if taskType == "" {
		return resolvedAutoDuty{reason: "task_type_unavailable"}
	}
	if layout == store.AutoRoutingLayoutExpanded {
		switch taskType {
		case "routine_support":
			switch strings.ToLower(strings.TrimSpace(nodeID)) {
			case "prepare", "protect-baseline", "setup", "design-file-gate", "cleanup":
				return resolvedAutoDuty{duty: "preparation"}
			case "finalize":
				return resolvedAutoDuty{duty: "finalization"}
			default:
				return resolvedAutoDuty{reason: "expanded_routine_support_node_unknown"}
			}
		case "pr_lifecycle":
			switch strings.ToLower(strings.TrimSpace(nodeID)) {
			case "submit":
				return resolvedAutoDuty{duty: "pr_submit"}
			case "respond":
				return resolvedAutoDuty{duty: "pr_feedback"}
			default:
				return resolvedAutoDuty{reason: "expanded_pr_lifecycle_node_unknown"}
			}
		}
	}
	for _, duty := range store.AutoDutyNames(layout) {
		if duty == taskType {
			return resolvedAutoDuty{duty: duty}
		}
	}
	return resolvedAutoDuty{reason: "task_type_outside_layout"}
}

func conversationNodeIdentity(conv *converter.ParsedConversation) string {
	if conv == nil {
		return ""
	}
	definitionID := conv.WorkflowDefinitionID
	revision := conv.WorkflowDefinitionRevision
	path := conv.WorkflowNodePath
	if definitionID == "" || revision == "" || path == "" {
		return ""
	}
	if definitionID != strings.TrimSpace(definitionID) || revision != strings.TrimSpace(revision) || path != strings.TrimSpace(path) ||
		strings.ContainsAny(definitionID+revision+path, "\x00\r\n") {
		return ""
	}
	return autoNodeIdentity(definitionID, revision, path)
}

func autoNodeIdentity(definitionID, revision, path string) string {
	return definitionID + "\x00" + revision + "\x00" + path
}

func hasNonEmptyImages(conv *converter.ParsedConversation) bool {
	if conv == nil {
		return false
	}
	if conv.RawResponsesHasImages {
		return true
	}
	for _, image := range conv.CurrentImages {
		if strings.TrimSpace(image.Source.Bytes) != "" {
			return true
		}
	}
	for _, entry := range conv.History {
		for _, image := range entry.Images {
			if strings.TrimSpace(image.Source.Bytes) != "" {
				return true
			}
		}
	}
	return false
}

func modelSupportsImages(model *store.Model) bool {
	if model == nil {
		return false
	}
	mode, err := store.NormalizeImageInputMode(model.ImageInputMode)
	return err == nil && mode == store.ImageInputSupported
}

func hasToolContinuation(conv *converter.ParsedConversation) bool {
	if conv == nil {
		return false
	}
	if conv.CurrentToolContinuation != nil {
		return *conv.CurrentToolContinuation
	}
	return len(conv.CurrentToolResults) > 0
}

func (r *Router) autoModelLocked() *store.Model {
	candidateIDs := []string{r.autoConfig.DefaultModelID}
	if r.autoConfig.Mode == store.AutoRoutingModeStageFixed {
		candidateIDs = append(candidateIDs, r.autoConfig.StageModels.ModelIDs()...)
	} else {
		for _, target := range r.autoConfig.DutyModels {
			candidateIDs = append(candidateIDs, target.ModelID)
		}
		for _, target := range r.autoConfig.NodeOverrides {
			candidateIDs = append(candidateIDs, target.ModelID)
		}
	}
	candidates := make([]*store.Model, 0, len(candidateIDs))
	seen := make(map[string]bool, len(candidateIDs))
	for _, id := range candidateIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		for i := range r.models {
			if r.models[i].ID == id && r.models[i].Enabled {
				candidates = append(candidates, &r.models[i])
				break
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	imageMode := store.ImageInputSupported
	for _, candidate := range candidates {
		if !modelSupportsImages(candidate) {
			imageMode = store.ImageInputUnsupported
			break
		}
	}
	name := "Auto（职责固定模型）"
	if r.autoConfig.Mode == store.AutoRoutingModeStageFixed {
		name = "Auto（五阶段固定模型）"
	}
	model := &store.Model{ID: "auto", Name: name, Note: name, ImageInputMode: imageMode,
		ContextWindow: groupMinContextWindow(candidates), MaxTokens: groupMinMaxTokens(candidates), Provider: "group",
		RateMultiplier: groupRateMultiplier(candidates), Enabled: true}
	model.AutoRoutingMode = r.autoConfig.Mode
	for _, candidate := range candidates {
		if candidate.ContextWindow <= 0 {
			model.ContextWindow = 0
		}
		if candidate.MaxTokens <= 0 {
			model.MaxTokens = 0
		}
	}
	return model
}
