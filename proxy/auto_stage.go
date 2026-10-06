package proxy

import (
	"context"
	"kiro-proxy/converter"
	"net/http"
)

type workflowStageContextKey struct{}

func withWorkflowStageHeaders(ctx context.Context, values []string) context.Context {
	return context.WithValue(ctx, workflowStageContextKey{}, append([]string(nil), values...))
}

func workflowStageHeaders(req *http.Request) []string {
	if values, ok := req.Context().Value(workflowStageContextKey{}).([]string); ok {
		return values
	}
	// parseRequest 的直接调用也支持接收；Forward 创建新请求，不复制私有头。
	values := req.Header.Values(converter.WorkflowStageHeader)
	req.Header.Del(converter.WorkflowStageHeader)
	return values
}

func workflowStageStatus(conv *converter.ParsedConversation) string {
	if !conv.WorkflowStageProvided {
		return "unavailable"
	}
	switch conv.WorkflowStage {
	case "run", "plan", "implement", "review", "finalize":
		return "recognized"
	default:
		return "unknown"
	}
}
