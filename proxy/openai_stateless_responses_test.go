package proxy

import (
	"bytes"
	"testing"

	"kiro-proxy/converter"
	"kiro-proxy/store"
)

// 只有 store=false 的 OpenAI 官方 host 请求 encrypted_content 与推理摘要；
// 自定义中转与开启续接（store=true）的官方请求保持原有字段。
func TestOfficialOpenAIStatelessResponsesRequestFields(t *testing.T) {
	conv := &converter.ParsedConversation{ConversationID: "stateless", CurrentContent: "hi"}
	tests := []struct {
		name         string
		model        *store.Model
		wantStateful bool
	}{
		{name: "official", model: &store.Model{Provider: "openai", BaseURL: "https://api.openai.com/v1", Endpoint: "/responses", ModelID: "gpt-5.5", ReasoningEffort: "high"}},
		{name: "codex", model: &store.Model{Provider: "codex", BaseURL: "https://chatgpt.com/backend-api/codex", Endpoint: "/responses", ModelID: "gpt-5.5", ReasoningEffort: "high"}},
		{name: "relay", model: &store.Model{Provider: "openai", BaseURL: "https://relay.example/v1", Endpoint: "/responses", ModelID: "gpt-5.5", ReasoningEffort: "high"}, wantStateful: true},
		{name: "continuation", model: &store.Model{Provider: "openai", BaseURL: "https://api.openai.com/v1", Endpoint: "/responses", ModelID: "gpt-5.5", ReasoningEffort: "high", EnableContinuation: true}, wantStateful: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _, err := convertRequestWithProtocol(&Preflight{Protocol: "kiro", Conv: conv}, tt.model, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			hasInclude := bytes.Contains(body, []byte(`"include":["reasoning.encrypted_content"]`))
			hasSummary := bytes.Contains(body, []byte(`"summary":"auto"`))
			if tt.wantStateful == hasInclude || !tt.wantStateful && !hasSummary || tt.wantStateful && hasSummary {
				t.Fatalf("include=%v summary=%v body=%s", hasInclude, hasSummary, body)
			}
			if !tt.wantStateful && !bytes.Contains(body, []byte(`"store":false`)) {
				t.Fatalf("stateless fields require store=false: %s", body)
			}
		})
	}
}
