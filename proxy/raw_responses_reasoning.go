package proxy

import (
	"bytes"
	"fmt"

	"kiro-proxy/converter"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

// rawResponsesReasoning 是 raw 请求体中 reasoning 字段的解析结果。
type rawResponsesReasoning struct {
	fields      map[string]json.RawMessage // reasoning 对象；字段缺失或为 null 时为空 map
	effort      string                     // reasoning.effort 的字符串值
	effortIsStr bool                       // effort 存在且是字符串（含空字符串）
}

func parseRawResponsesReasoning(root map[string]json.RawMessage) (rawResponsesReasoning, error) {
	parsed := rawResponsesReasoning{fields: make(map[string]json.RawMessage)}
	raw, exists := root["reasoning"]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return parsed, nil
	}
	if err := json.Unmarshal(raw, &parsed.fields); err != nil || parsed.fields == nil {
		if err == nil {
			err = fmt.Errorf("reasoning must be an object")
		}
		return parsed, fmt.Errorf("parse raw Responses reasoning: %w", err)
	}
	if effortRaw, ok := parsed.fields["effort"]; ok {
		parsed.effortIsStr = json.Unmarshal(effortRaw, &parsed.effort) == nil
	}
	return parsed, nil
}

// resolveRawResponsesReasoning 以请求体自身的 reasoning.effort 作为请求层档位；
// 请求体未给出字符串档位时沿用解析得到的 Conv.ReasoningEffort。非字符串 effort
// 不参与解析（由 Chat 映射或上游按原规则报错）。
func resolveRawResponsesReasoning(root map[string]json.RawMessage, pre *Preflight, model *store.Model) (rawResponsesReasoning, reasoningEffortDecision, error) {
	reasoning, err := parseRawResponsesReasoning(root)
	if err != nil {
		return reasoning, reasoningEffortDecision{}, err
	}
	requestEffort := reasoning.effort
	if requestEffort == "" && pre != nil && pre.Conv != nil {
		requestEffort = pre.Conv.ReasoningEffort
	}
	decision, err := resolveReasoningEffortDecisionWithRequest(pre, model, requestEffort)
	if err != nil {
		return reasoning, decision, fmt.Errorf("resolve raw Responses reasoning effort: %w", err)
	}
	return reasoning, decision, nil
}

// applyRawResponsesReasoning 把下发档位写回 raw Responses 根对象：
//   - 模型禁用推理字段：移除 reasoning 与 Chat 风格的 reasoning_effort；
//   - Auto 固定档位：覆盖客户端值，固定为空时只删除 effort；
//   - 客户端字符串档位：按模型有效档位就近映射后写回；
//   - 客户端未带档位：用分组/模型默认档位补齐。
func applyRawResponsesReasoning(root map[string]json.RawMessage, pre *Preflight, model *store.Model) error {
	reasoning, decision, err := resolveRawResponsesReasoning(root, pre, model)
	if err != nil {
		return err
	}
	if decision.FieldDisabled {
		delete(root, "reasoning")
		delete(root, "reasoning_effort")
		return nil
	}
	clientSetEffort := reasoning.effortIsStr && reasoning.effort != ""
	switch {
	case decision.Source == reasoningEffortSourceAuto:
	case clientSetEffort:
		if decision.Wire == reasoning.effort {
			return nil
		}
	case reasoning.effortIsStr || reasoning.fields["effort"] == nil:
		// 缺失或空字符串：仅在有可下发档位时补齐，保持其余字段不变。
		if decision.Wire == "" {
			return nil
		}
	default:
		// 非字符串 effort 保持原样。
		return nil
	}
	if decision.Wire == "" {
		delete(reasoning.fields, "effort")
	} else {
		effortJSON, err := json.Marshal(decision.Wire)
		if err != nil {
			return fmt.Errorf("encode raw Responses reasoning effort: %w", err)
		}
		reasoning.fields["effort"] = effortJSON
	}
	if len(reasoning.fields) == 0 {
		delete(root, "reasoning")
		return nil
	}
	encoded, err := json.Marshal(reasoning.fields)
	if err != nil {
		return fmt.Errorf("encode raw Responses reasoning: %w", err)
	}
	root["reasoning"] = encoded
	return nil
}

// rawAnthropicThinkingFields 返回 raw Anthropic 请求应写入的 thinking/output_config。
// 本阶段只负责让它拿到正确的下发档位；thinking 形态的重写留给后续阶段。
func rawAnthropicThinkingFields(root map[string]json.RawMessage, pre *Preflight, model *store.Model) (map[string]string, map[string]string, error) {
	reasoning, decision, err := resolveRawResponsesReasoning(root, pre, model)
	if err != nil {
		return nil, nil, err
	}
	if _, present := reasoning.fields["effort"]; present && !reasoning.effortIsStr && decision.Source != reasoningEffortSourceAuto {
		return nil, nil, fmt.Errorf("raw Responses reasoning effort must be a string")
	}
	switch decision.Wire {
	case "":
		return nil, nil, nil
	case converter.ReasoningEffortNone:
		return map[string]string{"type": "disabled"}, nil, nil
	case converter.ReasoningEffortLow, converter.ReasoningEffortMedium, converter.ReasoningEffortHigh:
		return map[string]string{"type": "adaptive"}, map[string]string{"effort": decision.Wire}, nil
	default:
		return nil, nil, fmt.Errorf("raw Responses reasoning effort %q is unsupported by Anthropic", decision.Wire)
	}
}
