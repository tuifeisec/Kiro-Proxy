package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"kiro-proxy/converter"
	"kiro-proxy/store"
	"strings"
	"time"
)

// autoChannelIdentity 标识出站模型、账号和调用布局，不暴露密钥或带凭据地址。
// Codex access token 正常刷新不改变账号身份；缺少账号字段时才使用 refresh token。
func autoChannelIdentity(model *store.Model) string {
	identity := model.APIKey
	if strings.EqualFold(model.Provider, "codex") {
		identity = model.CodexAccountID
		if identity == "" {
			identity = model.CodexEmail
		}
		if identity == "" {
			identity = model.RefreshToken
		}
	}
	fields := []interface{}{model.ID, model.Provider, model.ModelID, strings.TrimRight(model.BaseURL, "/"),
		model.Endpoint, identity, model.ProxyURL, model.DisableAutoSuffix, model.ServiceTier,
		model.EnableContinuation, model.EnableOpenCodeSession, model.ReasoningEffort}
	encoded, _ := json.Marshal(fields)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

// 调用时已持有 autoMu 与 mu.RLock；阶段策略修改不能改写进行中的工具链。
func (r *Router) resolveAutoContinuationLocked(conv *converter.ParsedConversation, lock autoModelLock, scope string, now time.Time) (*Route, error) {
	route, err := r.routeForLockedAutoModel(lock.ModelID, conv.ModelID)
	if err != nil {
		return nil, err
	}
	if lock.ChannelIdentity != autoChannelIdentity(route.Model) {
		return nil, fmt.Errorf("auto tool continuation target %q has changed channel identity; start a new task before using the updated configuration", lock.ModelID)
	}
	if hasNonEmptyImages(conv) && !modelSupportsImages(route.Model) {
		return nil, fmt.Errorf("auto conversation is locked to model %q, which does not support image input", lock.ModelID)
	}
	lock.LastUsed = now
	r.autoLocks[scope] = lock
	route.AutoReason = "conversation_lock:" + lock.Reason
	route.AutoMode = store.AutoRoutingModeStageFixed
	route.AutoStage = lock.Stage
	route.AutoChannelIdentity = lock.ChannelIdentity
	return route, nil
}
