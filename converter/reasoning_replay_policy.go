package converter

import "strings"

// ReasoningReplayPlaceholder 是上游要求每个工具调用轮回传推理、但该轮没有推理文本时的占位。
// 空串会被部分中转丢弃或被 DeepSeek 判为缺失，单个空格可被接受（见 magpie chattidy.go noReasoning）。
const ReasoningReplayPlaceholder = " "

// UpstreamRequiresReasoningReplay 判断上游是否要求工具调用轮回传推理（DeepSeek 系，按 host 或模型名）。
func UpstreamRequiresReasoningReplay(host, modelID string) bool {
	return strings.Contains(strings.ToLower(host), "deepseek") || strings.Contains(strings.ToLower(modelID), "deepseek")
}
