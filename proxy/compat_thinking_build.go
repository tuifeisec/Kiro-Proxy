package proxy

import (
	"strings"

	"kiro-proxy/converter"

	json "github.com/goccy/go-json"
)

const (
	// 关闭思考被拒时改用的档位：最低档，避免在用户要求不思考时擅自提高推理强度。
	compatThinkingNeededEffort = converter.ReasoningEffortLow
	// 只用于向 converter 探测某档位的原始预算；不会作为 max_tokens 发出。
	compatThinkingBudgetProbeTokens = 1 << 20
)

// compatBudgetThinking 复用 converter.BuildAnthropicThinking 的 budget 形态构造，
// 档位到 budget_tokens 的映射与 host/模型规则都以 converter 为准。
func compatBudgetThinking(target compatTarget, effort string, maxTokens int) (converter.AnthropicThinkingParams, error) {
	return converter.BuildAnthropicThinking(converter.AnthropicThinkingInput{
		ModelID:       target.modelID,
		UpstreamHost:  target.host,
		ThinkingStyle: converter.ThinkingStyleBudget,
		Effort:        effort,
		MaxTokens:     maxTokens,
	})
}

// compatWithThinkingEnabled 把关闭或缺省的思考改为最低档 budget 思考。Anthropic 要求
// max_tokens 大于 budget_tokens：max_tokens 放不下预算时抬高为 预算 + 原值，原回复长度不被预算挤占。
func compatWithThinkingEnabled(target compatTarget, body []byte) ([]byte, bool, error) {
	root, err := decodeCompatRoot(body)
	if err != nil {
		return nil, false, err
	}
	switch compatString(compatObject(root["thinking"])["type"]) {
	case "enabled", "adaptive":
		return body, false, nil
	}
	maxTokens := compatInt(root["max_tokens"])
	params, err := compatBudgetThinking(target, compatThinkingNeededEffort, maxTokens)
	if err != nil {
		params, err = compatBudgetThinking(target, compatThinkingNeededEffort, compatThinkingBudgetProbeTokens)
		if err != nil {
			return nil, false, err
		}
		raised, err := json.Marshal(params.Thinking.BudgetTokens + max(maxTokens, 1))
		if err != nil {
			return nil, false, err
		}
		root["max_tokens"] = raised
	}
	return compatSetThinkingParams(root, params)
}

// compatWithBudgetThinking 把 adaptive 改为同档位的 budget 形态；预算放不进 max_tokens 时不适用，
// 不擅自抬高 max_tokens（与 converter 的 budget 校验一致）。
func compatWithBudgetThinking(target compatTarget, body []byte) ([]byte, bool, error) {
	root, err := decodeCompatRoot(body)
	if err != nil {
		return nil, false, err
	}
	if compatString(compatObject(root["thinking"])["type"]) != "adaptive" {
		return body, false, nil
	}
	effort := strings.ToLower(compatString(compatObject(root["output_config"])["effort"]))
	if effort == "" || effort == converter.ReasoningEffortNone {
		effort = compatThinkingNeededEffort
	}
	params, err := compatBudgetThinking(target, effort, compatInt(root["max_tokens"]))
	if err != nil || params.Thinking == nil {
		return body, false, nil
	}
	return compatSetThinkingParams(root, params)
}

// compatSetThinkingParams 按 converter 结果写入 thinking 与 output_config.effort；
// converter 不下发 output_config 时去掉原有 effort。思考请求不接受采样参数，一并去掉。
func compatSetThinkingParams(root map[string]json.RawMessage, params converter.AnthropicThinkingParams) ([]byte, bool, error) {
	thinking, err := json.Marshal(params.Thinking)
	if err != nil {
		return nil, false, err
	}
	root["thinking"] = thinking
	config := compatObject(root["output_config"])
	if params.OutputConfig != nil {
		if config == nil {
			config = make(map[string]json.RawMessage)
		}
		effort, err := json.Marshal(params.OutputConfig.Effort)
		if err != nil {
			return nil, false, err
		}
		config["effort"] = effort
	} else {
		delete(config, "effort")
	}
	if len(config) == 0 {
		delete(root, "output_config")
	} else {
		encoded, err := encodeCompatObject(config)
		if err != nil {
			return nil, false, err
		}
		root["output_config"] = encoded
	}
	for _, field := range compatSamplingFields {
		delete(root, field)
	}
	return encodeCompatRoot(root)
}

func compatInt(raw json.RawMessage) int {
	var value int
	if json.Unmarshal(raw, &value) != nil {
		return 0
	}
	return value
}
