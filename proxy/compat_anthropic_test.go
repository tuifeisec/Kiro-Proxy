package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"
)

const compatAnthropicSuccessSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestForwardKiroAnthropicLearnsAlwaysThinkingModel(t *testing.T) {
	upstream := &compatRecordingUpstream{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, body := upstream.record(r)
		if compatThinkingType(body) == "disabled" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			// magpie anthropic.go alwaysThinks 记录的 Z.ai GLM 报错。
			_, _ = io.WriteString(w, `{"error":{"code":"1210","message":"The model always engages in thinking and the thinking cannot be disabled"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, compatAnthropicSuccessSSE)
	}))
	defer server.Close()
	db := rawResponsesTestDB(t)
	f, _ := rawResponsesTestForwarder(t, db)
	model := &store.Model{ID: "glm", Name: "glm", ModelID: "glm-5.1", Provider: "anthropic", BaseURL: server.URL, Endpoint: "/v1/messages", APIKey: "test", Enabled: true, UpdatedAt: "2026-10-01T00:00:00Z"}
	route := &router.Route{Model: model, Candidates: []*store.Model{model}}
	forward := func(conversationID string) *Preflight {
		body := []byte(`{"conversationState":{"conversationId":"` + conversationID + `","currentMessage":{"userInputMessage":{"content":"hello","modelId":"m"}}},"inferenceConfig":{"maxTokens":4096},"additionalModelRequestFields":{"reasoningEffort":"none"}}`)
		conv, err := converter.ParseKiroRequest(body)
		if err != nil {
			t.Fatal(err)
		}
		pre := &Preflight{Body: body, Conv: conv, Protocol: "kiro", StartTime: time.Now(), ConversationID: conversationID}
		pre.usage = newRequestUsageRecorder(db, pre.StartTime, nil, "/", "kiro")
		compatForward(t, f, pre, route)
		return pre
	}

	first := forward("glm-1")
	calls := upstream.calls("glm-5.1")
	if len(calls) != 2 || compatThinkingType(calls[1]) != "enabled" || strings.Contains(string(calls[1]), `"temperature"`) {
		t.Fatalf("first request should retry once with thinking enabled: calls=%d", len(calls))
	}
	item := compatUsageItem(first.usage)
	if item.ReasoningEffort != "low" || item.RequestedReasoningEffort != "none" || item.ReasoningEffortSource != "request" || !strings.Contains(item.DispatchReason, "compat_learned:thinking_enabled=enabled") {
		t.Fatalf("first usage = wire %q requested %q source %q reason %q", item.ReasoningEffort, item.RequestedReasoningEffort, item.ReasoningEffortSource, item.DispatchReason)
	}

	second := forward("glm-2")
	if calls := upstream.calls("glm-5.1"); len(calls) != 3 || compatThinkingType(calls[2]) != "enabled" {
		t.Fatalf("learned thinking shape was not applied before send: calls=%d", len(calls))
	}
	if item := compatUsageItem(second.usage); item.ReasoningEffort != "low" || item.RequestedReasoningEffort != "none" || !strings.Contains(item.DispatchReason, "compat_applied:thinking_enabled=enabled") {
		t.Fatalf("second usage = wire %q requested %q reason %q", item.ReasoningEffort, item.RequestedReasoningEffort, item.DispatchReason)
	}
}
