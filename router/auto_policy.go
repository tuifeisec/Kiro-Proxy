package router

import (
	"fmt"
	"kiro-proxy/converter"
	"kiro-proxy/store"
	"strings"
	"time"
	"unicode/utf8"
)

const autoLockIdleLimit = time.Hour

type autoModelLock struct {
	ModelID  string
	LastUsed time.Time
}

// ResolveAuto 为 Kiro 主生成请求按明确字段和能力配置选择一个实际模型。
func (r *Router) ResolveAuto(conv *converter.ParsedConversation) (*Route, error) {
	if conv == nil {
		return nil, fmt.Errorf("auto routing requires a parsed conversation")
	}
	if conv.IsClassifier || !strings.EqualFold(conv.ModelID, "auto") {
		return nil, fmt.Errorf("auto task routing requires a Kiro main request with modelId=auto")
	}

	now := time.Now()
	scopeKey := autoScopeKey(conv)
	r.autoMu.Lock()
	defer r.autoMu.Unlock()
	r.expireAutoLocksLocked(now)

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.autoConfigErr != nil {
		return nil, fmt.Errorf("auto routing configuration is invalid: %w", r.autoConfigErr)
	}
	if !r.autoConfig.Enabled {
		if scopeKey != "" {
			delete(r.autoLocks, scopeKey)
		}
		route := r.resolveAutoLocked(conv.ModelID)
		if route == nil {
			return nil, fmt.Errorf("auto default route has no enabled target")
		}
		route.AutoReason = "policy_disabled"
		return route, nil
	}

	if scopeKey != "" && hasToolContinuation(conv) {
		if lock, ok := r.autoLocks[scopeKey]; ok {
			route, err := r.routeForLockedAutoModel(lock.ModelID, conv.ModelID)
			if err != nil {
				return nil, err
			}
			if hasNonEmptyImages(conv) && !modelSupportsImages(route.Model) {
				return nil, fmt.Errorf("auto conversation is locked to model %q, which does not support image input", lock.ModelID)
			}
			lock.LastUsed = now
			r.autoLocks[scopeKey] = lock
			route.AutoReason = "conversation_lock"
			return route, nil
		}
	}

	modelID, policyReason, err := selectAutoTarget(conv, r.autoConfig)
	if err != nil {
		return nil, err
	}
	route, err := r.routeForLockedAutoModel(modelID, conv.ModelID)
	if err != nil {
		return nil, fmt.Errorf("auto routing target %q for %s is unavailable: %w", modelID, policyReason, err)
	}
	if hasNonEmptyImages(conv) && !modelSupportsImages(route.Model) {
		return nil, fmt.Errorf("auto routing target %q does not support image input", modelID)
	}

	if hasToolContinuation(conv) {
		policyReason = "tool_continuation_unlocked:" + policyReason
	}
	if scopeKey != "" {
		if r.autoLocks == nil {
			r.autoLocks = make(map[string]autoModelLock)
		}
		r.autoLocks[scopeKey] = autoModelLock{ModelID: route.Model.ID, LastUsed: now}
	}
	route.AutoReason = policyReason
	return route, nil
}

// ForgetAuto 清除手动改选模型时对应主会话或分支的 Auto 模型锁。
func (r *Router) ForgetAuto(conv *converter.ParsedConversation) {
	if conv == nil {
		return
	}
	key := autoScopeKey(conv)
	if key == "" {
		return
	}
	r.autoMu.Lock()
	delete(r.autoLocks, key)
	r.autoMu.Unlock()
}

func autoScopeKey(conv *converter.ParsedConversation) string {
	if conv.ContinuationStateKey != "" {
		return conv.ContinuationStateKey
	}
	return conv.ConversationID
}

