package proxy

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

func chatTestStream(t *testing.T, chunks ...string) string {
	t.Helper()
	var stream strings.Builder
	for _, chunk := range chunks {
		stream.WriteString("data: " + chunk + "\n\n")
	}
	stream.WriteString("data: [DONE]\n\n")
	recorder := httptest.NewRecorder()
	if _, _, _, err := copyChatStreamAsResponsesWithState(context.Background(), recorder, strings.NewReader(stream.String())); err != nil {
		t.Fatalf("convert Chat stream: %v\n%s", err, recorder.Body.String())
	}
	return recorder.Body.String()
}

func chatTestMessages(t *testing.T, input []interface{}) []map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := responsesInputToChatMessages(raw)
	if err != nil {
		t.Fatalf("convert input: %v", err)
	}
	return messages
}

// Chat 流的 reasoning（OpenRouter 字段名）产出 reasoning item，经 Kiro 重建后写回原字段。
func TestChatReasoningRoundTripsThroughKiro(t *testing.T) {
	sse := chatTestStream(t,
		`{"id":"chatcmpl-1","model":"m","choices":[{"delta":{"reasoning":"check "}}]}`,
		`{"id":"chatcmpl-1","choices":[{"delta":{"reasoning":"the file"}}]}`,
		`{"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{}"}}]}}]}`,
		`{"id":"chatcmpl-1","choices":[{"finish_reason":"tool_calls","delta":{}}]}`)
	events := reasoningTestSSEEvents(t, sse)
	added := reasoningEventIndex(events, "response.output_item.added", "reasoning", "")
	done := reasoningEventIndex(events, "response.output_item.done", "reasoning", "")
	call := reasoningEventIndex(events, "response.output_item.added", "function_call", "call_1")
	if added < 0 || done < added || call < done {
		t.Fatalf("reasoning must close before the tool call: added=%d done=%d call=%d\n%s", added, done, call, sse)
	}
	replay := kiroReplayReasoningItems(t, sse)
	if len(replay) != 1 {
		t.Fatalf("Kiro kept %d reasoning items\n%s", len(replay), sse)
	}
	messages := chatTestMessages(t, []interface{}{
		map[string]interface{}{"role": "user", "content": "go"}, replay[0],
		map[string]interface{}{"type": "function_call", "call_id": "call_1", "name": "read_file", "arguments": "{}"},
		map[string]interface{}{"type": "function_call_output", "call_id": "call_1", "output": "package a"},
	})
	if len(messages) != 3 || messages[1]["reasoning"] != "check the file" || messages[1]["reasoning_content"] != nil {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestChatReasoningIgnoresNonStringReasoningField(t *testing.T) {
	sse := chatTestStream(t,
		`{"id":"chatcmpl-2","choices":[{"delta":{"reasoning":{"effort":"high"},"content":"hello"}}]}`,
		`{"id":"chatcmpl-2","choices":[{"finish_reason":"stop","delta":{}}]}`)
	if !strings.Contains(sse, `"delta":"hello"`) || strings.Contains(sse, `"type":"reasoning"`) {
		t.Fatalf("object reasoning must not drop the chunk or create an item: %s", sse)
	}
}

func chatTestDeepSeekBody(t *testing.T, model *store.Model, input []interface{}) []map[string]interface{} {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{"model": "local", "input": input})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareRawResponsesChatBody(body, model)
	if err != nil {
		t.Fatal(err)
	}
	var chat struct {
		Messages []map[string]interface{} `json:"messages"`
	}
	if err := json.Unmarshal(prepared, &chat); err != nil {
		t.Fatal(err)
	}
	return chat.Messages
}

// DeepSeek 要求工具调用轮回传 reasoning_content：缺推理的轮补占位，已有推理保持原文。
func TestRawChatReasoningPlaceholderForDeepSeek(t *testing.T) {
	seal, err := textReasoningEnvelope("real", sealReasoningPayload(chatReasoningSealPrefix, []byte(`{"field":"reasoning_content","text":"real"}`)))
	if err != nil {
		t.Fatal(err)
	}
	input := []interface{}{
		map[string]interface{}{"role": "user", "content": "go"},
		map[string]interface{}{"type": "function_call", "call_id": "call_1", "name": "read_file", "arguments": "{}"},
		map[string]interface{}{"type": "function_call_output", "call_id": "call_1", "output": "a"},
		map[string]interface{}{"type": "reasoning", "summary": []interface{}{}, "encrypted_content": seal},
		map[string]interface{}{"type": "function_call", "call_id": "call_2", "name": "read_file", "arguments": "{}"},
		map[string]interface{}{"type": "function_call_output", "call_id": "call_2", "output": "b"},
	}
	deepseek := &store.Model{Provider: "openai", BaseURL: "https://api.deepseek.com", Endpoint: "/chat/completions", ModelID: "deepseek-v4"}
	messages := chatTestDeepSeekBody(t, deepseek, input)
	if len(messages) != 5 || messages[1]["reasoning_content"] != " " || messages[3]["reasoning_content"] != "real" {
		t.Fatalf("DeepSeek messages = %#v", messages)
	}
	other := &store.Model{Provider: "openai", BaseURL: "https://relay.example", Endpoint: "/chat/completions", ModelID: "gpt-5.5"}
	messages = chatTestDeepSeekBody(t, other, input)
	if _, ok := messages[1]["reasoning_content"]; ok || messages[3]["reasoning_content"] != "real" {
		t.Fatalf("non-DeepSeek messages = %#v", messages)
	}
}

// 发往原生 Responses 上游前剥离本项目封装的 reasoning item，其他上游的保留。
func TestRawResponsesStripsProxyReasoningBeforeResponsesUpstream(t *testing.T) {
	lines := captureReasoningDiagnostics(t)
	anthropicItem, err := anthropicReasoningOutputItem("rs_a", anthropicReasoningBlock{Type: anthropicThinkingBlock, Thinking: "t", Signature: "s"})
	if err != nil {
		t.Fatal(err)
	}
	redactedItem, err := anthropicReasoningOutputItem("rs_r", anthropicReasoningBlock{Type: anthropicRedactedThinkingBlock, Data: "opaque"})
	if err != nil {
		t.Fatal(err)
	}
	input := []interface{}{
		map[string]interface{}{"role": "user", "content": "go"},
		map[string]interface{}{"type": "reasoning", "id": "rs_native", "summary": []interface{}{}, "encrypted_content": "gAAAAABnative"},
		anthropicItem, redactedItem,
		map[string]interface{}{"type": "function_call", "call_id": "call_1", "name": "read_file", "arguments": "{}"},
		map[string]interface{}{"type": "function_call_output", "call_id": "call_1", "output": "a"},
	}
	body, err := json.Marshal(map[string]interface{}{"model": "local", "input": input})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareRawResponsesUpstreamBody(body, nil, rawResponsesTestModel("https://api.openai.com/v1", "openai", "/responses"))
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Input []map[string]interface{} `json:"input"`
	}
	if err := json.Unmarshal(prepared, &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Input) != 4 || request.Input[1]["encrypted_content"] != "gAAAAABnative" || strings.Contains(string(prepared), anthropicReasoningSealPrefix) {
		t.Fatalf("prepared input = %s", prepared)
	}
	if len(*lines) != 1 || !strings.Contains((*lines)[0], "count=2") {
		t.Fatalf("diagnostic = %q", *lines)
	}

	*lines = nil
	if _, err := prepareRawResponsesUpstreamBody(body, nil, rawResponsesTestModel("https://example.test", "anthropic", "/messages")); err != nil {
		t.Fatal(err)
	}
	if len(*lines) != 0 {
		t.Fatalf("Anthropic candidates must keep their reasoning: %q", *lines)
	}
}

