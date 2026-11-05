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