func (r *Router) expireAutoLocksLocked(now time.Time) {
	for key, lock := range r.autoLocks {
		if now.Sub(lock.LastUsed) >= autoLockIdleLimit {
			delete(r.autoLocks, key)
		}
	}
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

func selectAutoTarget(conv *converter.ParsedConversation, config store.AutoRoutingConfig) (string, string, error) {
	if hasNonEmptyImages(conv) {
		if config.VisionModelID == "" {
			return "", "", fmt.Errorf("auto routing requires a configured vision model for image input")
		}
		return config.VisionModelID, "vision_required", nil
	}

	for _, rule := range config.TaskRules {
		value := autoTaskFieldValue(conv, rule.Field)
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(rule.Value)) {
			field := strings.ToLower(strings.TrimSpace(rule.Field))
			return rule.ModelID, "task:" + field + "=" + strings.TrimSpace(rule.Value), nil
		}
	}

	reasoningEffort := strings.ToLower(strings.TrimSpace(conv.ReasoningEffort))
	if (reasoningEffort == "high" || reasoningEffort == "xhigh" || reasoningEffort == "max") && config.ReasoningModelID != "" {
		return config.ReasoningModelID, "high_reasoning", nil
	}

	chars := conversationUnicodeChars(conv)
	if chars >= config.LongContextMinChars && config.LongContextModelID != "" {
		return config.LongContextModelID, "long_context", nil
	}
	if !hasFastRouteBlockers(conv) && chars <= config.FastMaxChars && config.FastModelID != "" {
		return config.FastModelID, "fast_chat", nil
	}
	if config.DefaultModelID == "" {
		return "", "", fmt.Errorf("auto routing has no configured default model")
	}
	return config.DefaultModelID, "default", nil
}

func autoTaskFieldValue(conv *converter.ParsedConversation, field string) string {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "agent_task_type":
		return conv.AgentTaskType
	case "agent_mode":
		return conv.AgentMode
	case "chat_trigger_type":
		return conv.ChatTriggerType
	default:
		return ""
	}
}

func hasNonEmptyImages(conv *converter.ParsedConversation) bool {
	if conv == nil {
		return false
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

func hasFastRouteBlockers(conv *converter.ParsedConversation) bool {
	if len(conv.RawTools) > 0 || len(conv.CurrentToolResults) > 0 || hasNonEmptyImages(conv) {
		return true
	}
	for _, entry := range conv.History {
		if len(entry.ToolUses) > 0 || len(entry.ToolResults) > 0 {
			return true
		}
	}
	effort := strings.ToLower(strings.TrimSpace(conv.ReasoningEffort))
	return effort == "high" || effort == "xhigh" || effort == "max"
}

func hasToolContinuation(conv *converter.ParsedConversation) bool {
	return conv != nil && len(conv.CurrentToolResults) > 0
}

func conversationUnicodeChars(conv *converter.ParsedConversation) int {
	if conv == nil {
		return 0
	}
	total := unicodeChars(conv.SystemText) + unicodeChars(conv.CurrentContent)
	total += toolResultUnicodeChars(conv.CurrentToolResults)
	for _, raw := range conv.RawTools {
		total += utf8.RuneCount(raw)
	}
	for _, entry := range conv.History {
		if entry.IsSystem {
			continue
		}
		total += unicodeChars(entry.UserContent)
		total += unicodeChars(entry.AssistantContent)
		for _, use := range entry.ToolUses {
			total += unicodeChars(use.Name)
			total += utf8.RuneCount(use.Input)
		}
		total += toolResultUnicodeChars(entry.ToolResults)
	}
	return total
}

func toolResultUnicodeChars(results []converter.ParsedToolResult) int {
	total := 0
	for _, result := range results {
		if len(result.Blocks) == 0 {
			total += unicodeChars(result.Content)
			continue
		}
		for _, block := range result.Blocks {
			if len(block.Raw) > 0 {
				total += utf8.RuneCount(block.Raw)
			} else {
				total += unicodeChars(block.Text)
			}
		}
	}
	return total
}

func unicodeChars(value string) int {
	return utf8.RuneCountInString(value)
}

func (r *Router) autoModelLocked() *store.Model {
	candidateIDs := []string{
		r.autoConfig.DefaultModelID,
		r.autoConfig.VisionModelID,
		r.autoConfig.ReasoningModelID,
		r.autoConfig.LongContextModelID,
		r.autoConfig.FastModelID,
	}
	for _, rule := range r.autoConfig.TaskRules {
		candidateIDs = append(candidateIDs, rule.ModelID)
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

	imageMode := store.ImageInputUnsupported
	if r.autoConfig.VisionModelID != "" {
		for _, candidate := range candidates {
			if candidate.ID == r.autoConfig.VisionModelID && modelSupportsImages(candidate) {
				imageMode = store.ImageInputSupported
				break
			}
		}
	}
	name := "Auto（按任务选模）"
	return &store.Model{
		ID:              "auto",
		Name:            name,
		Note:            name,
		ImageInputMode:  imageMode,
		ContextWindow:   groupMinContextWindow(candidates),
		MaxTokens:       groupMinMaxTokens(candidates),
		ReasoningEffort: groupReasoningEffort(candidates),
		Provider:        "group",
		RateMultiplier:  groupRateMultiplier(candidates),
		Enabled:         true,
	}
}
