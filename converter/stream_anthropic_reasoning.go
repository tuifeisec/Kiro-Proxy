package converter

import "strings"

// anthropicReasoningCollector 按 content block index 采集 thinking / redacted_thinking。
// 每块独立保存文本与签名：Anthropic 的签名只校验它所在块的文本，拼接后回放会被判为签名不匹配。
// 调用方负责加锁（AnthropicStreamConverter.mu）。
type anthropicReasoningCollector struct {
	blocks  []*anthropicReasoningBlock
	byIndex map[int]*anthropicReasoningBlock
}

type anthropicReasoningBlock struct {
	kind      string
	text      strings.Builder
	signature strings.Builder
	data      string
}

func (c *anthropicReasoningCollector) start(index int, kind string) *anthropicReasoningBlock {
	if c.byIndex == nil {
		c.byIndex = make(map[int]*anthropicReasoningBlock)
	}
	block := &anthropicReasoningBlock{kind: kind}
	c.byIndex[index] = block
	c.blocks = append(c.blocks, block)
	return block
}

// thinkingAt 返回 index 处的 thinking 块。部分中转省略 content_block_start，
// thinking_delta / signature_delta 本身已表明块类型，按该 index 新建 thinking 块即可，内容不丢不改。
func (c *anthropicReasoningCollector) thinkingAt(index int) *anthropicReasoningBlock {
	if block, ok := c.byIndex[index]; ok && block.kind == ReasoningBlockThinking {
		return block
	}
	return c.start(index, ReasoningBlockThinking)
}

// snapshot 按上游顺序返回有内容的块：无文本且无签名的 thinking、无 data 的 redacted_thinking 不可回放，丢弃。
func (c *anthropicReasoningCollector) snapshot() []ReasoningBlock {
	result := make([]ReasoningBlock, 0, len(c.blocks))
	for _, block := range c.blocks {
		item := ReasoningBlock{Type: block.kind, Text: block.text.String(), Signature: block.signature.String(), Data: block.data}
		switch item.Type {
		case ReasoningBlockRedactedThinking:
			if item.Data == "" {
				continue
			}
		default:
			if strings.TrimSpace(item.Text) == "" && item.Signature == "" {
				continue
			}
		}
		result = append(result, item)
	}
	return result
}
