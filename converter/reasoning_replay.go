package converter

import "strings"

// 构建上游请求时，决定每个 assistant 轮回放哪些推理。

// followsToolResults 跟踪"工具结果之后、下一条 user 之前"：工具结果开启，用户文本或图片关闭。
// DeepSeek 对这段里的 assistant 回复与工具调用轮一样要求回传推理（magpie chat.go afterTool，#1822）。
func followsToolResults(previous bool, entry ParsedHistoryEntry) bool {
	if entry.UserContent != "" || len(entry.Images) > 0 {
		return false
	}
	if len(entry.ToolResults) > 0 {
		return true
	}
	return previous
}

// openAIReplayReasoning 返回该 assistant 轮要回放的推理。已有推理原样返回；
// 要求回放的上游在需要推理的轮缺推理时，补一个 reasoning_content 占位（DeepSeek 的字段名）。
func openAIReplayReasoning(reasoning *ReasoningTrace, replayRequired, needsReasoning bool) *ReasoningTrace {
	if reasoning != nil && (reasoning.Present || strings.TrimSpace(reasoning.Text) != "") {
		return reasoning
	}
	if replayRequired && needsReasoning {
		return &ReasoningTrace{Present: true, Text: ReasoningReplayPlaceholder, Protocol: "chat", WireField: "reasoning_content"}
	}
	return nil
}

// anthropicReasoningBlocks 构造放在 assistant 消息 text/tool_use 之前的推理块，保持上游原顺序。
//
// Claude（replayRequired=false）：回放有签名的 thinking（文本可为空）与 redacted_thinking；
// 无签名的 thinking 不发，Claude 会以签名无效拒绝整个请求。
//
// 要求回放的上游（replayRequired=true，如 DeepSeek /anthropic）：每个需要推理的轮都要有 thinking 块，
// 接受无签名、文本为空格的 thinking；不认识 redacted_thinking（422），所以不发，
// 该轮因此没有 thinking 时补占位（magpie anthropic.go anthropicReplaysThinking）。
func anthropicReasoningBlocks(reasoning *ReasoningTrace, replayRequired, needsThinking bool) []AnthropicBlock {
	var source []ReasoningBlock
	if reasoning != nil {
		source = reasoning.ReplayBlocks()
	}
	blocks := make([]AnthropicBlock, 0, len(source)+1)
	for _, block := range source {
		switch {
		case block.Type == ReasoningBlockRedactedThinking:
			if !replayRequired && block.Data != "" {
				blocks = append(blocks, AnthropicBlock{Type: ReasoningBlockRedactedThinking, Data: block.Data})
			}
		case replayRequired && block.Text == "":
			// 签名只对原文本有效，换成占位后不再携带签名
			blocks = append(blocks, anthropicThinkingBlock(ReasoningReplayPlaceholder, ""))
		case replayRequired || block.Signature != "":
			blocks = append(blocks, anthropicThinkingBlock(block.Text, block.Signature))
		}
	}
	if replayRequired && needsThinking && len(blocks) == 0 {
		blocks = append(blocks, anthropicThinkingBlock(ReasoningReplayPlaceholder, ""))
	}
	return blocks
}

func anthropicThinkingBlock(text, signature string) AnthropicBlock {
	return AnthropicBlock{Type: ReasoningBlockThinking, Thinking: &text, Signature: signature}
}
