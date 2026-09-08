package proxy

import (
	"kiro-proxy/converter"
	"kiro-proxy/store"
	"testing"
)

func TestResponsesSessionScopeTracksConfigButNotDynamicTail(t *testing.T) {
	conv := &converter.ParsedConversation{SystemText: "rules\nHere is the last user message:\none", ToolsHash: "tools"}
	model := &store.Model{ID: "same-id", Provider: "openai", ModelID: "upstream", BaseURL: "https://example.invalid", Endpoint: "/v1/responses", APIKey: "secret"}
	initial := responsesSessionScope("conversation", conv, model, "high")
	conv.SystemText = "rules\nHere is the last user message:\ntwo"
	if responsesSessionScope("conversation", conv, model, "high") != initial {
		t.Fatal("dynamic tail must not invalidate instructions continuation")
	}
	for name, change := range map[string]func(*store.Model){
		"provider": func(m *store.Model) { m.Provider = "other" },
		"model":    func(m *store.Model) { m.ModelID = "new" },
		"base":     func(m *store.Model) { m.BaseURL = "https://new.invalid" },
		"endpoint": func(m *store.Model) { m.Endpoint = "/other" },
		"account":  func(m *store.Model) { m.APIKey = "new-key" },
		"tier":     func(m *store.Model) { m.ServiceTier = "priority" },
	} {
		t.Run(name, func(t *testing.T) {
			copied := *model
			change(&copied)
			if responsesSessionScope("conversation", conv, &copied, "high") == initial {
				t.Fatal("reused changed scope")
			}
		})
	}
	conv.SystemText = "new rules\nHere is the last user message:\ntwo"
	if responsesSessionScope("conversation", conv, model, "high") == initial {
		t.Fatal("static instructions change reused state")
	}
}
