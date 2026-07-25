// stream_gemini.go
// Gemini SSE 流 → Kiro AWS Event Stream 二进制帧
//
// Gemini SSE 格式：
//   data: {"candidates":[{"content":{"parts":[{"text":"chunk"}],"role":"model"}}],"usageMetadata":{...}}
//
// 工具调用：
//   data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"tool","args":{...}}}],"role":"model"},"finishReason":"STOP"}]}
//
// 设计要点：
//   - 单 goroutine 读 SSE、写 pipe
//   - functionCall 生成确定性 toolUseId（Gemini 不提供 call id）
//   - 结尾补 metadata + contextUsage + metering 三帧

package converter

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"

	json "github.com/goccy/go-json"
)

// GeminiStreamConverter 将 Gemini SSE 流转换为 Kiro AWS Event Stream
type GeminiStreamConverter struct {
	pr        *io.PipeReader
	pw        *io.PipeWriter
	closeOnce sync.Once

	mu           sync.Mutex
	inputTokens  int
	outputTokens int
	cacheRead    int
	outputChars  int
	toolUsed     bool
	done         chan struct{}

	// 工具调用计数器（用于生成确定性 ID）
	toolCallIndex int

	modelID                                                    string
	maxTokens                                                  int
	inputPrice, outputPrice, cacheInputPrice, cacheOutputPrice float64
	rateMultiplier                                             float64
}

func NewGeminiStreamConverter(source io.ReadCloser, modelID string, maxTokens int, inputPrice, outputPrice, cacheInputPrice, cacheOutputPrice, rateMultiplier float64) *GeminiStreamConverter {
	pr, pw := io.Pipe()
	c := &GeminiStreamConverter{
		pr:               pr,
		pw:               pw,
		done:             make(chan struct{}),
		modelID:          modelID,
		maxTokens:        maxTokens,
		inputPrice:       inputPrice,
		outputPrice:      outputPrice,
		cacheInputPrice:  cacheInputPrice,
		cacheOutputPrice: cacheOutputPrice,
		rateMultiplier:   normalizeRateMultiplier(rateMultiplier),
	}
	go c.process(source)
	return c
}

func (c *GeminiStreamConverter) Read(p []byte) (int, error) {
	return c.pr.Read(p)
}

func (c *GeminiStreamConverter) Close() error {
	c.closeOnce.Do(func() { c.pr.Close() })
	return nil
}

// Usage 等待流结束后返回 token 用量（阻塞直到 done）
func (c *GeminiStreamConverter) Usage() (inputTokens, outputTokens int) {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inputTokens, c.outputTokens
}

// CacheTokens 返回缓存命中/写入 token 数
func (c *GeminiStreamConverter) CacheTokens() (cacheRead, cacheWrite int) {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cacheRead, 0
}

func (c *GeminiStreamConverter) process(source io.ReadCloser) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[stream_gemini] panic recovered: %v", r)
		}
	}()
	defer close(c.done)
	defer source.Close()
	defer c.pw.Close()

	reader := bufio.NewReaderSize(source, 64*1024)
	c.write(InitialResponseFrame(""))

	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")

		if line != "" {
			c.handleLine(line)
		}

		if err != nil {
			if err != io.EOF {
				log.Printf("[stream_gemini] 读取错误: %v", err)
			}
			break
		}
	}

	// 流结束：写入 Kiro 必需的结尾帧
	c.mu.Lock()
	in, out := c.inputTokens, c.outputTokens
	cacheR := c.cacheRead
	stopReason := "END_TURN"
	if c.toolUsed {
		stopReason = "TOOL_USE"
	}
	if out == 0 && c.outputChars > 0 {
		out = c.outputChars / 4
		if out == 0 {
			out = 1
		}
		c.outputTokens = out
	}
	c.mu.Unlock()

	c.write(MetadataStopFrame(stopReason))
	c.write(ContextUsageFrame(in+out, c.maxTokens))
	c.write(MeteringFrame(calcCredits(in, out, cacheR, 0, c.inputPrice, c.outputPrice, c.cacheInputPrice, c.cacheOutputPrice, c.rateMultiplier)))
}

func (c *GeminiStreamConverter) handleLine(line string) {
	// Gemini SSE 格式: "data: {...}"
	if len(line) < 6 || line[0] != 'd' || line[:5] != "data:" {
		return
	}
	data := strings.TrimSpace(line[5:])
	if len(data) == 0 || data == "[DONE]" {
		return
	}

	var chunk geminiChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return
	}

	// 提取 usageMetadata
	if chunk.UsageMetadata != nil {
		c.mu.Lock()
		if chunk.UsageMetadata.PromptTokenCount > 0 {
			c.inputTokens = chunk.UsageMetadata.PromptTokenCount
		}
		if chunk.UsageMetadata.CandidatesTokenCount > 0 {
			c.outputTokens = chunk.UsageMetadata.CandidatesTokenCount
		}
		if chunk.UsageMetadata.CachedContentTokenCount > 0 {
			c.cacheRead = chunk.UsageMetadata.CachedContentTokenCount
		}
		c.mu.Unlock()
	}

	if len(chunk.Candidates) == 0 {
		return
	}

	candidate := chunk.Candidates[0]
	parts := candidate.Content.Parts

	for _, part := range parts {
		// 文本内容
		if part.Text != "" {
			c.mu.Lock()
			c.outputChars += len(part.Text)
			c.mu.Unlock()
			c.write(AssistantResponseFrame(part.Text, c.modelID))
		}

		// 工具调用
		if part.FunctionCall != nil {
			c.mu.Lock()
			c.toolUsed = true
			c.mu.Unlock()
			c.toolCallIndex++
			callID := fmt.Sprintf("call_%s_%d", part.FunctionCall.Name, c.toolCallIndex)

			c.write(ToolUseStartFrame(callID, part.FunctionCall.Name))

			// 序列化 args 为 JSON 字符串
			if part.FunctionCall.Args != nil {
				argsBytes, err := json.Marshal(part.FunctionCall.Args)
				if err == nil {
					c.write(ToolUseInputFrame(callID, part.FunctionCall.Name, string(argsBytes)))
				}
			}

			c.write(ToolUseStopFrame(callID, part.FunctionCall.Name))
		}
	}
}

func (c *GeminiStreamConverter) write(frame []byte) {
	if len(frame) == 0 {
		return
	}
	if _, err := c.pw.Write(frame); err != nil {
		log.Printf("[stream_gemini] 写入帧失败: %v", err)
	}
}

// ── Gemini SSE 数据结构 ───────────────────────────────────────────

type geminiChunk struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata *geminiUsageMeta  `json:"usageMetadata"`
}

type geminiCandidate struct {
	Content      geminiContentChunk `json:"content"`
	FinishReason string             `json:"finishReason"`
}

type geminiContentChunk struct {
	Parts []geminiPartChunk `json:"parts"`
	Role  string            `json:"role"`
}

type geminiPartChunk struct {
	Text         string               `json:"text"`
	FunctionCall *geminiFuncCallChunk `json:"functionCall"`
}

type geminiFuncCallChunk struct {
	Name string      `json:"name"`
	Args interface{} `json:"args"`
}

type geminiUsageMeta struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
}
