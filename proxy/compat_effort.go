package proxy

import (
	"strings"

	"kiro-proxy/converter"

	json "github.com/goccy/go-json"
)

// compatFitEffort 用 converter 统一的就近映射，把已发送档位落到上游列出的可用档位；
// 无法映射或无需改变时返回空。
func compatFitEffort(sent string, allowed []string) string {
	target, err := converter.FitReasoningEffort(sent, allowed)
	if err != nil || target == "" || target == sent {
		return ""
	}
	return target
}

// compatSentEffort 读取已发送 body 中的推理档位：Chat 的 reasoning_effort、
// Responses/Codex 的 reasoning.effort、Anthropic 的 output_config.effort。
func compatSentEffort(protocol compatProtocol, body []byte) string {
	root, err := decodeCompatRoot(body)
	if err != nil {
		return ""
	}
	switch protocol {
	case compatProtocolChat:
		return strings.ToLower(compatString(root["reasoning_effort"]))
	case compatProtocolResponses, compatProtocolCodex:
		return strings.ToLower(compatString(compatObject(root["reasoning"])["effort"]))
	case compatProtocolAnthropic:
		return strings.ToLower(compatString(compatObject(root["output_config"])["effort"]))
	}
	return ""
}

// compatWithEffort 把已发送档位改为 effort，其他推理设置保持不变。
func compatWithEffort(protocol compatProtocol, body []byte, effort string) ([]byte, bool, error) {
	root, err := decodeCompatRoot(body)
	if err != nil {
		return nil, false, err
	}
	encodedEffort, err := json.Marshal(effort)
	if err != nil {
		return nil, false, err
	}
	switch protocol {
	case compatProtocolChat:
		root["reasoning_effort"] = encodedEffort
	case compatProtocolResponses, compatProtocolCodex, compatProtocolAnthropic:
		container := "reasoning"
		if protocol == compatProtocolAnthropic {
			container = "output_config"
		}
		object := compatObject(root[container])
		if object == nil {
			return body, false, nil
		}
		object["effort"] = encodedEffort
		encoded, err := encodeCompatObject(object)
		if err != nil {
			return nil, false, err
		}
		root[container] = encoded
	default:
		return body, false, nil
	}
	return encodeCompatRoot(root)
}
