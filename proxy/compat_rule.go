package proxy

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	json "github.com/goccy/go-json"
)

// compatTarget 是规则作用的上游：协议决定字段位置，模型 ID 与 host 供复用 converter 中按模型/host 的构造规则。
type compatTarget struct {
	protocol compatProtocol
	modelID  string
	host     string
}

// compatRule 是一条兼容改写规则。识别必须同时满足：状态码为 400/422（由会话保证）、
// 报错命中规则正则、已发送 body 中确实存在被拒的字段或形态；改写只有在 body 确实变化时才算适用。
// 规则只作用于其声明的上游协议，避免把某个厂商的规则套到其他协议上。
type compatRule struct {
	kind      modelCapabilityKind
	protocols []compatProtocol
	// recognize 返回要记忆的值与证据片段；不匹配时 ok=false。
	recognize func(target compatTarget, refusal compatRefusal, body []byte) (value, evidence string, ok bool)
	// apply 按记忆的值改写 body；changed=false 表示该 body 无需改写。
	apply func(target compatTarget, body []byte, value string) (updated []byte, changed bool, err error)
	// wireEffort 返回改写后实际下发的推理档位，用于同步用量记录；nil 表示该规则不改变档位。
	wireEffort func(target compatTarget, updated []byte) string
}

// 顺序即优先级：更具体的规则在前（档位被拒先于推理字段不被支持，思考开关先于采样参数）。
var compatRules = []compatRule{
	developerRoleRule,
	tokenFieldRule,
	thinkingEnabledRule,
	thinkingAdaptiveRule,
	thinkingDisplayRule,
	reasoningEffortRule,
	reasoningFieldRule,
	samplingRule,
}

func (r compatRule) appliesTo(protocol compatProtocol) bool {
	for _, candidate := range r.protocols {
		if candidate == protocol {
			return true
		}
	}
	return false
}

func decodeCompatRoot(body []byte) (map[string]json.RawMessage, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("parse upstream request JSON: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("upstream request body must be a JSON object")
	}
	return root, nil
}

func encodeCompatRoot(root map[string]json.RawMessage) ([]byte, bool, error) {
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, false, fmt.Errorf("encode upstream request JSON: %w", err)
	}
	return encoded, true, nil
}

func encodeCompatObject(object map[string]json.RawMessage) (json.RawMessage, error) {
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("encode upstream request object: %w", err)
	}
	return encoded, nil
}

func compatObject(raw json.RawMessage) map[string]json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(trimmed, &object) != nil {
		return nil
	}
	return object
}

func compatString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func compatHasField(body []byte, field string) bool {
	root, err := decodeCompatRoot(body)
	if err != nil {
		return false
	}
	_, ok := root[field]
	return ok
}

// compatMatch 在错误文本中查找规则正则命中的片段，作为证据。
func compatMatch(pattern *regexp.Regexp, errText string) (string, bool) {
	match := pattern.FindString(errText)
	if match == "" {
		return "", false
	}
	return compatEvidence(match), true
}
