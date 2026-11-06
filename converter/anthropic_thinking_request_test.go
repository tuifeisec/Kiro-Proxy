package converter

import (
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

// TestKiroToAnthropicThinkingFinalJSON 断言 Kiro 主路径最终请求体中的 thinking、output_config 与 max_tokens。
func TestKiroToAnthropicThinkingFinalJSON(t *testing.T) {
	cases := []struct {
		name, model, effort string
		maxTokens           int
		opts                AnthropicRequestOptions
		thinking, output    string // 空串表示该字段不应出现
		wantMaxTokens       string
	}{
		{name: "4.7 adaptive 带 summarized", model: "claude-opus-4-7", effort: "high",
			thinking: `{"type":"adaptive","display":"summarized"}`, output: `{"effort":"high"}`, wantMaxTokens: "16384"},
		{name: "4.6 xhigh 改发 max", model: "claude-sonnet-4-6", effort: "xhigh",
			thinking: `{"type":"adaptive","display":"summarized"}`, output: `{"effort":"max"}`, wantMaxTokens: "16384"},
		{name: "4.5 max 夹到默认 max_tokens", model: "claude-sonnet-4-5", effort: "max",
			thinking: `{"type":"enabled","budget_tokens":15360}`, wantMaxTokens: "16384"},
		{name: "4.5 使用 Kiro maxTokens 夹紧", model: "claude-sonnet-4-5", effort: "high", maxTokens: 6000,
			thinking: `{"type":"enabled","budget_tokens":4976}`, wantMaxTokens: "6000"},
		// Kiro 不发 maxTokens（抓包确认），由模型配置的最大输出决定 max_tokens，xhigh 与 max 不再被同一个默认值夹平
		{name: "4.5 xhigh 使用模型配置的最大输出", model: "claude-sonnet-4-5", effort: "xhigh", opts: AnthropicRequestOptions{MaxTokens: 64000},
			thinking: `{"type":"enabled","budget_tokens":16384}`, wantMaxTokens: "64000"},
		{name: "4.5 max 使用模型配置的最大输出", model: "claude-sonnet-4-5", effort: "max", opts: AnthropicRequestOptions{MaxTokens: 64000},
			thinking: `{"type":"enabled","budget_tokens":32000}`, wantMaxTokens: "64000"},
		{name: "请求的 maxTokens 优先于模型配置", model: "claude-sonnet-4-5", effort: "high", maxTokens: 6000, opts: AnthropicRequestOptions{MaxTokens: 64000},
			thinking: `{"type":"enabled","budget_tokens":4976}`, wantMaxTokens: "6000"},
		{name: "deepseek 官方 host 带三档 effort", model: "deepseek-chat", effort: "medium", opts: AnthropicRequestOptions{UpstreamHost: "api.deepseek.com"},
			thinking: `{"type":"enabled","budget_tokens":4096}`, output: `{"effort":"high"}`, wantMaxTokens: "16384"},
		{name: "4.7 显式 budget 覆盖", model: "claude-opus-4-7", effort: "low", opts: AnthropicRequestOptions{ThinkingStyle: ThinkingStyleBudget},
			thinking: `{"type":"enabled","budget_tokens":1024}`, wantMaxTokens: "16384"},
		{name: "sonnet 5.5 none 发 between_tools", model: "claude-sonnet-5-5", effort: "none",
			thinking: `{"type":"between_tools"}`, wantMaxTokens: "16384"},
		{name: "opus 5.5 none 不写 thinking", model: "claude-opus-5-5", effort: "none",
			output: `{"effort":"low"}`, wantMaxTokens: "16384"},
		{name: "未请求档位不写思考参数", model: "claude-opus-4-7", wantMaxTokens: "16384"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conv := &ParsedConversation{ModelID: tc.model, CurrentContent: "hello", MaxTokens: tc.maxTokens}
			body, err := KiroToAnthropic(conv, "", tc.effort, tc.opts)
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			var root map[string]json.RawMessage
			if err := json.Unmarshal(body, &root); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			assertRawField(t, root, "thinking", tc.thinking)
			assertRawField(t, root, "output_config", tc.output)
			assertRawField(t, root, "max_tokens", tc.wantMaxTokens)
		})
	}
}

func TestKiroToAnthropicRejectsBudgetThatDoesNotFitMaxTokens(t *testing.T) {
	conv := &ParsedConversation{ModelID: "claude-sonnet-4-5", CurrentContent: "hello", MaxTokens: 1500}
	body, err := KiroToAnthropic(conv, "", "low", AnthropicRequestOptions{})
	if err == nil || !strings.Contains(err.Error(), "max_tokens=1500") {
		t.Fatalf("want max_tokens error, got body=%s err=%v", body, err)
	}
}

func assertRawField(t *testing.T, root map[string]json.RawMessage, field, want string) {
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
