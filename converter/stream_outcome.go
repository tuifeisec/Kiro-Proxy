package converter

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// StreamOutcome 表示上游流结束时的终态。
type StreamOutcome string

const (
	StreamOutcomePending                 StreamOutcome = "pending"
	StreamOutcomeCompleted               StreamOutcome = "completed"
	StreamOutcomeUpstreamProtocolFailure StreamOutcome = "upstream_protocol_failure"
	StreamOutcomeMissingTerminal         StreamOutcome = "missing_terminal"
	StreamOutcomeReadFailure             StreamOutcome = "read_failure"
	StreamOutcomeClientCanceled          StreamOutcome = "client_canceled"
)

func streamFailureMessage(outcome StreamOutcome) string {
	switch outcome {
	case StreamOutcomeUpstreamProtocolFailure:
		return "上游返回流式协议错误"
	case StreamOutcomeMissingTerminal:
		return "上游流式响应在完成事件前中断"
	case StreamOutcomeReadFailure:
		return "读取上游流式响应时连接中断"
	case StreamOutcomeClientCanceled:
		return "下游客户端已取消流式响应"
	default:
		return "上游流式响应中断"
	}
}

func streamOutcomeError(outcome StreamOutcome) error {
	if outcome == StreamOutcomePending || outcome == StreamOutcomeCompleted {
		return nil
	}
	return fmt.Errorf("%s: %s", outcome, streamFailureMessage(outcome))
}

func streamOutcomeFromReadError(err error) StreamOutcome {
	switch {
	case errors.Is(err, context.Canceled):
		return StreamOutcomeClientCanceled
	case err != nil && !errors.Is(err, io.EOF):
		return StreamOutcomeReadFailure
	default:
		return StreamOutcomeMissingTerminal
	}
}
