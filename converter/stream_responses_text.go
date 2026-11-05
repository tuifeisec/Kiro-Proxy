package converter

import (
	"log"

	json "github.com/goccy/go-json"
)

// 本文件处理 Responses 流中的事件解码失败与正文开头 <think> 拆分的输出。

// decodeEvent 解码已知事件；失败时按流错误终止，避免内容或终态被静默丢弃。
func (c *ResponsesStreamConverter) decodeEvent(data string, v interface{}) bool {
	if err := json.Unmarshal([]byte(data), v); err != nil {
		c.failUnparseable(data, err)
		return false
	}
	return true
}

// failUnparseable 让容错后仍无法解析的事件终止流，错误经 ExceptionFrame 与 Error() 传给 Kiro 和请求详情。
// process 在任何终态后都会停止读取，因此这里只会在 Pending 时被调用。
func (c *ResponsesStreamConverter) failUnparseable(data string, err error) {
	detail := streamDecodeFailureMessage("Responses", data, err)
	c.mu.Lock()
	if c.outcome == StreamOutcomePending {
		c.outcome = StreamOutcomeUpstreamProtocolFailure
		c.failureDetail = detail
	}
	c.mu.Unlock()
	log.Printf("[stream_responses] %s", detail)
}

// flushThinkSplit 在消息项结束、工具调用开始或流完成时交还 <think> 拆分的缓冲。
func (c *ResponsesStreamConverter) flushThinkSplit() {
	c.writeThinkSplit(c.thinkSplit.finish())
}

func (c *ResponsesStreamConverter) writeThinkSplit(out thinkSplit) {
	if out.Reasoning != "" {
		c.mu.Lock()
		c.thinkReasoning.WriteString(out.Reasoning)
		c.mu.Unlock()
	}
	if out.Unclosed {
		c.mu.Lock()
		reasoningBytes := c.thinkReasoning.Len()
		c.mu.Unlock()
		log.Printf("[stream_responses] 正文开头的 <think> 到消息结束仍未闭合，已缓冲的思考作为推理输出保留（共 %d 字节）", reasoningBytes)
	}
	if out.Content != "" {
		c.write(AssistantResponseFrame(out.Content, c.modelID))
	}
}
