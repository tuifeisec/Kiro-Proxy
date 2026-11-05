package converter

import (
	"fmt"
	"log"
	"strings"
)

// 实际下发的思考形态，供调用方观测与测试断言。
const (
	AnthropicThinkingModeUnset        = ""              // 未请求档位：不写 thinking / output_config
	AnthropicThinkingModeAdaptive     = "adaptive"      // thinking.type=adaptive + output_config.effort
	AnthropicThinkingModeBudget       = "budget"        // thinking.type=enabled + budget_tokens（部分上游另带 output_config.effort）
	AnthropicThinkingModeDisabled     = "disabled"      // thinking.type=disabled
	AnthropicThinkingModeBetweenTools = "between_tools" // thinking.type=between_tools（Sonnet 5.5 的关闭方式）
	AnthropicThinkingModeAlwaysOn     = "always_on"     // 模型不能关闭思考：不写 thinking，output_config.effort 降到最低
)

const (
	anthropicThinkingTypeAdaptive     = "adaptive"
	anthropicThinkingTypeEnabled      = "enabled"
	anthropicThinkingTypeDisabled     = "disabled"
	anthropicThinkingTypeBetweenTools = "between_tools"
	// Opus 4.7 起 adaptive 默认 display=omitted，不返回思考文本；显式要 summarized 才能把思考展示给客户端
	// （magpie anthropic.go adaptiveThinking 注释 #1485）。
	anthropicThinkingDisplaySummarized = "summarized"
	// 不能关闭思考的模型在 none 档改发的最低档位（magpie thinkingOffAsTaken 注释）。
	anthropicAlwaysOnOffEffort = "low"
)

// budget 形态各档位的 budget_tokens：low/medium/high 沿用既有取值，xhigh/max 为新增档位。
const (
	anthropicBudgetLow    = 1024
	anthropicBudgetMedium = 4096
	anthropicBudgetHigh   = 8192
	anthropicBudgetXhigh  = 16384
	anthropicBudgetMax    = 32000
	// Anthropic 要求 1024 <= budget_tokens < max_tokens；夹紧时为正文至少预留 anthropicAnswerReserve。
	anthropicMinThinkingBudget = 1024
	anthropicAnswerReserve     = 1024
)

var anthropicBudgetByEffort = map[string]int{
	"low":    anthropicBudgetLow,
	"medium": anthropicBudgetMedium,
	"high":   anthropicBudgetHigh,
	"xhigh":  anthropicBudgetXhigh,
	"max":    anthropicBudgetMax,
}

// AnthropicThinkingInput 是构造 Anthropic 思考参数的输入。
type AnthropicThinkingInput struct {
	ModelID       string // 发给上游的模型 ID，用于识别 Claude 版本与按模型名的规则
	UpstreamHost  string // 小写、不含端口；空表示未知，不触发按 host 的规则
	ThinkingStyle string // 模型级思考形态覆盖，见 thinking_style.go；空表示自动判断
	Effort        string // 已按模型能力映射的档位；空表示不下发任何思考参数
	MaxTokens     int    // 请求实际下发的 max_tokens，budget 形态据此校验
}

// AnthropicThinkingParams 是实际下发的思考参数；字段为 nil 表示不写入请求。
type AnthropicThinkingParams struct {
	Thinking     *AnthropicThinking
	OutputConfig *AnthropicOutputConfig
	Mode         string // 实际下发形态，取值见 AnthropicThinkingMode*
}

