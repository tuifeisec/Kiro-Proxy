package converter

import (
	"testing"

	json "github.com/goccy/go-json"
)

// 推理写在正文 <think> 中的上游（MiniMax、未开 reasoning parser 的 vLLM/SGLang）：
// 拆出的推理在回放时按原形态包回正文，不改发它没用过的 reasoning_content。
func TestThinkTagReasoningRoundTripsIntoContent(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"<think>inspect first</think>\\n\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	sc := runOpenAIPipeline(sse)
	trace := sc.ReasoningTrace()
	if trace.Text != "inspect first" || trace.WireField != ThinkTagWireField {
		t.Fatalf("captured trace = %+v", trace)
	}
	msg := buildOpenAIAssistantMsg("", []ParsedToolUse{{ToolUseID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}}, &trace)
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	var content string
	if err := json.Unmarshal(sent["content"], &content); err != nil || content != "<think>inspect first</think>\n" {
		t.Fatalf("think tag reasoning was not wrapped back into content: %s", body)
	}
	for _, field := range []string{"reasoning_content", "reasoning_text", "reasoning"} {
		if _, ok := sent[field]; ok {
			t.Fatalf("think tag reasoning must not be sent as %s: %s", field, body)
		}
	}
}
