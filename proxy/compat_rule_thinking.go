package proxy

import (
	"regexp"

	json "github.com/goccy/go-json"
)

const (
	compatThinkingEnabledValue  = "enabled"
	compatThinkingAdaptiveValue = "budget"
	compatThinkingDisplayValue  = "omit"
)

// 总是思考、不能关闭思考的模型（magpie anthropic.go alwaysThinks / thinkingOffRefused 记录）：
// Z.ai GLM "always engages in thinking"、"该模型始终支持思考，不可关闭"、DashScope
// "The value of the enable_thinking parameter is restricted to True."、MiniMax
// "thinking.type="disabled" (including reasoning.effort=none) is not allowed"、Volcengine
// "thinking.type `disabled` is not supported by this model"、Kimi "does not support disabled thinking mode"、
// Opus ""thinking.type.disabled" is not supported for this model"。只在已发送 thinking.type=disabled 时适用。
var thinkingOffRefused = regexp.MustCompile("(?i)always engages in thinking|thinking (?:can ?not|can't) be (?:disabled|turned off)|enable_thinking[^\"\\n]{0,60}restricted to true|始终(?:支持|开启|启用)?思考|思考[^\"\\n]{0,20}(?:不可|无法|不能)关闭|thinking\\.type\\W{0,3}disabled\\W{0,6}(?:\\(including reasoning\\.effort=none\\) )?is not (?:allowed|supported)|does not support disabled thinking mode")

// 要求 thinking.type 必须为 enabled（magpie thinking_needed.go 记录的天翼云中转：`thinking.type must be "enabled"`）。
var thinkingNeededRefused = regexp.MustCompile(`(?i)thinking[._ ]type\W+(?:must|should|has to) be\W+enabled\b`)

// 不认识 adaptive 的中转：上面的 must be "enabled"，以及按 serde（unknown variant `adaptive`）
// 与 pydantic 判别联合（Input tag 'adaptive' ... does not match any of the expected tags）格式构造的样本，需实测。
var thinkingAdaptiveRefused = regexp.MustCompile("(?i)thinking[._ ]type\\W+(?:must|should|has to) be\\W+enabled\\b|unknown variant [`'\"\\\\]*adaptive\\b[^\\n]{0,80}|input tag\\W+adaptive\\W+found using\\W+type\\W+does not match any of the expected tags[^\\n]{0,40}")

// 中转不认识 thinking.display（magpie anthropic_shapes.go 记录）：
// "thinking.adaptive.display: Input should be 'summarized', 'omitted'" 或 "... Extra inputs are not permitted"。
var thinkingDisplayRefused = regexp.MustCompile(`thinking\.(?:adaptive|enabled)\.display:? (?:Input should be|Extra inputs are not permitted)[^\n]{0,60}`)

// thinking 关闭或缺省被拒：改为 converter 构造的最低档 budget 思考，max_tokens 不足时抬高到大于预算。
var thinkingEnabledRule = compatRule{
	kind:      capabilityThinkingEnabled,
	protocols: []compatProtocol{compatProtocolAnthropic},
	recognize: func(_ compatTarget, refusal compatRefusal, body []byte) (string, string, bool) {
		thinkingType := compatThinkingType(body)
		if thinkingType == "disabled" {
			if evidence, ok := compatMatch(thinkingOffRefused, refusal.text); ok {
				return compatThinkingEnabledValue, evidence, true
			}
		}
		if thinkingType == "" || thinkingType == "disabled" {
			if evidence, ok := compatMatch(thinkingNeededRefused, refusal.text); ok {
				return compatThinkingEnabledValue, evidence, true
			}
		}
		return "", "", false
	},
	apply: func(target compatTarget, body []byte, _ string) ([]byte, bool, error) {
		return compatWithThinkingEnabled(target, body)
	},
	wireEffort: func(compatTarget, []byte) string { return compatThinkingNeededEffort },
}

// adaptive 被拒：按原档位改为 converter 构造的 budget 形态；预算放不进 max_tokens 时不适用。
var thinkingAdaptiveRule = compatRule{
	kind:      capabilityThinkingAdaptive,
	protocols: []compatProtocol{compatProtocolAnthropic},
	recognize: func(target compatTarget, refusal compatRefusal, body []byte) (string, string, bool) {
		if compatThinkingType(body) != "adaptive" {
			return "", "", false
		}
		evidence, ok := compatMatch(thinkingAdaptiveRefused, refusal.text)
		if !ok {
			return "", "", false
		}
		if _, changed, err := compatWithBudgetThinking(target, body); err != nil || !changed {
			return "", "", false
		}
		return compatThinkingAdaptiveValue, evidence, true
	},
	apply: func(target compatTarget, body []byte, _ string) ([]byte, bool, error) {
		return compatWithBudgetThinking(target, body)
	},
}

var thinkingDisplayRule = compatRule{
	kind:      capabilityThinkingDisplay,
	protocols: []compatProtocol{compatProtocolAnthropic},
	recognize: func(_ compatTarget, refusal compatRefusal, body []byte) (string, string, bool) {
		if _, has := compatThinkingObject(body)["display"]; !has {
			return "", "", false
		}
		evidence, ok := compatMatch(thinkingDisplayRefused, refusal.text)
		if !ok {
			return "", "", false
		}
		return compatThinkingDisplayValue, evidence, true
	},
	apply: func(_ compatTarget, body []byte, _ string) ([]byte, bool, error) {
		root, err := decodeCompatRoot(body)
		if err != nil {
			return nil, false, err
		}
		thinking := compatObject(root["thinking"])
		if _, has := thinking["display"]; !has {
			return body, false, nil
		}
		delete(thinking, "display")
		encoded, err := encodeCompatObject(thinking)
		if err != nil {
			return nil, false, err
		}
		root["thinking"] = encoded
		return encodeCompatRoot(root)
	},
}

func compatThinkingObject(body []byte) map[string]json.RawMessage {
	root, err := decodeCompatRoot(body)
	if err != nil {
		return nil
	}
	return compatObject(root["thinking"])
}

func compatThinkingType(body []byte) string {
	return compatString(compatThinkingObject(body)["type"])
}
