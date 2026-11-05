package converter

import (
	"io"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

// 一步内两个并行调用：Gemini 3 只在第一个 functionCall 上签发 thoughtSignature。
const geminiThoughtToolSSE = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Let me inspect the file.","thought":true}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Checking files."}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"path":"a.go"}},"thoughtSignature":"c2lnLW9uZQ=="},{"functionCall":{"name":"list_dir","args":{}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}

`

func runGeminiStream(t *testing.T, sse string) (*GeminiStreamConverter, []decodedFrame) {
	t.Helper()
	c := NewGeminiStreamConverter(io.NopCloser(strings.NewReader(sse)), "model", 2048, 0, 0, 0, 0, 1)
	stream, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read converted stream: %v", err)
	}
	return c, decodeFrames(t, stream)
}

func geminiToolUseIDs(t *testing.T, frames []decodedFrame) []string {
	t.Helper()
	var ids []string
	for _, frame := range frames {
		if frame.eventType != "toolUseEvent" {
			continue
		}
		var payload struct {
			ToolUseID string `json:"toolUseId"`
		}
		if err := json.Unmarshal([]byte(frame.payload), &payload); err != nil {
			t.Fatalf("decode tool frame: %v", err)
		}
		ids = appendUniqueToolCallID(ids, payload.ToolUseID)
	}
	return ids
}

func TestGeminiStreamKeepsThoughtsOutOfAssistantText(t *testing.T) {
	c, frames := runGeminiStream(t, geminiThoughtToolSSE)
	if got := concatText(frames); got != "Checking files." {
		t.Fatalf("assistant text = %q, thought summary must not be mixed in", got)
	}
	ids := geminiToolUseIDs(t, frames)
	if len(ids) != 2 {
		t.Fatalf("tool use ids = %v", ids)
	}
	trace := c.ReasoningTrace()
	if !trace.Present || trace.Protocol != "gemini" || trace.Text != "Let me inspect the file." {
		t.Fatalf("unexpected trace: %+v", trace)
	}
	if trace.Signature != "c2lnLW9uZQ==" {
		t.Fatalf("signature = %q, want the first functionCall signature", trace.Signature)
	}
	if strings.Join(trace.ToolCallIDs, ",") != strings.Join(ids, ",") {
		t.Fatalf("trace tool ids %v must match emitted ids %v", trace.ToolCallIDs, ids)
	}
}

func TestGeminiStreamToolCallIDsDifferAcrossStreams(t *testing.T) {
	// 同名调用在不同轮次的 ID 若重复，按 ID 保存的签名会被下一轮覆盖。
	_, first := runGeminiStream(t, geminiThoughtToolSSE)
	_, second := runGeminiStream(t, geminiThoughtToolSSE)
	firstIDs, secondIDs := geminiToolUseIDs(t, first), geminiToolUseIDs(t, second)
	if firstIDs[0] == secondIDs[0] || ReasoningTurnKey(firstIDs) == ReasoningTurnKey(secondIDs) {
		t.Fatalf("tool ids collide across streams: %v vs %v", firstIDs, secondIDs)
	}
}

func TestGeminiStreamIgnoresSignatureOnLaterCall(t *testing.T) {
	sse := `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{}}},{"functionCall":{"name":"list_dir","args":{}},"thoughtSignature":"bGF0ZXI="}]},"finishReason":"STOP"}]}

`
	c, _ := runGeminiStream(t, sse)
	if trace := c.ReasoningTrace(); trace.Signature != "" || trace.Present {
		t.Fatalf("signature on a later call cannot be replayed in place, got %+v", trace)
	}
}

func TestGeminiStreamWithoutToolCallsHasNoTrace(t *testing.T) {
	sse := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true},{"text":"answer"}]},"finishReason":"STOP"}]}

`
	c, frames := runGeminiStream(t, sse)
	if got := concatText(frames); got != "answer" {
		t.Fatalf("assistant text = %q", got)
	}
	if trace := c.ReasoningTrace(); trace.Present {
		t.Fatalf("plain text turn must not produce a replay trace: %+v", trace)
	}
}

func modelContents(t *testing.T, body []byte) []GeminiContent {
	t.Helper()
	var req GeminiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode Gemini request: %v", err)
	}
	var result []GeminiContent
	for _, content := range req.Contents {
		if content.Role == "model" {
			result = append(result, content)
		}
	}
	return result
}

func TestGeminiReplayWritesSignatureBackToFirstCall(t *testing.T) {
	stream, frames := runGeminiStream(t, geminiThoughtToolSSE)
	trace := stream.ReasoningTrace()
	ids := geminiToolUseIDs(t, frames)
	conv := &ParsedConversation{
		History: []ParsedHistoryEntry{
			{UserContent: "inspect", HasAssistant: true, AssistantContent: "Checking files.", ToolUses: []ParsedToolUse{
				{ToolUseID: ids[0], Name: "read_file", Input: json.RawMessage(`{"path":"a.go"}`)},
				{ToolUseID: ids[1], Name: "list_dir", Input: json.RawMessage(`{}`)},
			}},
			{ToolResults: []ParsedToolResult{{ToolUseID: ids[0], Content: "ok"}, {ToolUseID: ids[1], Content: "ok"}},
				HasAssistant: true, ToolUses: []ParsedToolUse{{ToolUseID: "call_unsigned", Name: "read_file", Input: json.RawMessage(`{}`)}}},
		},
		CurrentToolResults: []ParsedToolResult{{ToolUseID: "call_unsigned", Content: "ok"}},
	}
	replayed := WithReasoningHistory(conv, map[string]ReasoningTrace{ReasoningTurnKey(trace.ToolCallIDs): trace})
	body, err := KiroToGemini(replayed, "gemini-3-pro-preview", "high", GeminiRequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	models := modelContents(t, body)
	if len(models) != 2 {
		t.Fatalf("model contents = %+v", models)
	}
	signed := models[0].Parts
	if signed[0].Text != "Checking files." || signed[0].ThoughtSignature != "" {
		t.Fatalf("text part must not carry a signature: %+v", signed[0])
	}
	if signed[1].ThoughtSignature != "c2lnLW9uZQ==" || signed[2].ThoughtSignature != "" {
		t.Fatalf("signature must go back on the first call only: %+v", signed)
	}
	// 没有保存签名的历史工具轮使用 Google 约定的跳过校验标记。
	if got := models[1].Parts[0].ThoughtSignature; got != "skip_thought_signature_validator" {
		t.Fatalf("unsigned history call signature = %q", got)
	}
	if strings.Contains(string(body), "Let me inspect the file.") {
		t.Fatalf("thought summary must not be replayed: %s", body)
	}
}

func TestGeminiCallSignaturesIgnoreOtherProtocols(t *testing.T) {
	foreign := &ReasoningTrace{Present: true, Signature: "anthropic-signature", Protocol: "anthropic"}
	for _, trace := range []*ReasoningTrace{nil, foreign, {Present: true, Text: "only text", Protocol: "gemini"}} {
		got := geminiCallSignatures(trace, 2)
		if got[0] != geminiSkipSignature || got[1] != geminiSkipSignature {
			t.Fatalf("trace %+v: signatures = %v", trace, got)
		}
	}
	if got := geminiCallSignatures(foreign, 0); len(got) != 0 {
		t.Fatalf("no calls should produce no signatures: %v", got)
	}
}
