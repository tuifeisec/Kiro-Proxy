package proxy

import (
	"strings"

	"kiro-proxy/converter"
	"kiro-proxy/store"
)

// chatUpstreamHost 返回模型配置指向的上游 host，供按 host 选择 Chat 请求字段。
// URL 拼接规则与 convertRequestWithProtocol、rawResponsesUpstreamURL 一致；
// 端点路径的改写（/responses ↔ /chat/completions）不改变 host，因此这里只看 BaseURL 与 Endpoint。
func chatUpstreamHost(model *store.Model) string {
	if model == nil {
		return ""
	}
	upstreamURL := buildUpstreamURL(strings.TrimRight(model.BaseURL, "/"), strings.TrimSpace(model.Endpoint))
	if model.DisableAutoSuffix {
		upstreamURL = strings.TrimSpace(model.BaseURL)
	}
	return converter.UpstreamHostOf(upstreamURL)
}
