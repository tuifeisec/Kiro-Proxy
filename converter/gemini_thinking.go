package converter

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Gemini 各代思考配置形态不同：3 及以后用 thinkingLevel（2.5 会以
// "thinking_level is not supported by this model" 拒绝），2.5 用 thinkingBudget
// 且每个型号的取值范围不同，1.x/2.0 不支持思考。

// GeminiRequestOptions 是模型配置中影响 Gemini 请求构造的选项。
type GeminiRequestOptions struct {
	ThinkingStyle string // 空=按模型名自动判断；level / budget 为用户显式覆盖
}

const (
	geminiLevelMinimal = "minimal"
	geminiLevelLow     = "low"
	geminiLevelMedium  = "medium"
	geminiLevelHigh    = "high"

	geminiBudgetLow    = 1024
	geminiBudgetMedium = 4096
	geminiBudgetHigh   = 8192
	geminiBudgetXHigh  = 16384

	geminiTierFlashLite = "flash-lite"
	geminiTierFlash     = "flash"
	geminiTierPro       = "pro"
)

// geminiBudgetRange 是 2.5 型号可接受的 thinkingBudget：off 为关闭思考时发送的值
// （Pro 不能关闭，只能取最小值），min/max 为开启时的范围。
type geminiBudgetRange struct {
	off, min, max int
}

// 取值来自 Google 模型文档：Pro 128..32768 且不能关闭（"does not support setting
// thinking_budget to 0"）；Flash 1..24576；Flash-Lite 512..24576；两者用 0 关闭。
var (
	geminiBudgetPro       = geminiBudgetRange{off: 128, min: 128, max: 32768}
	geminiBudgetFlash     = geminiBudgetRange{off: 0, min: 1, max: 24576}
	geminiBudgetFlashLite = geminiBudgetRange{off: 0, min: 512, max: 24576}
	// 无法识别型号时沿用旧行为发送 budget：上限取 Flash 系的 24576，下限取三者最大值，
	// 夹紧后的值对任一 2.5 型号都合法。
	geminiBudgetUnknown = geminiBudgetRange{off: 0, min: 512, max: 24576}
)

var (
	geminiVersionPattern = regexp.MustCompile(`gemini-(\d+)(?:\.(\d+))?`)
	// flash-lite 写在 flash 之前，保证最左匹配优先取更长的型号名。
	geminiTierPattern = regexp.MustCompile(`(?:^|[^a-z0-9])(flash-lite|flash|pro)(?:[^a-z0-9]|$)`)
)

// ResolveGeminiThinking 计算实际下发的 thinkingConfig，调用方可据此记录下发值。
// effort 视为已按模型能力映射的档位；空 effort 返回 nil，表示沿用上游默认。
func ResolveGeminiThinking(model, effort string, opts GeminiRequestOptions) (*GeminiThinkingConfig, error) {
	style, err := NormalizeThinkingStyle("gemini", opts.ThinkingStyle)
	if err != nil {
		return nil, err
	}
	effort = strings.ToLower(strings.TrimSpace(effort))
	if err := ValidateProviderReasoningEffort("gemini", effort); err != nil {
		return nil, err
	}
	if effort == "" {
		return nil, nil
	}
	name := strings.ToLower(strings.TrimSpace(model))
	if style == ThinkingStyleAuto {
		supported := false
		style, supported = geminiAutoThinkingStyle(name)
		if !supported {
			return nil, fmt.Errorf("Gemini 模型 %q 不支持思考，请清空该模型或模型组的思考强度（当前 %q）", model, effort)
		}
	}
	if style == ThinkingStyleLevel {
		return geminiLevelThinking(name, effort), nil
	}
	return geminiBudgetThinking(geminiBudgetRangeFor(name), effort), nil
}

// geminiAutoThinkingStyle 按模型名中的版本号选择形态；第二个返回值为 false
// 表示 1.x/2.0 这类不支持思考的型号。版本无法识别时按 2.5 的 budget 形态处理。
func geminiAutoThinkingStyle(name string) (string, bool) {
	match := geminiVersionPattern.FindStringSubmatch(name)
	if match == nil {
		return ThinkingStyleBudget, true
	}
	major, err := strconv.Atoi(match[1])
	if err != nil {
		return ThinkingStyleBudget, true
	}
	minor := 0
	if match[2] != "" {
		if minor, err = strconv.Atoi(match[2]); err != nil {
			return ThinkingStyleBudget, true
		}
	}
	switch {
	case major >= 3:
		return ThinkingStyleLevel, true
	case major == 2 && minor >= 5:
		return ThinkingStyleBudget, true
	default:
		return "", false
	}
}

func geminiModelTier(name string) string {
	match := geminiTierPattern.FindStringSubmatch(name)
	if match == nil {
		return ""
	}
	return match[1]
}

// geminiLevelThinking 映射到 Gemini 3 的 thinkingLevel。flash 类有
// minimal/low/medium/high，pro 类只有 low/high；Gemini 3 无法完全关闭思考，
// none 取最低档且不返回思考摘要。
func geminiLevelThinking(name, effort string) *GeminiThinkingConfig {
	tier := geminiModelTier(name)
	flash := tier == geminiTierFlash || tier == geminiTierFlashLite
	config := &GeminiThinkingConfig{IncludeThoughts: true}
	switch effort {
	case "none":
		config.ThinkingLevel = geminiLevelLow
		if flash {
			config.ThinkingLevel = geminiLevelMinimal
		}
		config.IncludeThoughts = false
	case "low":
		config.ThinkingLevel = geminiLevelLow
	case "medium":
		config.ThinkingLevel = geminiLevelHigh
		if flash {
			config.ThinkingLevel = geminiLevelMedium
		}
	default: // high / xhigh / max，档位已由 ResolveGeminiThinking 校验
		config.ThinkingLevel = geminiLevelHigh
	}
	return config
}

func geminiBudgetRangeFor(name string) geminiBudgetRange {
	switch geminiModelTier(name) {
	case geminiTierFlashLite:
		return geminiBudgetFlashLite
	case geminiTierFlash:
		return geminiBudgetFlash
	case geminiTierPro:
		return geminiBudgetPro
	default:
		return geminiBudgetUnknown
	}
}

// geminiBudgetThinking 映射到 2.5 的 thinkingBudget 并按型号范围夹紧。
// 关闭思考时不发 includeThoughts（"include_thoughts is only enabled when
// thinking is enabled"）。
func geminiBudgetThinking(budgetRange geminiBudgetRange, effort string) *GeminiThinkingConfig {
	if effort == "none" {
		off := budgetRange.off
		return &GeminiThinkingConfig{ThinkingBudget: &off}
	}
	budget := budgetRange.max // max
	switch effort {
	case "low":
		budget = geminiBudgetLow
	case "medium":
		budget = geminiBudgetMedium
	case "high":
		budget = geminiBudgetHigh
	case "xhigh":
		budget = geminiBudgetXHigh
	}
	budget = min(max(budget, budgetRange.min), budgetRange.max)
	return &GeminiThinkingConfig{ThinkingBudget: &budget, IncludeThoughts: true}
}
