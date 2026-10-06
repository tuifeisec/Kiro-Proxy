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
	source    io.ReadCloser
	closeOnce sync.Once

	mu           sync.Mutex
	inputTokens  int
	outputTokens int
	cacheRead    int
	outputChars  int
	toolUsed     bool
	outcome      StreamOutcome
	writeFailed  bool
	done         chan struct{}

	// 工具调用计数器（用于生成确定性 ID）
	toolCallIndex int

	modelID                                                    string
	maxTokens                                                  int
	inputPrice, outputPrice, cacheInputPrice, cacheOutputPrice float64
	rateMultiplier                                             float64
}

func NewGeminiStreamConverter(source io.ReadCloser, modelID string, maxTokens int, inputPrice, outputPrice, cacheInputPrice, cacheOutputPrice, rateMultiplier float64) *GeminiStreamConverter {
	if inputPrice <= 0 {
		inputPrice = 1.5
	}
	if outputPrice <= 0 {
		outputPrice = 9
	}
	if cacheInputPrice <= 0 {
		cacheInputPrice = 0.15
	}
	if cacheOutputPrice <= 0 {
		cacheOutputPrice = 1.5
	}
	pr, pw := io.Pipe()
	c := &GeminiStreamConverter{
		pr:               pr,
		pw:               pw,
		source:           source,
		outcome:          StreamOutcomePending,
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
	c.closeOnce.Do(func() {
		select {
		case <-c.done:
		default:
			c.mu.Lock()
			if c.outcome == StreamOutcomePending {
				c.outcome = StreamOutcomeClientCanceled
			}
			c.mu.Unlock()
		}
		_ = c.pr.Close()
		_ = c.source.Close()
	})
	return nil
}

func (c *GeminiStreamConverter) Outcome() StreamOutcome {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.outcome
}

func (c *GeminiStreamConverter) Succeeded() bool {
	return c.Outcome() == StreamOutcomeCompleted
}

func (c *GeminiStreamConverter) Error() error {
	return streamOutcomeError(c.Outcome())
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
	defer close(c.done)
	defer source.Close()
	defer c.pw.Close()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[stream_gemini] panic recovered: %v", r)
			c.mu.Lock()
			if c.outcome == StreamOutcomePending || c.outcome == StreamOutcomeCompleted {
				c.outcome = StreamOutcomeReadFailure
			}
			outcome := c.outcome
			c.mu.Unlock()
			if outcome != StreamOutcomeClientCanceled {
				c.write(ExceptionFrame("upstream_stream_interrupted", streamFailureMessage(outcome)))
			}
		}
	}()

	reader := bufio.NewReaderSize(source, 64*1024)
	if !c.write(InitialResponseFrame("")) {
		return
	}
	var readErr error

	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")

		if line != "" {
			c.handleLine(line)
		}
		c.mu.Lock()
		writeFailed := c.writeFailed
		terminal := c.outcome != StreamOutcomePending
		c.mu.Unlock()
		if writeFailed || terminal {
			break
		}

		if err != nil {
			readErr = err
			if err != io.EOF {
				log.Printf("[stream_gemini] 读取错误: %v", err)
			}
			break
		}
	}

	c.mu.Lock()
	if c.outcome == StreamOutcomePending {
		c.outcome = streamOutcomeFromReadError(readErr)
	}
	outcome := c.outcome
	c.mu.Unlock()
	if outcome != StreamOutcomeCompleted {
		if outcome != StreamOutcomeClientCanceled {
			c.write(ExceptionFrame("upstream_stream_interrupted", streamFailureMessage(outcome)))
		}
		return
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

	if !c.write(MetadataStopFrame(stopReason)) {
		return
	}
	if !c.write(ContextUsageFrame(in+out, c.maxTokens)) {
		return
	}
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
	if chunk.Error != nil {
		c.mu.Lock()
		if c.outcome == StreamOutcomePending {
			c.outcome = StreamOutcomeUpstreamProtocolFailure
		}
		c.mu.Unlock()
		log.Printf("[stream_gemini] upstream error: %s", data)
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
	if candidate.FinishReason != "" {
		c.mu.Lock()
		if c.outcome == StreamOutcomePending {
			c.outcome = StreamOutcomeCompleted
		}
		c.mu.Unlock()
	}
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

func (c *GeminiStreamConverter) write(frame []byte) bool {
	if len(frame) == 0 {
		return true
	}
	if _, err := c.pw.Write(frame); err != nil {
		c.mu.Lock()
		firstFailure := !c.writeFailed
		c.writeFailed = true
		if c.outcome == StreamOutcomePending || c.outcome == StreamOutcomeCompleted {
			c.outcome = StreamOutcomeClientCanceled
		}
		c.mu.Unlock()
		if firstFailure {
			log.Printf("[stream_gemini] downstream closed: %v", err)
		}
		_ = c.source.Close()
		return false
	}
	return true
}

// ── Gemini SSE 数据结构 ───────────────────────────────────────────

type geminiChunk struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata *geminiUsageMeta  `json:"usageMetadata"`
	Error         json.RawMessage   `json:"error"`
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
