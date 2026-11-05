package proxy

import (
	"net/http"
	"regexp"
)

// 格式类不兼容：上游拒绝的是请求形态（字段、取值、角色），换一个成员可能接受同一请求。
// 判定宁可漏判不可误判：必须命中格式信号；"invalid value"、"not supported by this model" 等宽泛措辞
// 还须涉及推理/思考/采样/长度/角色/工具等字段；上下文超长、内容安全、鉴权、额度、模型不存在一律排除。
// 流式解析失败（"无法解析上游 Chat/Responses 流事件"）发生在 200 之后、以 UpstreamProtocolFailure 终止，
// 只判 400/422 已把它排除；其样本可能含任意上游文本，这里再显式排除一次。
var (
	formatRefusalSignal = regexp.MustCompile(`(?i)unsupported[ _]parameter|unknown[ _](?:parameter|field|name|argument|variant)|unrecogni[sz]ed (?:request )?(?:argument|parameter|field)|extra inputs are not permitted|extra_forbidden|additional properties are not allowed|cannot find field`)
	formatValueSignal   = regexp.MustCompile(`(?i)invalid[ _]value|unsupported[ _]value|is not one of|must be one of|expected one of|not supported (?:by|for|with) (?:this|the) model|does not support|is deprecated for this model`)
	formatFieldWords    = regexp.MustCompile(`(?i)reasoning|thinking|effort|max_(?:completion_|output_)?tokens|temperature|top_p|top_k|\brole\b|tool|response_format|output_config|previous_response_id|stream_options|parallel_tool_calls|service_tier`)
	formatExcluded      = regexp.MustCompile(`(?i)context[ _](?:length|window|limit)|maximum context|too many tokens|prompt is too long|input is too long|token limit|content[ _](?:policy|filter|management)|safety|moderation|flagged|unauthori[sz]ed|authentication|invalid[ _]api[ _]?key|api key|permission|\bforbidden\b|access denied|quota|billing|insufficient|balance|credit|rate[ _]limit|model[_ ]not[_ ]found|no such model|unknown model|invalid model|unsupported model|model\W{0,12}(?:does not exist|not found|is not supported|is not available)|余额|额度|欠费|上下文|无法解析上游`)
)

// isFormatIncompatible 判断 400/422 是否为格式类不兼容，返回命中的证据片段。
func isFormatIncompatible(status int, body []byte) (string, bool) {
	if !isCompatRetryStatus(status) || len(body) == 0 {
		return "", false
	}
	text := compatErrorText(body)
	if formatExcluded.MatchString(text) {
		return "", false
	}
	if evidence, ok := compatMatch(formatRefusalSignal, text); ok {
		return evidence, true
	}
	if !formatFieldWords.MatchString(text) {
		return "", false
	}
	return compatMatch(formatValueSignal, text)
}

// formatIncompatibleResponse 在不改变正文与 ContentLength 的前提下判定响应。
func formatIncompatibleResponse(resp *http.Response) (string, bool) {
	if resp == nil || !isCompatRetryStatus(resp.StatusCode) {
		return "", false
	}
	return isFormatIncompatible(resp.StatusCode, peekUpstreamErrorBody(resp))
}
