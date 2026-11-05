package converter

import (
	"bytes"
	"io"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

func statelessTestConversation() *ParsedConversation {
	conv := &ParsedConversation{History: []ParsedHistoryEntry{
		{UserContent: "read both"},
		{HasAssistant: true, ToolUses: []ParsedToolUse{{ToolUseID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}}},
		{ToolResults: []ParsedToolResult{{ToolUseID: "call_1", Content: "a"}}},
		{HasAssistant: true, ToolUses: []ParsedToolUse{{ToolUseID: "call_2", Name: "read_file", Input: json.RawMessage(`{}`)}}},
		{ToolResults: []ParsedToolResult{{ToolUseID: "call_2", Content: "b"}}},
	}}
	return WithReasoningHistory(conv, map[string]ReasoningTrace{
		ReasoningTurnKey([]string{"call_1"}): {Present: true, Protocol: "responses", WireField: "reasoning_summary_text", Text: "first", ItemID: "rs_1", Status: "completed", Signature: "enc_1"},
		// 早期保存、没有 encrypted_content 的 trace。
		ReasoningTurnKey([]string{"call_2"}): {Present: true, Protocol: "responses", WireField: "reasoning_summary_text", Text: "second", ItemID: "rs_2", Status: "completed"},
	})
}

type statelessTestRequest struct {
	Include   []string `json:"include"`
	Reasoning *struct {
		Effort  string `json:"effort"`
		Summary string `json:"summary"`
	} `json:"reasoning"`
	Input []map[string]json.RawMessage `json:"input"`
}

func statelessTestDecode(t *testing.T, body []byte) (statelessTestRequest, []map[string]json.RawMessage) {
	t.Helper()
	var request statelessTestRequest
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	var reasoning []map[string]json.RawMessage
	for _, item := range request.Input {
		if string(item["type"]) == `"reasoning"` {
			reasoning = append(reasoning, item)
		}
	}
	return request, reasoning
}

// store=false 的官方 OpenAI 不保存输出项：推理只能以 encrypted_content 回放，
// 只有 id 的旧 trace 会被拒绝（"Item with id 'rs_…' not found"），必须省略。
func TestKiroToOpenAIResponsesStatelessReplaysEncryptedReasoning(t *testing.T) {
	body, err := KiroToOpenAIResponsesStateless(statelessTestConversation(), "gpt-5.5", "high")
	if err != nil {
		t.Fatal(err)
	}
	request, reasoning := statelessTestDecode(t, body)
	if len(request.Include) != 1 || request.Include[0] != "reasoning.encrypted_content" || request.Reasoning == nil || request.Reasoning.Summary != "auto" {
		t.Fatalf("stateless request fields = %s", body)
	}
	if len(reasoning) != 1 || string(reasoning[0]["encrypted_content"]) != `"enc_1"` || reasoning[0]["id"] != nil || reasoning[0]["status"] != nil || reasoning[0]["content"] != nil {
		t.Fatalf("stateless reasoning replay = %s", body)
	}
	if !bytes.Contains(reasoning[0]["summary"], []byte(`"first"`)) {
		t.Fatalf("summary missing: %s", reasoning[0]["summary"])
	}

	body, err = KiroToOpenAIResponsesStateless(statelessTestConversation(), "gpt-5.5", "none")
	if err != nil {
		t.Fatal(err)
	}
	if request, _ := statelessTestDecode(t, body); request.Include != nil || request.Reasoning.Summary != "" {
		t.Fatalf("effort none must not request reasoning output: %s", body)
	}
}

func TestKiroToOpenAIResponsesUpstreamKeepsIDReplay(t *testing.T) {
	body, err := KiroToOpenAIResponsesUpstream(statelessTestConversation(), "gpt-5.5", "high", false)
	if err != nil {
		t.Fatal(err)
	}
	request, reasoning := statelessTestDecode(t, body)
	if request.Include != nil || request.Reasoning.Summary != "" || len(reasoning) != 2 ||
		string(reasoning[0]["id"]) != `"rs_1"` || reasoning[0]["encrypted_content"] != nil {
		t.Fatalf("non-stateless replay changed: %s", body)
	}
}

func TestResponsesStreamCapturesEncryptedReasoning(t *testing.T) {
	sse := `data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","summary_index":0,"delta":"plan"}

data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"plan"}],"encrypted_content":"enc_stream"}}

data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":""}}

data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}"}}

data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":1}}}

`
	converter := NewResponsesStreamConverter(io.NopCloser(strings.NewReader(sse)), "gpt-5.5", 1000, 0, 0, 0, 0, 1)
	_, _ = io.ReadAll(converter)
	if trace := converter.ReasoningTrace(); trace.Signature != "enc_stream" || trace.Protocol != "responses" || trace.Text != "plan" {
		t.Fatalf("trace = %+v", trace)
	}
}
