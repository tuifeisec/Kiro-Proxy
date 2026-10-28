package proxy

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"kiro-proxy/store"
)

func TestSetResponsesStoreAndServiceTierMatchesLegacyPipeline(t *testing.T) {
	modelFast := &store.Model{Provider: "openai", ServiceTier: "fast"}
	modelStandard := &store.Model{Provider: "openai", ServiceTier: ""}
	cases := []struct {
		name           string
		body           string
		storeValue     bool
		preserveNumber bool
		model          *store.Model
	}{
		{
			name:           "missing tier and precise fields",
			body:           `{"model":"gpt-test","input":[{"type":"message","content":{"large":9007199254740993,"decimal":1.0}}],"prompt_cache_key":"client-key","extensions":{"keep":{"array":[3,2,1]}}}`,
			storeValue:     true,
			preserveNumber: true,
			model:          modelFast,
		},
		{
			name:           "null tier",
			body:           `{"model":"gpt-test","service_tier":null,"input":[{"type":"message","content":"hello"}]}`,
			storeValue:     false,
			preserveNumber: true,
			model:          modelFast,
		},
		{
			name:           "default tier",
			body:           `{"model":"gpt-test","service_tier":" Default ","input":[]}`,
			storeValue:     false,
			preserveNumber: true,
			model:          modelFast,
		},
		{
			name:           "explicit tiers stay unchanged",
			body:           `{"model":"gpt-test","service_tier":"flex","input":[]}`,
			storeValue:     true,
			preserveNumber: true,
			model:          modelFast,
		},
		{
			name:           "empty tier stays unchanged",
			body:           `{"model":"gpt-test","service_tier":"","input":[]}`,
			storeValue:     true,
			preserveNumber: true,
			model:          modelFast,
		},
		{
			name:           "non string tier stays unchanged",
			body:           `{"model":"gpt-test","service_tier":123,"input":[]}`,
			storeValue:     true,
			preserveNumber: true,
			model:          modelFast,
		},
		{
			name:           "standard model does not add tier",
			body:           `{"model":"gpt-test","prompt_cache_key":"stable","input":[]}`,
			storeValue:     false,
			preserveNumber: true,
			model:          modelStandard,
		},
		{
			name:           "legacy number normalization remains",
			body:           `{"model":"gpt-test","input":[{"type":"message","content":{"large":9007199254740993,"decimal":1.0}}]}`,
			storeValue:     false,
			preserveNumber: false,
			model:          modelFast,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			legacy := applyConfiguredResponsesServiceTier(setResponsesStore([]byte(tc.body), tc.storeValue, tc.preserveNumber), tc.model)
			got := setResponsesStoreAndServiceTier([]byte(tc.body), tc.storeValue, tc.preserveNumber, tc.model)
			if !bytes.Equal(got, legacy) {
				t.Fatalf("combined mutation changed body:\n got: %s\nwant: %s", got, legacy)
			}
		})
	}
}

func TestSetResponsesStoreAndServiceTierRejectsTrailingDocument(t *testing.T) {
	body := []byte(`{"model":"gpt-test","input":[]} {"ignored":true}`)
	got := setResponsesStoreAndServiceTier(body, false, true, &store.Model{Provider: "openai", ServiceTier: "fast"})
	if !bytes.Equal(got, body) {
		t.Fatalf("trailing JSON was rewritten: got=%s want=%s", got, body)
	}
}

func BenchmarkResponsesEnvelopeMutation(b *testing.B) {
	model := &store.Model{Provider: "openai", ServiceTier: "fast"}
	for _, size := range []int{256 << 10, 1 << 20} {
		body := []byte(fmt.Sprintf(`{"model":"gpt-test","input":[{"type":"message","content":"%s"}],"prompt_cache_key":"stable"}`, strings.Repeat("x", size)))
		b.Run(fmt.Sprintf("legacy/%dKiB", size>>10), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for i := 0; i < b.N; i++ {
				got := applyConfiguredResponsesServiceTier(setResponsesStore(body, true, true), model)
				if len(got) == 0 {
					b.Fatal("legacy mutation returned empty body")
				}
			}
		})
		b.Run(fmt.Sprintf("combined/%dKiB", size>>10), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for i := 0; i < b.N; i++ {
				got := setResponsesStoreAndServiceTier(body, true, true, model)
				if len(got) == 0 {
					b.Fatal("combined mutation returned empty body")
				}
			}
		})
	}
}
