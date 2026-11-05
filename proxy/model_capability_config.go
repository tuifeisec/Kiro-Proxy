package proxy

import (
	"strings"

	"kiro-proxy/store"
)

// modelCapabilityConfigured 是"该能力是否由模型配置显式决定"的唯一判定口径。
// 返回 true 时兼容层既不应用已学形态、也不记录观测，被拒的 400 直接透传，
// 让用户修正配置，而不是在配置之外悄悄改写请求。
//   - 档位类：声明了 SupportedReasoningEfforts 或开启 DisableReasoningField。
//   - 思考形态类（关闭被拒改 budget、adaptive 被拒改 budget）：ThinkingStyle 非空。
//
// thinking.display 与思考形态无关，没有对应配置，始终交给观测。
func modelCapabilityConfigured(model *store.Model, kind modelCapabilityKind) bool {
	if model == nil {
		return false
	}
	switch kind {
	case capabilityReasoningEffort, capabilityReasoningField:
		return len(model.SupportedReasoningEfforts) > 0 || model.DisableReasoningField
	case capabilityThinkingEnabled, capabilityThinkingAdaptive:
		return strings.TrimSpace(model.ThinkingStyle) != ""
	default:
		return false
	}
}
