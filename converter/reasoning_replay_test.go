package converter

import (
	"io"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

type replayedBlock struct {
	Type      string  `json:"type"`
	Thinking  *string `json:"thinking"`
	Signature string  `json:"signature"`
	Data      string  `json:"data"`
	Text      string  `json:"text"`
}

type replayedMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	ReasoningContent *string         `json:"reasoning_content"`
}

func decodeReplayedMessages(t *testing.T, body []byte) []replayedMessage {
	t.Helper()
	var req struct {
		Messages []replayedMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode body: %v\n%s", err, body)
	}
	return req.Messages
}

func assistantMessages(messages []replayedMessage) []replayedMessage {
	var result []replayedMessage
	for _, msg := range messages {
		if msg.Role == "assistant" {
			result = append(result, msg)
		}
	}
	return result
}

func replayedBlocks(t *testing.T, msg replayedMessage) []replayedBlock {
	t.Helper()
	var blocks []replayedBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil // 纯文本 content
	}
	return blocks
}

func toolTurn(id string) ParsedHistoryEntry {
	return ParsedHistoryEntry{HasAssistant: true, ToolUses: []ParsedToolUse{{ToolUseID: id, Name: "read_file", Input: json.RawMessage(`{}`)}}}
}

func TestAnthropicMultipleThinkingBlocksKeepOwnSignatures(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"first "}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"idea"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"sealed-data"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"signature_delta","signature":"sig-2"}}`,
		`data: {"type":"content_block_stop","index":2}`,
		`data: {"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"call_1","name":"read_file"}}`,
		`data: {"type":"content_block_stop","index":3}`,
		`data: {"type":"message_stop"}`,
	}, "\n\n") + "\n\n"
	sc := NewAnthropicStreamConverter(io.NopCloser(strings.NewReader(sse)), "claude", 1000, 0, 0, 0, 0, 1)
	_, _ = io.ReadAll(sc)
	trace := sc.ReasoningTrace()
	want := []ReasoningBlock{
		{Type: ReasoningBlockThinking, Text: "first idea", Signature: "sig-1"},
		{Type: ReasoningBlockRedactedThinking, Data: "sealed-data"},
		{Type: ReasoningBlockThinking, Signature: "sig-2"},
	}
	if len(trace.Blocks) != len(want) || !trace.HasReplayContent() {
		t.Fatalf("captured blocks = %+v", trace.Blocks)
	}
	for i := range want {
		if trace.Blocks[i] != want[i] {
			t.Fatalf("block %d = %+v, want %+v", i, trace.Blocks[i], want[i])
		}
	}

	conv := WithReasoningHistory(&ParsedConversation{History: []ParsedHistoryEntry{toolTurn("call_1")}, CurrentContent: "go on"},
		map[string]ReasoningTrace{ReasoningTurnKey([]string{"call_1"}): trace})
	body, err := KiroToAnthropic(conv, "claude-sonnet-4.5", "", AnthropicRequestOptions{UpstreamHost: "api.anthropic.com"})
	if err != nil {
		t.Fatal(err)
	}
	blocks := replayedBlocks(t, assistantMessages(decodeReplayedMessages(t, body))[0])
	if len(blocks) != 4 || blocks[3].Type != "tool_use" {
		t.Fatalf("replayed blocks = %s", body)
	}
	if blocks[0].Type != "thinking" || blocks[0].Thinking == nil || *blocks[0].Thinking != "first idea" || blocks[0].Signature != "sig-1" {
		t.Fatalf("first thinking = %+v", blocks[0])
	}
	if blocks[1].Type != "redacted_thinking" || blocks[1].Data != "sealed-data" {
		t.Fatalf("redacted thinking = %+v", blocks[1])
	}
	// 空文本 + 签名：thinking 字段必须存在（空串），签名原样回放
	if blocks[2].Type != "thinking" || blocks[2].Thinking == nil || *blocks[2].Thinking != "" || blocks[2].Signature != "sig-2" {
		t.Fatalf("signature-only thinking = %+v, body=%s", blocks[2], body)
	}
}

