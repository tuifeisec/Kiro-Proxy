package converter

import (
	"reflect"
	"strings"
	"testing"
)

func TestFitReasoningEffortNearestTier(t *testing.T) {
	cases := []struct {
		name    string
		want    string
		allowed []string
		fit     string
	}{
		{"exact match", "high", []string{"low", "high"}, "high"},
		{"down to nearest", "xhigh", []string{"none", "low", "medium", "high"}, "high"},
		{"max far down", "max", []string{"low", "medium"}, "medium"},
		{"tie prefers stronger", "medium", []string{"low", "high"}, "high"},
		{"up to nearest", "low", []string{"medium", "high"}, "medium"},
		{"non-none skips none", "low", []string{"none", "high"}, "high"},
		{"none to nearest when none missing", "none", []string{"low", "medium", "high"}, "low"},
		{"none kept", "none", []string{"none", "low"}, "none"},
		{"empty stays empty", "", []string{"low"}, ""},
		{"case normalized", " HIGH ", []string{"high"}, "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FitReasoningEffort(tc.want, tc.allowed)
			if err != nil {
				t.Fatalf("FitReasoningEffort(%q, %v) error: %v", tc.want, tc.allowed, err)
			}
			if got != tc.fit {
				t.Fatalf("FitReasoningEffort(%q, %v) = %q, want %q", tc.want, tc.allowed, got, tc.fit)
			}
		})
	}
}

func TestFitReasoningEffortRejectsUnmappable(t *testing.T) {
	if _, err := FitReasoningEffort("high", []string{"none"}); err == nil || !strings.Contains(err.Error(), "无法映射") {
		t.Fatalf("only-none allowed must reject non-none want, got %v", err)
	}
	if _, err := FitReasoningEffort("ultra", []string{"high"}); err == nil {
		t.Fatal("unknown want must be rejected")
	}
	if _, err := FitReasoningEffort("high", nil); err == nil {
		t.Fatal("empty allowed set must reject a non-empty want")
	}
}

func TestNormalizeSupportedReasoningEffortsSortsDedupesAndValidates(t *testing.T) {
	got, err := NormalizeSupportedReasoningEfforts("openai", []string{"max", " High ", "low", "high", "none"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"none", "low", "high", "max"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized = %v, want %v", got, want)
	}
	if _, err := NormalizeSupportedReasoningEfforts("anthropic", []string{"ultra"}); err == nil {
		t.Fatal("anthropic must reject an unknown tier")
	}
	if _, err := NormalizeSupportedReasoningEfforts("gemini", []string{"minimal"}); err == nil {
		t.Fatal("gemini must reject a tier outside the shared order")
	}
	if _, err := NormalizeSupportedReasoningEfforts("openai", []string{""}); err == nil {
		t.Fatal("empty tier must be rejected")
	}
	empty, err := NormalizeSupportedReasoningEfforts("openai", nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("undeclared list must normalize to an empty slice, got %#v, %v", empty, err)
	}
}

func TestModelReasoningEffortsFallsBackToProviderTable(t *testing.T) {
	cases := map[string][]string{
		"openai":    {"none", "low", "medium", "high", "xhigh", "max"},
		"codex":     {"none", "low", "medium", "high", "xhigh", "max"},
		"":          {"none", "low", "medium", "high", "xhigh", "max"},
		"anthropic": {"none", "low", "medium", "high", "xhigh", "max"},
		"gemini":    {"none", "low", "medium", "high", "xhigh", "max"},
	}
	for provider, want := range cases {
		got, err := ModelReasoningEfforts(provider, nil)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("provider %q effective = %v (%v), want %v", provider, got, err, want)
		}
	}
	declared, err := ModelReasoningEfforts("openai", []string{"high", "low"})
	if err != nil || !reflect.DeepEqual(declared, []string{"low", "high"}) {
		t.Fatalf("declared effective = %v (%v)", declared, err)
	}
	if _, err := ModelReasoningEfforts("gemini", []string{"ultra"}); err == nil {
		t.Fatal("stored declaration outside the provider table must surface an error")
	}
}

func TestProviderReasoningTablesMatchConverterMappings(t *testing.T) {
	// 档位表是唯一来源；若扩展了表而未补转换映射，这里会立即失败。
	for _, effort := range ProviderReasoningEfforts("anthropic") {
		for _, model := range []string{"claude-sonnet-4-5", "claude-opus-4-7"} {
			if _, err := BuildAnthropicThinking(AnthropicThinkingInput{ModelID: model, Effort: effort, MaxTokens: 64000}); err != nil {
				t.Fatalf("anthropic table tier %q has no thinking mapping for %s: %v", effort, model, err)
			}
		}
	}
	for _, effort := range ProviderReasoningEfforts("gemini") {
		for _, model := range []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-3-pro", "gemini-3-flash"} {
			if config, err := ResolveGeminiThinking(model, effort, GeminiRequestOptions{}); err != nil || config == nil {
				t.Fatalf("gemini table tier %q has no thinking mapping for %s: %v", effort, model, err)
			}
		}
	}
	if err := ValidateProviderReasoningEffort("openai", "max"); err != nil {
		t.Fatalf("openai max must be expressible: %v", err)
	}
}

func TestModelWireReasoningEffortFitsDeclaredTiers(t *testing.T) {
	cases := []struct {
		name     string
		declared []string
		disabled bool
		want     string
		wire     string
		wantErr  bool
	}{
		{"provider table keeps tier", nil, false, "xhigh", "xhigh", false},
		{"declared tiers fit nearest stronger", []string{"low", "high"}, false, "medium", "high", false},
		{"only none cannot carry a request", []string{"none"}, false, "xhigh", "", true},
		{"disabled field sends nothing", []string{"none"}, true, "xhigh", "", false},
		{"empty request sends nothing", nil, false, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := ModelWireReasoningEffort("anthropic", tc.declared, tc.disabled, tc.want)
			if (err != nil) != tc.wantErr || wire != tc.wire {
				t.Fatalf("wire=%q err=%v, want wire=%q err=%v", wire, err, tc.wire, tc.wantErr)
			}
		})
	}
}