// SDK /v1/responses 入口：Chat 回退给出的封装推理不能发往原生 Responses 模型。
func TestLocalCompatStripsProxyReasoningForNativeResponses(t *testing.T) {
	lines := captureReasoningDiagnostics(t)
	seal, err := textReasoningEnvelope("x", sealReasoningPayload(chatReasoningSealPrefix, []byte(`{"field":"reasoning_content","text":"x"}`)))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]interface{}{"model": "m", "input": []interface{}{
		map[string]interface{}{"role": "user", "content": "go"},
		map[string]interface{}{"type": "reasoning", "summary": []interface{}{}, "encrypted_content": seal},
	}})
	if err != nil {
		t.Fatal(err)
	}
	stripped, err := stripProxyReasoningItems(body, "gpt-5.5")
	if err != nil || strings.Contains(string(stripped), chatReasoningSealPrefix) || len(*lines) != 1 {
		t.Fatalf("stripped=%s err=%v diagnostics=%q", stripped, err, *lines)
	}
	plain := []byte(`{"model":"m","input":"hello","x":9007199254740993}`)
	if unchanged, err := stripProxyReasoningItems(plain, "gpt-5.5"); err != nil || string(unchanged) != string(plain) {
		t.Fatalf("body without reasoning must stay byte-identical: %s err=%v", unchanged, err)
	}
}
