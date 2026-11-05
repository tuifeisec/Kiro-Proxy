package converter

import "regexp"

// deepseekAnthropicHost 是 DeepSeek 官方 Anthropic 兼容端点的 host。
// DeepSeek 文档（magpie outputEffortAt 注释引用 api-docs.deepseek.com/guides/anthropic_api）：
// thinking "Supported (budget_tokens is ignored)"，output_config "Only effort is supported"。
// 其他 host 上的 DeepSeek 模型不在该文档覆盖范围内，按普通非 Claude 模型处理。
const deepseekAnthropicHost = "api.deepseek.com"

// outputEffortModelPattern 匹配忽略 budget、只从 output_config.effort 读取档位的非 Claude 模型
// （magpie effortInOutputConfig 注释）：Z.ai 的 GLM-5.2/5.3、MiniMax-M3.1-Flash，
// 以及经中转映射到 OpenAI 的 gpt-数字 模型。仅传 budget 时这些上游停留在默认档位。
// 调用方传入小写模型 ID。
var outputEffortModelPattern = regexp.MustCompile(`glm-5\.[23](?:$|[-.:/\[])|minimax-m3\.1-flash(?:$|[-.:/\[])|(?:^|[^a-z0-9])gpt-\d`)

// deepseekEffortByRequested 把已映射档位就近落到 DeepSeek 的 low/high/max 三档，距离相同取更强
// （magpie deepseekEfforts 注释：DeepSeek 把 medium、xhigh 分别读作 high、max 附近）。
var deepseekEffortByRequested = map[string]string{
	"low":    "low",
	"medium": "high",
	"high":   "high",
	"xhigh":  "max",
	"max":    "max",
}

// upstreamReadsOutputEffort 判断非 Claude 上游是否需要在 budget 之外额外下发 output_config.effort。
// 其他非 Claude 模型不发 output_config，避免未知字段被拒。
func upstreamReadsOutputEffort(host, lowerModelID string) bool {
	return host == deepseekAnthropicHost || outputEffortModelPattern.MatchString(lowerModelID)
}

// fitOutputEffortToHost 把档位适配到上游实际支持的 output_config.effort 取值。
func fitOutputEffortToHost(host, effort string) string {
	if host == deepseekAnthropicHost {
		if fitted, ok := deepseekEffortByRequested[effort]; ok {
			return fitted
		}
	}
	return effort
}
