package proxy

import (
	"strings"
	"testing"

	"kiro-proxy/converter"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

func TestGeminiReasoningReplayUsesGeminiProtocol(t *testing.T) {
	pre := &Preflight{Protocol: "kiro", Conv: &converter.ParsedConversation{
		ConversationID: "gemini-conv",
		History: []converter.ParsedHistoryEntry{{
			HasAssistant: true,
			ToolUses:     []converter.ParsedToolUse{{ToolUseID: "call_read_file_abc_1", Name: "read_file", Input: json.RawMessage(`{}`)}},
		}},
		CurrentToolResults: []converter.ParsedToolResult{{ToolUseID: "call_read_file_abc_1", Content: "ok"}},
	}}
	model := &store.Model{
		ID: "gemini-config", ModelID: "relay-model", Provider: "gemini", BaseURL: "https://gemini.test",
		Endpoint: "/v1beta/models/{model}:streamGenerateContent", ReasoningEffort: "medium", ThinkingStyle: "level",
	}
	if got := reasoningProtocol(pre, model); got != "gemini" {
		t.Fatalf("reasoning protocol = %q, want gemini", got)
	}
	if reasoningStateKeyFor(pre, model) == "" {
		t.Fatal("gemini models must have a reasoning replay scope")
	}

	history := map[string]converter.ReasoningTrace{
		converter.ReasoningTurnKey([]string{"call_read_file_abc_1"}): {Present: true, Signature: "c2ln", Protocol: "gemini"},
	}
	body, _, err := convertRequestWithProtocol(pre, model, history, false)
	if err != nil {
		t.Fatal(err)
	}
	// ThinkingStyle=level 覆盖了无法从模型名判断版本的自动形态；名称里没有 flash，
	// 按 pro 类只有 low/high 的保守档位，medium 发 high。
	if !strings.Contains(string(body), `"thinkingLevel":"high"`) {
		t.Fatalf("thinking style override was not applied: %s", body)
	}
	if !strings.Contains(string(body), `"thoughtSignature":"c2ln"`) {
		t.Fatalf("stored signature was not replayed: %s", body)
	}
}
