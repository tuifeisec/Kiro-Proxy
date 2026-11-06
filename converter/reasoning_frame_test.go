package converter

import (
	"io"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

// reasoningFrameTexts 返回 reasoningContentEvent 帧的 text，并确认没有携带签名或加密内容：
// 签名只留在代理自己的回放状态里，不经 Kiro 历史回传。
func reasoningFrameTexts(t *testing.T, frames []decodedFrame) []string {
	t.Helper()
	var texts []string
	for _, frame := range frames {
		if frame.eventType != "reasoningContentEvent" {
			continue
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(frame.payload), &payload); err != nil {
			t.Fatalf("decode reasoning frame: %v", err)
		}
		for key := range payload {
			if key != "text" {
				t.Fatalf("reasoning frame must only carry text, got %q in %s", key, frame.payload)
			}
		}
		text, _ := payload["text"].(string)
		texts = append(texts, text)
	}
	return texts
}

// firstIndex 返回第一个指定类型帧的位置，用于断言推理先于正文与工具调用展示。
func firstIndex(frames []decodedFrame, eventType string) int {
	for index, frame := range frames {
		if frame.eventType == eventType {
			return index
		}
	}
	return -1
}

func assertReasoningShownBefore(t *testing.T, frames []decodedFrame, wantText string, laterEvents ...string) {
	t.Helper()
	if got := strings.Join(reasoningFrameTexts(t, frames), ""); got != wantText {
		t.Fatalf("shown reasoning = %q, want %q", got, wantText)
	}
	reasoning := firstIndex(frames, "reasoningContentEvent")
	for _, eventType := range laterEvents {
		if later := firstIndex(frames, eventType); later >= 0 && later < reasoning {
			t.Fatalf("%s (frame %d) arrived before reasoning (frame %d)", eventType, later, reasoning)
		}
	}
}

func readConverterFrames(t *testing.T, reader io.Reader) []decodedFrame {
	t.Helper()
	stream, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read converted stream: %v", err)
	}
	return decodeFrames(t, stream)
}

func TestAnthropicStreamShowsThinkingToKiro(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"first "}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"idea"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"sealed-data"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"answer"}}`,
		`data: {"type":"content_block_stop","index":2}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`data: {"type":"message_stop"}`,
	}, "\n\n") + "\n\n"
	sc := NewAnthropicStreamConverter(io.NopCloser(strings.NewReader(sse)), "claude", 1000, 0, 0, 0, 0, 1)
	frames := readConverterFrames(t, sc)
	// redacted_thinking 没有可读文本，不展示；签名不展示
	assertReasoningShownBefore(t, frames, "first idea", "assistantResponseEvent")
	if got := concatText(frames); got != "answer" {
		t.Fatalf("assistant text = %q, thinking must not be mixed in", got)
	}
}

func TestOpenAIStreamShowsReasoningContentAndThinkTag(t *testing.T) {
	cases := map[string]struct {
		sse, reasoning, content string
	}{
		"reasoning_content": {
			sse: "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"plan \"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"steps\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
			reasoning: "plan steps", content: "done",
		},
		"think tag in content": {
			sse: "data: {\"choices\":[{\"delta\":{\"content\":\"<think>look\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\" first</think>\\nok\"},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
			reasoning: "look first", content: "ok",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sc := NewOpenAIStreamConverter(io.NopCloser(strings.NewReader(tc.sse)), "deepseek", 1000, 0, 0, 0, 0, 1)
			frames := readConverterFrames(t, sc)
			assertReasoningShownBefore(t, frames, tc.reasoning, "assistantResponseEvent")
			if got := concatText(frames); got != tc.content {
				t.Fatalf("assistant text = %q, want %q", got, tc.content)
			}
		})
	}
}

func TestGeminiStreamShowsThoughtsToKiro(t *testing.T) {
	_, frames := runGeminiStream(t, geminiThoughtToolSSE)
	assertReasoningShownBefore(t, frames, "Let me inspect the file.", "assistantResponseEvent", "toolUseEvent")
}

// 上游同时给出推理原文与推理摘要时只展示先出现的一种，避免同一段思考在 Kiro 中出现两次；
// 只有 done、没有增量的那种也按同一规则补齐。
func TestResponsesStreamShowsOneReasoningSourceOnce(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","summary_index":0,"delta":"sum "}`,
		`data: {"type":"response.reasoning_text.delta","item_id":"rs_1","content_index":0,"delta":"raw"}`,
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","summary_index":0,"delta":"mary"}`,
		`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","summary_index":0,"text":"sum mary"}`,
		`data: {"type":"response.reasoning_text.done","item_id":"rs_1","content_index":0,"text":"raw"}`,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"hi"}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":1}}}`,
	}, "\n\n") + "\n\n"
	frames := decodeFrames(t, runResponsesPipeline(sse))
	assertReasoningShownBefore(t, frames, "sum mary", "assistantResponseEvent")

	doneOnly := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_2"}}`,
		`data: {"type":"response.reasoning_text.done","item_id":"rs_2","content_index":0,"text":"whole thought"}`,
		`data: {"type":"response.output_text.delta","item_id":"msg_2","delta":"hi"}`,
		`data: {"type":"response.completed","response":{"id":"resp_2","usage":{"input_tokens":1,"output_tokens":1}}}`,
	}, "\n\n") + "\n\n"
	assertReasoningShownBefore(t, decodeFrames(t, runResponsesPipeline(doneOnly)), "whole thought", "assistantResponseEvent")
}

func TestReasoningContentFrameSkipsEmptyText(t *testing.T) {
	if frame := ReasoningContentFrame(""); frame != nil {
		t.Fatalf("empty reasoning must not produce a frame, got %d bytes", len(frame))
	}
	frames := decodeFrames(t, ReasoningContentFrame(`含 "引号" 与换行
`))
	if len(frames) != 1 || frames[0].eventType != "reasoningContentEvent" {
		t.Fatalf("frames = %+v", frames)
	}
	if texts := reasoningFrameTexts(t, frames); len(texts) != 1 || texts[0] != "含 \"引号\" 与换行\n" {
		t.Fatalf("text = %q", texts)
	}
}
