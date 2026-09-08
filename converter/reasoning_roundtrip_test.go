package converter

import (
	"bytes"
	"io"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

func TestReasoningTurnKeyCanonicalizesToolCallIDs(t *testing.T) {
	first := ReasoningTurnKey([]string{"call_b", "call_a", "call_b"})
	second := ReasoningTurnKey([]string{"call_a", "call_b"})
	if first == "" || first != second {
		t.Fatalf("turn keys differ: %q != %q", first, second)
	}
}

func TestResponsesReasoningRoundTripBeforeToolCall(t *testing.T) {
	sse := `event: response.output_item.added
data: {"type":"response.output_item.added","item":{"id":"item_reasoning","type":"reasoning","status":"in_progress"}}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","item_id":"item_reasoning","summary_index":0,"delta":"need to inspect"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"id":"item_reasoning","type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"need to inspect"}]}}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"id":"item_call","type":"function_call","call_id":"call_1","name":"read_file","arguments":""}}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"id":"item_call","type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"a.go\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":10,"output_tokens":5}}}

`
	sc := NewResponsesStreamConverter(io.NopCloser(strings.NewReader(sse)), "deepseek", 1000, 0, 0, 0, 0, 1)
	_, _ = io.ReadAll(sc)
	trace := sc.ReasoningTrace()
	if trace.Text != "need to inspect" || trace.ItemID != "item_reasoning" || trace.Status != "completed" {
		t.Fatalf("captured trace = %+v", trace)
	}

	conv := &ParsedConversation{History: []ParsedHistoryEntry{
		{HasAssistant: true, ToolUses: []ParsedToolUse{{ToolUseID: "call_1", Name: "read_file", Input: json.RawMessage(`{"path":"a.go"}`)}}},
		{ToolResults: []ParsedToolResult{{ToolUseID: "call_1", Content: "package a"}}},
	}}
	turnKey := ReasoningTurnKey([]string{"call_1"})
	conv = WithReasoningHistory(conv, map[string]ReasoningTrace{turnKey: trace})
	body, err := KiroToResponses(conv, "deepseek", "high")
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Input) != 3 {
		t.Fatalf("input count = %d, body=%s", len(req.Input), body)
	}
	var reasoning struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Status  string `json:"status"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Summary []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(req.Input[0], &reasoning); err != nil {
		t.Fatal(err)
	}
	if reasoning.Type != "reasoning" || reasoning.ID != "item_reasoning" || reasoning.Status != "completed" ||
		len(reasoning.Content) != 0 || len(reasoning.Summary) != 1 || reasoning.Summary[0].Type != "summary_text" || reasoning.Summary[0].Text != trace.Text {
		t.Fatalf("reasoning item = %s", req.Input[0])
	}
	if !strings.Contains(string(req.Input[1]), `"type":"function_call"`) ||
		!strings.Contains(string(req.Input[2]), `"type":"function_call_output"`) {
		t.Fatalf("wrong item order: %s", body)
	}
}

func TestBuildResponsesReasoningBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		trace       *ReasoningTrace
		wantNil     bool
		wantSummary bool
	}{
		{name: "nil", trace: nil, wantNil: true},
		{name: "blank", trace: &ReasoningTrace{Text: "  \t\n"}, wantNil: true},
		{name: "summary", trace: &ReasoningTrace{Text: "inspect", ItemID: "item_summary", Status: "completed", WireField: "reasoning_summary_text"}, wantSummary: true},
		{name: "native reasoning text", trace: &ReasoningTrace{Text: "inspect native", ItemID: "item_native", Status: "completed", WireField: "reasoning_text"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildResponsesReasoning(tt.trace)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("builder result = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("builder returned nil")
			}
			if got.Type != "reasoning" || got.ID != tt.trace.ItemID || got.Status != tt.trace.Status {
				t.Fatalf("metadata = %+v, want id=%q status=%q", got, tt.trace.ItemID, tt.trace.Status)
			}
			if tt.wantSummary {
				if got.Content != nil {
					t.Fatalf("summary content = %#v, want nil", got.Content)
				}
				summary, ok := got.Summary.([]map[string]string)
				if !ok || len(summary) != 1 || summary[0]["type"] != "summary_text" || summary[0]["text"] != tt.trace.Text {
					t.Fatalf("summary = %#v", got.Summary)
				}
				return
			}
			content, ok := got.Content.([]map[string]string)
			if !ok || len(content) != 1 || content[0]["type"] != "reasoning_text" || content[0]["text"] != tt.trace.Text {
				t.Fatalf("content = %#v", got.Content)
			}
			if got.Summary != nil {
				t.Fatalf("native summary = %#v, want nil", got.Summary)
			}
		})
	}
}

func TestOpenAIReasoningContentRoundTrip(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"inspect first\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	sc := runOpenAIPipeline(sse)
	trace := sc.ReasoningTrace()
	if trace.Text != "inspect first" || trace.WireField != "reasoning_content" {
		t.Fatalf("captured trace = %+v", trace)
	}
	msg := buildOpenAIAssistantMsg("", []ParsedToolUse{{ToolUseID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}}, &trace)
	body, _ := json.Marshal(msg)
	if !strings.Contains(string(body), `"reasoning_content":"inspect first"`) || strings.Contains(string(body), `"reasoning_text"`) {
		t.Fatalf("chat reasoning replay = %s", body)
	}
}

func TestOpenAIReasoningSurvivesSplitToolMetadata(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"inspect\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	sc := NewOpenAIStreamConverter(io.NopCloser(strings.NewReader(sse)), "deepseek", 1000, 0, 0, 0, 0, 1)
	out, err := io.ReadAll(sc)
	if err != nil {
		t.Fatal(err)
	}
	trace := sc.ReasoningTrace()
	if len(trace.ToolCallIDs) != 1 || trace.ToolCallIDs[0] != "call_1" {
		t.Fatalf("captured tool ids = %v", trace.ToolCallIDs)
	}
	wantMetadata := []byte(`"toolUseId":"call_1","name":"read_file"`)
	if count := bytes.Count(out, wantMetadata); count != 3 {
		t.Fatalf("complete tool metadata frames = %d, want 3", count)
	}
}

func TestResponsesReasoningCapturesCallIDFromDoneEvent(t *testing.T) {
	sse := `event: response.reasoning_text.delta
data: {"type":"response.reasoning_text.delta","item_id":"reasoning_1","content_index":0,"delta":"inspect"}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"id":"item_call","type":"function_call","name":"read_file"}}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"id":"item_call","type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1"}}

