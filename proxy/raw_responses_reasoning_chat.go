package proxy

import (
	"fmt"
	"net/url"
	"strings"

	"kiro-proxy/converter"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

// chatReasoningFields 是 Chat 上游流中携带推理的字段，按优先级读取首个非空值；
// 同一字段名会在下一轮写回 assistant 消息（DeepSeek 用 reasoning_content，
// OpenRouter 等用 reasoning）。
var chatReasoningFields = []string{"reasoning_content", "reasoning", "reasoning_text"}

// chatReasoningSealPayload 是 Chat 推理封装的内容：文本与原始字段名。
type chatReasoningSealPayload struct {
	Field string `json:"field"`
	Text  string `json:"text"`
}

func isChatReasoningField(field string) bool {
	for _, known := range chatReasoningFields {
		if field == known {
			return true
		}
	}
	return false
}

// chatReasoningDelta 嵌入 Chat 流 chunk 的 delta。字段按原始 JSON 读取，
// 非字符串形态（如对象）不会让整个 chunk 解析失败。
type chatReasoningDelta struct {
	ReasoningContent json.RawMessage `json:"reasoning_content"`
	Reasoning        json.RawMessage `json:"reasoning"`
	ReasoningText    json.RawMessage `json:"reasoning_text"`
}

// reasoning 按 chatReasoningFields 的优先级返回首个非空推理文本及其字段名。
func (d chatReasoningDelta) reasoning() (field, text string) {
	for _, candidate := range []struct {
		field string
		raw   json.RawMessage
	}{{chatReasoningFields[0], d.ReasoningContent}, {chatReasoningFields[1], d.Reasoning}, {chatReasoningFields[2], d.ReasoningText}} {
		var value string
		if json.Unmarshal(candidate.raw, &value) == nil && value != "" {
			return candidate.field, value
		}
	}
	return "", ""
}

// chatStreamReasoning 把 Chat 流的推理增量组织为 Responses reasoning 输出项。
type chatStreamReasoning struct {
	responseID func() string
	count      int
	id         string
	field      string
	text       strings.Builder
}

func (r *chatStreamReasoning) open() bool { return r.id != "" }

func (r *chatStreamReasoning) append(write func(string, interface{}) error, field, text string) error {
	if !r.open() {
		r.id = fmt.Sprintf("rs_%s_%d", strings.TrimPrefix(r.responseID(), "resp_"), r.count)
		r.count++
		r.field = field
		r.text.Reset()
		if err := write("response.output_item.added", map[string]interface{}{"type": "response.output_item.added", "item": map[string]interface{}{"type": "reasoning", "id": r.id, "status": "in_progress", "summary": []interface{}{}}}); err != nil {
			return err
		}
	}
	r.text.WriteString(text)
	return write("response.reasoning_text.delta", map[string]interface{}{"type": "response.reasoning_text.delta", "item_id": r.id, "delta": text})
}

// close 在正文、工具调用或流结束前完成当前推理项；Kiro 以 done 中的签名封口
// 一个推理阶段，所以必须先于后续输出。
func (r *chatStreamReasoning) close(write func(string, interface{}) error) error {
	if !r.open() {
		return nil
	}
	id, text := r.id, r.text.String()
	r.id = ""
	payload, err := json.Marshal(chatReasoningSealPayload{Field: r.field, Text: text})
	if err != nil {
		return fmt.Errorf("encode Chat reasoning seal: %w", err)
	}
	encrypted, err := textReasoningEnvelope(text, sealReasoningPayload(chatReasoningSealPrefix, payload))
	if err != nil {
		return fmt.Errorf("encode Chat reasoning envelope: %w", err)
	}
	return write("response.output_item.done", map[string]interface{}{"type": "response.output_item.done", "item": reasoningOutputItem(id, text, encrypted)})
}

// chatReasoningFromInput 还原本项目封装的 Chat 推理；其他上游的推理不回传。
func chatReasoningFromInput(encrypted string) (chatReasoningSealPayload, reasoningSkip) {
	kind, payload := openReasoningSeal(encrypted)
	if kind != reasoningSealChat {
		return chatReasoningSealPayload{}, reasoningSkipForeign
	}
	var restored chatReasoningSealPayload
	if payload == nil || json.Unmarshal(payload, &restored) != nil || !isChatReasoningField(restored.Field) {
		return chatReasoningSealPayload{}, reasoningSkipUnreadable
	}
	return restored, reasoningKept
}

// attachChatReasoning 把推理写回 assistant 消息；同一轮多段推理按顺序拼接。
func attachChatReasoning(message map[string]interface{}, pending []chatReasoningSealPayload) {
	for _, reasoning := range pending {
		existing, _ := message[reasoning.Field].(string)
		message[reasoning.Field] = existing + reasoning.Text
	}
}

// rawResponsesUpstreamHost 只作为推理回传判定的线索；端点无效时由发送前的
// rawResponsesUpstreamURL 校验报错，这里返回空串，模型名判定仍然生效。
func rawResponsesUpstreamHost(model *store.Model) string {
	endpoint, _, err := rawResponsesUpstreamURL(model)
	if err != nil {
		return ""
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// fillChatReasoningPlaceholders 给缺推理的工具调用轮补占位：DeepSeek 要求工具
// 调用轮回传 reasoning_content，缺失时整个请求返回 400。
func fillChatReasoningPlaceholders(chat map[string]json.RawMessage) error {
	raw := chat["messages"]
	if len(raw) == 0 {
		return nil
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		return fmt.Errorf("parse Chat messages for reasoning replay: %w", err)
	}
	field := ""
	for _, message := range messages {
		if found := chatMessageReasoningField(message); found != "" {
			field = found
			break
		}
	}
	if field == "" {
		field = chatReasoningFields[0]
	}
	placeholder, err := json.Marshal(converter.ReasoningReplayPlaceholder)
	if err != nil {
		return err
	}
	changed := false
	for _, message := range messages {
		var role string
		var calls []json.RawMessage
		_ = json.Unmarshal(message["role"], &role)
		if role != "assistant" || json.Unmarshal(message["tool_calls"], &calls) != nil || len(calls) == 0 || chatMessageReasoningField(message) != "" {
			continue
		}
		message[field] = placeholder
		changed = true
	}
	if !changed {
		return nil
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("encode Chat messages for reasoning replay: %w", err)
	}
	chat["messages"] = encoded
	return nil
}

func chatMessageReasoningField(message map[string]json.RawMessage) string {
	for _, name := range chatReasoningFields {
		var value string
		if json.Unmarshal(message[name], &value) == nil && value != "" {
			return name
		}
	}
	return ""
}
