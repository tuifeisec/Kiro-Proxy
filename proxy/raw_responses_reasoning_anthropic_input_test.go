package proxy

import (
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

// echoResponsesOutputItems 模拟原样回传 output 的 Responses 客户端：按 added 顺序取 done 项。
func echoResponsesOutputItems(t *testing.T, sse string) []interface{} {
	t.Helper()
	events := reasoningTestSSEEvents(t, sse)
	done := make(map[string]interface{})
	var order []string
	for _, event := range events {
		item, _ := event["item"].(map[string]interface{})
		switch event["type"] {
		case "response.output_item.added":
			order = append(order, item["id"].(string))
		case "response.output_item.done":
			done[item["id"].(string)] = item
		}
	}
	items := make([]interface{}, 0, len(order))
	for _, id := range order {
		if done[id] == nil {
			t.Fatalf("output item %s has no done event", id)
		}
		items = append(items, done[id])
	}
	return items
}

func TestAnthropicReasoningKeepsEachBlockBeforeItsToolUse(t *testing.T) {
	sse := anthropicTestStream(t, joinAnthropicTestEvents([]string{anthropicTestMessageStart},
		anthropicTestThinkingBlock("0", "first", "sig-1"), anthropicTestToolBlock("1", "toolu_1"),
		anthropicTestThinkingBlock("2", "second", "sig-2"), anthropicTestToolBlock("3", "toolu_3"),
		[]string{anthropicTestStop, anthropicTestMessageStop}))
	events := reasoningTestSSEEvents(t, sse)
	firstDone := reasoningEventIndex(events, "response.output_item.done", "reasoning", "rs_msg_1_0")
	secondAdded := reasoningEventIndex(events, "response.output_item.added", "reasoning", "rs_msg_1_2")
	secondDone := reasoningEventIndex(events, "response.output_item.done", "reasoning", "rs_msg_1_2")
	firstCallDone := reasoningEventIndex(events, "response.output_item.done", "function_call", "toolu_1")
	if firstDone < 0 || secondAdded < firstDone || secondDone < secondAdded || firstCallDone < secondDone {
		t.Fatalf("block order: firstDone=%d secondAdded=%d secondDone=%d firstCallDone=%d\n%s", firstDone, secondAdded, secondDone, firstCallDone, sse)
	}

	echoed := echoResponsesOutputItems(t, sse)
	input := []interface{}{map[string]interface{}{"role": "user", "content": "go"}}
	input = append(input, echoed...)
	input = append(input, map[string]interface{}{"type": "function_call_output", "call_id": "toolu_1", "output": "a"},
		map[string]interface{}{"type": "function_call_output", "call_id": "toolu_3", "output": "b"})
	messages := anthropicTestPrepare(t, input)
	// 每个工具结果沿用既有行为各成一条 user 消息（Anthropic 合并相邻同角色消息）。
	if len(messages) != 4 || messages[1].Role != "assistant" || len(messages[1].Content) != 4 {
		t.Fatalf("messages = %#v", messages)
	}
	want := []string{"thinking:sig-1", "tool_use:toolu_1", "thinking:sig-2", "tool_use:toolu_3"}
	for index, block := range messages[1].Content {
		got := block["type"].(string) + ":"
		if block["type"] == "thinking" {
			got += block["signature"].(string)
		} else {
			got += block["id"].(string)
		}
		if got != want[index] {
			t.Fatalf("assistant block %d = %s, want %s: %#v", index, got, want[index], messages[1].Content)
		}
	}
}

func TestConvertAnthropicJSONResponseOutputsReasoningItems(t *testing.T) {
	body := `{"id":"msg_9","model":"claude","stop_reason":"tool_use","usage":{"input_tokens":3,"output_tokens":4},"content":[` +
		`{"type":"thinking","thinking":"plan","signature":"sig-json"},{"type":"redacted_thinking","data":"opaque"},` +
		`{"type":"text","text":"reading"},{"type":"tool_use","id":"toolu_9","name":"read_file","input":{"path":"a.go"}}]}`
	converted, err := convertAnthropicJSONResponse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output []map[string]interface{} `json:"output"`
	}
	if err := json.Unmarshal(converted, &response); err != nil {
		t.Fatal(err)
	}
	types := make([]string, 0, len(response.Output))
	for _, item := range response.Output {
		types = append(types, item["type"].(string))
	}
	if strings.Join(types, ",") != "reasoning,reasoning,message,function_call" || response.Output[0]["id"] != "rs_msg_9_0" {
		t.Fatalf("output = %s", converted)
	}
	input := []interface{}{map[string]interface{}{"role": "user", "content": "go"}, response.Output[0], response.Output[1], response.Output[3],
		map[string]interface{}{"type": "function_call_output", "call_id": "toolu_9", "output": "a"}}
	messages := anthropicTestPrepare(t, input)
	content := messages[1].Content
	if len(content) != 3 || content[0]["signature"] != "sig-json" || content[1]["type"] != "redacted_thinking" || content[1]["data"] != "opaque" || content[2]["type"] != "tool_use" {
		t.Fatalf("assistant blocks = %#v", content)
	}
}

