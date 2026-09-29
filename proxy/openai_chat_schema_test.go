package proxy

import (
	"strings"
	"testing"

	"kiro-proxy/converter"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

func TestKiroChatSchemaNormalizationRequiresActualOpenAIChatURL(t *testing.T) {
	raw := []json.RawMessage{json.RawMessage(`{"toolSpecification":{"name":"control_pwsh_process","inputSchema":{"json":{"type":"object","properties":{"command":{"anyOf":[{"not":{}},{"type":"string"}]}}}}}}`)}
	pre := &Preflight{Protocol: "kiro", Conv: &converter.ParsedConversation{
		ModelID: "deepseek", CurrentContent: "test", RawTools: raw,
	}}
	for _, tc := range []struct {
		name, provider, baseURL, endpoint string
		disableAutoSuffix, wantNormalized bool
	}{
		{name: "OpenAI Chat", provider: "openai", baseURL: "https://example.test/v1", endpoint: "/chat/completions", wantNormalized: true},
		{name: "OpenAI Responses", provider: "openai", baseURL: "https://example.test/v1", endpoint: "/responses"},
		{name: "custom actual path", provider: "openai", baseURL: "https://example.test/custom", endpoint: "/chat/completions", disableAutoSuffix: true},
		{name: "other provider on Chat path", provider: "other", baseURL: "https://example.test/v1", endpoint: "/chat/completions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &store.Model{Provider: tc.provider, BaseURL: tc.baseURL, Endpoint: tc.endpoint,
				ModelID: "deepseek", DisableAutoSuffix: tc.disableAutoSuffix}
			body, _, err := convertRequestWithProtocol(pre, model, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			hasImpossibleBranch := strings.Contains(string(body), `"not":{}`)
			if hasImpossibleBranch == tc.wantNormalized {
				t.Fatalf("actual provider/path gate mismatch: normalized=%v body=%s", !hasImpossibleBranch, body)
			}
		})
	}
}
