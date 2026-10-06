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
	Stage           string
	Reason          string
	ChannelIdentity string
}

// ResolveAuto 为 Kiro 主生成请求按当前同步阶段选择一个实际模型。
func (r *Router) ResolveAuto(conv *converter.ParsedConversation) (*Route, error) {
	return r.resolveAuto(conv, false)
}

// ResolveStageAuto 用于只提供同步路由元数据的原生 Responses 请求。
// 没有有效阶段工具锁时，该入口要求启用五阶段路由。
func (r *Router) ResolveStageAuto(conv *converter.ParsedConversation) (*Route, error) {
	return r.resolveAuto(conv, true)
}

func (r *Router) resolveAuto(conv *converter.ParsedConversation, requireStage bool) (*Route, error) {
	if conv == nil {
		return nil, fmt.Errorf("auto routing requires a parsed conversation")
	}
	if conv.IsClassifier || !strings.EqualFold(conv.ModelID, "auto") {
		return nil, fmt.Errorf("auto stage routing requires a Kiro main request with modelId=auto")
	}

	now := time.Now()
	scopeKey := autoScopeKey(conv)
	r.autoMu.Lock()
	defer r.autoMu.Unlock()
	r.expireAutoLocksLocked(now)

	r.mu.RLock()
	defer r.mu.RUnlock()
	// 阶段工具续轮先核对原目标；保存新策略或关闭策略只影响下一新任务。
	if scopeKey != "" && hasToolContinuation(conv) {
		if lock, ok := r.autoLocks[scopeKey]; ok {
			return r.resolveAutoContinuationLocked(conv, lock, scopeKey, now)
		}
	}
	if r.autoConfigErr != nil {
		return nil, fmt.Errorf("auto routing configuration is invalid: %w", r.autoConfigErr)
	}
	if requireStage && (!r.autoConfig.Enabled || r.autoConfig.Mode != store.AutoRoutingModeStageFixed) {
		return nil, fmt.Errorf("raw Kiro Responses Auto routing requires enabled stage_fixed mode")
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

	selectionReason := policyReason
	if hasToolContinuation(conv) {
		policyReason = "tool_continuation_unlocked:" + policyReason
	}
	route.AutoMode = store.AutoRoutingModeStageFixed
	if conv.WorkflowStageProvided {
		route.AutoStage = strings.ToLower(strings.TrimSpace(conv.WorkflowStage))
	}
	route.AutoChannelIdentity = autoChannelIdentity(route.Model)
	if scopeKey != "" {
		if r.autoLocks == nil {
			r.autoLocks = make(map[string]autoModelLock)
		}
		r.autoLocks[scopeKey] = autoModelLock{ModelID: route.Model.ID, LastUsed: now,
			Stage: route.AutoStage, Reason: selectionReason, ChannelIdentity: route.AutoChannelIdentity}
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
	if conv == nil || !conv.WorkflowStageProvided {
		return config.DefaultModelID, "stage_unavailable", nil
	}
	stage := strings.ToLower(strings.TrimSpace(conv.WorkflowStage))
	if modelID := config.StageModels.ModelID(stage); modelID != "" {
		return modelID, "stage:" + stage, nil
	}
	return config.DefaultModelID, "stage_unknown:" + stage, nil
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
	candidateIDs := append([]string{r.autoConfig.DefaultModelID}, r.autoConfig.StageModels.ModelIDs()...)

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
	name := "Auto（五阶段固定模型）"
	model := &store.Model{
		ID:             "auto",
		Name:           name,
		Note:           name,
		ImageInputMode: imageMode,
		ContextWindow:  groupMinContextWindow(candidates),
		MaxTokens:      groupMinMaxTokens(candidates),
		Provider:       "group",
		RateMultiplier: groupRateMultiplier(candidates),
		Enabled:        true,
	}
	model.AutoRoutingMode = store.AutoRoutingModeStageFixed
	// 任一目标未配置容量时，不能把其余目标的最小值当成安全下界。
	for _, candidate := range candidates {
		if candidate.ContextWindow <= 0 {
			model.ContextWindow = 0
		}
		if candidate.MaxTokens <= 0 {
			model.MaxTokens = 0
		}
	}
	// 默认档位不能证明支持集合，因此不推断支持档位 schema。
	return model
}
