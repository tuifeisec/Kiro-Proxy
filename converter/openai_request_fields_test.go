package converter

import (
	"testing"

	json "github.com/goccy/go-json"
)

func TestChatTokenLimitFieldByHost(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{"api.openai.com", ChatMaxCompletionTokensField},
		{"API.OPENAI.COM", ChatMaxCompletionTokensField},
		{"my-resource.openai.azure.com", ChatMaxCompletionTokensField},
		{"api.deepseek.com", ChatMaxTokensField},
		{"open.bigmodel.cn", ChatMaxTokensField},
		{"api.openai.com.relay.example", ChatMaxTokensField},
		{"openai.azure.com.relay.example", ChatMaxTokensField},
		{"", ChatMaxTokensField},
	}
	for _, tt := range tests {
		if got := ChatTokenLimitField(tt.host); got != tt.want {
			t.Errorf("ChatTokenLimitField(%q) = %q, want %q", tt.host, got, tt.want)
		}
	}
	// 调用方传入的 host 来自 UpstreamHostOf：大小写与端口不影响规则
	if got := ChatTokenLimitField(UpstreamHostOf("https://API.OpenAI.com:443/v1/chat/completions")); got != ChatMaxCompletionTokensField {
		t.Fatalf("token field for official URL with port = %q", got)
	}
	if got := UpstreamHostOf("://not a url"); got != "" {
		t.Fatalf("UpstreamHostOf(invalid) = %q, want empty", got)
	}
}

func TestIsOpenAIReasoningModel(t *testing.T) {
	for _, model := range []string{"o1", "o1-mini", "o3", "o3-pro", "o4-mini", "gpt-5", "GPT-5", "gpt-5-mini", "gpt-5-2025-08-07", "gpt-5.1", "gpt-5.5-codex"} {
		if !isOpenAIReasoningModel(model) {
			t.Errorf("%q should be a reasoning model", model)
		}
	}
	for _, model := range []string{"gpt-5-chat", "gpt-5-chat-latest", "gpt-4o", "gpt-4.1", "gpt-50", "o10", "omni-moderation", "deepseek-reasoner", ""} {
		if isOpenAIReasoningModel(model) {
			t.Errorf("%q should not be a reasoning model", model)
		}
	}
}

func TestKiroToOpenAIGenerationFieldsByHostAndModel(t *testing.T) {
	temperature, topP := 0.2, 0.9
	tests := []struct {
		name         string
		host         string
		model        string
		wantField    string
		wantSampling bool
	}{
		{name: "official reasoning model drops sampling", host: "api.openai.com", model: "gpt-5.5", wantField: "max_completion_tokens"},
		{name: "official o-series drops sampling", host: "api.openai.com", model: "o3-mini", wantField: "max_completion_tokens"},
		{name: "official gpt-5-chat keeps sampling", host: "api.openai.com", model: "gpt-5-chat-latest", wantField: "max_completion_tokens", wantSampling: true},
		{name: "official non-reasoning keeps sampling", host: "api.openai.com", model: "gpt-4.1", wantField: "max_completion_tokens", wantSampling: true},
		{name: "azure keeps sampling", host: "my-resource.openai.azure.com", model: "gpt-5", wantField: "max_completion_tokens", wantSampling: true},
		{name: "deepseek uses max_tokens", host: "api.deepseek.com", model: "deepseek-reasoner", wantField: "max_tokens", wantSampling: true},
		{name: "relay with gpt-5 name passes through", host: "relay.example", model: "gpt-5", wantField: "max_tokens", wantSampling: true},
		{name: "unknown host passes through", host: "", model: "gpt-5", wantField: "max_tokens", wantSampling: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conv := &ParsedConversation{ModelID: "kiro-model", CurrentContent: "hi", MaxTokens: 4096, Temperature: &temperature, TopP: &topP}
			body, err := KiroToOpenAI(conv, tt.model, "", OpenAIRequestOptions{UpstreamHost: tt.host})
			if err != nil {
				t.Fatalf("KiroToOpenAI: %v", err)
			}
			got := decodeGenerationFields(t, body)
			otherField := "max_tokens"
			if tt.wantField == "max_tokens" {
				otherField = "max_completion_tokens"
			}
			if string(got[tt.wantField]) != "4096" {
				t.Fatalf("%s = %s, want 4096: %s", tt.wantField, got[tt.wantField], body)
			}
			if _, ok := got[otherField]; ok {
				t.Fatalf("%s must not be sent alongside %s: %s", otherField, tt.wantField, body)
			}
			_, hasTemperature := got["temperature"]
			_, hasTopP := got["top_p"]
			if hasTemperature != tt.wantSampling || hasTopP != tt.wantSampling {
				t.Fatalf("temperature/top_p present = %t/%t, want %t: %s", hasTemperature, hasTopP, tt.wantSampling, body)
			}
		})
	}
}

func TestKiroToOpenAIOmitsUnsetGenerationFields(t *testing.T) {
	conv := &ParsedConversation{ModelID: "kiro-model", CurrentContent: "hi"}
	body, err := KiroToOpenAIChatUpstream(conv, "gpt-5", "", OpenAIRequestOptions{UpstreamHost: "api.openai.com"})
	if err != nil {
		t.Fatalf("KiroToOpenAIChatUpstream: %v", err)
	}
	got := decodeGenerationFields(t, body)
	for _, field := range []string{"max_tokens", "max_completion_tokens", "temperature", "top_p"} {
		if _, ok := got[field]; ok {
			t.Fatalf("unset %s was sent: %s", field, body)
		}
	}
}

func decodeGenerationFields(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return got
}
