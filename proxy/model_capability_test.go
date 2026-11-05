package proxy

import (
	"strings"
	"testing"
	"time"

	"kiro-proxy/store"
)

func capabilityTestModel() *store.Model {
	return &store.Model{ID: "cap-model", Name: "cap", ModelID: "cap-upstream", Provider: "openai", UpdatedAt: "2026-10-01T00:00:00Z"}
}

func TestModelCapabilityStoreExpiresAfterTTL(t *testing.T) {
	caps := newModelCapabilityStore()
	model := capabilityTestModel()
	now := time.Unix(1_800_000_000, 0)
	if !caps.observe(model, compatProtocolChat, capabilityTokenField, "max_completion_tokens", "Use 'max_completion_tokens' instead", now) {
		t.Fatal("observation was not recorded")
	}
	if got, ok := caps.lookup(model, compatProtocolChat, capabilityTokenField, now.Add(modelCapabilityTTL-time.Second)); !ok || got.value != "max_completion_tokens" || got.evidence == "" {
		t.Fatalf("observation before TTL = %+v ok=%v", got, ok)
	}
	if _, ok := caps.lookup(model, compatProtocolChat, capabilityTokenField, now.Add(modelCapabilityTTL)); ok {
		t.Fatal("observation must expire at TTL")
	}
}

func TestModelCapabilityStoreInvalidatesEditedModel(t *testing.T) {
	caps := newModelCapabilityStore()
	model := capabilityTestModel()
	now := time.Now()
	caps.observe(model, compatProtocolChat, capabilitySampling, compatSamplingOmitted, "temperature is deprecated for this model", now)
	edited := *model
	edited.UpdatedAt = "2026-10-02T00:00:00Z"
	if _, ok := caps.lookup(&edited, compatProtocolChat, capabilitySampling, now); ok {
		t.Fatal("editing the model must invalidate its observations")
	}
	if _, ok := caps.lookup(model, compatProtocolChat, capabilitySampling, now); !ok {
		t.Fatal("unedited key lost its observation")
	}
}

func TestModelCapabilityStoreScopesByProtocolAndKind(t *testing.T) {
	caps := newModelCapabilityStore()
	model := capabilityTestModel()
	now := time.Now()
	caps.observe(model, compatProtocolChat, capabilitySampling, compatSamplingOmitted, "evidence", now)
	if _, ok := caps.lookup(model, compatProtocolResponses, capabilitySampling, now); ok {
		t.Fatal("chat observation leaked into responses protocol")
	}
	if _, ok := caps.lookup(model, compatProtocolChat, capabilityTokenField, now); ok {
		t.Fatal("observation leaked into another kind")
	}
}

func TestModelCapabilityStoreConfigurationWins(t *testing.T) {
	caps := newModelCapabilityStore()
	model := capabilityTestModel()
	model.SupportedReasoningEfforts = []string{"low", "medium"}
	now := time.Now()
	if caps.observe(model, compatProtocolChat, capabilityReasoningEffort, "low|medium", "evidence", now) {
		t.Fatal("configured capability must not record observations")
	}
	if _, ok := caps.lookup(model, compatProtocolChat, capabilityReasoningEffort, now); ok {
		t.Fatal("configured capability must not return observations")
	}
	if !caps.observe(model, compatProtocolChat, capabilitySampling, compatSamplingOmitted, "evidence", now) {
		t.Fatal("unconfigured capability should still be observed")
	}
}

func TestModelCapabilityConfiguredFollowsModelFields(t *testing.T) {
	plain := capabilityTestModel()
	for _, kind := range []modelCapabilityKind{capabilityDeveloperRole, capabilityTokenField, capabilitySampling, capabilityThinkingEnabled, capabilityThinkingAdaptive, capabilityThinkingDisplay, capabilityReasoningEffort, capabilityReasoningField, capabilityContinuation} {
		if modelCapabilityConfigured(plain, kind) {
			t.Fatalf("%s is configured on a model without overrides", kind)
		}
	}
	declared := capabilityTestModel()
	declared.SupportedReasoningEfforts = []string{"low", "high"}
	disabled := capabilityTestModel()
	disabled.DisableReasoningField = true
	styled := capabilityTestModel()
	styled.ThinkingStyle = "budget"
	cases := []struct {
		model *store.Model
		kinds []modelCapabilityKind
	}{
		{declared, []modelCapabilityKind{capabilityReasoningEffort, capabilityReasoningField}},
		{disabled, []modelCapabilityKind{capabilityReasoningEffort, capabilityReasoningField}},
		{styled, []modelCapabilityKind{capabilityThinkingEnabled, capabilityThinkingAdaptive}},
	}
	for _, tc := range cases {
		for _, kind := range tc.kinds {
			if !modelCapabilityConfigured(tc.model, kind) {
				t.Fatalf("%s should be decided by configuration", kind)
			}
		}
		if modelCapabilityConfigured(tc.model, capabilityThinkingDisplay) || modelCapabilityConfigured(tc.model, capabilitySampling) {
			t.Fatal("unrelated capabilities must stay observed")
		}
	}
}

func TestCompatEvidenceRedactsSecretsAndBoundsLength(t *testing.T) {
	evidence := compatEvidence("Unsupported parameter with key sk-abcdefghijklmnop and Bearer abcdef123456\n" + strings.Repeat("x", 400))
	if strings.Contains(evidence, "sk-abcdefghijklmnop") || strings.Contains(evidence, "abcdef123456") || strings.Contains(evidence, "\n") {
		t.Fatalf("evidence leaked a credential or newline: %q", evidence)
	}
	if len([]rune(evidence)) > compatEvidenceMaxRunes+3 {
		t.Fatalf("evidence length = %d", len([]rune(evidence)))
	}
}
