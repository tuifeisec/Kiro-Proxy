package converter

import (
	"net/url"
	"strings"
)

// OpenAIRequestOptions 携带构建 OpenAI Chat 请求时需要的上游信息。
type OpenAIRequestOptions struct {
	UpstreamHost string // 小写、不含端口；空表示未知，不触发任何按 host 的规则
}

// AnthropicRequestOptions 携带构建 Anthropic Messages 请求时需要的上游信息与模型覆盖。
type AnthropicRequestOptions struct {
	UpstreamHost  string // 小写、不含端口；空表示未知，不触发任何按 host 的规则
	ThinkingStyle string // 见 thinking_style.go；空表示按模型名自动判断
	MaxTokens     int    // 模型配置的最大输出；请求未给出 max_tokens 时使用，0 表示用默认值
}

// anthropicDefaultMaxTokens 是请求与模型配置都没有给出最大输出时的 max_tokens（Anthropic 要求必填）。
const anthropicDefaultMaxTokens = 16384

// AnthropicMaxTokens 决定发往 Anthropic 的 max_tokens：请求值优先，其次模型配置，最后默认值。
// Kiro 不发送 maxTokens，所以通常由模型配置决定；budget 形态的思考预算按它夹紧。
func AnthropicMaxTokens(requested, configured int) int {
	switch {
	case requested > 0:
		return requested
	case configured > 0:
		return configured
	default:
		return anthropicDefaultMaxTokens
	}
}

// UpstreamHostOf 返回上游 URL 的小写 host（不含端口）。
// 解析失败返回空串：调用方据此不启用任何按 host 的规则，而不是猜测上游。
func UpstreamHostOf(upstreamURL string) string {
	u, err := url.Parse(strings.TrimSpace(upstreamURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
