package proxy

import (
	"bytes"
	"fmt"

	json "github.com/goccy/go-json"
)

var reasoningItemMarker = []byte(`"reasoning"`)

// stripProxyReasoningItemsRoot 在发往原生 Responses 上游前移除本项目封装的
// reasoning item：对方无法解密 encrypted_content，会以 400 拒绝整个请求。
// 推理对 Responses 上游是可选上下文，删除后其余 input 保持原始字节。
// 必须按候选执行：同一请求在模型组内可能发往需要这些块的 Anthropic 成员。
func stripProxyReasoningItemsRoot(root map[string]json.RawMessage, modelID string) (bool, error) {
	raw := root["input"]
	if !bytes.Contains(raw, reasoningItemMarker) {
		return false, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return false, nil // 字符串 input 不含 item
	}
	kept := make([]json.RawMessage, 0, len(items))
	stripped := 0
	for _, item := range items {
		if bytes.Contains(item, reasoningItemMarker) {
			var fields struct {
				Type             string `json:"type"`
				EncryptedContent string `json:"encrypted_content"`
			}
			if json.Unmarshal(item, &fields) == nil && fields.Type == "reasoning" && isProxyReasoningSeal(fields.EncryptedContent) {
				stripped++
				continue
			}
		}
		kept = append(kept, item)
	}
	if stripped == 0 {
		return false, nil
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return false, fmt.Errorf("encode raw Responses input without proxy reasoning: %w", err)
	}
	root["input"] = encoded
	reasoningDiagnosticLog("[responses-reasoning] 移除发往 Responses 上游前无法解密的本项目 reasoning item: model=%s count=%d", modelID, stripped)
	return true, nil
}

// stripProxyReasoningItems 是 SDK /v1/responses 入口的同一规则：Chat 回退会给
// 客户端本项目封装的推理，客户端换到原生 Responses 模型时必须剥离。
func stripProxyReasoningItems(body []byte, modelID string) ([]byte, error) {
	if !bytes.Contains(body, reasoningItemMarker) {
		return body, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		if err == nil {
			err = fmt.Errorf("body must be a JSON object")
		}
		return nil, fmt.Errorf("parse Responses body for proxy reasoning: %w", err)
	}
	changed, err := stripProxyReasoningItemsRoot(root, modelID)
	if err != nil || !changed {
		return body, err
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode Responses body without proxy reasoning: %w", err)
	}
	return encoded, nil
}
