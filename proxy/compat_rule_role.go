package proxy

import (
	"fmt"
	"regexp"

	json "github.com/goccy/go-json"
)

const compatDeveloperRoleValue = "system"

// 不认识 developer 角色的 OpenAI 兼容上游：列出可用角色但不含 developer，
// 例如 magpie codex_backend.go 记录的 "developer is not one of ['system', 'assistant', 'user', 'tool', 'function']"，
// 或 Rust 实现的 "unknown variant `developer`"。developer 与 system 语义相同，各后端都接受 system。
var developerRoleRefused = regexp.MustCompile("(?i)['\"`]?developer['\"`]? is not one of\\b[^\\n]{0,120}|unknown variant [`'\"\\\\]*developer\\b[^\\n]{0,80}")

var developerRoleRule = compatRule{
	kind:      capabilityDeveloperRole,
	protocols: []compatProtocol{compatProtocolChat, compatProtocolResponses},
	recognize: func(target compatTarget, refusal compatRefusal, body []byte) (string, string, bool) {
		if !compatHasDeveloperRole(target.protocol, body) {
			return "", "", false
		}
		if evidence, ok := compatMatch(developerRoleRefused, refusal.text); ok {
			return compatDeveloperRoleValue, evidence, true
		}
		// 既有判定：invalid_request 且 "message role must be one of" 列表中没有 developer。
		if isResponsesDeveloperRoleRejected(refusal.status, refusal.text) {
			if evidence, ok := compatMatch(messageRoleListRefused, refusal.text); ok {
				return compatDeveloperRoleValue, evidence, true
			}
			return compatDeveloperRoleValue, "message role must be one of", true
		}
		return "", "", false
	},
	apply: func(target compatTarget, body []byte, _ string) ([]byte, bool, error) {
		return compatDeveloperAsSystem(target.protocol, body)
	},
}

var messageRoleListRefused = regexp.MustCompile(`(?i)message role must be one of[^\n]{0,120}`)

func compatMessagesField(protocol compatProtocol) string {
	if protocol == compatProtocolChat {
		return "messages"
	}
	return "input"
}

func compatHasDeveloperRole(protocol compatProtocol, body []byte) bool {
	_, changed, err := compatDeveloperAsSystem(protocol, body)
	return err == nil && changed
}

// compatDeveloperAsSystem 把 developer 消息原位改为 system，保持消息顺序以免破坏缓存前缀。
func compatDeveloperAsSystem(protocol compatProtocol, body []byte) ([]byte, bool, error) {
	root, err := decodeCompatRoot(body)
	if err != nil {
		return nil, false, err
	}
	field := compatMessagesField(protocol)
	raw, ok := root[field]
	if !ok {
		return body, false, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return body, false, nil
	}
	changed := false
	for index, rawItem := range items {
		item := compatObject(rawItem)
		if item == nil || compatString(item["role"]) != "developer" {
			continue
		}
		// Responses 中只有 message 项带角色；缺省 type 的项按 message 处理。
		if itemType := compatString(item["type"]); protocol != compatProtocolChat && itemType != "" && itemType != "message" {
			continue
		}
		item["role"] = json.RawMessage(`"system"`)
		updated, err := encodeCompatObject(item)
		if err != nil {
			return nil, false, err
		}
		items[index] = updated
		changed = true
	}
	if !changed {
		return body, false, nil
	}
	encodedItems, err := json.Marshal(items)
	if err != nil {
		return nil, false, fmt.Errorf("encode upstream %s: %w", field, err)
	}
	root[field] = encodedItems
	return encodeCompatRoot(root)
}
