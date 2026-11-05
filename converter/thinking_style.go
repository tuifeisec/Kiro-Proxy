package converter

import (
	"fmt"
	"strings"
)

// 模型级思考形态覆盖。空值表示按 provider 与模型名自动判断；
// 自定义中转改名后无法从模型名判断版本时，由用户显式指定。
const (
	ThinkingStyleAuto     = ""
	ThinkingStyleAdaptive = "adaptive" // Anthropic：thinking.type=adaptive + output_config.effort
	ThinkingStyleBudget   = "budget"   // Anthropic：thinking.type=enabled + budget_tokens；Gemini：thinkingBudget
	ThinkingStyleLevel    = "level"    // Gemini：thinkingConfig.thinkingLevel
)

// NormalizeThinkingStyle 校验并规范化 provider 可用的思考形态。
func NormalizeThinkingStyle(provider, style string) (string, error) {
	style = strings.ToLower(strings.TrimSpace(style))
	if style == ThinkingStyleAuto {
		return ThinkingStyleAuto, nil
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		if style == ThinkingStyleAdaptive || style == ThinkingStyleBudget {
			return style, nil
		}
	case "gemini":
		if style == ThinkingStyleLevel || style == ThinkingStyleBudget {
			return style, nil
		}
	}
	return "", fmt.Errorf("provider %q 不支持思考形态 %q", provider, style)
}
