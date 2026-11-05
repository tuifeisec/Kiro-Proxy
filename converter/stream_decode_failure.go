package converter

import (
	"fmt"
	"regexp"
	"unicode/utf8"
)

// streamEventSampleLimit 限制错误信息中上游事件样本的长度：足够定位形态问题，
// 又不会把整段模型输出写进日志与 Kiro 的错误提示。
const streamEventSampleLimit = 200

type streamSecretPattern struct {
	re          *regexp.Regexp
	replacement string
}

// 上游错误事件偶尔会回显请求头或配置。样本先整体脱敏再截断，避免截断后的残片绕过匹配。
var streamSampleSecretPatterns = []streamSecretPattern{
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`), "${1}[REDACTED]"},
	{regexp.MustCompile(`\b(?:sk|rk|pk)-[A-Za-z0-9_-]{8,}`), "[REDACTED]"},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{20,}`), "[REDACTED]"},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), "[REDACTED]"},
	{regexp.MustCompile(`(?i)("(?:[a-z_-]*api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|authorization|secret|password|client[_-]?secret)"\s*:\s*)"[^"]*"`), `${1}"[REDACTED]"`},
}

// streamDecodeFailureMessage 生成"无法解析上游流事件"的错误信息，附带脱敏截断样本。
func streamDecodeFailureMessage(protocol string, data string, err error) string {
	return fmt.Sprintf("无法解析上游 %s 流事件: %v; 样本(%d 字节): %q", protocol, err, len(data), streamEventSample(data))
}

func streamEventSample(data string) string {
	sample := data
	for _, pattern := range streamSampleSecretPatterns {
		sample = pattern.re.ReplaceAllString(sample, pattern.replacement)
	}
	if len(sample) <= streamEventSampleLimit {
		return sample
	}
	cut := streamEventSampleLimit
	for cut > 0 && !utf8.RuneStart(sample[cut]) {
		cut--
	}
	return sample[:cut] + "…"
}

// streamFailureText 优先使用具体失败原因，否则回落到终态的通用说明。
func streamFailureText(outcome StreamOutcome, detail string) string {
	if detail != "" {
		return detail
	}
	return streamFailureMessage(outcome)
}

func streamOutcomeErrorWithDetail(outcome StreamOutcome, detail string) error {
	if outcome == StreamOutcomePending || outcome == StreamOutcomeCompleted {
		return nil
	}
	return fmt.Errorf("%s: %s", outcome, streamFailureText(outcome, detail))
}
