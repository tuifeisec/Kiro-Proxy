package proxy

import (
	"bytes"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

func anthropicTestSSE(events ...string) string {
	var builder strings.Builder
	for _, event := range events {
		builder.WriteString("event: x\ndata: " + event + "\n\n")
	}
	return builder.String()
}

func anthropicTestStream(t *testing.T, stream string) string {
	t.Helper()
	var out bytes.Buffer
	if err := copyAnthropicStreamAsResponses(&out, strings.NewReader(stream)); err != nil {
		t.Fatalf("convert Anthropic stream: %v\n%s", err, out.String())
	}
	return out.String()
}

type anthropicTestMessage struct {
	Role    string                   `json:"-"`
	Content []map[string]interface{} `json:"-"`
}

func anthropicTestPrepare(t *testing.T, input []interface{}) []anthropicTestMessage {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{"model": "local", "input": input, "reasoning": map[string]string{"effort": "high"}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareRawResponsesAnthropicBody(body, nil, rawResponsesTestModel("https://example.test", "anthropic", "/messages"))
	if err != nil {
		t.Fatalf("prepare Anthropic request: %v", err)
	}
	var request struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared, &request); err != nil {
		t.Fatalf("parse prepared request: %v\n%s", err, prepared)
	}
	messages := make([]anthropicTestMessage, 0, len(request.Messages))
	for _, message := range request.Messages {
		converted := anthropicTestMessage{Role: message.Role}
		// 字符串 content（普通用户消息）没有块，保持为空。
		_ = json.Unmarshal(message.Content, &converted.Content)
		messages = append(messages, converted)
	}
	return messages
}

func anthropicTestToolTurn(callID string) []interface{} {
	return []interface{}{
		map[string]interface{}{"type": "function_call", "call_id": callID, "name": "read_file", "arguments": `{"path":"a.go"}`},
		map[string]interface{}{"type": "function_call_output", "call_id": callID, "output": "package a"},
	}
}

const (
	anthropicTestMessageStart = `{"type":"message_start","message":{"id":"msg_1","model":"claude","usage":{"input_tokens":5}}}`
	anthropicTestStop         = `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`
	anthropicTestMessageStop  = `{"type":"message_stop"}`
)

func anthropicTestToolBlock(index, id string) []string {
	return []string{
		`{"type":"content_block_start","index":` + index + `,"content_block":{"type":"tool_use","id":"` + id + `","name":"read_file","input":{}}}`,
		`{"type":"content_block_delta","index":` + index + `,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.go\"}"}}`,
		`{"type":"content_block_stop","index":` + index + `}`,
	}
}

func anthropicTestThinkingBlock(index, text, signature string) []string {
	return []string{
		`{"type":"content_block_start","index":` + index + `,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":` + index + `,"delta":{"type":"thinking_delta","thinking":"` + text + `"}}`,
		`{"type":"content_block_delta","index":` + index + `,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`,
		`{"type":"content_block_stop","index":` + index + `}`,
	}
}

func joinAnthropicTestEvents(groups ...[]string) string {
	var events []string
	for _, group := range groups {
		events = append(events, group...)
	}
	return anthropicTestSSE(events...)
}

// 第一轮 thinking+tool_use 的输出经 Kiro 重建后作为第二轮 input，签名块必须原样回到 Anthropic。
func TestAnthropicReasoningRoundTripsSignedThinkingThroughKiro(t *testing.T) {
	sse := anthropicTestStream(t, joinAnthropicTestEvents([]string{anthropicTestMessageStart},
		anthropicTestThinkingBlock("0", "need the file", "sig-A=="), anthropicTestToolBlock("1", "toolu_1"),
		[]string{anthropicTestStop, anthropicTestMessageStop}))
	events := reasoningTestSSEEvents(t, sse)
	added := reasoningEventIndex(events, "response.output_item.added", "reasoning", "")
	done := reasoningEventIndex(events, "response.output_item.done", "reasoning", "")
	call := reasoningEventIndex(events, "response.output_item.added", "function_call", "toolu_1")
	if added < 0 || done < added || call < done {
		t.Fatalf("reasoning events must precede the function call: added=%d done=%d call=%d\n%s", added, done, call, sse)
	}
	if strings.Contains(sse, "reasoning_summary_text.delta") || !strings.Contains(sse, `"type":"response.reasoning_text.delta"`) {
		t.Fatalf("Kiro reads reasoning_text deltas only: %s", sse)
	}
	item := events[done]["item"].(map[string]interface{})
	if summary := item["summary"].([]interface{}); len(summary) != 1 || summary[0].(map[string]interface{})["text"] != "need the file" {
		t.Fatalf("reasoning summary = %#v", item["summary"])
	}

	replay := kiroReplayReasoningItems(t, sse)
	if len(replay) != 1 {
		t.Fatalf("Kiro kept %d reasoning items, want 1", len(replay))
	}
	input := append([]interface{}{map[string]interface{}{"role": "user", "content": "read a.go"}, replay[0]}, anthropicTestToolTurn("toolu_1")...)
	messages := anthropicTestPrepare(t, input)
	if len(messages) != 3 || messages[1].Role != "assistant" || len(messages[1].Content) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	thinking, tool := messages[1].Content[0], messages[1].Content[1]
	if thinking["type"] != "thinking" || thinking["thinking"] != "need the file" || thinking["signature"] != "sig-A==" || tool["type"] != "tool_use" {
		t.Fatalf("assistant blocks = %#v", messages[1].Content)
	}
}

func TestAnthropicReasoningRoundTripsRedactedThinkingThroughKiro(t *testing.T) {
	const data = "EmwKAhgBEgy3va3pzix/LafPsn4aDFIT2Xlxh0L5L8rLVyIwxtE3rAFBa8cr3qpPkNRj2YfWXGmKDxH4mPnZ5sQ7vB5URj"
	sse := anthropicTestStream(t, joinAnthropicTestEvents([]string{anthropicTestMessageStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"` + data + `"}}`,
		`{"type":"content_block_stop","index":0}`}, anthropicTestToolBlock("1", "toolu_1"),
		[]string{anthropicTestStop, anthropicTestMessageStop}))
	if strings.Contains(sse, "reasoning_text.delta") {
		t.Fatalf("redacted thinking must not stream text: %s", sse)
	}
	replay := kiroReplayReasoningItems(t, sse)
	if len(replay) != 1 {
		t.Fatalf("Kiro kept %d reasoning items, want 1\n%s", len(replay), sse)
	}
	messages := anthropicTestPrepare(t, append([]interface{}{map[string]interface{}{"role": "user", "content": "go"}, replay[0]}, anthropicTestToolTurn("toolu_1")...))
	if first := messages[1].Content[0]; first["type"] != "redacted_thinking" || first["data"] != data || len(first) != 2 {
		t.Fatalf("redacted block = %#v", messages[1].Content)
	}
}

// 有签名但没有可见文本的块要输出一个占位增量，否则 Kiro 丢弃签名；还原时以封装为准。
func TestAnthropicReasoningKeepsSignatureWithoutVisibleThinking(t *testing.T) {
	sse := anthropicTestStream(t, joinAnthropicTestEvents([]string{anthropicTestMessageStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-empty"}}`,
		`{"type":"content_block_stop","index":0}`}, anthropicTestToolBlock("1", "toolu_1"),
		[]string{anthropicTestStop, anthropicTestMessageStop}))
	replay := kiroReplayReasoningItems(t, sse)
	if len(replay) != 1 {
		t.Fatalf("signature was dropped by Kiro replay: %s", sse)
	}
	messages := anthropicTestPrepare(t, append([]interface{}{map[string]interface{}{"role": "user", "content": "go"}, replay[0]}, anthropicTestToolTurn("toolu_1")...))
	if first := messages[1].Content[0]; first["type"] != "thinking" || first["thinking"] != "" || first["signature"] != "sig-empty" {
		t.Fatalf("restored block = %#v", first)
	}
}
