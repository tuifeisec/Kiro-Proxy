package store

import "strings"

// supportedReasoningEffortsSeparator 用于 models.supported_reasoning_efforts 列。
// 档位值由 API 层校验为固定的小写枚举，不会包含分隔符。
const supportedReasoningEffortsSeparator = ","

func encodeSupportedReasoningEfforts(efforts []string) string {
	return strings.Join(efforts, supportedReasoningEffortsSeparator)
}

// decodeSupportedReasoningEfforts 总是返回非 nil 切片，使 JSON 输出为 [] 而非 null。
// 这里只做拆分；档位合法性在运行时由 converter.ModelReasoningEfforts 重新校验。
func decodeSupportedReasoningEfforts(value string) []string {
	efforts := []string{}
	for _, part := range strings.Split(value, supportedReasoningEffortsSeparator) {
		if part = strings.TrimSpace(part); part != "" {
			efforts = append(efforts, part)
		}
	}
	return efforts
}
