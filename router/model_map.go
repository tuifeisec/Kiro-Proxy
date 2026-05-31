// model_map.go
// 内置模型名映射表：context window 推荐值、模型别名等

package router

// defaultContextWindows 内置模型的推荐 context window（token 数）
// 用于计算 contextUsagePercentage，让 Kiro IDE 在合适时机触发对话压缩
var defaultContextWindows = map[string]int{
	// Claude 系列
	"claude-opus-4-5":            200000,
	"claude-sonnet-4-5":          200000,
	"claude-sonnet-4":            200000,
	"claude-haiku-4-5":           200000,
	"claude-3-7-sonnet-20250219": 200000,
	"claude-3-5-sonnet-20241022": 200000,
	"claude-3-5-haiku-20241022":  200000,
	"claude-3-opus-20240229":     200000,

	// GPT 系列
	"gpt-4o":              128000,
	"gpt-4o-mini":         128000,
	"gpt-4-turbo":         128000,
	"gpt-4":               8192,
	"gpt-3.5-turbo":       16385,
	"o1":                  200000,
	"o1-mini":             128000,
	"o3-mini":             200000,

	// Gemini 系列
	"gemini-2.0-flash":         1048576,
	"gemini-2.0-flash-thinking": 1048576,
	"gemini-1.5-pro":           2097152,
	"gemini-1.5-flash":         1048576,
}

// DefaultContextWindowFor 返回模型的推荐 context window，未知模型返回 0
func DefaultContextWindowFor(model string) int {
	if v, ok := defaultContextWindows[model]; ok {
		return v
	}
	// 模糊匹配：包含关键词时给默认值
	for k, v := range defaultContextWindows {
		if len(model) >= len(k) && model[:len(k)] == k {
			return v
		}
	}
	return 0
}

// IsClassifierModel 判断是否是 intent-classification 专用模型
func IsClassifierModel(model string) bool {
	return model == "simple-task" || model == "intent-classification"
}
