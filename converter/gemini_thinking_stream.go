package converter

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
)

// geminiToolCallPrefixBytes 决定每条流随机前缀的长度（hex 后 12 字符）。
const geminiToolCallPrefixBytes = 6

// geminiReasoningCapture 收集一条 Gemini 流中的思考摘要与思考签名，
// 供工具轮回放使用；思考摘要不写入 Kiro 正文。
type geminiReasoningCapture struct {
	thinking    strings.Builder
	signature   string
	toolCallIDs []string
}

// newGeminiToolCallPrefix 为每条流生成随机前缀。Gemini 不保证返回 call id，
// 只用流内序号会在不同轮次的同名调用之间重复，导致按工具调用 ID 保存的
// 签名被覆盖或回放到错误的轮次。crypto/rand.Read 自 Go 1.24 起不返回错误。
func newGeminiToolCallPrefix() string {
	raw := make([]byte, geminiToolCallPrefixBytes)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

func geminiToolCallID(prefix, name string, index int) string {
	return fmt.Sprintf("call_%s_%s_%d", name, prefix, index)
}

func (c *GeminiStreamConverter) recordThought(text string) {
	if text == "" {
		return
	}
	c.mu.Lock()
	c.reasoning.thinking.WriteString(text)
	c.mu.Unlock()
	c.write(ReasoningContentFrame(text))
}

// recordToolCall 记录工具调用 ID 与其 thoughtSignature。Gemini 只在一步的第一个
// functionCall 上签发签名，回放也只写回第一个调用；其他位置出现的签名无法按原
// 位置回放，记录日志后不保存（日志不含签名内容）。
func (c *GeminiStreamConverter) recordToolCall(callID, signature string, index int) {
	c.mu.Lock()
	c.reasoning.toolCallIDs = appendUniqueToolCallID(c.reasoning.toolCallIDs, callID)
	firstSigned := c.reasoning.signature != ""
	if signature != "" && index == 1 {
		c.reasoning.signature = signature
	}
	c.mu.Unlock()
	if signature != "" && index != 1 {
		log.Printf("[stream_gemini] 第 %d 个 functionCall 携带 thoughtSignature，未保存（首个调用签名已保存=%t）", index, firstSigned)
	}
}

// ReasoningTrace 返回本轮工具调用的推理回放状态；无工具调用或流未完成时为空。
// 签名为空时仍返回思考摘要，回放端会为该轮写跳过校验标记。
func (c *GeminiStreamConverter) ReasoningTrace() ReasoningTrace {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	capture := &c.reasoning
	if c.outcome != StreamOutcomeCompleted || len(capture.toolCallIDs) == 0 {
		return ReasoningTrace{}
	}
	if capture.thinking.Len() == 0 && capture.signature == "" {
		return ReasoningTrace{}
	}
	return ReasoningTrace{
		Present:     true,
		Text:        capture.thinking.String(),
		Signature:   capture.signature,
		Protocol:    geminiReasoningProtocol,
		ToolCallIDs: append([]string(nil), capture.toolCallIDs...),
	}
}
