package api

import (
	"fmt"
	"slices"

	"kiro-proxy/converter"
	"kiro-proxy/store"
)

// normalizeModelReasoningConfig 在保存前校验思考配置：支持档位须为该 provider
// 本地可表达的档位并按强度排序存储；非空默认档位必须属于模型有效档位；
// 思考形态须为该 provider 可用的形态。不在保存时就近改写默认档位，
// 避免配置值与用户所选不一致却无提示。
func normalizeModelReasoningConfig(m *store.Model) error {
	style, err := converter.NormalizeThinkingStyle(m.Provider, m.ThinkingStyle)
	if err != nil {
		return err
	}
	m.ThinkingStyle = style
	supported, err := converter.NormalizeSupportedReasoningEfforts(m.Provider, m.SupportedReasoningEfforts)
	if err != nil {
		return err
	}
	m.SupportedReasoningEfforts = supported
	effort, err := converter.NormalizeReasoningEffort(m.ReasoningEffort)
	if err != nil {
		return err
	}
	if effort != "" {
		allowed, err := converter.ModelReasoningEfforts(m.Provider, supported)
		if err != nil {
			return err
		}
		if !slices.Contains(allowed, effort) {
			return fmt.Errorf("默认思考档位 %q 不在模型有效档位 %v 中", effort, allowed)
		}
	}
	m.ReasoningEffort = effort
	return nil
}
