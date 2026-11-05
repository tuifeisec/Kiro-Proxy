package proxy

import (
	"net/url"
	"strings"

	"kiro-proxy/store"
)

const officialOpenAIResponsesHost = "api.openai.com"

// isOfficialOpenAIStatelessResponses 判断 Kiro 主路径是否发往 store=false 的
// OpenAI 官方 Responses。只有官方 host 才请求 reasoning.encrypted_content 与
// reasoning.summary；自定义中转可能拒绝未知字段，保持原样。
func isOfficialOpenAIStatelessResponses(model *store.Model, upstreamURL string) bool {
	if model == nil || model.EnableContinuation || !isOpenAIResponsesUpstream(model, upstreamURL) {
		return false
	}
	parsed, err := url.Parse(upstreamURL)
	return err == nil && strings.EqualFold(parsed.Hostname(), officialOpenAIResponsesHost)
}
