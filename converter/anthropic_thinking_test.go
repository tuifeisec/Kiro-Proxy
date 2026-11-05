package converter

import (
	"fmt"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

// anthropicThinkingEfforts 是表驱动用例 want 数组对应的档位顺序。
var anthropicThinkingEfforts = [6]string{"none", "low", "medium", "high", "xhigh", "max"}

const (
	// 两条调用路径在未给 max_tokens 时都补 16384，表中以此代表"未给 max_tokens"。
	testAnthropicDefaultMaxTokens = 16384
	testRelayHost                 = "relay.example.com"

	wantThinkingDisabled     = `{"mode":"disabled","thinking":{"type":"disabled"}}`
	wantThinkingBetweenTools = `{"mode":"between_tools","thinking":{"type":"between_tools"}}`
	wantThinkingAlwaysOn     = `{"mode":"always_on","output_config":{"effort":"low"}}`
	wantThinkingError        = "error"
)

func wantBudget(n int) string {
	return fmt.Sprintf(`{"mode":"budget","thinking":{"type":"enabled","budget_tokens":%d}}`, n)
}

func wantBudgetEffort(n int, effort string) string {
	return fmt.Sprintf(`{"mode":"budget","thinking":{"type":"enabled","budget_tokens":%d},"output_config":{"effort":%q}}`, n, effort)
}

func wantAdaptive(effort string) string {
	return fmt.Sprintf(`{"mode":"adaptive","thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":%q}}`, effort)
}

// defaultBudgetRow：默认 max_tokens 下 budget 形态，xhigh/max 被夹到 16384-1024=15360。
func defaultBudgetRow(off string) [6]string {
	return [6]string{off, wantBudget(1024), wantBudget(4096), wantBudget(8192), wantBudget(15360), wantBudget(15360)}
}

func adaptiveRow(off, xhigh string) [6]string {
	return [6]string{off, wantAdaptive("low"), wantAdaptive("medium"), wantAdaptive("high"), wantAdaptive(xhigh), wantAdaptive("max")}
}

// outputEffortRow：忽略 budget 的非 Claude 模型，budget 之外原样带 output_config.effort。
func outputEffortRow() [6]string {
	return [6]string{wantThinkingDisabled, wantBudgetEffort(1024, "low"), wantBudgetEffort(4096, "medium"),
		wantBudgetEffort(8192, "high"), wantBudgetEffort(15360, "xhigh"), wantBudgetEffort(15360, "max")}
}

func summarizeAnthropicThinking(t *testing.T, p AnthropicThinkingParams) string {
	t.Helper()
	b, err := json.Marshal(struct {
		Mode         string                 `json:"mode"`
		Thinking     *AnthropicThinking     `json:"thinking,omitempty"`
		OutputConfig *AnthropicOutputConfig `json:"output_config,omitempty"`
	}{p.Mode, p.Thinking, p.OutputConfig})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return string(b)
}

func TestBuildAnthropicThinkingMatrix(t *testing.T) {
	const defMax = testAnthropicDefaultMaxTokens
	errRow := [6]string{wantThinkingDisabled, wantThinkingError, wantThinkingError, wantThinkingError, wantThinkingError, wantThinkingError}
	cases := []struct {
		name, model, host, style string
		maxTokens                int
		want                     [6]string
	}{
		// Claude 4.6 以下：budget
		{name: "claude 3.7 旧顺序", model: "claude-3-7-sonnet-20250219", maxTokens: defMax, want: defaultBudgetRow(wantThinkingDisabled)},
		{name: "claude 4.5 日期后缀", model: "claude-sonnet-4-5-20250929", maxTokens: defMax, want: defaultBudgetRow(wantThinkingDisabled)},
		{name: "claude 4.5 Bedrock 前缀", model: "anthropic.claude-sonnet-4-5-20250929-v1:0", maxTokens: defMax, want: defaultBudgetRow(wantThinkingDisabled)},
		{name: "claude 4.1 max_tokens 充足不夹紧", model: "claude-opus-4-1", maxTokens: 64000,
			want: [6]string{wantThinkingDisabled, wantBudget(1024), wantBudget(4096), wantBudget(8192), wantBudget(16384), wantBudget(32000)}},
		// Claude 4.6 起：adaptive
		{name: "claude 4.6 xhigh 改发 max", model: "claude-opus-4-6", maxTokens: defMax, want: adaptiveRow(wantThinkingDisabled, "max")},
		{name: "claude 4.6 中转前缀与后缀", model: "kiro/claude-sonnet-4.6[1m]", maxTokens: defMax, want: adaptiveRow(wantThinkingDisabled, "max")},
		{name: "claude 4.7", model: "claude-opus-4-7", maxTokens: defMax, want: adaptiveRow(wantThinkingDisabled, "xhigh")},
		{name: "opus 5", model: "claude-opus-5", maxTokens: defMax, want: adaptiveRow(wantThinkingDisabled, "xhigh")},
		{name: "sonnet 5", model: "claude-sonnet-5", maxTokens: defMax, want: adaptiveRow(wantThinkingDisabled, "xhigh")},
		{name: "haiku 5.5", model: "claude-haiku-5-5", maxTokens: defMax, want: adaptiveRow(wantThinkingDisabled, "xhigh")},
		{name: "sonnet 5.5 关闭发 between_tools", model: "claude-sonnet-5-5-20261001", maxTokens: defMax, want: adaptiveRow(wantThinkingBetweenTools, "xhigh")},
		{name: "opus 5.5 中转改名不能关闭", model: "opus-5.5", maxTokens: defMax, want: adaptiveRow(wantThinkingAlwaysOn, "xhigh")},
		{name: "fable 5.1 不能关闭", model: "claude-fable-5-1", maxTokens: defMax, want: adaptiveRow(wantThinkingAlwaysOn, "xhigh")},
		{name: "mythos 5 Vertex 后缀不能关闭", model: "claude-mythos-5@20261101", maxTokens: defMax, want: adaptiveRow(wantThinkingAlwaysOn, "xhigh")},
		// 非 Claude 的 Anthropic 兼容端点：budget，仅特定上游额外带 output_config.effort
		{name: "deepseek 官方 host 就近三档", model: "deepseek-chat", host: "api.deepseek.com", maxTokens: defMax,
			want: [6]string{wantThinkingDisabled, wantBudgetEffort(1024, "low"), wantBudgetEffort(4096, "high"),
				wantBudgetEffort(8192, "high"), wantBudgetEffort(15360, "max"), wantBudgetEffort(15360, "max")}},
		{name: "deepseek 其他 host 不带 output_config", model: "deepseek-chat", host: testRelayHost, maxTokens: defMax, want: defaultBudgetRow(wantThinkingDisabled)},
		{name: "glm-5.2", model: "glm-5.2", maxTokens: defMax, want: outputEffortRow()},
		{name: "GLM-5.3 大写带前缀", model: "zai/GLM-5.3", maxTokens: defMax, want: outputEffortRow()},
		{name: "minimax m3.1 flash", model: "MiniMax-M3.1-Flash", maxTokens: defMax, want: outputEffortRow()},
		{name: "gpt 数字", model: "gpt-6.1", host: testRelayHost, maxTokens: defMax, want: outputEffortRow()},
		{name: "未知模型", model: "my-custom-model", host: testRelayHost, maxTokens: defMax, want: defaultBudgetRow(wantThinkingDisabled)},
		{name: "无版本 claude 别名按未知处理", model: "claude-sonnet-latest", maxTokens: defMax, want: defaultBudgetRow(wantThinkingDisabled)},
		// max_tokens：budget 必须 < max_tokens，夹到 max_tokens-1024，低于 1024 报错
		{name: "max_tokens 偏小夹紧", model: "claude-sonnet-4-5", maxTokens: 4096,
			want: [6]string{wantThinkingDisabled, wantBudget(1024), wantBudget(3072), wantBudget(3072), wantBudget(3072), wantBudget(3072)}},
		{name: "max_tokens 恰好容纳 high", model: "claude-sonnet-4-5", maxTokens: 9216,
			want: [6]string{wantThinkingDisabled, wantBudget(1024), wantBudget(4096), wantBudget(8192), wantBudget(8192), wantBudget(8192)}},
		{name: "max_tokens 最小可用边界", model: "claude-sonnet-4-5", maxTokens: 2048,
			want: [6]string{wantThinkingDisabled, wantBudget(1024), wantBudget(1024), wantBudget(1024), wantBudget(1024), wantBudget(1024)}},
		{name: "max_tokens 低于边界报错", model: "claude-sonnet-4-5", maxTokens: 2047, want: errRow},
		{name: "max_tokens 未补默认报错", model: "claude-sonnet-4-5", maxTokens: 0, want: errRow},
		{name: "非 Claude 同样校验 max_tokens", model: "deepseek-chat", host: "api.deepseek.com", maxTokens: 1500, want: errRow},
		{name: "adaptive 不受 max_tokens 约束", model: "claude-opus-4-7", maxTokens: 1024, want: adaptiveRow(wantThinkingDisabled, "xhigh")},
		// ThinkingStyle 显式覆盖
		{name: "改名中转显式 adaptive", model: "relay-claude", style: ThinkingStyleAdaptive, maxTokens: defMax, want: adaptiveRow(wantThinkingDisabled, "xhigh")},
		{name: "4.6 显式 budget", model: "claude-opus-4-6", style: ThinkingStyleBudget, maxTokens: defMax, want: defaultBudgetRow(wantThinkingDisabled)},
		{name: "4.5 显式 adaptive 保留 xhigh", model: "claude-sonnet-4-5", style: ThinkingStyleAdaptive, maxTokens: defMax, want: adaptiveRow(wantThinkingDisabled, "xhigh")},
		{name: "deepseek 显式 adaptive 仍就近三档", model: "deepseek-chat", host: "api.deepseek.com", style: ThinkingStyleAdaptive, maxTokens: defMax,
			want: [6]string{wantThinkingDisabled, wantAdaptive("low"), wantAdaptive("high"), wantAdaptive("high"), wantAdaptive("max"), wantAdaptive("max")}},
		{name: "opus 5.5 显式 budget 时 none 仍按模型规则", model: "opus-5.5", style: ThinkingStyleBudget, maxTokens: defMax, want: defaultBudgetRow(wantThinkingAlwaysOn)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i, effort := range anthropicThinkingEfforts {
				got, err := BuildAnthropicThinking(AnthropicThinkingInput{
					ModelID: tc.model, UpstreamHost: tc.host, ThinkingStyle: tc.style, Effort: effort, MaxTokens: tc.maxTokens,
				})
				want := tc.want[i]
				if want == wantThinkingError {
					if err == nil || !strings.Contains(err.Error(), "放不下思考强度") {
						t.Fatalf("effort=%s: want max_tokens error, got params=%+v err=%v", effort, got, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("effort=%s: unexpected error: %v", effort, err)
				}
				if s := summarizeAnthropicThinking(t, got); s != want {
					t.Fatalf("effort=%s:\n got %s\nwant %s", effort, s, want)
				}
			}
		})
	}
}

func TestBuildAnthropicThinkingWithoutEffortWritesNothing(t *testing.T) {
	for _, model := range []string{"claude-opus-4-7", "claude-sonnet-4-5", "opus-5.5", "deepseek-chat"} {
		for _, style := range []string{ThinkingStyleAuto, ThinkingStyleAdaptive, ThinkingStyleBudget} {
			got, err := BuildAnthropicThinking(AnthropicThinkingInput{ModelID: model, UpstreamHost: "api.deepseek.com", ThinkingStyle: style, MaxTokens: 0})
			if err != nil || got.Thinking != nil || got.OutputConfig != nil || got.Mode != AnthropicThinkingModeUnset {
				t.Fatalf("model=%s style=%q: params=%+v err=%v", model, style, got, err)
			}
		}
	}
}

func TestBuildAnthropicThinkingNormalizesAndRejectsInput(t *testing.T) {
	got, err := BuildAnthropicThinking(AnthropicThinkingInput{ModelID: "claude-opus-4-7", ThinkingStyle: " Budget ", Effort: " HIGH ", MaxTokens: testAnthropicDefaultMaxTokens})
	if err != nil || summarizeAnthropicThinking(t, got) != wantBudget(8192) {
		t.Fatalf("normalized input: params=%+v err=%v", got, err)
	}
	for name, in := range map[string]AnthropicThinkingInput{
		"未知档位":        {ModelID: "claude-opus-4-7", Effort: "minimal", MaxTokens: testAnthropicDefaultMaxTokens},
		"gemini 专用形态": {ModelID: "claude-opus-4-7", ThinkingStyle: ThinkingStyleLevel, Effort: "high", MaxTokens: testAnthropicDefaultMaxTokens},
		"无效形态即使未请求档位也报错": {ModelID: "claude-opus-4-7", ThinkingStyle: "bogus", MaxTokens: testAnthropicDefaultMaxTokens},
	} {
		if got, err := BuildAnthropicThinking(in); err == nil {
			t.Fatalf("%s: want error, got %+v", name, got)
		}
	}
}

func TestValidateProviderReasoningEffortAnthropicAcceptsAllEfforts(t *testing.T) {
	for _, effort := range append([]string{""}, anthropicThinkingEfforts[:]...) {
		if err := ValidateProviderReasoningEffort("anthropic", effort); err != nil {
			t.Fatalf("effort %q rejected: %v", effort, err)
		}
	}
	if err := ValidateProviderReasoningEffort("anthropic", "minimal"); err == nil {
		t.Fatal("unknown effort must be rejected")
	}
}
