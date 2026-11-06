package converter

import "log"

// 本文件处理 OpenAI Chat 流中 delta 的正文与推理输出：<think> 拆分、形态诊断与无法解析事件的终止。

// handleDeltaText 处理一个 delta 的正文与推理文本。
// 独立推理字段先于正文处理：同一 chunk 里出现推理字段时，正文中的 <think> 不再拆分。
func (c *OpenAIStreamConverter) handleDeltaText(text openAIDeltaText) {
	for _, partType := range text.SkippedParts {
		c.diagnoseOnce("part:"+partType, "[stream_openai] delta.content 中的 %s 片段不是正文文本，已跳过", partType)
	}
	for _, field := range text.OpaqueFields {
		c.diagnoseOnce("opaque:"+field, "[stream_openai] delta.%s 有结构但没有可读文本（如加密推理），未计入推理输出", field)
	}
	if text.Reasoning != "" {
		c.appendReasoning(text.Reasoning, text.ReasoningField)
		c.writeThinkSplit(c.thinkSplit.markIndependentReasoning())
	}
	if text.Content != "" {
		c.mu.Lock()
		c.outputChars += len(text.Content)
		c.mu.Unlock()
		c.writeThinkSplit(c.thinkSplit.feed(text.Content))
	}
}

// flushThinkSplit 在正文结束（工具调用开始、finish_reason、流完成）时交还 <think> 拆分的缓冲。
func (c *OpenAIStreamConverter) flushThinkSplit() {
	c.writeThinkSplit(c.thinkSplit.finish())
}

func (c *OpenAIStreamConverter) writeThinkSplit(out thinkSplit) {
	if out.Reasoning != "" {
		c.appendReasoning(out.Reasoning, ThinkTagWireField)
	}
	if out.Unclosed {
		c.mu.Lock()
		reasoningBytes := c.reasoning.Len()
		c.mu.Unlock()
		log.Printf("[stream_openai] 正文开头的 <think> 到正文结束仍未闭合，已缓冲的思考作为推理输出保留（推理共 %d 字节）", reasoningBytes)
	}
	if out.Content != "" {
		c.write(AssistantResponseFrame(out.Content, c.modelID))
	}
}

func (c *OpenAIStreamConverter) appendReasoning(text, field string) {
	c.mu.Lock()
	c.reasoning.WriteString(text)
	if c.wireField == "" {
		c.wireField = field
	}
	c.mu.Unlock()
	c.write(ReasoningContentFrame(text))
}

// failUnparseable 让容错后仍无法解析的事件终止流，错误经 ExceptionFrame 与 Error() 传给 Kiro 和请求详情。
// 已进入终态（如 finish_reason 之后的 usage 尾帧）时只记录日志：正文已完整，不因尾帧丢弃整轮回复。
func (c *OpenAIStreamConverter) failUnparseable(data string, err error) {
	detail := streamDecodeFailureMessage("Chat", data, err)
	c.mu.Lock()
	failed := c.outcome == StreamOutcomePending
	outcome := c.outcome
	if failed {
		c.outcome = StreamOutcomeUpstreamProtocolFailure
		c.failureDetail = detail
	}
	c.mu.Unlock()
	if failed {
		log.Printf("[stream_openai] %s", detail)
		return
	}
	log.Printf("[stream_openai] 流已进入终态 %s，不再因其后的事件改判: %s", outcome, detail)
}

// diagnoseOnce 每个流每类形态只记录一次，避免逐 chunk 刷屏。
func (c *OpenAIStreamConverter) diagnoseOnce(key, format, arg string) {
	if c.diagnosedDeltaKind[key] {
		return
	}
	if c.diagnosedDeltaKind == nil {
		c.diagnosedDeltaKind = make(map[string]bool)
	}
	c.diagnosedDeltaKind[key] = true
	log.Printf(format, arg)
}
