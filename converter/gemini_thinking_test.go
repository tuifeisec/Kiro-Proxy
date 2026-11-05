package converter

import (
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

// thinkingWant 描述期望下发的 thinkingConfig；budget<0 表示不应发送 thinkingBudget。
type thinkingWant struct {
	budget  int
	level   string
	include bool
}

func assertThinking(t *testing.T, label string, got *GeminiThinkingConfig, want thinkingWant) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: thinkingConfig = nil", label)
	}
	gotBudget := -1
	if got.ThinkingBudget != nil {
		gotBudget = *got.ThinkingBudget
	}
	if gotBudget != want.budget || got.ThinkingLevel != want.level || got.IncludeThoughts != want.include {
		t.Fatalf("%s: got budget=%d level=%q include=%t, want budget=%d level=%q include=%t",
			label, gotBudget, got.ThinkingLevel, got.IncludeThoughts, want.budget, want.level, want.include)
	}
}

func TestResolveGeminiThinkingPerModelAndEffort(t *testing.T) {
	efforts := []string{"none", "low", "medium", "high", "xhigh", "max"}
	cases := []struct {
		model string
		want  []thinkingWant // 与 efforts 一一对应
	}{
		// 2.5 Pro 不能关闭思考，none 发最小值 128；上限 32768。
		{"gemini-2.5-pro", []thinkingWant{{128, "", false}, {1024, "", true}, {4096, "", true}, {8192, "", true}, {16384, "", true}, {32768, "", true}}},
		{"gemini-2.5-flash", []thinkingWant{{0, "", false}, {1024, "", true}, {4096, "", true}, {8192, "", true}, {16384, "", true}, {24576, "", true}}},
		{"gemini-2.5-flash-lite", []thinkingWant{{0, "", false}, {1024, "", true}, {4096, "", true}, {8192, "", true}, {16384, "", true}, {24576, "", true}}},
		// Gemini 3 flash 类有 minimal/low/medium/high。
		{"gemini-3-flash-preview", []thinkingWant{{-1, "minimal", false}, {-1, "low", true}, {-1, "medium", true}, {-1, "high", true}, {-1, "high", true}, {-1, "high", true}}},
		// Gemini 3 pro 类只有 low/high。
		{"gemini-3-pro-preview", []thinkingWant{{-1, "low", false}, {-1, "low", true}, {-1, "high", true}, {-1, "high", true}, {-1, "high", true}, {-1, "high", true}}},
		{"models/gemini-3.1-pro-preview", []thinkingWant{{-1, "low", false}, {-1, "low", true}, {-1, "high", true}, {-1, "high", true}, {-1, "high", true}, {-1, "high", true}}},
		// 无法识别版本：按 2.5 budget 形态，上限 24576。
		{"my-relay-model", []thinkingWant{{0, "", false}, {1024, "", true}, {4096, "", true}, {8192, "", true}, {16384, "", true}, {24576, "", true}}},
	}
	for _, tc := range cases {
		for i, effort := range efforts {
			got, err := ResolveGeminiThinking(tc.model, effort, GeminiRequestOptions{})
			if err != nil {
				t.Fatalf("%s/%s: %v", tc.model, effort, err)
			}
			assertThinking(t, tc.model+"/"+effort, got, tc.want[i])
		}
	}
}

func TestResolveGeminiThinkingStyleOverride(t *testing.T) {
	// 中转改名后模型名无法判断版本，显式 level 覆盖自动判断。
	got, err := ResolveGeminiThinking("relay-flash", "medium", GeminiRequestOptions{ThinkingStyle: "LEVEL"})
	if err != nil {
		t.Fatal(err)
	}
	assertThinking(t, "level override", got, thinkingWant{-1, "medium", true})

	got, err = ResolveGeminiThinking("gemini-3-pro-preview", "high", GeminiRequestOptions{ThinkingStyle: ThinkingStyleBudget})
	if err != nil {
		t.Fatal(err)
	}
	assertThinking(t, "budget override", got, thinkingWant{8192, "", true})

	// 显式覆盖优先于老模型判断：用户确认上游支持时不应被模型名拦截。
	if _, err := ResolveGeminiThinking("gemini-2.0-flash-thinking-exp", "low", GeminiRequestOptions{ThinkingStyle: ThinkingStyleBudget}); err != nil {
		t.Fatalf("explicit budget style should bypass version detection: %v", err)
	}

	if _, err := ResolveGeminiThinking("gemini-2.5-pro", "low", GeminiRequestOptions{ThinkingStyle: ThinkingStyleAdaptive}); err == nil {
		t.Fatal("anthropic-only adaptive style must be rejected for gemini")
	}
}