`
	sc := NewResponsesStreamConverter(io.NopCloser(strings.NewReader(sse)), "deepseek", 1000, 0, 0, 0, 0, 1)
	_, _ = io.ReadAll(sc)
	trace := sc.ReasoningTrace()
	if len(trace.ToolCallIDs) != 1 || trace.ToolCallIDs[0] != "call_1" {
		t.Fatalf("captured trace = %+v", trace)
	}
}

func TestAnthropicThinkingSignatureRoundTrip(t *testing.T) {
	sse := `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"inspect"}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed"}}

data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"read_file"}}

data: {"type":"content_block_stop","index":1}

data: {"type":"message_stop"}

`
	sc := NewAnthropicStreamConverter(io.NopCloser(strings.NewReader(sse)), "claude", 1000, 0, 0, 0, 0, 1)
	_, _ = io.ReadAll(sc)
	trace := sc.ReasoningTrace()
	if trace.Text != "inspect" || trace.Signature != "signed" {
		t.Fatalf("captured trace = %+v", trace)
	}
	msg := buildAnthropicAssistantMsg("", []ParsedToolUse{{ToolUseID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}}, &trace)
	body, _ := json.Marshal(msg)
	thinkingAt := strings.Index(string(body), `"type":"thinking"`)
	toolAt := strings.Index(string(body), `"type":"tool_use"`)
	if thinkingAt < 0 || toolAt < 0 || thinkingAt >= toolAt || !strings.Contains(string(body), `"signature":"signed"`) {
		t.Fatalf("anthropic reasoning replay = %s", body)
	}
}

func TestReasoningFieldsAreAbsentWithoutSidecar(t *testing.T) {
	conv := &ParsedConversation{History: []ParsedHistoryEntry{{
		HasAssistant: true,
		ToolUses:     []ParsedToolUse{{ToolUseID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}},
	}}}
	chat, _ := KiroToOpenAI(conv, "gpt-5.5", "")
	responses, _ := KiroToResponses(conv, "gpt-5.5", "")
	anthropic, _ := KiroToAnthropic(conv, "claude", "")
	for name, body := range map[string][]byte{"chat": chat, "responses": responses, "anthropic": anthropic} {
		if strings.Contains(string(body), "reasoning_content") || strings.Contains(string(body), "reasoning_text") ||
			strings.Contains(string(body), `"type":"reasoning"`) || strings.Contains(string(body), `"type":"thinking"`) {
			t.Fatalf("%s added reasoning without a sidecar: %s", name, body)
		}
	}
}

func TestContinuationTrimsReplayedReasoningOutputs(t *testing.T) {
	delta := []json.RawMessage{
		json.RawMessage(`{"type":"reasoning","content":[{"type":"reasoning_text","text":"think"}]}`),
		json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"call_1","output":"ok"}`),
	}
	trimmed := trimPreviousResponseOutputs(delta)
	if len(trimmed) != 1 || !strings.Contains(string(trimmed[0]), "function_call_output") {
		t.Fatalf("trimmed outputs = %s", trimmed)
	}
}
