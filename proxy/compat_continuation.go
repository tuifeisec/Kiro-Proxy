package proxy

import "regexp"

const compatContinuationDisabled = "disabled"

// 上游根本不支持 previous_response_id（参数不被识别），与"前次响应不存在或过期"不同：
// 后者是会话状态问题，回退全量即可，不应记成模型能力。样本按 OpenAI 参数拒绝的常见措辞构造，需实测。
var continuationRefused = regexp.MustCompile("(?i)(?:unsupported|unknown|unrecogni[sz]ed|unexpected)\\s+(?:request\\s+)?(?:parameter|argument|field|property|name|key)s?(?:\\s+supplied)?\\W{0,4}previous_response_id\\b|\\bprevious_response_id['\"`]?\\W{0,4}(?:is not supported|is unsupported|extra inputs are not permitted)")

// continuationDisabled 判断该模型在 TTL 内是否已确认不支持续传；命中时记录日志与 dispatch reason。
func (s *compatSession) continuationDisabled() bool {
	if s == nil {
		return false
	}
	observation, ok := s.caps.lookup(s.model, s.target.protocol, capabilityContinuation, s.now())
	if !ok {
		return false
	}
	s.warn("应用已学兼容形态：该模型不使用续传", compatFinding{kind: capabilityContinuation, value: observation.value, evidence: observation.evidence}, 0)
	appendCompatDispatchReason(s.usage, "compat_applied:"+string(capabilityContinuation)+"="+observation.value)
	return true
}

// noteContinuationRefusal 在续传请求被拒、即将回退全量时调用：只有回退后的请求成功才记录观测。
func (s *compatSession) noteContinuationRefusal(status int, errBody, sentBody []byte) {
	if s == nil || s.retried[capabilityContinuation] || !compatHasField(sentBody, "previous_response_id") {
		return
	}
	evidence, ok := compatMatch(continuationRefused, compatErrorText(errBody))
	if !ok {
		return
	}
	finding := compatFinding{kind: capabilityContinuation, value: compatContinuationDisabled, evidence: evidence}
	s.retried[capabilityContinuation] = true
	s.pending = append(s.pending, finding)
	s.warn("上游不支持续传参数，回退全量请求", finding, status)
	appendCompatDispatchReason(s.usage, "compat_retry:"+string(capabilityContinuation))
}