func TestResolveGeminiThinkingRejectsLegacyModels(t *testing.T) {
	for _, model := range []string{"gemini-2.0-flash", "gemini-1.5-pro", "gemini-2-flash"} {
		_, err := ResolveGeminiThinking(model, "low", GeminiRequestOptions{})
		if err == nil || !strings.Contains(err.Error(), "不支持思考") || !strings.Contains(err.Error(), "清空") {
			t.Fatalf("%s: expected explicit unsupported-thinking error, got %v", model, err)
		}
		// 未配置档位时不构造 thinkingConfig，也不报错。
		got, err := ResolveGeminiThinking(model, "", GeminiRequestOptions{})
		if err != nil || got != nil {
			t.Fatalf("%s without effort: got %+v, %v", model, got, err)
		}
	}
}

func TestGeminiBudgetThinkingClampsToModelRange(t *testing.T) {
	narrow := geminiBudgetRange{off: 0, min: 2048, max: 4096}
	assertThinking(t, "below min", geminiBudgetThinking(narrow, "low"), thinkingWant{2048, "", true})
	assertThinking(t, "above max", geminiBudgetThinking(narrow, "xhigh"), thinkingWant{4096, "", true})
}

func TestValidateProviderReasoningEffortGeminiAcceptsAllEfforts(t *testing.T) {
	// 调用方传入的是 NormalizeReasoningEffort 规范化后的小写档位
	for _, effort := range []string{"none", "low", "medium", "high", "xhigh", "max"} {
		if err := ValidateProviderReasoningEffort("gemini", effort); err != nil {
			t.Fatalf("%s: %v", effort, err)
		}
	}
	if err := ValidateProviderReasoningEffort("gemini", "extreme"); err == nil {
		t.Fatal("unknown effort must be rejected")
	}
}

func TestKiroToGeminiEmitsExplicitThinkingConfig(t *testing.T) {
	conv := &ParsedConversation{CurrentContent: "hi"}
	cases := []struct {
		model, effort string
		want          string
		absent        []string
	}{
		// thinkingBudget=0 必须显式发出，且关闭思考时不能带 includeThoughts。
		{"gemini-2.5-flash", "none", `"thinkingConfig":{"thinkingBudget":0}`, []string{"includeThoughts"}},
		{"gemini-2.5-pro", "max", `"thinkingConfig":{"thinkingBudget":32768,"includeThoughts":true}`, []string{"thinkingLevel"}},
		{"gemini-3-flash-preview", "medium", `"thinkingConfig":{"thinkingLevel":"medium","includeThoughts":true}`, []string{"thinkingBudget"}},
	}
	for _, tc := range cases {
		body, err := KiroToGemini(conv, tc.model, tc.effort, GeminiRequestOptions{})
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.model, tc.effort, err)
		}
		if !strings.Contains(string(body), tc.want) {
			t.Fatalf("%s/%s: body missing %s: %s", tc.model, tc.effort, tc.want, body)
		}
		for _, field := range tc.absent {
			if strings.Contains(string(body), field) {
				t.Fatalf("%s/%s: body should not contain %s: %s", tc.model, tc.effort, field, body)
			}
		}
	}

	body, err := KiroToGemini(conv, "gemini-2.5-pro", "", GeminiRequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var req GeminiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if req.GenerationConfig != nil {
		t.Fatalf("empty effort should leave generationConfig unset: %s", body)
	}

	if _, err := KiroToGemini(conv, "gemini-2.0-flash", "high", GeminiRequestOptions{}); err == nil {
		t.Fatal("legacy model with effort must fail at construction")
	}
}
