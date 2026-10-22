package proxy

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"kiro-proxy/converter"
)

const autoActivityDiagnosticSampleLimit = 16

var autoDiagnosticRequestSequence atomic.Uint64

func (s *Server) recordAutoDiagnostic(event string, metadata map[string]interface{}) {
	if s.logger == nil {
		return
	}
	if err := s.logger.AutoDiagnostic(event, metadata); err != nil {
		s.logger.Error("auto-diagnostic", err.Error())
	}
}

func autoDiagnosticRequestKey(pre *Preflight) string {
	if pre.autoDiagnosticRequestID == "" {
		pre.autoDiagnosticRequestID = fmt.Sprintf("%x-%x", time.Now().UnixNano(), autoDiagnosticRequestSequence.Add(1))
	}
	return pre.autoDiagnosticRequestID
}

// 请求结果只记录失败阶段或上游响应头；不提前读取响应，也不把头部到达当成流完成。
func (s *Server) logAutoRequestResult(pre *Preflight, phase string, status int) {
	if s.logger == nil || pre == nil || pre.Conv == nil || pre.IsClassifier || !strings.EqualFold(pre.SourceModel, "auto") {
		return
	}
	metadata := map[string]interface{}{
		"request_id": autoDiagnosticRequestKey(pre), "phase": phase, "status": status,
		"conversation_key": autoDiagnosticKey(pre.Conv.ConversationID),
		"continuation_key": autoDiagnosticKey(pre.Conv.ContinuationStateKey),
	}
	if !pre.StartTime.IsZero() {
		metadata["elapsed_ms"] = max(int64(0), time.Since(pre.StartTime).Milliseconds())
	}
	s.recordAutoDiagnostic("request_result", metadata)
}

// 记录已有解析与状态提交的结果，不额外解析正文或改变状态。
func (s *Server) logKiroActivityBatch(report kiroActivityParseReport, parseErr, consumeErr error) {
	if s.logger == nil {
		return
	}
	categories := make([]string, 0, len(report.Categories))
	for _, category := range report.Categories {
		categories = append(categories, string(category))
	}
	sort.Strings(categories)
	entries := append([]kiroActivityRoutingMeta(nil), report.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].SessionID < entries[j].SessionID })
	samples := make([]map[string]interface{}, 0, min(len(entries), autoActivityDiagnosticSampleLimit))
	for _, entry := range entries[:min(len(entries), autoActivityDiagnosticSampleLimit)] {
		samples = append(samples, map[string]interface{}{
			"conversation_key": autoDiagnosticKey(entry.SessionID),
			"workflow_key":     autoDiagnosticKey(entry.WorkflowID),
			"execution_key":    autoDiagnosticKey(entry.ExecutionID),
			"node_id":          entry.NodeID, "node_path": entry.NodePath,
			"agent_mode": entry.AgentMode, "sequence": entry.Sequence, "has_sequence": entry.HasSequence,
		})
	}
	metadata := map[string]interface{}{
		"parse_ok": parseErr == nil, "consume_ok": consumeErr == nil,
		"categories": categories, "candidate_sessions": len(report.Entries),
		"committed_sessions": report.Committed, "store_ignored_sessions": report.StoreIgnored,
		"candidate_samples": samples, "candidate_samples_omitted": max(0, len(entries)-len(samples)),
	}
	if parseErr != nil {
		metadata["parse_error_category"] = string(kiroActivityErrorCategory(parseErr))
	}
	s.recordAutoDiagnostic("activity_batch", metadata)
}

// 查找事件仅说明当时是否可合成头，不宣称 continuation 最终采用了该观察。
func (s *Server) logKiroActivityLookup(conv *converter.ParsedConversation, status string, meta kiroActivityRoutingMeta) {
	if s.logger == nil || conv == nil {
		return
	}
	metadata := map[string]interface{}{
		"conversation_key": autoDiagnosticKey(conv.ConversationID),
		"continuation_key": autoDiagnosticKey(conv.ContinuationStateKey),
		"status":           status,
	}
	if status != "no_observation" {
		metadata["workflow_key"] = autoDiagnosticKey(meta.WorkflowID)
		metadata["sequence"] = meta.Sequence
		metadata["has_sequence"] = meta.HasSequence
		if !meta.UpdatedAt.IsZero() {
			metadata["observation_age_ms"] = max(int64(0), time.Since(meta.UpdatedAt).Milliseconds())
		}
	}
	s.recordAutoDiagnostic("activity_lookup", metadata)
}
