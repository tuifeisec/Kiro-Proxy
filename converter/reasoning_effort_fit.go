package converter

import (
	"fmt"
	"strings"
)

// 思考档位按强度从弱到强排列；就近映射的"距离"即两档在此序列中的下标差。
const (
	ReasoningEffortNone   = "none"
	ReasoningEffortLow    = "low"
	ReasoningEffortMedium = "medium"
	ReasoningEffortHigh   = "high"
	ReasoningEffortXHigh  = "xhigh"
	ReasoningEffortMax    = "max"
)

var reasoningEffortOrder = []string{
	ReasoningEffortNone, ReasoningEffortLow, ReasoningEffortMedium,
	ReasoningEffortHigh, ReasoningEffortXHigh, ReasoningEffortMax,
}

// provider 默认档位表是本地转换器可表达档位的唯一定义；后续扩展 anthropic/gemini
// 只改这里，校验、就近映射和分组候选判断都从此派生。前端
// frontend/src/utils/reasoningEffort.js 必须保持一致。
var (
	openAIReasoningEfforts = []string{
		ReasoningEffortNone, ReasoningEffortLow, ReasoningEffortMedium,
		ReasoningEffortHigh, ReasoningEffortXHigh, ReasoningEffortMax,
	}
	anthropicReasoningEfforts = []string{
		ReasoningEffortNone, ReasoningEffortLow, ReasoningEffortMedium,
		ReasoningEffortHigh, ReasoningEffortXHigh, ReasoningEffortMax,
	}
	geminiReasoningEfforts = []string{
		ReasoningEffortNone, ReasoningEffortLow, ReasoningEffortMedium,
		ReasoningEffortHigh, ReasoningEffortXHigh, ReasoningEffortMax,
	}
)

func reasoningEffortRank(effort string) int {
	for index, value := range reasoningEffortOrder {
		if value == effort {
			return index
		}
	}
	return -1
}

// ProviderReasoningEfforts 返回 provider 默认档位表的副本。openai、codex 与其他
// 按 OpenAI 兼容格式转发的 provider 共用 OpenAI 表，与转发层的协议选择一致。
func ProviderReasoningEfforts(provider string) []string {
	var table []string
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		table = anthropicReasoningEfforts
	case "gemini":
		table = geminiReasoningEfforts
	default:
		table = openAIReasoningEfforts
	}
	return append([]string(nil), table...)
}

// NormalizeSupportedReasoningEfforts 校验模型声明的支持档位：每项必须是该
// provider 本地可表达的档位；结果去重并按强度排序。空输入表示未声明。
func NormalizeSupportedReasoningEfforts(provider string, values []string) ([]string, error) {
	allowed := ProviderReasoningEfforts(provider)
	seen := make(map[string]bool, len(values))
	for _, raw := range values {
		effort := strings.ToLower(strings.TrimSpace(raw))
		if !containsReasoningEffort(allowed, effort) {
			return nil, fmt.Errorf("provider %q 不支持声明思考档位 %q，可选: %s", provider, raw, strings.Join(allowed, ","))
		}
		seen[effort] = true
	}
	normalized := make([]string, 0, len(seen))
	for _, effort := range reasoningEffortOrder {
		if seen[effort] {
			normalized = append(normalized, effort)
		}
	}
	return normalized, nil
}

// ModelReasoningEfforts 返回模型的有效档位：已声明则用声明值，否则用 provider 默认表。
// 声明值来自存储，运行时仍重新校验，避免手工改库的非法值被静默使用。
func ModelReasoningEfforts(provider string, declared []string) ([]string, error) {
	if len(declared) == 0 {
		return ProviderReasoningEfforts(provider), nil
	}
	return NormalizeSupportedReasoningEfforts(provider, declared)
}

// LowestReasoningEffort 返回有效档位中最弱的一档；allowed 须已按强度排序。
func LowestReasoningEffort(allowed []string) string {
	if len(allowed) == 0 {
		return ""
	}
	return allowed[0]
}

// FitReasoningEffort 把期望档位映射到允许集合：命中则原样返回；否则取强度距离
// 最近的一档，距离相同取更强的一档。want 非 none 时不会映射为 none（关闭思考
// 不能作为"降级"结果），allowed 只剩 none 时返回明确错误。
func FitReasoningEffort(want string, allowed []string) (string, error) {
	want, err := NormalizeReasoningEffort(want)
	if err != nil || want == "" {
		return want, err
	}
	if containsReasoningEffort(allowed, want) {
		return want, nil
	}
	wantRank := reasoningEffortRank(want)
	best, bestDistance := "", -1
	for _, candidate := range allowed {
		rank := reasoningEffortRank(candidate)
		if rank < 0 || (want != ReasoningEffortNone && candidate == ReasoningEffortNone) {
			continue
		}
		distance := rank - wantRank
		if distance < 0 {
			distance = -distance
		}
		if bestDistance < 0 || distance < bestDistance || (distance == bestDistance && rank > reasoningEffortRank(best)) {
			best, bestDistance = candidate, distance
		}
	}
	if best == "" {
		return "", fmt.Errorf("思考档位 %q 无法映射到模型支持的档位 [%s]", want, strings.Join(allowed, ","))
	}
	return best, nil
}

// ModelWireReasoningEffort 返回期望档位在该模型上实际下发的档位：按模型有效档位
// 就近映射并确认本地转换器可表达。期望为空或模型禁用推理字段时返回空（不发送）。
// 代理转发与 Auto 配置校验共用它，保证"配置能保存"与"运行时能下发"一致。
func ModelWireReasoningEffort(provider string, declared []string, fieldDisabled bool, want string) (string, error) {
	want, err := NormalizeReasoningEffort(want)
	if err != nil || want == "" || fieldDisabled {
		return "", err
	}
	allowed, err := ModelReasoningEfforts(provider, declared)
	if err != nil {
		return "", err
	}
	wire, err := FitReasoningEffort(want, allowed)
	if err != nil {
		return "", err
	}
	if err := ValidateProviderReasoningEffort(provider, wire); err != nil {
		return "", err
	}
	return wire, nil
}

// ValidateProviderReasoningEffort 仅检查本地转换器能否表达该档位，
// 不代表具体上游模型或端点一定接受该配置。
func ValidateProviderReasoningEffort(provider, effort string) error {
	if effort == "" || containsReasoningEffort(ProviderReasoningEfforts(provider), effort) {
		return nil
	}
	switch provider {
	case "anthropic":
		return fmt.Errorf("Anthropic 模型不支持思考强度 %q", effort)
	case "gemini":
		return fmt.Errorf("Gemini 模型不支持思考强度 %q", effort)
	default:
		return fmt.Errorf("模型 provider %q 不支持思考强度 %q", provider, effort)
	}
}

func containsReasoningEffort(values []string, effort string) bool {
	for _, value := range values {
		if value == effort {
			return true
		}
	}
	return false
}
