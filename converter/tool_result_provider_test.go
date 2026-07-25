package converter

import (
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

func mixedProviderToolResult() ParsedToolResult {
	return ParsedToolResult{
		ToolUseID: "call_mixed",
		Content:   "hello",
		Blocks: []ParsedToolResultBlock{
			{Kind: "text", Text: "hello"},
			{Kind: "json", Raw: json.RawMessage(`{"type":"json","value":{"answer":42}}`)},
		},
	}
}

func assertMixedToolEnvelope(t *testing.T, value string) {
	t.Helper()
	if !strings.Contains(value, `"_kiro_content_blocks"`) ||
		!strings.Contains(value, `"answer":42`) ||
		!strings.Contains(value, `"_kiro_text":"hello"`) {
		t.Fatalf("mixed tool result was not preserved: %s", value)
	}
}

func TestOpenAIAndAnthropicPreserveMixedToolResults(t *testing.T) {
	conv := &ParsedConversation{
		ModelID:            "gpt-5.6",
		CurrentToolResults: []ParsedToolResult{mixedProviderToolResult()},
	}

	openAIBody, err := KiroToOpenAI(conv, "gpt-5.6", "")
	if err != nil {
		t.Fatalf("convert OpenAI request: %v", err)
	}
	openAIReq := parseOpenAIReq(t, openAIBody)
	foundOpenAI := false
	for _, message := range openAIReq.Messages {
		if message.Role != "tool" {
			continue
		}
		content, ok := message.Content.(string)
		if !ok {
			t.Fatalf("OpenAI tool content has unexpected type: %T", message.Content)
		}
		assertMixedToolEnvelope(t, content)
		foundOpenAI = true
	}
	if !foundOpenAI {
		t.Fatal("OpenAI request did not contain a tool result")
	}

	anthropicBody, err := KiroToAnthropic(conv, "claude-sonnet-4.5", "")
	if err != nil {
		t.Fatalf("convert Anthropic request: %v", err)
	}
	anthropicReq := parseAnthropicReq(t, anthropicBody)
	foundAnthropic := false
	for _, message := range anthropicReq.Messages {
		blocks := anthropicContentBlocks(t, message.Content)
		for _, block := range blocks {
			if block.Type != "tool_result" {
				continue
			}
			content, ok := block.Content.(string)
			if !ok {
				t.Fatalf("Anthropic tool content has unexpected type: %T", block.Content)
			}
			assertMixedToolEnvelope(t, content)
			foundAnthropic = true
		}
	}
	if !foundAnthropic {
		t.Fatal("Anthropic request did not contain a tool result")
	}
}
