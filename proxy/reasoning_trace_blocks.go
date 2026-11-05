package proxy

import (
	"kiro-proxy/converter"
	"kiro-proxy/store"
)

// 推理块在转换层与持久层之间逐字段映射；持久格式由 store.ReasoningTraceBlock 的 JSON tag 固定。

func storeReasoningBlocks(blocks []converter.ReasoningBlock) []store.ReasoningTraceBlock {
	if len(blocks) == 0 {
		return nil
	}
	result := make([]store.ReasoningTraceBlock, len(blocks))
	for i, block := range blocks {
		result[i] = store.ReasoningTraceBlock{Type: block.Type, Text: block.Text, Signature: block.Signature, Data: block.Data}
	}
	return result
}

func converterReasoningBlocks(blocks []store.ReasoningTraceBlock) []converter.ReasoningBlock {
	if len(blocks) == 0 {
		return nil
	}
	result := make([]converter.ReasoningBlock, len(blocks))
	for i, block := range blocks {
		result[i] = converter.ReasoningBlock{Type: block.Type, Text: block.Text, Signature: block.Signature, Data: block.Data}
	}
	return result
}
