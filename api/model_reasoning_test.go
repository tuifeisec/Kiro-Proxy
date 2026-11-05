package api

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"kiro-proxy/store"
)

func reasoningAPITestModel(id, provider string) store.Model {
	return store.Model{
		ID: id, Name: id, ModelID: id + "-upstream", Provider: provider,
		BaseURL: "https://example.test", APIKey: "api-key", Endpoint: "/v1/chat/completions",
	}
}

func TestModelReasoningCapabilityAPIRoundTrip(t *testing.T) {
	db, server := newCredentialAPITestServer(t, nil)
	model := reasoningAPITestModel("reasoning-cap", "openai")
	model.SupportedReasoningEfforts = []string{"max", "Low", "high", "low"}
	model.ReasoningEffort = "HIGH"
	model.DisableReasoningField = true

	status, response, apiError := requestModelJSON(t, server.Handler(), http.MethodPost, "/api/models", model)
	if status != http.StatusCreated {
		t.Fatalf("POST status=%d error=%v", status, apiError)
	}
	if want := []string{"low", "high", "max"}; !reflect.DeepEqual(response.SupportedReasoningEfforts, want) {
		t.Fatalf("supported efforts = %v, want sorted/deduped %v", response.SupportedReasoningEfforts, want)
	}
	if response.ReasoningEffort != "high" || !response.DisableReasoningField {
		t.Fatalf("POST response = effort %q disable %v", response.ReasoningEffort, response.DisableReasoningField)
	}
	saved, err := db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.SupportedReasoningEfforts, []string{"low", "high", "max"}) || !saved.DisableReasoningField || saved.ReasoningEffort != "high" {
		t.Fatalf("persisted model = %+v", saved)
	}

	updated := response
	updated.SupportedReasoningEfforts = nil
	updated.DisableReasoningField = false
	status, response, apiError = requestModelJSON(t, server.Handler(), http.MethodPut, "/api/models/"+model.ID, updated)
	if status != http.StatusOK {
		t.Fatalf("PUT status=%d error=%v", status, apiError)
	}
	saved, err = db.GetModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.SupportedReasoningEfforts) != 0 || saved.SupportedReasoningEfforts == nil || saved.DisableReasoningField {
		t.Fatalf("PUT did not clear the declaration: %#v", saved)
	}
}

func TestModelReasoningCapabilityAPIRejectsInvalidConfig(t *testing.T) {
	_, server := newCredentialAPITestServer(t, nil)
	cases := []struct {
		name   string
		mutate func(*store.Model)
		want   string
	}{
		{"unknown supported tier", func(m *store.Model) { m.SupportedReasoningEfforts = []string{"ultra"} }, "ultra"},
		{"default outside declared tiers", func(m *store.Model) {
			m.SupportedReasoningEfforts, m.ReasoningEffort = []string{"low", "medium"}, "high"
		}, "默认思考档位"},
		{"default outside declared gemini tiers", func(m *store.Model) {
			m.Provider, m.Endpoint, m.ReasoningEffort = "gemini", "/v1beta/models/{model}:streamGenerateContent", "none"
			m.SupportedReasoningEfforts = []string{"low", "high"}
		}, "默认思考档位"},
		{"thinking style not for provider", func(m *store.Model) { m.ThinkingStyle = "budget" }, "思考形态"},
		{"gemini rejects adaptive", func(m *store.Model) {
			m.Provider, m.Endpoint, m.ThinkingStyle = "gemini", "/v1beta/models/{model}:streamGenerateContent", "adaptive"
		}, "思考形态"},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := reasoningAPITestModel("invalid-"+strings.ReplaceAll(tc.name, " ", "-"), "openai")
			model.Name = model.ID + string(rune('a'+index))
			tc.mutate(&model)
			status, _, apiError := requestModelJSON(t, server.Handler(), http.MethodPost, "/api/models", model)
			if status != http.StatusBadRequest || !strings.Contains(apiError["error"], tc.want) {
				t.Fatalf("status=%d error=%v, want 400 containing %q", status, apiError, tc.want)
			}
		})
	}
}

func TestModelThinkingStyleAPINormalizesPerProvider(t *testing.T) {
	db, server := newCredentialAPITestServer(t, nil)
	for _, tc := range []struct{ provider, endpoint, style string }{
		{"anthropic", "/messages", "adaptive"},
		{"anthropic", "/messages", "budget"},
		{"gemini", "/v1beta/models/{model}:streamGenerateContent", "level"},
		{"gemini", "/v1beta/models/{model}:streamGenerateContent", "budget"},
	} {
		model := reasoningAPITestModel("style-"+tc.provider+"-"+tc.style, tc.provider)
		model.Endpoint, model.ThinkingStyle = tc.endpoint, " "+strings.ToUpper(tc.style)+" "
		status, response, apiError := requestModelJSON(t, server.Handler(), http.MethodPost, "/api/models", model)
		if status != http.StatusCreated || response.ThinkingStyle != tc.style {
			t.Fatalf("%s/%s: status=%d style=%q error=%v", tc.provider, tc.style, status, response.ThinkingStyle, apiError)
		}
		saved, err := db.GetModel(model.ID)
		if err != nil || saved.ThinkingStyle != tc.style {
			t.Fatalf("%s/%s persisted style = %q (%v)", tc.provider, tc.style, saved.ThinkingStyle, err)
		}
	}
}
