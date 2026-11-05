package proxy

import (
	"fmt"
	"net/http"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/store"
)

// 每个请求每类最多重试一次；总次数再设上限，避免多条规则串联拖长首字延迟。
const compatMaxRetries = 3

type compatFinding struct {
	kind     modelCapabilityKind
	value    string
	evidence string
}

// compatSender 是兼容重试的发送方：send 发送改写后的 body，budget 返回剩余重试预算是否可用。
type compatSender struct {
	send   func(body []byte) (*http.Response, error)
	budget func() bool
}

// compatSession 管理单个上游请求的兼容改写：发送前应用已学形态；遇到可识别的 400/422
// 改写后重试；只有重试最终成功才记录观测。所有应用、重试与学习都写日志并追加 dispatch reason。
type compatSession struct {
	caps    *modelCapabilityStore
	model   *store.Model
	target  compatTarget
	logger  *TrafficLogger
	usage   *requestUsageRecorder
	retried map[modelCapabilityKind]bool
	pending []compatFinding
	now     func() time.Time
}

func newCompatSession(caps *modelCapabilityStore, model *store.Model, protocol compatProtocol, upstreamURL string, logger *TrafficLogger, usage *requestUsageRecorder) *compatSession {
	if model == nil {
		return nil
	}
	target := compatTarget{protocol: protocol, modelID: model.ModelID, host: converter.UpstreamHostOf(upstreamURL)}
	return &compatSession{caps: caps, model: model, target: target, logger: logger, usage: usage, retried: make(map[modelCapabilityKind]bool), now: time.Now}
}

func isCompatRetryStatus(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusUnprocessableEntity
}

// applyLearned 在发送前按 TTL 内的观测改写 body；配置已决定的能力不应用。
func (s *compatSession) applyLearned(body []byte) ([]byte, error) {
	if s == nil {
		return body, nil
	}
	for _, rule := range compatRules {
		if !rule.appliesTo(s.target.protocol) {
			continue
		}
		observation, ok := s.caps.lookup(s.model, s.target.protocol, rule.kind, s.now())
		if !ok {
			continue
		}
		updated, changed, err := rule.apply(s.target, body, observation.value)
		if err != nil {
			return nil, fmt.Errorf("应用已学兼容形态 %s 失败: %w", rule.kind, err)
		}
		if !changed {
			continue
		}
		body = updated
		s.syncWireEffort(rule, body)
		s.warn("应用已学兼容形态", compatFinding{kind: rule.kind, value: observation.value, evidence: observation.evidence}, 0)
		appendCompatDispatchReason(s.usage, "compat_applied:"+string(rule.kind)+"="+observation.value)
	}
	return body, nil
}

// retry 处理可识别的格式类 400/422：改写后重试，规则无法修复或重试仍失败时透传最后一次上游响应。
func (s *compatSession) retry(resp *http.Response, body []byte, sender compatSender) (*http.Response, error) {
	if s == nil || resp == nil {
		return resp, nil
	}
	for attempts := 0; attempts < compatMaxRetries && isCompatRetryStatus(resp.StatusCode); attempts++ {
		errBody := peekUpstreamErrorBody(resp)
		rule, finding, ok := s.match(resp.StatusCode, errBody, body)
		if !ok {
			break
		}
		updated, changed, err := rule.apply(s.target, body, finding.value)
		if err != nil || !changed {
			if err != nil {
				s.warn(fmt.Sprintf("兼容改写失败，透传上游错误: %v", err), finding, resp.StatusCode)
			}
			break
		}
		if sender.budget != nil && !sender.budget() {
			s.warn("兼容重试预算已耗尽，透传上游错误", finding, resp.StatusCode)
			break
		}
		s.retried[finding.kind] = true
		s.pending = append(s.pending, finding)
		s.warn("上游拒绝请求形态，改写后重试一次", finding, resp.StatusCode)
		appendCompatDispatchReason(s.usage, "compat_retry:"+string(finding.kind)+"="+finding.value)
		_ = resp.Body.Close()
		body = updated
		s.syncWireEffort(rule, body)
		next, sendErr := sender.send(body)
		if sendErr != nil {
			return nil, sendErr
		}
		resp = next
	}
	s.finish(resp)
	return resp, nil
}

// finish 在重试链结束时调用：成功才记录观测；仍失败则记录日志并透传最后一次上游错误。
func (s *compatSession) finish(resp *http.Response) {
	if s == nil || len(s.pending) == 0 || resp == nil {
		return
	}
	if resp.StatusCode < http.StatusBadRequest {
		s.commit()
		return
	}
	for _, finding := range s.pending {
		s.warn("兼容重试仍失败，透传最后一次上游错误", finding, resp.StatusCode)
	}
	appendCompatDispatchReason(s.usage, fmt.Sprintf("compat_retry_failed:http_%d", resp.StatusCode))
	s.pending = nil
}

func (s *compatSession) commit() {
	for _, finding := range s.pending {
		if s.caps.observe(s.model, s.target.protocol, finding.kind, finding.value, finding.evidence, s.now()) {
			s.warn("兼容重试成功，已记忆该模型的请求形态", finding, 0)
			appendCompatDispatchReason(s.usage, "compat_learned:"+string(finding.kind)+"="+finding.value)
		}
	}
	s.pending = nil
}

// match 返回第一条命中的规则；能力已由配置决定时不学习，记录日志后继续检查其他规则。
func (s *compatSession) match(status int, errBody, body []byte) (compatRule, compatFinding, bool) {
	refusal := newCompatRefusal(status, errBody)
	for _, rule := range compatRules {
		if !rule.appliesTo(s.target.protocol) || s.retried[rule.kind] {
			continue
		}
		value, evidence, ok := rule.recognize(s.target, refusal, body)
		if !ok {
			continue
		}
		finding := compatFinding{kind: rule.kind, value: value, evidence: evidence}
		if s.caps.configuredBy(s.model, rule.kind) {
			s.retried[rule.kind] = true
			s.warn("能力已由模型配置决定，不自动改写，透传上游错误", finding, status)
			continue
		}
		return rule, finding, true
	}
	return compatRule{}, compatFinding{}, false
}

// syncWireEffort 在规则改变了实际下发档位时同步用量记录；requested 与 source 保持不变。
func (s *compatSession) syncWireEffort(rule compatRule, body []byte) {
	if rule.wireEffort != nil {
		setUsageWireReasoningEffort(s.usage, rule.wireEffort(s.target, body))
	}
}

func (s *compatSession) warn(message string, finding compatFinding, status int) {
	if s.logger == nil {
		return
	}
	s.logger.WarnWithMeta("compat", fmt.Sprintf("%s: model=%s protocol=%s kind=%s value=%s", message, statsModelID(s.model), s.target.protocol, finding.kind, finding.value), LogMeta{
		Model:  statsModelID(s.model),
		Status: status,
		Metadata: map[string]interface{}{
			"compat_kind":     string(finding.kind),
			"compat_value":    finding.value,
			"compat_evidence": finding.evidence,
			"protocol":        string(s.target.protocol),
			"model_config_id": s.model.ID,
		},
	})
}
