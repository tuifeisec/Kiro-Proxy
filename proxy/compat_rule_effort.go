package proxy

import (
	"regexp"
	"slices"
	"strings"

	"kiro-proxy/converter"
)

const (
	compatEffortSeparator     = "|"
	compatReasoningOmitted    = "omit"
	compatEffortListMaxLength = 160
)

// 档位被拒且报错列出了可用档位（magpie effort_accounts.go / gateway.go 记录）：
// OpenAI "Unsupported value: 'minimal' is not supported with the 'gpt-5.1' model. Supported values are: 'none', 'low', 'medium', and 'high'."
// Sub2API "gpt-6.1-sol does not support reasoning effort \"none\"; use low, medium, high, xhigh or max"
// Command Code `expected one of "low"|"medium"|"high"|"xhigh"|"max"`
var (
	effortLevelsListed = regexp.MustCompile(`(?i)(?:supported values are|expected one of|must be one of|should be one of|valid values are|allowed values are|input should be|\buse\b)\W{0,4}([^.;\n]{0,160})`)
	effortNamed        = regexp.MustCompile(`(?i)reasoning[._ ]?effort|\beffort\b`)
	effortDenied       = regexp.MustCompile(`(?i)not supported|unsupported|does not support|expected one of|must be one of|input should be|invalid value|not allowed`)
	effortLevelWord    = regexp.MustCompile(`(?i)\b(none|minimal|low|medium|high|xhigh|max)\b`)
)

// 上游不认识推理字段本身（magpie automode.go effortFieldUnknown 记录）：
// OpenAI "Unrecognized request argument supplied: reasoning_effort"，校验器 "reasoning_effort: Extra inputs are not permitted"。
// 只认顶层字段：messages.1.output_config 这类逐条消息上的字段被拒不是这里能修的。
var reasoningFieldRefused = regexp.MustCompile("(?i)(?:unrecogni[sz]ed|unknown|unsupported|unexpected|extra)\\s+(?:request\\s+)?(?:argument|parameter|field|property|name|key)s?(?:\\s+supplied)?\\W{0,4}(?:reasoning(?:[._]effort)?|output_config(?:\\.effort)?)\\b|(?:^|[^.\\w])(?:reasoning(?:[._]effort)?|output_config(?:\\.effort)?)['\"`]?\\W{0,4}(?:extra inputs are not permitted|is not (?:supported|allowed|permitted)|is unsupported)")

var reasoningEffortRule = compatRule{
	kind:      capabilityReasoningEffort,
	protocols: []compatProtocol{compatProtocolChat, compatProtocolResponses, compatProtocolCodex, compatProtocolAnthropic},
	recognize: func(target compatTarget, refusal compatRefusal, body []byte) (string, string, bool) {
		sent := compatSentEffort(target.protocol, body)
		if sent == "" || refusal.param != "" && !effortNamed.MatchString(refusal.param) {
			return "", "", false
		}
		if !effortNamed.MatchString(refusal.text) || !effortDenied.MatchString(refusal.text) {
			return "", "", false
		}
		for _, listing := range effortLevelsListed.FindAllStringSubmatch(refusal.text, -1) {
			allowed := compatListedEfforts(listing[1])
			if len(allowed) == 0 || slices.Contains(allowed, sent) || compatFitEffort(sent, allowed) == "" {
				continue
			}
			return strings.Join(allowed, compatEffortSeparator), compatEvidence(listing[0]), true
		}
		return "", "", false
	},
	apply: func(target compatTarget, body []byte, value string) ([]byte, bool, error) {
		effort := compatFitEffort(compatSentEffort(target.protocol, body), strings.Split(value, compatEffortSeparator))
		if effort == "" {
			return body, false, nil
		}
		return compatWithEffort(target.protocol, body, effort)
	},
	wireEffort: func(target compatTarget, updated []byte) string {
		return compatSentEffort(target.protocol, updated)
	},
}

var reasoningFieldRule = compatRule{
	kind:      capabilityReasoningField,
	protocols: []compatProtocol{compatProtocolChat, compatProtocolResponses, compatProtocolAnthropic},
	recognize: func(target compatTarget, refusal compatRefusal, body []byte) (string, string, bool) {
		if _, changed, err := compatWithoutReasoning(target.protocol, body); err != nil || !changed {
			return "", "", false
		}
		evidence, ok := compatMatch(reasoningFieldRefused, refusal.text)
		if !ok {
			return "", "", false
		}
		return compatReasoningOmitted, evidence, true
	},
	apply: func(target compatTarget, body []byte, _ string) ([]byte, bool, error) {
		return compatWithoutReasoning(target.protocol, body)
	},
	// 去掉推理字段后不再下发任何档位。
	wireEffort: func(compatTarget, []byte) string { return "" },
}

// compatListedEfforts 提取报错列出的档位，只保留本地可表达的档位（如 minimal 无法表达，丢弃）。
func compatListedEfforts(segment string) []string {
	if len(segment) > compatEffortListMaxLength {
		segment = segment[:compatEffortListMaxLength]
	}
	var levels []string
	for _, match := range effortLevelWord.FindAllStringSubmatch(segment, -1) {
		level, err := converter.NormalizeReasoningEffort(match[1])
		if err != nil || level == "" || slices.Contains(levels, level) {
			continue
		}
		levels = append(levels, level)
	}
	return levels
}

// compatWithoutReasoning 去掉推理字段：Chat 的 reasoning_effort、Responses 的 reasoning、
// Anthropic 的 output_config.effort（output_config 为空时一并去掉）。
func compatWithoutReasoning(protocol compatProtocol, body []byte) ([]byte, bool, error) {
	root, err := decodeCompatRoot(body)
	if err != nil {
		return nil, false, err
	}
	switch protocol {
	case compatProtocolChat, compatProtocolResponses:
		field := "reasoning"
		if protocol == compatProtocolChat {
			field = "reasoning_effort"
		}
		if _, ok := root[field]; !ok {
			return body, false, nil
		}
		delete(root, field)
	case compatProtocolAnthropic:
		config := compatObject(root["output_config"])
		if _, ok := config["effort"]; !ok {
			return body, false, nil
		}
		delete(config, "effort")
		if len(config) == 0 {
			delete(root, "output_config")
		} else {
			encoded, err := encodeCompatObject(config)
			if err != nil {
				return nil, false, err
			}
			root["output_config"] = encoded
		}
	default:
		return body, false, nil
	}
	return encodeCompatRoot(root)
}