// 其他上游的 reasoning item 不能发给 Anthropic：跳过并记录计数，日志不含密文或正文。
// 此前没有 role 的 reasoning item 会落进 user 分支，content 为 null 时整个请求报错。
func TestAnthropicReasoningSkipsForeignItemsWithDiagnostic(t *testing.T) {
	lines := captureReasoningDiagnostics(t)
	chatSeal, err := textReasoningEnvelope("chat thought", sealReasoningPayload(chatReasoningSealPrefix, []byte(`{"field":"reasoning","text":"chat thought"}`)))
	if err != nil {
		t.Fatal(err)
	}
	input := []interface{}{
		map[string]interface{}{"role": "user", "content": "go"},
		map[string]interface{}{"type": "reasoning", "id": "rs_openai", "summary": []interface{}{map[string]string{"type": "summary_text", "text": "secret plan"}}, "encrypted_content": "gAAAAABopenai-sealed"},
		map[string]interface{}{"type": "reasoning", "summary": []interface{}{}, "encrypted_content": `{"reasoningText":{"text":"x","signature":"bedrock-signature"}}`},
		map[string]interface{}{"type": "reasoning", "summary": []interface{}{}, "encrypted_content": chatSeal},
		map[string]interface{}{"type": "reasoning", "summary": []interface{}{}, "encrypted_content": `{"reasoningText":{"text":"x","signature":"` + anthropicReasoningSealPrefix + `!!!"}}`},
		map[string]interface{}{"type": "reasoning", "content": nil},
	}
	input = append(input, anthropicTestToolTurn("toolu_1")...)
	messages := anthropicTestPrepare(t, input)
	if len(messages) != 3 || len(messages[1].Content) != 1 || messages[1].Content[0]["type"] != "tool_use" {
		t.Fatalf("foreign reasoning reached Anthropic: %#v", messages)
	}
	if len(*lines) != 1 || !strings.Contains((*lines)[0], "foreign=4 unreadable=1 orphaned=0") {
		t.Fatalf("diagnostic = %q", *lines)
	}
	for _, leaked := range []string{"secret plan", "gAAAA", "bedrock-signature", "chat thought"} {
		if strings.Contains((*lines)[0], leaked) {
			t.Fatalf("diagnostic leaked %q: %s", leaked, (*lines)[0])
		}
	}
}

func TestAnthropicReasoningCountsOrphanedBlocks(t *testing.T) {
	lines := captureReasoningDiagnostics(t)
	item, err := anthropicReasoningOutputItem("rs_x", anthropicReasoningBlock{Type: anthropicThinkingBlock, Thinking: "t", Signature: "s"})
	if err != nil {
		t.Fatal(err)
	}
	messages := anthropicTestPrepare(t, []interface{}{item, map[string]interface{}{"role": "user", "content": "next"}})
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("orphaned thinking must not create an assistant turn: %#v", messages)
	}
	if len(*lines) != 1 || !strings.Contains((*lines)[0], "orphaned=1") {
		t.Fatalf("diagnostic = %q", *lines)
	}
}
