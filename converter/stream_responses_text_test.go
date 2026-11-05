package converter

import (
	"io"
	"strings"
	"testing"
)

func runResponsesFrames(t *testing.T, sse string) (*ResponsesStreamConverter, []decodedFrame) {
	t.Helper()
	sc := NewResponsesStreamConverter(io.NopCloser(strings.NewReader(sse)), "model", 200000, 0, 0, 0, 0, 1)
	out, err := io.ReadAll(sc)
	if err != nil {
		t.Fatalf("read converted stream: %v", err)
	}
	return sc, decodeFrames(t, out)
}

const (
	responsesFunctionCallEvents = "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"{}\"}}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"{}\"}}\n\n"
	responsesCompletedEvent = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n"
)

func responsesTextDelta(delta string) string {
	return "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":" + delta + "}\n\n"
}

func TestResponsesStreamUnparseableEventFailsFast(t *testing.T) {
	tests := []struct {
		name  string
		event string
	}{
		{name: "known event invalid JSON", event: "event: response.output_text.delta\ndata: {\"delta\":\"cut\n\n"},
		{name: "known event wrong delta type", event: responsesTextDelta(`{"text":"x"}`)},
		{name: "completed with wrong usage type", event: "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":\"many\"}}}\n\n"},
		{name: "unknown event not JSON", event: "event: response.vendor_ping\ndata: not json\n\n"},
		{name: "untyped event not JSON", event: "data: <html>bad gateway</html>\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc, frames := runResponsesFrames(t, responsesTextDelta(`"partial"`)+tt.event+responsesCompletedEvent)
			assertControlledFailure(t, frames)
			if sc.Outcome() != StreamOutcomeUpstreamProtocolFailure {
				t.Fatalf("outcome = %s", sc.Outcome())
			}
			if message := exceptionMessage(t, frames); !strings.Contains(message, "无法解析上游 Responses 流事件") {
				t.Fatalf("exception message = %q", message)
			}
			if err := sc.Error(); err == nil || !strings.Contains(err.Error(), "无法解析上游 Responses 流事件") {
				t.Fatalf("Error() = %v", err)
			}
		})
	}
}

func TestResponsesStreamIgnoresUnknownJSONEvents(t *testing.T) {
	sse := "event: response.vendor_ping\ndata: {\"type\":\"response.vendor_ping\"}\n\n" + responsesTextDelta(`"ok"`) + responsesCompletedEvent
	sc, frames := runResponsesFrames(t, sse)
	if sc.Outcome() != StreamOutcomeCompleted || concatText(frames) != "ok" {
		t.Fatalf("outcome=%s content=%q", sc.Outcome(), concatText(frames))
	}
}

func TestResponsesStreamSplitsLeadingThinkTag(t *testing.T) {
	sse := responsesTextDelta(`"\n<thi"`) + responsesTextDelta(`"nk>look first</th"`) + responsesTextDelta(`"ink>\nLooking."`) +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ignored\"}]}}\n\n" +
		responsesFunctionCallEvents + responsesCompletedEvent
	sc, frames := runResponsesFrames(t, sse)
	if got := concatText(frames); got != "Looking." {
		t.Fatalf("visible content = %q", got)
	}
	trace := sc.ReasoningTrace()
	if trace.Text != "look first" || trace.WireField != ThinkTagWireField {
		t.Fatalf("trace = %+v", trace)
	}
}

func TestResponsesStreamUnclosedThinkFlushedAtToolCall(t *testing.T) {
	sse := responsesTextDelta(`"<think>never closed"`) + responsesFunctionCallEvents + responsesCompletedEvent
	sc, frames := runResponsesFrames(t, sse)
	if got := concatText(frames); got != "" {
		t.Fatalf("unclosed think leaked into content: %q", got)
	}
	if trace := sc.ReasoningTrace(); trace.Text != "never closed" {
		t.Fatalf("trace = %+v", trace)
	}
}

func TestResponsesStreamIndependentReasoningKeepsThinkTag(t *testing.T) {
	sse := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\"}}\n\n" +
		"event: response.reasoning_text.delta\ndata: {\"type\":\"response.reasoning_text.delta\",\"item_id\":\"rs_1\",\"delta\":\"native\"}\n\n" +
		responsesTextDelta(`"<think>quoted</think>"`) + responsesFunctionCallEvents + responsesCompletedEvent
	sc, frames := runResponsesFrames(t, sse)
	if got := concatText(frames); got != "<think>quoted</think>" {
		t.Fatalf("content = %q", got)
	}
	if trace := sc.ReasoningTrace(); trace.Text != "native" || trace.WireField != "reasoning_text" {
		t.Fatalf("trace = %+v", trace)
	}
}