// BuildAnthropicThinking 是 Kiro 主路径与 raw Responses 路径共用的 Anthropic 思考参数构造，
// 保证同一模型在两条路径上的思考形态一致。
// 形态判定：ThinkingStyle 显式值优先；否则 Claude 4.6 及以上为 adaptive，其余（含非 Claude）为 budget。
// budget 放不进 max_tokens 时返回 error，不静默去掉 thinking，也不擅自抬高 max_tokens。
func BuildAnthropicThinking(in AnthropicThinkingInput) (AnthropicThinkingParams, error) {
	effort, err := NormalizeReasoningEffort(in.Effort)
	if err != nil {
		return AnthropicThinkingParams{}, err
	}
	style, err := NormalizeThinkingStyle("anthropic", in.ThinkingStyle)
	if err != nil {
		return AnthropicThinkingParams{}, err
	}
	if effort == "" {
		return AnthropicThinkingParams{}, nil
	}
	version := parseClaudeVersion(in.ModelID)
	if effort == "none" {
		return anthropicThinkingOff(version), nil
	}
	if style == ThinkingStyleAdaptive || style == ThinkingStyleAuto && version.adaptiveOnly() {
		return anthropicAdaptiveThinking(version, in.UpstreamHost, effort), nil
	}
	return anthropicBudgetThinking(in, effort)
}

func anthropicThinkingOff(version claudeVersion) AnthropicThinkingParams {
	switch thinkingOffRule(version) {
	case claudeOffBetweenTools:
		return AnthropicThinkingParams{
			Thinking: &AnthropicThinking{Type: anthropicThinkingTypeBetweenTools},
			Mode:     AnthropicThinkingModeBetweenTools,
		}
	case claudeOffNever:
		return AnthropicThinkingParams{
			OutputConfig: &AnthropicOutputConfig{Effort: anthropicAlwaysOnOffEffort},
			Mode:         AnthropicThinkingModeAlwaysOn,
		}
	default:
		return AnthropicThinkingParams{
			Thinking: &AnthropicThinking{Type: anthropicThinkingTypeDisabled},
			Mode:     AnthropicThinkingModeDisabled,
		}
	}
}

func anthropicAdaptiveThinking(version claudeVersion, host, effort string) AnthropicThinkingParams {
	if effort == "xhigh" && version.lacksXhigh() {
		effort = "max"
	}
	return AnthropicThinkingParams{
		Thinking:     &AnthropicThinking{Type: anthropicThinkingTypeAdaptive, Display: anthropicThinkingDisplaySummarized},
		OutputConfig: &AnthropicOutputConfig{Effort: fitOutputEffortToHost(host, effort)},
		Mode:         AnthropicThinkingModeAdaptive,
	}
}

func anthropicBudgetThinking(in AnthropicThinkingInput, effort string) (AnthropicThinkingParams, error) {
	requested, ok := anthropicBudgetByEffort[effort]
	if !ok {
		return AnthropicThinkingParams{}, fmt.Errorf("Anthropic 模型不支持思考强度 %q", effort)
	}
	budget := requested
	if limit := in.MaxTokens - anthropicAnswerReserve; budget > limit {
		budget = limit
	}
	if budget < anthropicMinThinkingBudget {
		return AnthropicThinkingParams{}, fmt.Errorf(
			"max_tokens=%d 放不下思考强度 %q：budget_tokens 至少 %d，且需为正文预留 %d token；请调大 max_tokens 或降低思考强度",
			in.MaxTokens, effort, anthropicMinThinkingBudget, anthropicAnswerReserve)
	}
	if budget != requested {
		log.Printf("[anthropic_thinking] model=%s effort=%s budget_tokens %d 超过 max_tokens=%d 减去正文预留 %d，夹紧为 %d",
			in.ModelID, effort, requested, in.MaxTokens, anthropicAnswerReserve, budget)
	}
	params := AnthropicThinkingParams{
		Thinking: &AnthropicThinking{Type: anthropicThinkingTypeEnabled, BudgetTokens: budget},
		Mode:     AnthropicThinkingModeBudget,
	}
	if upstreamReadsOutputEffort(in.UpstreamHost, strings.ToLower(in.ModelID)) {
		params.OutputConfig = &AnthropicOutputConfig{Effort: fitOutputEffortToHost(in.UpstreamHost, effort)}
	}
	return params, nil
}
