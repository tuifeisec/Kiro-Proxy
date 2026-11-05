package converter

import (
	"io"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

func sseEvents(data ...string) string {
	var b strings.Builder
	for _, d := range data {
		b.WriteString("data: ")
		b.WriteString(d)
		b.WriteString("\n\n")
	}
	return b.String()
}

func runOpenAIFrames(t *testing.T, sse string) (*OpenAIStreamConverter, []decodedFrame) {
	t.Helper()
	sc := NewOpenAIStreamConverter(io.NopCloser(strings.NewReader(sse)), "model", 200000, 0, 0, 0, 0, 1)
	out, err := io.ReadAll(sc)
	if err != nil {
		t.Fatalf("read converted stream: %v", err)
	}
	return sc, decodeFrames(t, out)
}

const toolCallChunk = `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`

func TestOpenAIStreamKeepsChunkWithPartsContentAndToolCall(t *testing.T) {
	sse := sseEvents(
		`{"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"hm"}]},{"type":"text","text":"o"}]}}]}`,
		`{"choices":[{"delta":{"content":[{"type":"text","text":"k"}],"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	)
	sc, frames := runOpenAIFrames(t, sse)
	if got := concatText(frames); got != "ok" {
		t.Fatalf("content = %q, want ok", got)
	}
	if countEvent(frames, "toolUseEvent") != 3 || sc.Outcome() != StreamOutcomeCompleted {
		t.Fatalf("tool frames=%d outcome=%s: %+v", countEvent(frames, "toolUseEvent"), sc.Outcome(), frames)
	}
}

func TestOpenAIStreamCapturesReasoningDetailsAndObjects(t *testing.T) {
	sse := sseEvents(
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","text":"check ","index":0}]}}]}`,
		`{"choices":[{"delta":{"reasoning":{"text":"the file"}}}]}`,
		toolCallChunk,
		`[DONE]`,
	)
	sc, _ := runOpenAIFrames(t, sse)
	trace := sc.ReasoningTrace()
	if trace.Text != "check the file" || trace.WireField != "reasoning_details" {
		t.Fatalf("trace = %+v", trace)
	}
}

func TestOpenAIStreamThinkTagAcrossChunks(t *testing.T) {
	// 分片方式参照 magpie internal/gateway/minimax_think_test.go 记录的 MiniMax Chat 流
	sse := sseEvents(
		`{"choices":[{"index":0,"delta":{"content":"<th","role":"assistant","name":"MiniMax AI","audio_content":""}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"ink>\nread it first","role":"assistant"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":".\n</thi","role":"assistant"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"nk>\n\nReading.","role":"assistant"}}]}`,
		toolCallChunk,
		`[DONE]`,
	)
	sc, frames := runOpenAIFrames(t, sse)
	if got := concatText(frames); got != "Reading." {
		t.Fatalf("visible content = %q, want Reading.", got)
	}
	trace := sc.ReasoningTrace()
	if trace.Text != "\nread it first.\n" || trace.WireField != ThinkTagWireField {
		t.Fatalf("trace = %+v", trace)
	}
	if !textFrameBeforeTool(frames) {
		t.Fatalf("content frame must precede tool frames: %+v", frames)
	}
}

func TestOpenAIStreamUnclosedThinkKeptAsReasoning(t *testing.T) {
	sse := sseEvents(
		`{"choices":[{"delta":{"content":"<think>still thinking"}}]}`,
		`{"choices":[{"delta":{"content":" when the call came </th"}}]}`,
		toolCallChunk,
		`[DONE]`,
	)
	sc, frames := runOpenAIFrames(t, sse)
	if got := concatText(frames); got != "" {
		t.Fatalf("unclosed think leaked into content: %q", got)
	}
	if trace := sc.ReasoningTrace(); trace.Text != "still thinking when the call came </th" {
		t.Fatalf("trace = %+v", trace)
	}
}

func TestOpenAIStreamThinkWithoutFinishReasonFlushedAtDone(t *testing.T) {
	_, frames := runOpenAIFrames(t, sseEvents(`{"choices":[{"delta":{"content":"<th"}}]}`, `[DONE]`))
	if got := concatText(frames); got != "<th" {
		t.Fatalf("held lead was not flushed at [DONE]: %q", got)
	}
	if countEvent(frames, "metadataEvent") != 1 {
		t.Fatalf("missing terminal frames: %+v", frames)
	}
}

func TestOpenAIStreamIndependentReasoningKeepsThinkTagInContent(t *testing.T) {
	sse := sseEvents(
		`{"choices":[{"delta":{"reasoning_content":"real reasoning"}}]}`,
		`{"choices":[{"delta":{"content":"<think>literal</think> text"}}]}`,
		toolCallChunk,
		`[DONE]`,
	)
	sc, frames := runOpenAIFrames(t, sse)
	if got := concatText(frames); got != "<think>literal</think> text" {
		t.Fatalf("content = %q", got)
	}
	if trace := sc.ReasoningTrace(); trace.Text != "real reasoning" || trace.WireField != "reasoning_content" {
		t.Fatalf("trace = %+v", trace)
	}
}

func TestOpenAIStreamUnparseableEventFailsFast(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "invalid JSON", data: `{"choices":[{"delta":{"content":"cut`},
		{name: "plain text", data: `upstream overloaded`},
		{name: "content number", data: `{"choices":[{"delta":{"content":42}}]}`},
		{name: "choices object", data: `{"choices":{"delta":{}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sse := sseEvents(`{"choices":[{"delta":{"content":"partial"}}]}`, tt.data, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
			sc, frames := runOpenAIFrames(t, sse)
			assertControlledFailure(t, frames)
			if sc.Outcome() != StreamOutcomeUpstreamProtocolFailure {
				t.Fatalf("outcome = %s", sc.Outcome())
			}
			message := exceptionMessage(t, frames)
			if !strings.Contains(message, "无法解析上游 Chat 流事件") {
				t.Fatalf("exception message = %q", message)
			}
			if err := sc.Error(); err == nil || !strings.Contains(err.Error(), "无法解析上游 Chat 流事件") {
				t.Fatalf("Error() = %v", err)
			}
		})
	}
}

func TestOpenAIStreamIgnoresCommentsBlankLinesAndNullError(t *testing.T) {
	sse := ": keep-alive\n\n\n\ndata: {\"error\":null,\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n: trailing comment\n\ndata: [DONE]\n\n"
	sc, frames := runOpenAIFrames(t, sse)
	if sc.Outcome() != StreamOutcomeCompleted || concatText(frames) != "ok" {
		t.Fatalf("outcome=%s content=%q frames=%+v", sc.Outcome(), concatText(frames), frames)
	}
}

func exceptionMessage(t *testing.T, frames []decodedFrame) string {
	t.Helper()
	for _, f := range frames {
		if f.eventType != "exceptionEvent" {
			continue
		}
		var payload struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(f.payload), &payload); err != nil {
			t.Fatalf("decode exception payload: %v", err)
		}
		return payload.Message
	}
	t.Fatalf("no exceptionEvent in %+v", frames)
	return ""
}

func textFrameBeforeTool(frames []decodedFrame) bool {
	sawText := false
	for _, f := range frames {
		switch f.eventType {
		case "assistantResponseEvent":
			sawText = true
		case "toolUseEvent":
			return sawText
		}
	}
	return false
}
