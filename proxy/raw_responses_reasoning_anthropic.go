package proxy

import (
	"fmt"
	"strings"

	json "github.com/goccy/go-json"
)

// kiroSignedReasoningPlaceholder 只在签名块没有可见思考文本时输出：Kiro 丢弃没有
// 文本的签名（extension.js nvd: reasoning.unsigned.dropped 的对称分支），而下一轮
// 还原时以封装中的原始块为准，这个空格不会进入 Anthropic 请求。
const kiroSignedReasoningPlaceholder = " "

const (
	anthropicThinkingBlock         = "thinking"
	anthropicRedactedThinkingBlock = "redacted_thinking"
)

// anthropicReasoningBlock 是 thinking/redacted_thinking 块需要原样回传的字段。
type anthropicReasoningBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
	Data      string `json:"data"`
}

// wire 按块类型只输出 Anthropic 接受的字段；thinking 为空串时也必须保留。
func (b anthropicReasoningBlock) wire() map[string]interface{} {
	if b.Type == anthropicRedactedThinkingBlock {
		return map[string]interface{}{"type": b.Type, "data": b.Data}
	}
	return map[string]interface{}{"type": b.Type, "thinking": b.Thinking, "signature": b.Signature}
}

// anthropicReasoningOutputItem 把一个推理块变成 Responses reasoning 输出项。
// 没有签名的 thinking 或没有 data 的 redacted_thinking 无法回传，只给出文本。
func anthropicReasoningOutputItem(id string, block anthropicReasoningBlock) (map[string]interface{}, error) {
	sealable := block.Type == anthropicThinkingBlock && block.Signature != "" ||
		block.Type == anthropicRedactedThinkingBlock && block.Data != ""
	if !sealable {
		return reasoningOutputItem(id, block.Thinking, ""), nil
	}
	payload, err := json.Marshal(block.wire())
	if err != nil {
		return nil, fmt.Errorf("encode Anthropic reasoning block: %w", err)
	}
	seal := sealReasoningPayload(anthropicReasoningSealPrefix, payload)
	var encrypted string
	if block.Type == anthropicRedactedThinkingBlock {
		encrypted, err = redactedReasoningEnvelope(seal)
	} else {
		encrypted, err = textReasoningEnvelope(block.Thinking, seal)
	}
	if err != nil {
		return nil, fmt.Errorf("encode Anthropic reasoning envelope: %w", err)
	}
	return reasoningOutputItem(id, block.Thinking, encrypted), nil
}

func anthropicReasoningItemID(responseID string, index int) string {
	return fmt.Sprintf("rs_%s_%d", strings.TrimPrefix(responseID, "resp_"), index)
}

// anthropicReasoningFromInput 还原本项目封装的 Anthropic 推理块。其他上游的
// 推理（包括 Chat 封装）无法通过 Anthropic 的签名校验，必须跳过。
func anthropicReasoningFromInput(encrypted string) (map[string]interface{}, reasoningSkip) {
	kind, payload := openReasoningSeal(encrypted)
	if kind != reasoningSealAnthropic {
		return nil, reasoningSkipForeign
	}
	var block anthropicReasoningBlock
	if payload == nil || json.Unmarshal(payload, &block) != nil {
		return nil, reasoningSkipUnreadable
	}
	switch {
	case block.Type == anthropicThinkingBlock && block.Signature != "":
	case block.Type == anthropicRedactedThinkingBlock && block.Data != "":
	default:
		return nil, reasoningSkipUnreadable
	}
	return block.wire(), reasoningKept
}

// anthropicStreamReasoning 按 content block index 收集流中的推理块。
type anthropicStreamReasoning struct {
	blocks map[int]*anthropicStreamReasoningBlock
	order  []int
}

type anthropicStreamReasoningBlock struct {
	id        string
	block     anthropicReasoningBlock
	thinking  strings.Builder
	signature strings.Builder
	textSent  bool
	done      bool
}

type anthropicEventWriter func(event string, payload interface{}) error

func (r *anthropicStreamReasoning) has(index int) bool {
	_, ok := r.blocks[index]
	return ok
}

func (r *anthropicStreamReasoning) thinkingOpen(index int) bool {
	entry := r.blocks[index]
	return entry != nil && !entry.done && entry.block.Type == anthropicThinkingBlock
}

func (r *anthropicStreamReasoning) start(write anthropicEventWriter, responseID string, index int, block anthropicReasoningBlock) error {
	if r.blocks == nil {
		r.blocks = make(map[int]*anthropicStreamReasoningBlock)
	}
	entry := &anthropicStreamReasoningBlock{id: anthropicReasoningItemID(responseID, index), block: block}
	r.blocks[index] = entry
	r.order = append(r.order, index)
	entry.signature.WriteString(block.Signature)
	if err := write("response.output_item.added", map[string]interface{}{"type": "response.output_item.added", "item": map[string]interface{}{"type": "reasoning", "id": entry.id, "status": "in_progress", "summary": []interface{}{}}}); err != nil {
		return err
	}
	return r.appendThinking(write, index, block.Thinking)
}

// appendThinking 输出 reasoning_text 增量：Kiro 只从该事件读取推理文本。
func (r *anthropicStreamReasoning) appendThinking(write anthropicEventWriter, index int, text string) error {
	entry := r.blocks[index]
	if text == "" {
		return nil
	}
	entry.thinking.WriteString(text)
	entry.textSent = true
	return write("response.reasoning_text.delta", map[string]interface{}{"type": "response.reasoning_text.delta", "item_id": entry.id, "delta": text})
}

func (r *anthropicStreamReasoning) appendSignature(index int, signature string) {
	r.blocks[index].signature.WriteString(signature)
}

func (r *anthropicStreamReasoning) finish(write anthropicEventWriter, index int) error {
	entry := r.blocks[index]
	if entry == nil || entry.done {
		return nil
	}
	entry.done = true
	block := entry.block
	block.Thinking = entry.thinking.String()
	block.Signature = entry.signature.String()
	if block.Type == anthropicThinkingBlock && block.Signature != "" && !entry.textSent {
		if err := write("response.reasoning_text.delta", map[string]interface{}{"type": "response.reasoning_text.delta", "item_id": entry.id, "delta": kiroSignedReasoningPlaceholder}); err != nil {
			return err
		}
	}
	item, err := anthropicReasoningOutputItem(entry.id, block)
	if err != nil {
		return err
	}
	return write("response.output_item.done", map[string]interface{}{"type": "response.output_item.done", "item": item})
}

// finishAll 在 message_stop 时补齐缺少 content_block_stop 的推理块。
func (r *anthropicStreamReasoning) finishAll(write anthropicEventWriter) error {
	for _, index := range r.order {
		if err := r.finish(write, index); err != nil {
			return err
		}
	}
	return nil
}
