package proxy

import (
	"fmt"

	"kiro-proxy/converter"
	"kiro-proxy/store"
)

// reasoningEffortSource 标记请求档位来自哪一层配置，写入用量记录和日志。
type reasoningEffortSource string

const (
	reasoningEffortSourceAuto       reasoningEffortSource = "auto"
	reasoningEffortSourceRequest    reasoningEffortSource = "request"
	reasoningEffortSourceGroup      reasoningEffortSource = "group"
	reasoningEffortSourceModel      reasoningEffortSource = "model"
	reasoningEffortSourceClassifier reasoningEffortSource = "classifier"
)

// reasoningEffortDecision 区分"按优先级得到的档位"和"实际下发给上游的档位"。
// Wire 为空表示不向上游发送任何推理字段。
type reasoningEffortDecision struct {
	Requested     string
	Wire          string
	Source        reasoningEffortSource
	FieldDisabled bool // 模型开启 disable_reasoning_field，客户端带来的推理字段也要移除
}

// resolveReasoningEffortDecision 按 Auto 固定 > 请求 > 分组 > 模型默认 的优先级得到
// requested，再按该模型有效档位就近映射得到 wire。classifier 不使用前三层：
// 模型默认档位为空时不发送，非空时取有效档位中最低的一档。
func resolveReasoningEffortDecision(pre *Preflight, model *store.Model) (reasoningEffortDecision, error) {
	requestEffort := ""
	if pre != nil && pre.Conv != nil {
		requestEffort = pre.Conv.ReasoningEffort
	}
	return resolveReasoningEffortDecisionWithRequest(pre, model, requestEffort)
}

// resolveReasoningEffortDecisionWithRequest 允许 raw Responses 路径直接传入请求体中的
// reasoning.effort；生产路径中它与解析得到的 Conv.ReasoningEffort 相同。
func resolveReasoningEffortDecisionWithRequest(pre *Preflight, model *store.Model, requestEffort string) (reasoningEffortDecision, error) {
	var decision reasoningEffortDecision
	classifier := pre != nil && pre.IsClassifier
	switch {
	case preIsAutoReasoningRequest(pre) && !classifier && pre.AutoReasoningEffort != nil:
		decision.Requested, decision.Source = *pre.AutoReasoningEffort, reasoningEffortSourceAuto
	case !classifier && requestEffort != "":
		decision.Requested, decision.Source = requestEffort, reasoningEffortSourceRequest
	case pre != nil && pre.Conv != nil && !classifier && pre.GroupReasoningEffort != "":
		decision.Requested, decision.Source = pre.GroupReasoningEffort, reasoningEffortSourceGroup
	}
	if model == nil {
		// 没有具体候选时无法确定下发档位；转发前会按实际候选重新解析。
		return decision, nil
	}
	decision.FieldDisabled = model.DisableReasoningField
	allowed, err := converter.ModelReasoningEfforts(model.Provider, model.SupportedReasoningEfforts)
	if err != nil {
		return decision, fmt.Errorf("模型 %s 的支持思考档位无效: %w", statsModelID(model), err)
	}
	if decision.Source == "" {
		defaultEffort, err := converter.NormalizeReasoningEffort(model.ReasoningEffort)
		if err != nil {
			return decision, fmt.Errorf("模型 %s 的默认思考档位无效: %w", statsModelID(model), err)
		}
		switch {
		case defaultEffort == "":
		case classifier:
			decision.Requested, decision.Source = converter.LowestReasoningEffort(allowed), reasoningEffortSourceClassifier
		default:
			decision.Requested, decision.Source = defaultEffort, reasoningEffortSourceModel
		}
	}
	requested, err := converter.NormalizeReasoningEffort(decision.Requested)
	if err != nil {
		return decision, fmt.Errorf("%s 思考档位无效: %w", decision.Source, err)
	}
	decision.Requested = requested
	if decision.FieldDisabled || requested == "" {
		return decision, nil
	}
	wire, err := converter.ModelWireReasoningEffort(model.Provider, model.SupportedReasoningEfforts, false, requested)
	if err != nil {
		return decision, fmt.Errorf("模型 %s: %w", statsModelID(model), err)
	}
	decision.Wire = wire
	return decision, nil
}

// bindUsageReasoningEffort 记录用量的请求/下发档位。解析失败时只记录 requested 与
// source，wire 留空；同一错误会在随后的请求转换中返回，不会被当作成功发送。
func bindUsageReasoningEffort(usage *requestUsageRecorder, pre *Preflight, model *store.Model) {
	decision, _ := resolveReasoningEffortDecision(pre, model)
	usage.setReasoningEffortDecision(decision)
}

// reasoningEffortLogMeta 返回思考档位决策的日志字段；无任何推理相关信息时返回 nil。
func reasoningEffortLogMeta(decision reasoningEffortDecision) map[string]interface{} {
	if decision.Requested == "" && decision.Wire == "" && !decision.FieldDisabled {
		return nil
	}
	return map[string]interface{}{
		"reasoning_effort_requested":      decision.Requested,
		"reasoning_effort_wire":           decision.Wire,
		"reasoning_effort_source":         string(decision.Source),
		"reasoning_effort_field_disabled": decision.FieldDisabled,
	}
}

// logCandidateReasoningEffort 在请求体准备成功后记录档位决策。准备阶段已用同一
// 输入解析过档位并会返回其错误，因此这里只在成功时记录；直通协议（无 Conv）不经过
// 档位解析，不记录以免误报下发档位。
func logCandidateReasoningEffort(logger *TrafficLogger, pre *Preflight, model *store.Model) {
	if pre == nil || pre.Conv == nil {
		return
	}
	if decision, err := resolveReasoningEffortDecision(pre, model); err == nil {
		logReasoningEffortDecision(logger, model, decision)
	}
}

// logReasoningEffortDecision 在每次候选发送前记录一次档位决策，使就近映射和
// 不发送推理字段的情况在日志详情中可见。
func logReasoningEffortDecision(logger *TrafficLogger, model *store.Model, decision reasoningEffortDecision) {
	meta := reasoningEffortLogMeta(decision)
	if logger == nil || model == nil || meta == nil {
		return
	}
	message := fmt.Sprintf("思考档位: requested=%s wire=%s source=%s", decision.Requested, displayWireEffort(decision), decision.Source)
	if decision.Requested != decision.Wire {
		message += "（已按模型有效档位调整）"
	}
	logger.InfoWithMeta("forwarder", message, LogMeta{Model: statsModelID(model), Metadata: meta})
}

func displayWireEffort(decision reasoningEffortDecision) string {
	if decision.Wire != "" {
		return decision.Wire
	}
	if decision.FieldDisabled {
		return "(不发送:已禁用推理字段)"
	}
	return "(不发送)"
}
