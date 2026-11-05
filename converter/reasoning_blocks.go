package converter

import "strings"

const (
	ReasoningBlockThinking         = "thinking"
	ReasoningBlockRedactedThinking = "redacted_thinking"
)

// ReasoningBlock 是一个上游推理块。签名只对它自己的文本有效，
// 所以多块必须分开保存、按原顺序回放，不能把文本拼接后套用某一个签名。
type ReasoningBlock struct {
	Type      string // ReasoningBlockThinking 或 ReasoningBlockRedactedThinking
	Text      string // thinking 文本；adaptive 只返回签名时可为空
	Signature string // thinking 签名
	Data      string // redacted_thinking 的加密数据
}

// ReplayBlocks 返回按原顺序回放的推理块。旧数据没有 Blocks，按单个 thinking 块（Text+Signature）解释。
func (t ReasoningTrace) ReplayBlocks() []ReasoningBlock {
	if len(t.Blocks) > 0 {
		return t.Blocks
	}
	if strings.TrimSpace(t.Text) == "" && t.Signature == "" {
		return nil
	}
	return []ReasoningBlock{{Type: ReasoningBlockThinking, Text: t.Text, Signature: t.Signature}}
}

// HasReplayContent 判断这条推理是否值得保存：文本非空、有签名、有 redacted 数据三者之一。
// adaptive thinking 可能只返回签名不返回文本，此时签名本身就是回放所需的全部内容。
func (t ReasoningTrace) HasReplayContent() bool {
	if strings.TrimSpace(t.Text) != "" || t.Signature != "" {
		return true
	}
	for _, block := range t.Blocks {
		if strings.TrimSpace(block.Text) != "" || block.Signature != "" || block.Data != "" {
			return true
		}
	}
	return false
}
