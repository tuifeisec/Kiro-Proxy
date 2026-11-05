package converter

import (
	"bytes"
	"fmt"

	json "github.com/goccy/go-json"
)

// openAIDelta 保留 content 与推理字段的原始 JSON：兼容上游对同一字段有不同形态，
// 按字符串解码会让整个 chunk（含工具调用与 finish_reason）一起丢失。
type openAIDelta struct {
	Content          json.RawMessage       `json:"content"`
	ReasoningContent json.RawMessage       `json:"reasoning_content"`
	ReasoningText    json.RawMessage       `json:"reasoning_text"`
	Reasoning        json.RawMessage       `json:"reasoning"`
	ReasoningDetails json.RawMessage       `json:"reasoning_details"`
	ToolCalls        []openAIToolCallDelta `json:"tool_calls"`
}

// openAIDeltaText 是一个 delta 中可用的文本。
type openAIDeltaText struct {
	Content        string
	Reasoning      string
	ReasoningField string   // 推理文本来自哪个字段，回放时按原字段发回
	SkippedParts   []string // content 分段里未取用的片段类型（如 image_url、thinking）
	OpaqueFields   []string // 推理字段有结构但没有可读文本（如加密推理）
}

// openAIReasoningFields 按优先级排列：部分中转会把同一段推理同时放进多个字段，只取第一个非空的。
var openAIReasoningFields = []string{"reasoning_content", "reasoning_text", "reasoning", "reasoning_details"}

func (d openAIDelta) reasoningRaw(field string) json.RawMessage {
	switch field {
	case "reasoning_content":
		return d.ReasoningContent
	case "reasoning_text":
		return d.ReasoningText
	case "reasoning":
		return d.Reasoning
	default:
		return d.ReasoningDetails
	}
}

// decodeText 解出 content 与推理文本；只有 content 形态无法识别时返回错误。
func (d openAIDelta) decodeText() (openAIDeltaText, error) {
	var out openAIDeltaText
	content, skipped, err := decodeOpenAIDeltaContent(d.Content)
	if err != nil {
		return out, err
	}
	out.Content, out.SkippedParts = content, skipped
	for _, field := range openAIReasoningFields {
		raw := d.reasoningRaw(field)
		if isJSONAbsent(raw) {
			continue
		}
		text, err := extractReasoningText(raw)
		if err != nil {
			return out, fmt.Errorf("delta.%s: %w", field, err)
		}
		if text != "" {
			out.Reasoning, out.ReasoningField = text, field
			break
		}
		if isNonEmptyJSONContainer(raw) {
			out.OpaqueFields = append(out.OpaqueFields, field)
		}
	}
	return out, nil
}

// DecodeOpenAIDeltaContent 供代理层的 Chat 流转换复用同一 content 解码规则：
// 返回正文文本；形态无法识别时返回错误，调用方应终止流而不是丢弃该 chunk。
func DecodeOpenAIDeltaContent(raw json.RawMessage) (string, error) {
	text, _, err := decodeOpenAIDeltaContent(raw)
	return text, err
}

// decodeOpenAIDeltaContent 接受字符串或分段数组（Mistral 等），只取 text 片段。
// thinking 等其他片段跳过并上报类型：它们的回放形态各家不同，混入推理字段会被部分上游拒绝。
func decodeOpenAIDeltaContent(raw json.RawMessage) (string, []string, error) {
	raw = bytes.TrimSpace(raw)
	if isJSONAbsent(raw) {
		return "", nil, nil
	}
	switch raw[0] {
	case '"':
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", nil, fmt.Errorf("delta.content: %w", err)
		}
		return text, nil, nil
	case '[':
		var parts []json.RawMessage
		if err := json.Unmarshal(raw, &parts); err != nil {
			return "", nil, fmt.Errorf("delta.content: %w", err)
		}
		var text bytes.Buffer
		var skipped []string
		for _, part := range parts {
			partText, partType, ok := decodeOpenAIContentPart(part)
			if !ok {
				skipped = append(skipped, partType)
				continue
			}
			text.WriteString(partText)
		}
		return text.String(), skipped, nil
	default:
		return "", nil, fmt.Errorf("delta.content 应为字符串或分段数组，实际为 %s", jsonKind(raw))
	}
}

// decodeOpenAIContentPart 返回 text 片段的文本；字符串元素按文本处理。
func decodeOpenAIContentPart(raw json.RawMessage) (text, partType string, ok bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if json.Unmarshal(raw, &text) == nil {
			return text, "text", true
		}
	}
	var part struct {
		Type string          `json:"type"`
		Text json.RawMessage `json:"text"`
	}
	if json.Unmarshal(raw, &part) != nil {
		return "", jsonKind(raw), false
	}
	if part.Type != "" && part.Type != "text" {
		return "", part.Type, false
	}
	if json.Unmarshal(part.Text, &text) != nil {
		return "", "text(无文本)", false
	}
	return text, "text", true
}

// extractReasoningText 从字符串、对象或数组形态的推理字段中提取文本。
// 对象按 text → content → summary → thinking 取第一个有文本的键；数组逐项拼接
// （OpenRouter / MiniMax 的 reasoning_details）。加密推理没有可读文本，返回空串。
func extractReasoningText(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if raw[0] == '"' {
		var text string
		err := json.Unmarshal(raw, &text)
		return text, err
	}
	if raw[0] != '{' && raw[0] != '[' {
		return "", fmt.Errorf("推理字段应为字符串、对象或数组，实际为 %s", jsonKind(raw))
	}
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	var text bytes.Buffer
	collectReasoningText(value, &text)
	return text.String(), nil
}

var reasoningTextKeys = []string{"text", "content", "summary", "thinking"}

func collectReasoningText(value interface{}, out *bytes.Buffer) {
	switch v := value.(type) {
	case string:
		out.WriteString(v)
	case []interface{}:
		for _, item := range v {
			collectReasoningText(item, out)
		}
	case map[string]interface{}:
		for _, key := range reasoningTextKeys {
			before := out.Len()
			collectReasoningText(v[key], out)
			if out.Len() > before {
				return
			}
		}
	}
}

func isJSONAbsent(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}

// isNonEmptyJSONContainer 区分"有结构却没有文本"（值得诊断）与空字符串、空数组这类无内容的占位。
func isNonEmptyJSONContainer(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 2 && (raw[0] == '{' || raw[0] == '[')
}

func jsonKind(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "空值"
	}
	switch raw[0] {
	case '{':
		return "对象"
	case '[':
		return "数组"
	case '"':
		return "字符串"
	case 't', 'f':
		return "布尔值"
	case 'n':
		return "null"
	default:
		return "数字"
	}
}