func TestAnthropicSignatureOnlyThinkingIsCapturedWithoutStart(t *testing.T) {
	sse := `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"adaptive-sig"}}

data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"read_file"}}

data: {"type":"message_stop"}

`
	sc := NewAnthropicStreamConverter(io.NopCloser(strings.NewReader(sse)), "claude", 1000, 0, 0, 0, 0, 1)
	_, _ = io.ReadAll(sc)
	trace := sc.ReasoningTrace()
	if len(trace.Blocks) != 1 || trace.Blocks[0].Signature != "adaptive-sig" || trace.Blocks[0].Text != "" || !trace.HasReplayContent() {
		t.Fatalf("signature-only trace = %+v", trace)
	}
}

func TestAnthropicClaudeReplaySkipsUnsignedThinking(t *testing.T) {
	// 旧数据：只有 Text+Signature，按单个 thinking 块回放；无签名的旧文本不发给 Claude
	legacySigned := ReasoningTrace{Present: true, Text: "legacy", Signature: "legacy-sig", Protocol: "anthropic"}
	blocks := anthropicReasoningBlocks(&legacySigned, false, true)
	if len(blocks) != 1 || *blocks[0].Thinking != "legacy" || blocks[0].Signature != "legacy-sig" {
		t.Fatalf("legacy replay = %+v", blocks)
	}
	legacyUnsigned := ReasoningTrace{Present: true, Text: "unsigned", Protocol: "anthropic"}
	if blocks := anthropicReasoningBlocks(&legacyUnsigned, false, true); len(blocks) != 0 {
		t.Fatalf("unsigned thinking sent to Claude: %+v", blocks)
	}
	mixed := ReasoningTrace{Present: true, Blocks: []ReasoningBlock{
		{Type: ReasoningBlockThinking, Text: "unsigned"},
		{Type: ReasoningBlockThinking, Text: "signed", Signature: "sig"},
	}}
	if blocks := anthropicReasoningBlocks(&mixed, false, true); len(blocks) != 1 || blocks[0].Signature != "sig" {
		t.Fatalf("mixed replay = %+v", blocks)
	}
}

// DeepSeek 系上游的历史：工具调用轮、工具结果之后的回复都要回传推理；新的 user 之后的回复不要。
func deepSeekReplayConversation() *ParsedConversation {
	return &ParsedConversation{
		History: []ParsedHistoryEntry{
			{UserContent: "fix it", HasAssistant: true, ToolUses: []ParsedToolUse{{ToolUseID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}}},
			{ToolResults: []ParsedToolResult{{ToolUseID: "call_1", Content: "a"}}, HasAssistant: true, ToolUses: []ParsedToolUse{{ToolUseID: "call_2", Name: "read_file", Input: json.RawMessage(`{}`)}}},
			{ToolResults: []ParsedToolResult{{ToolUseID: "call_2", Content: "b"}}, HasAssistant: true, AssistantContent: "done"},
			{UserContent: "thanks", HasAssistant: true, AssistantContent: "welcome"},
		},
		CurrentContent: "next",
	}
}

