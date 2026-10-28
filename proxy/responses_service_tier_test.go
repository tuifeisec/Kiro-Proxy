package proxy

import (
	"testing"

	"kiro-proxy/converter"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

func responseServiceTier(t *testing.T, body []byte) (string, bool) {
	t.Helper()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("decode Responses body: %v", err)
	}
	raw, ok := root["service_tier"]
	if !ok {
		return "", false
	}
	var tier string
	if err := json.Unmarshal(raw, &tier); err != nil {
		t.Fatalf("decode service_tier: %v", err)
	}
	return tier, true
}

func TestApplyConfiguredResponsesServiceTierOnlyFillsNeutralValues(t *testing.T) {
	model := &store.Model{Provider: "openai", ServiceTier: "fast"}
	for _, tc := range []struct {
		name, body, want string
		present          bool
	}{
		{name: "missing", body: `{"model":"m"}`, want: "priority", present: true},
		{name: "null", body: `{"model":"m","service_tier":null}`, want: "priority", present: true},
		{name: "default", body: `{"model":"m","service_tier":"default"}`, want: "priority", present: true},
		{name: "priority", body: `{"model":"m","service_tier":"priority"}`, want: "priority", present: true},
		{name: "flex", body: `{"model":"m","service_tier":"flex"}`, want: "flex", present: true},
		{name: "empty", body: `{"model":"m","service_tier":""}`, want: "", present: true},
		{name: "auto", body: `{"model":"m","service_tier":"auto"}`, want: "auto", present: true},
		{name: "disabled", body: `{"model":"m"}`, want: "", present: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := model
			if tc.name == "disabled" {
				current = &store.Model{Provider: "openai"}
			}
			got := applyConfiguredResponsesServiceTier([]byte(tc.body), current)
			tier, present := responseServiceTier(t, got)
			if tier != tc.want || present != tc.present {
				t.Fatalf("service_tier=(%q,%v), want (%q,%v); body=%s", tier, present, tc.want, tc.present, got)
			}
		})
	}
}

func TestOpenAIResponsesServiceTierAppliedAcrossEntryPoints(t *testing.T) {
	model := &store.Model{
		Provider: "openai", BaseURL: "https://provider.example/v1", Endpoint: "/responses",
		ModelID: "gpt-test", ServiceTier: "fast",
	}
	conv := &converter.ParsedConversation{ModelID: "gpt-test", CurrentContent: "hello"}
	converted, _, err := convertRequestWithProtocol(&Preflight{Protocol: "kiro", Conv: conv}, model, nil, false)
	if err != nil {
		t.Fatalf("convert Responses request: %v", err)
	}
	if tier, ok := responseServiceTier(t, converted); !ok || tier != "priority" {
		t.Fatalf("converted Responses tier=(%q,%v), body=%s", tier, ok, converted)
	}

	raw, err := prepareRawResponsesBody([]byte(`{"model":"client","input":"hello"}`), nil, model)
	if err != nil {
		t.Fatalf("prepare raw Responses request: %v", err)
	}
	if tier, ok := responseServiceTier(t, raw); !ok || tier != "priority" {
		t.Fatalf("raw Responses tier=(%q,%v), body=%s", tier, ok, raw)
	}
	explicitRaw, err := prepareRawResponsesBody([]byte(`{"model":"client","input":"hello","service_tier":"flex"}`), nil, model)
	if err != nil {
		t.Fatalf("prepare explicit raw Responses request: %v", err)
	}
	if tier, ok := responseServiceTier(t, explicitRaw); !ok || tier != "flex" {
		t.Fatalf("explicit raw Responses tier=(%q,%v), body=%s", tier, ok, explicitRaw)
	}

	prepared, err := (&Server{}).prepareLocalCompatCandidate([]byte(`{"model":"client","input":"hello"}`), localCompatResponses, model, false, "session")
	if err != nil {
		t.Fatalf("prepare local Responses request: %v", err)
	}
	if tier, ok := responseServiceTier(t, prepared.upstreamBody); !ok || tier != "priority" {
		t.Fatalf("local Responses tier=(%q,%v), body=%s", tier, ok, prepared.upstreamBody)
	}
}

func TestResponsesServiceTierDoesNotRewriteCodexOrChatFallback(t *testing.T) {
	codex := &store.Model{Provider: "codex", ServiceTier: "fast"}
	body := applyConfiguredResponsesServiceTier([]byte(`{"model":"m"}`), codex)
	if _, ok := responseServiceTier(t, body); ok {
		t.Fatalf("Codex helper unexpectedly added service_tier: %s", body)
	}

	chat := &store.Model{Provider: "anthropic", ServiceTier: "fast", Endpoint: "/chat/completions"}
	if !responsesNeedsChatFallback(chat) {
		t.Fatal("expected non-OpenAI chat endpoint to use Responses fallback")
	}
	converted, _, err := responsesToChatFallback([]byte(`{"model":"m","input":"hello"}`), "m")
	if err != nil {
		t.Fatalf("convert fallback request: %v", err)
	}
	if _, ok := responseServiceTier(t, converted); ok {
		t.Fatalf("Chat fallback unexpectedly retained service_tier: %s", converted)
	}

	rawChat := &store.Model{Provider: "openai", ServiceTier: "fast", BaseURL: "https://provider.example/v1", Endpoint: "/chat/completions", ModelID: "m"}
	prepared, err := prepareRawResponsesUpstreamBody([]byte(`{"model":"m","input":"hello"}`), nil, rawChat)
	if err != nil {
		t.Fatalf("prepare raw Chat fallback request: %v", err)
	}
	if _, ok := responseServiceTier(t, prepared); ok {
		t.Fatalf("raw Chat fallback unexpectedly synthesized service_tier: %s", prepared)
	}
}
