package proxy

import (
	"strings"
	"testing"

	"kiro-proxy/converter"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

func rawAnthropicThinkingModel(serverURL, modelID, style string) *store.Model {
	model := rawResponsesTestModel(serverURL, "anthropic", "/messages")
	model.ModelID = modelID
	model.ThinkingStyle = style
	// The raw path reads effort from the request body; keep the model default empty.
	model.ReasoningEffort = ""
	return model
}

func decodeRawAnthropicRoot(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("decode prepared body: %v\n%s", err, body)
	}
	return root
}

func assertRawAnthropicField(t *testing.T, root map[string]json.RawMessage, field, want string) {
	t.Helper()
	got, ok := root[field]
	if want == "" {
		if ok {
			t.Fatalf("%s must be absent, got %s", field, got)
		}
		return
	}
	if !ok || string(got) != want {
		t.Fatalf("%s = %s, want %s", field, got, want)
	}
}

// TestPrepareRawResponsesAnthropicThinkingFinalJSON covers the raw Responses
// path: it previously sent thinking.type=adaptive for every model, which Claude
// before 4.6 does not take, and refused xhigh/max outright.
func TestPrepareRawResponsesAnthropicThinkingFinalJSON(t *testing.T) {
	cases := []struct {
		name, server, model, style, request string
		thinking, output, maxTokens         string
	}{
		{name: "4.7 adaptive max", server: "https://api.anthropic.com", model: "claude-opus-4-7",
			request:  `"reasoning":{"effort":"max"}`,
			thinking: `{"type":"adaptive","display":"summarized"}`, output: `{"effort":"max"}`, maxTokens: "16384"},
		{name: "4.5 budget clamped by max_output_tokens", server: "https://api.anthropic.com", model: "claude-sonnet-4-5",
			request:  `"reasoning":{"effort":"high"},"max_output_tokens":5000`,
			thinking: `{"type":"enabled","budget_tokens":3976}`, maxTokens: "5000"},
		{name: "deepseek host adds three-level effort", server: "https://api.deepseek.com/anthropic", model: "deepseek-chat",
			request:  `"reasoning":{"effort":"xhigh"}`,
			thinking: `{"type":"enabled","budget_tokens":15360}`, output: `{"effort":"max"}`, maxTokens: "16384"},
		{name: "opus 5.5 none leaves thinking out", server: "https://api.anthropic.com", model: "claude-opus-5-5",
			request: `"reasoning":{"effort":"none"}`, output: `{"effort":"low"}`, maxTokens: "16384"},
		{name: "renamed relay with explicit adaptive", server: "https://relay.example.com", model: "relay-claude", style: converter.ThinkingStyleAdaptive,
			request:  `"reasoning":{"effort":"medium"}`,
			thinking: `{"type":"adaptive","display":"summarized"}`, output: `{"effort":"medium"}`, maxTokens: "16384"},
		{name: "no effort writes nothing", server: "https://api.anthropic.com", model: "claude-opus-4-7",
			request: `"reasoning":{"summary":"auto"}`, maxTokens: "16384"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"local","input":"hello",` + tc.request + `}`)
			prepared, err := prepareRawResponsesAnthropicBody(body, nil, rawAnthropicThinkingModel(tc.server, tc.model, tc.style))
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			root := decodeRawAnthropicRoot(t, prepared)
			assertRawAnthropicField(t, root, "thinking", tc.thinking)
			assertRawAnthropicField(t, root, "output_config", tc.output)
			assertRawAnthropicField(t, root, "max_tokens", tc.maxTokens)
		})
	}
}

func TestPrepareRawResponsesAnthropicThinkingRejectsUnbuildableEffort(t *testing.T) {
	for name, tc := range map[string]struct{ request, want string }{
		"budget does not fit max_output_tokens": {request: `"reasoning":{"effort":"low"},"max_output_tokens":2000`, want: "max_tokens=2000"},
		"unknown effort":                        {request: `"reasoning":{"effort":"minimal"}`, want: "unsupported reasoning effort"},
	} {
		body := []byte(`{"model":"local","input":"hello",` + tc.request + `}`)
		prepared, err := prepareRawResponsesAnthropicBody(body, nil, rawAnthropicThinkingModel("https://api.anthropic.com", "claude-sonnet-4-5", ""))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want error containing %q, got body=%s err=%v", name, tc.want, prepared, err)
		}
	}
}

// TestAnthropicThinkingMatchesAcrossKiroAndRawPaths guards the original bug:
// the two paths built opposite thinking shapes for the same model.
func TestAnthropicThinkingMatchesAcrossKiroAndRawPaths(t *testing.T) {
	for _, modelID := range []string{"claude-sonnet-4-5", "claude-opus-4-6", "claude-opus-4-7", "claude-sonnet-5-5", "claude-opus-5-5"} {
		for _, effort := range []string{"none", "low", "medium", "high", "xhigh", "max"} {
			model := rawAnthropicThinkingModel("https://api.anthropic.com", modelID, "")
			raw, err := prepareRawResponsesAnthropicBody([]byte(`{"model":"local","input":"hello","reasoning":{"effort":"`+effort+`"}}`), nil, model)
			if err != nil {
				t.Fatalf("%s/%s raw: %v", modelID, effort, err)
			}
			kiro, err := converter.KiroToAnthropic(&converter.ParsedConversation{ModelID: modelID, CurrentContent: "hello"}, modelID, effort,
				converter.AnthropicRequestOptions{UpstreamHost: "api.anthropic.com"})
			if err != nil {
				t.Fatalf("%s/%s kiro: %v", modelID, effort, err)
			}
			rawRoot, kiroRoot := decodeRawAnthropicRoot(t, raw), decodeRawAnthropicRoot(t, kiro)
			for _, field := range []string{"thinking", "output_config", "max_tokens"} {
				if string(rawRoot[field]) != string(kiroRoot[field]) {
					t.Fatalf("%s/%s %s differs: raw=%s kiro=%s", modelID, effort, field, rawRoot[field], kiroRoot[field])
				}
			}
		}
	}
}
