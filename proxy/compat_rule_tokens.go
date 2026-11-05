package proxy

import (
	"regexp"
	"strings"
)

const (
	compatMaxTokensField     = "max_tokens"
	compatCompletionTokField = "max_completion_tokens"
)

// Chat Completions 有两种回复长度字段：OpenAI 新模型拒绝 max_tokens，
// 报错 "Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead."
// （magpie provider/token_field.go 记录）；部分中转只认 max_tokens，会反向要求。
// 只有报错明确说"改用另一个字段"时才互换，"max_tokens must be at most 32768" 这类取值限制不是字段名问题。
var tokenFieldAsked = regexp.MustCompile("(?i)\\buse\\s+['\"`]?(max_completion_tokens|max_tokens)['\"`]?\\s+instead")

var tokenFieldRule = compatRule{
	kind:      capabilityTokenField,
	protocols: []compatProtocol{compatProtocolChat},
	recognize: func(_ compatTarget, refusal compatRefusal, body []byte) (string, string, bool) {
		match := tokenFieldAsked.FindStringSubmatch(refusal.text)
		if match == nil {
			return "", "", false
		}
		want := strings.ToLower(match[1])
		if !compatHasField(body, compatOtherTokenField(want)) {
			return "", "", false
		}
		return want, compatEvidence(match[0]), true
	},
	apply: func(_ compatTarget, body []byte, want string) ([]byte, bool, error) {
		from := compatOtherTokenField(want)
		if from == "" {
			return body, false, nil
		}
		root, err := decodeCompatRoot(body)
		if err != nil {
			return nil, false, err
		}
		value, ok := root[from]
		if !ok {
			return body, false, nil
		}
		delete(root, from)
		// 两个字段同时存在时保留上游要求的那个原值。
		if _, exists := root[want]; !exists {
			root[want] = value
		}
		return encodeCompatRoot(root)
	},
}

func compatOtherTokenField(want string) string {
	switch want {
	case compatMaxTokensField:
		return compatCompletionTokField
	case compatCompletionTokField:
		return compatMaxTokensField
	default:
		return ""
	}
}
