package proxy

import (
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

func TestChatUpstreamHostFollowsConfiguredURL(t *testing.T) {
	tests := []struct {
		name  string
		model *store.Model
		want  string
	}{
		{name: "base URL", model: &store.Model{BaseURL: "https://API.OpenAI.com/v1/", Endpoint: "/chat/completions"}, want: "api.openai.com"},
		{name: "absolute endpoint", model: &store.Model{BaseURL: "https://ignored.example/v1", Endpoint: "https://res.openai.azure.com/openai/v1/chat/completions"}, want: "res.openai.azure.com"},
		{name: "disable auto suffix", model: &store.Model{BaseURL: "https://api.deepseek.com:8443/chat/completions", Endpoint: "/responses", DisableAutoSuffix: true}, want: "api.deepseek.com"},
		{name: "missing base URL", model: &store.Model{Endpoint: "/chat/completions"}, want: ""},
		{name: "nil model", model: nil, want: ""},
	}
	for _, tt := range tests {
		if got := chatUpstreamHost(tt.model); got != tt.want {
			t.Errorf("%s: chatUpstreamHost = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestRawResponsesChatTokenFieldByHost(t *testing.T) {
	tests := []struct {
		baseURL   string
		wantField string
	}{
		{baseURL: "https://api.openai.com/v1", wantField: "max_completion_tokens"},
		{baseURL: "https://api.deepseek.com/v1", wantField: "max_tokens"},
		{baseURL: "https://open.bigmodel.cn/api/paas/v4", wantField: "max_tokens"},
	}
	for _, tt := range tests {
		t.Run(tt.baseURL, func(t *testing.T) {
			model := &store.Model{Provider: "openai", ModelID: "configured", BaseURL: tt.baseURL, Endpoint: "/chat/completions"}
			var root map[string]json.RawMessage
			if err := json.Unmarshal([]byte(`{"model":"local","input":"hi","max_output_tokens":2048}`), &root); err != nil {
				t.Fatal(err)
			}
			body, err := prepareRawResponsesChatRoot(root, model)
			if err != nil {
				t.Fatalf("prepare raw Chat body: %v", err)
			}
			assertOnlyTokenField(t, body, tt.wantField, "2048")
		})
	}
}

func TestLocalCompatResponsesFallbackTokenFieldByHost(t *testing.T) {
	tests := []struct {
		baseURL   string
		wantField string
	}{
		{baseURL: "https://api.deepseek.com/v1", wantField: "max_tokens"},
		{baseURL: "https://my-resource.openai.azure.com/openai/v1", wantField: "max_completion_tokens"},
	}
	for _, tt := range tests {
		t.Run(tt.baseURL, func(t *testing.T) {
			model := &store.Model{Provider: "anthropic", ModelID: "m", BaseURL: tt.baseURL, Endpoint: "/chat/completions"}
			prepared, err := (&Server{}).prepareLocalCompatCandidate([]byte(`{"model":"client","input":"hello","max_output_tokens":512}`), localCompatResponses, model, false, "session")
			if err != nil {
				t.Fatalf("prepare local Responses fallback: %v", err)
			}
			if !prepared.responsesFallback {
				t.Fatal("expected the Chat fallback")
			}
			assertOnlyTokenField(t, prepared.upstreamBody, tt.wantField, "512")
		})
	}
}

func TestConvertRequestChatGenerationFieldsUseUpstreamHost(t *testing.T) {
	temperature := 0.3
	tests := []struct {
		name            string
		baseURL         string
		modelID         string
		wantField       string
		wantTemperature bool
	}{
		{name: "official reasoning model", baseURL: "https://api.openai.com/v1", modelID: "gpt-5", wantField: "max_completion_tokens"},
		{name: "official non-reasoning model", baseURL: "https://api.openai.com/v1", modelID: "gpt-4.1", wantField: "max_completion_tokens", wantTemperature: true},
		{name: "deepseek", baseURL: "https://api.deepseek.com/v1", modelID: "deepseek-chat", wantField: "max_tokens", wantTemperature: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pre := &Preflight{Protocol: "kiro", StartTime: time.Now(), Conv: &converter.ParsedConversation{
				ModelID: "kiro-model", ConversationID: "conv-token-fields", CurrentContent: "hi", MaxTokens: 1024, Temperature: &temperature,
			}}
			model := &store.Model{Provider: "openai", ModelID: tt.modelID, BaseURL: tt.baseURL, Endpoint: "/chat/completions"}
			body, _, err := convertRequest(pre, model)
			if err != nil {
				t.Fatalf("convertRequest: %v", err)
			}
			got := assertOnlyTokenField(t, body, tt.wantField, "1024")
			if _, ok := got["temperature"]; ok != tt.wantTemperature {
				t.Fatalf("temperature present = %t, want %t: %s", ok, tt.wantTemperature, body)
			}
		})
	}
}

func assertOnlyTokenField(t *testing.T, body []byte, wantField, wantValue string) map[string]json.RawMessage {
	t.Helper()
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if string(got[wantField]) != wantValue {
		t.Fatalf("%s = %s, want %s: %s", wantField, got[wantField], wantValue, body)
	}
	other := converter.ChatMaxTokensField
	if wantField == converter.ChatMaxTokensField {
		other = converter.ChatMaxCompletionTokensField
	}
	if _, ok := got[other]; ok {
		t.Fatalf("%s must not be sent with %s: %s", other, wantField, body)
	}
	return got
}