func TestDeepSeekChatFirstSendFillsReasoningPlaceholder(t *testing.T) {
	conv := deepSeekReplayConversation()
	body, err := KiroToOpenAI(conv, "deepseek-v4", "high", OpenAIRequestOptions{UpstreamHost: "api.deepseek.com"})
	if err != nil {
		t.Fatal(err)
	}
	assistants := assistantMessages(decodeReplayedMessages(t, body))
	if len(assistants) != 4 {
		t.Fatalf("assistant count = %d: %s", len(assistants), body)
	}
	for i, want := range []bool{true, true, true, false} {
		got := assistants[i].ReasoningContent
		if want && (got == nil || *got != ReasoningReplayPlaceholder) {
			t.Fatalf("assistant %d reasoning = %v, body=%s", i, got, body)
		}
		if !want && got != nil {
			t.Fatalf("assistant %d after new user got reasoning: %s", i, body)
		}
	}
	// 已有推理原样回放，不被占位覆盖
	withTrace := WithReasoningHistory(conv, map[string]ReasoningTrace{
		ReasoningTurnKey([]string{"call_1"}): {Present: true, Text: "inspect", Protocol: "chat", WireField: "reasoning_content"},
	})
	body, err = KiroToOpenAI(withTrace, "relay-name", "high", OpenAIRequestOptions{UpstreamHost: "api.deepseek.com"})
	if err != nil {
		t.Fatal(err)
	}
	if first := assistantMessages(decodeReplayedMessages(t, body))[0].ReasoningContent; first == nil || *first != "inspect" {
		t.Fatalf("stored reasoning not replayed: %s", body)
	}
	// 非 DeepSeek 上游不补
	body, err = KiroToOpenAI(conv, "gpt-5.5", "high", OpenAIRequestOptions{UpstreamHost: "api.openai.com"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "reasoning_content") {
		t.Fatalf("non-DeepSeek upstream got reasoning placeholder: %s", body)
	}
}

func TestDeepSeekAnthropicFirstSendFillsThinkingPlaceholder(t *testing.T) {
	conv := deepSeekReplayConversation()
	// call_1 带 Claude 签名 thinking + redacted；DeepSeek 不认识 redacted_thinking，只保留可读 thinking
	conv = WithReasoningHistory(conv, map[string]ReasoningTrace{
		ReasoningTurnKey([]string{"call_1"}): {Present: true, Protocol: "anthropic", Blocks: []ReasoningBlock{
			{Type: ReasoningBlockThinking, Text: "claude thought", Signature: "claude-sig"},
			{Type: ReasoningBlockRedactedThinking, Data: "sealed"},
		}},
		ReasoningTurnKey([]string{"call_2"}): {Present: true, Protocol: "anthropic", Blocks: []ReasoningBlock{
			{Type: ReasoningBlockRedactedThinking, Data: "sealed-only"},
		}},
	})
	body, err := KiroToAnthropic(conv, "deepseek-v4", "", AnthropicRequestOptions{UpstreamHost: "api.deepseek.com"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "redacted_thinking") {
		t.Fatalf("redacted_thinking sent to DeepSeek: %s", body)
	}
	assistants := assistantMessages(decodeReplayedMessages(t, body))
	if len(assistants) != 4 {
		t.Fatalf("assistant count = %d: %s", len(assistants), body)
	}
	first := replayedBlocks(t, assistants[0])
	if len(first) != 2 || first[0].Type != "thinking" || *first[0].Thinking != "claude thought" || first[0].Signature != "claude-sig" || first[1].Type != "tool_use" {
		t.Fatalf("signed thinking turn = %s", assistants[0].Content)
	}
	for i := 1; i <= 2; i++ {
		blocks := replayedBlocks(t, assistants[i])
		if len(blocks) < 2 || blocks[0].Type != "thinking" || *blocks[0].Thinking != ReasoningReplayPlaceholder || blocks[0].Signature != "" {
			t.Fatalf("assistant %d missing unsigned placeholder thinking: %s", i, assistants[i].Content)
		}
	}
	if blocks := replayedBlocks(t, assistants[2]); blocks[1].Type != "text" || blocks[1].Text != "done" {
		t.Fatalf("reply after tool result = %s", assistants[2].Content)
	}
	if strings.Contains(string(assistants[3].Content), "thinking") {
		t.Fatalf("reply after new user got thinking: %s", assistants[3].Content)
	}
	// 同一会话发给 Claude：不补占位，签名 thinking 与 redacted 原样回放
	body, err = KiroToAnthropic(conv, "claude-sonnet-4.5", "", AnthropicRequestOptions{UpstreamHost: "api.anthropic.com"})
	if err != nil {
		t.Fatal(err)
	}
	claude := assistantMessages(decodeReplayedMessages(t, body))
	if blocks := replayedBlocks(t, claude[0]); len(blocks) != 3 || blocks[1].Type != "redacted_thinking" {
		t.Fatalf("claude replay = %s", claude[0].Content)
	}
	if strings.Contains(string(claude[2].Content), "thinking") || strings.Count(string(body), `"thinking":" "`) != 0 {
		t.Fatalf("claude got placeholder thinking: %s", body)
	}
}

func TestReasoningReplayRequiredFlagFillsUnknownRelay(t *testing.T) {
	conv := &ParsedConversation{History: []ParsedHistoryEntry{toolTurn("call_1")}, CurrentContent: "go", ReasoningReplayRequired: true}
	body, err := KiroToAnthropic(conv, "relay-model", "", AnthropicRequestOptions{UpstreamHost: "relay.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"thinking":" "`) {
		t.Fatalf("learned replay requirement not applied: %s", body)
	}
}
