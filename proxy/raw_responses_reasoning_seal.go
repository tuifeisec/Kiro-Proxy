package proxy

import (
	"encoding/base64"
	"strings"

	json "github.com/goccy/go-json"
)

// Kiro 的原生 Responses 客户端不会原样回传 encrypted_content：它把该字段按
// JSON 解析为 {"reasoningText":{"text","signature"}} 或 {"redactedContent":b64}，
// 只把 signature 与 redactedContent 当作不透明值存进历史，下一轮再用同一形态
// 重建 reasoning item（kiro.kiro-agent extension.js 的 UGo/tAd 与 nvd）。
// 因此本项目的封装放在这两个不透明位置，外层保持 Kiro 能解析的形态；
// 原样回传 encrypted_content 的其他 Responses 客户端同样可往返。
const (
	anthropicReasoningSealPrefix = "kiro-proxy-anthropic1:"
	chatReasoningSealPrefix      = "kiro-proxy-chat1:"
)

type reasoningSealKind int

const (
	reasoningSealNone reasoningSealKind = iota
	reasoningSealAnthropic
	reasoningSealChat
)

// reasoningEnvelope 与 Kiro 的 encrypted_content 形态逐字段一致。
type reasoningEnvelope struct {
	ReasoningText   *reasoningEnvelopeText `json:"reasoningText,omitempty"`
	RedactedContent string                 `json:"redactedContent,omitempty"`
}

type reasoningEnvelopeText struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

func sealReasoningPayload(prefix string, payload []byte) string {
	return prefix + base64.StdEncoding.EncodeToString(payload)
}

// textReasoningEnvelope 把封装放进 signature；Kiro 只在同一阶段同时收到推理
// 文本与 signature 时才保留它，所以流中必须先输出 reasoning_text 增量。
func textReasoningEnvelope(text, seal string) (string, error) {
	encoded, err := json.Marshal(reasoningEnvelope{ReasoningText: &reasoningEnvelopeText{Text: text, Signature: seal}})
	return string(encoded), err
}

// redactedReasoningEnvelope 把封装整体 base64 后放进 redactedContent；Kiro 解码为
// 字节保存、下一轮再按标准 base64 编码，往返后字节不变。
func redactedReasoningEnvelope(seal string) (string, error) {
	encoded, err := json.Marshal(reasoningEnvelope{RedactedContent: base64.StdEncoding.EncodeToString([]byte(seal))})
	return string(encoded), err
}

// openReasoningSeal 识别 encrypted_content 中本项目的封装。返回 None 表示其他
// 上游的推理；payload 为 nil 而 kind 非 None 表示带本项目前缀但已无法读取。
func openReasoningSeal(encrypted string) (reasoningSealKind, []byte) {
	seal := reasoningSealCarrier(encrypted)
	for _, candidate := range []struct {
		prefix string
		kind   reasoningSealKind
	}{{anthropicReasoningSealPrefix, reasoningSealAnthropic}, {chatReasoningSealPrefix, reasoningSealChat}} {
		encoded, ok := strings.CutPrefix(seal, candidate.prefix)
		if !ok {
			continue
		}
		payload, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || !json.Valid(payload) {
			return candidate.kind, nil
		}
		return candidate.kind, payload
	}
	return reasoningSealNone, nil
}

func isProxyReasoningSeal(encrypted string) bool {
	kind, _ := openReasoningSeal(encrypted)
	return kind != reasoningSealNone
}

// reasoningSealCarrier 取出可能承载封装的不透明值；不是 Kiro 形态时按原值判断。
func reasoningSealCarrier(encrypted string) string {
	trimmed := strings.TrimSpace(encrypted)
	if !strings.HasPrefix(trimmed, "{") {
		return trimmed
	}
	var envelope reasoningEnvelope
	if json.Unmarshal([]byte(trimmed), &envelope) != nil {
		return ""
	}
	if envelope.RedactedContent != "" {
		decoded, err := base64.StdEncoding.DecodeString(envelope.RedactedContent)
		if err != nil {
			return ""
		}
		return string(decoded)
	}
	if envelope.ReasoningText != nil {
		return envelope.ReasoningText.Signature
	}
	return ""
}

// reasoningOutputItem 是一个已完成的 Responses reasoning 输出项；summary 承载
// 推理文本，encrypted 为空时不附带（例如上游没有给出签名）。
func reasoningOutputItem(id, text, encrypted string) map[string]interface{} {
	summary := []interface{}{}
	if text != "" {
		summary = append(summary, map[string]interface{}{"type": "summary_text", "text": text})
	}
	item := map[string]interface{}{"type": "reasoning", "id": id, "status": "completed", "summary": summary}
	if encrypted != "" {
		item["encrypted_content"] = encrypted
	}
	return item
}
