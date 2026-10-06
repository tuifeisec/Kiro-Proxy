// stream_openai.go
// OpenAI Chat Completions SSE 流 → Kiro AWS Event Stream 二进制帧
//
// 职责：
//   - 读取上游 OpenAI SSE 流
//   - 逐事件转换为 Kiro 二进制帧并写入 pipe
//   - 追踪 tool_calls 状态（OpenAI 只在首帧给 id/name，后续只给 arguments 增量）
//   - 流结束后追加 contextUsageEvent + meteringEvent（Kiro IDE 需要这两帧才认为响应完成）
//
// 性能优化：
//   - 使用 bufio.Writer 包装 pipe 写端，减少 syscall 次数
//   - SSE 行解析使用切片操作避免 strings 分配
//   - 缓存 token 信息透传

package converter

import (
	"bufio"
	json "github.com/goccy/go-json"
	"io"
	"log"
	"strings"
	"sync"

	"kiro-proxy/billing"
)

// OpenAIStreamConverter 将 OpenAI SSE 流转换为 Kiro AWS Event Stream
type OpenAIStreamConverter struct {
	pr        *io.PipeReader
	pw        *io.PipeWriter
	source    io.ReadCloser
	closeOnce sync.Once

	// 统计（流结束后可读）
	mu           sync.Mutex
	inputTokens  int
	outputTokens int
	cacheRead    int // prompt_tokens_details.cached_tokens
	cacheWrite   int
	outputChars  int // 输出字符累计，用于上游无 usage 时估算
	toolUsed     bool
	outcome      StreamOutcome
	writeFailed  bool
	doneReceived bool
	done         chan struct{}

	// 工具调用状态跟踪
	// key = tool_calls[].index，value = {id, name}
	activeTools map[int]*toolCallState
	reasoning   strings.Builder
	wireField   string
	toolCallIDs []string

	// 配置
	modelID          string // 原始 Kiro modelId，写入 assistantResponseEvent
	maxTokens        int    // 用于计算 contextUsagePercentage
	inputPrice       float64
	outputPrice      float64
	cacheInputPrice  float64
	cacheOutputPrice float64
	rateMultiplier   float64
}

type toolCallState struct {
	id      string
	name    string
	started bool
}

// NewOpenAIStreamConverter 创建转换器并在后台启动转换 goroutine
func NewOpenAIStreamConverter(source io.ReadCloser, modelID string, maxTokens int, inputPrice, outputPrice, cacheInputPrice, cacheOutputPrice, rateMultiplier float64) *OpenAIStreamConverter {
	pr, pw := io.Pipe()
	c := &OpenAIStreamConverter{
		pr:               pr,
		pw:               pw,
		source:           source,
		outcome:          StreamOutcomePending,
		done:             make(chan struct{}),
		activeTools:      make(map[int]*toolCallState, 4),
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

// Read 实现 io.Reader，供 http.Response.Body 使用
func (c *OpenAIStreamConverter) Read(p []byte) (int, error) {
	return c.pr.Read(p)
}

// Close 实现 io.Closer
func (c *OpenAIStreamConverter) Close() error {
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

func (c *OpenAIStreamConverter) Outcome() StreamOutcome {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.outcome
}

func (c *OpenAIStreamConverter) Succeeded() bool {
	return c.Outcome() == StreamOutcomeCompleted
}

func (c *OpenAIStreamConverter) Error() error {
	return streamOutcomeError(c.Outcome())
}

// Usage 等待流结束后返回 token 用量（阻塞直到 done）
func (c *OpenAIStreamConverter) Usage() (inputTokens, outputTokens int) {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inputTokens, c.outputTokens
}

// CacheTokens 返回缓存命中/写入的 token 数
func (c *OpenAIStreamConverter) CacheTokens() (cacheRead, cacheWrite int) {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cacheRead, c.cacheWrite
}

// ReasoningTrace returns the completed provider-issued reasoning for a
// tool-call turn. Plain text turns intentionally do not produce a sidecar.
func (c *OpenAIStreamConverter) ReasoningTrace() ReasoningTrace {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.outcome != StreamOutcomeCompleted || c.reasoning.Len() == 0 || len(c.toolCallIDs) == 0 {
		return ReasoningTrace{}
	}
	return ReasoningTrace{
		Present:     true,
		Text:        c.reasoning.String(),
		WireField:   c.wireField,
		Protocol:    "chat",
		ToolCallIDs: append([]string(nil), c.toolCallIDs...),
	}
}

// process 在独立 goroutine 中运行，读取 SSE 并写入 pipe
func (c *OpenAIStreamConverter) process(source io.ReadCloser) {
	defer close(c.done)
	defer source.Close()
	defer c.pw.Close()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[stream_openai] panic recovered: %v", r)
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
		terminalFailure := c.outcome == StreamOutcomeUpstreamProtocolFailure
		doneReceived := c.doneReceived
		c.mu.Unlock()
		if writeFailed || terminalFailure || doneReceived {
			break
		}

		if err != nil {
			readErr = err
			if err != io.EOF {
				log.Printf("[stream_openai] 读取错误: %v", err)
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
	cacheR, cacheW := c.cacheRead, c.cacheWrite
	stopReason := "END_TURN"
	if c.toolUsed {
		stopReason = "TOOL_USE"
	}
	// 上游未返回 usage 时用字符数估算（约 4 字符/token）
	if out == 0 && c.outputChars > 0 {
		out = c.outputChars / 4
		if out == 0 {
			out = 1
		}
		c.outputTokens = out
	}
	c.mu.Unlock()

	// 始终发送 metadata 帧（Kiro 需要 token 信息，含缓存命中数据）
	if !c.write(MetadataStopFrame(stopReason)) {
		return
	}
	if !c.write(ContextUsageFrame(in+out, c.maxTokens)) {
		return
	}
	c.write(MeteringFrame(calcCredits(in, out, cacheR, cacheW, c.inputPrice, c.outputPrice, c.cacheInputPrice, c.cacheOutputPrice, c.rateMultiplier)))
}

func (c *OpenAIStreamConverter) handleLine(line string) {
	if len(line) < 6 || line[0] != 'd' || line[4] != ':' {
		return
	}
	// 快速前缀检测：data: (5 chars)
	if line[:5] != "data:" {
		return
	}
	data := strings.TrimSpace(line[5:])
	if len(data) == 0 {
		return
	}
	if data == "[DONE]" {
		c.mu.Lock()
		if c.outcome == StreamOutcomePending {
			c.outcome = StreamOutcomeCompleted
		}
		c.doneReceived = true
		c.mu.Unlock()
		return
	}

	var chunk openAIChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return
	}
	if chunk.Error != nil {
		c.mu.Lock()
		if c.outcome == StreamOutcomePending {
			c.outcome = StreamOutcomeUpstreamProtocolFailure
		}
		c.mu.Unlock()
		log.Printf("[stream_openai] upstream error: %s", data)
		return
	}

	// 提取 usage（OpenAI 在最后一帧带 usage，含缓存命中详情）
	if chunk.Usage != nil {
		c.mu.Lock()
		if chunk.Usage.PromptTokens > 0 {
			c.inputTokens = chunk.Usage.PromptTokens
		}
		if chunk.Usage.CompletionTokens > 0 {
			c.outputTokens = chunk.Usage.CompletionTokens
		}
		// OpenAI: prompt_tokens_details.cached_tokens
		if chunk.Usage.PromptTokensDetails != nil {
			if chunk.Usage.PromptTokensDetails.CachedTokens > 0 {
				c.cacheRead = chunk.Usage.PromptTokensDetails.CachedTokens
			}
			if chunk.Usage.PromptTokensDetails.CacheWriteTokens > 0 {
				c.cacheWrite = chunk.Usage.PromptTokensDetails.CacheWriteTokens
			}
		}
		// DeepSeek/OpenAI-compatible: prompt_cache_hit_tokens / miss_tokens
		if chunk.Usage.PromptCacheHitTokens > 0 {
			c.cacheRead = chunk.Usage.PromptCacheHitTokens
			if c.inputTokens == 0 {
				c.inputTokens = chunk.Usage.PromptCacheHitTokens + chunk.Usage.PromptCacheMissTokens
			}
		}
		if c.inputTokens > 0 && c.cacheRead+c.cacheWrite > c.inputTokens {
			log.Printf("[stream_openai] cache_read(%d) + cache_write(%d) > input_tokens(%d)，已按 input_tokens 限制", c.cacheRead, c.cacheWrite, c.inputTokens)
			c.cacheRead = min(c.cacheRead, c.inputTokens)
			c.cacheWrite = min(c.cacheWrite, c.inputTokens-c.cacheRead)
		}
		c.mu.Unlock()
	}

	if len(chunk.Choices) == 0 {
		return
	}

	choice := chunk.Choices[0]
	delta := choice.Delta
	finishReason := choice.FinishReason
	if finishReason != "" {
		c.mu.Lock()
		if c.outcome == StreamOutcomePending {
			c.outcome = StreamOutcomeCompleted
		}
		c.mu.Unlock()
	}

	// 文本内容
	if delta.Content != "" {
		c.mu.Lock()
		c.outputChars += len(delta.Content)
		c.mu.Unlock()
		c.write(AssistantResponseFrame(delta.Content, c.modelID))
	}

	if text, field := delta.reasoningDelta(); text != "" {
		c.mu.Lock()
		c.reasoning.WriteString(text)
		if c.wireField == "" {
			c.wireField = field
		}
		c.mu.Unlock()
	}

	// 工具调用增量
	if len(delta.ToolCalls) > 0 {
		c.handleToolCalls(delta.ToolCalls)
	}

	// 工具调用结束
	if finishReason == "tool_calls" {
		c.writeToolStopFrames()
	}
}

func (c *OpenAIStreamConverter) handleToolCalls(tcs []openAIToolCallDelta) {
	for _, tc := range tcs {
		idx := tc.Index
		state := c.activeTools[idx]
		if state == nil {
			state = &toolCallState{}
			c.activeTools[idx] = state
		}
		if tc.ID != "" {
			state.id = tc.ID
			c.mu.Lock()
			c.toolUsed = true
			c.toolCallIDs = appendUniqueToolCallID(c.toolCallIDs, tc.ID)
			c.mu.Unlock()
		}
		if tc.Function.Name != "" {
			state.name = tc.Function.Name
			c.mu.Lock()
			c.toolUsed = true
			c.mu.Unlock()
		}
		if !state.started && state.id != "" && state.name != "" {
			state.started = true
			c.write(ToolUseStartFrame(state.id, state.name))
		}

		// arguments 增量
		if tc.Function.Arguments != "" {
			if !state.started {
				state.started = true
				c.write(ToolUseStartFrame(state.id, state.name))
			}
			c.write(ToolUseInputFrame(state.id, state.name, tc.Function.Arguments))
		}
	}
}

func (c *OpenAIStreamConverter) writeToolStopFrames() {
	if len(c.activeTools) == 0 {
		c.write(ToolUseStopFrame("", ""))
		return
	}
	for _, state := range c.activeTools {
		if !state.started {
			c.write(ToolUseStartFrame(state.id, state.name))
		}
		c.write(ToolUseStopFrame(state.id, state.name))
	}
	c.activeTools = make(map[int]*toolCallState)
}

func (c *OpenAIStreamConverter) write(frame []byte) bool {
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
			log.Printf("[stream_openai] downstream closed: %v", err)
		}
		_ = c.source.Close()
		return false
	}
	return true
}

// ── OpenAI SSE 数据结构 ───────────────────────────────────────────

type openAIChunk struct {
	Choices []openAIChoice  `json:"choices"`
	Usage   *openAIUsage    `json:"usage"`
	Error   json.RawMessage `json:"error"`
}

type openAIChoice struct {
	Delta        openAIDelta `json:"delta"`
	FinishReason string      `json:"finish_reason"`
}

type openAIDelta struct {
	Content          string                `json:"content"`
	ReasoningContent string                `json:"reasoning_content"`
	ReasoningText    string                `json:"reasoning_text"`
	Reasoning        string                `json:"reasoning"`
	ToolCalls        []openAIToolCallDelta `json:"tool_calls"`
}

func (d openAIDelta) reasoningDelta() (string, string) {
	if d.ReasoningContent != "" {
		return d.ReasoningContent, "reasoning_content"
	}
	if d.ReasoningText != "" {
		return d.ReasoningText, "reasoning_text"
	}
	if d.Reasoning != "" {
		return d.Reasoning, "reasoning"
	}
	return "", ""
}

type openAIToolCallDelta struct {
	Index    int                 `json:"index"`
	ID       string              `json:"id"`
	Function openAIFunctionDelta `json:"function"`
}

type openAIFunctionDelta struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIUsage struct {
	PromptTokens          int                      `json:"prompt_tokens"`
	CompletionTokens      int                      `json:"completion_tokens"`
	PromptTokensDetails   *openAIPromptTokenDetail `json:"prompt_tokens_details"`
	PromptCacheHitTokens  int                      `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int                      `json:"prompt_cache_miss_tokens"`
}

type openAIPromptTokenDetail struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// calcCredits 根据 token 用量和每百万 token 单价计算本地估算积分。
func calcCredits(inputTokens, outputTokens, cacheRead, cacheWrite int, inputPrice, outputPrice, cacheInputPrice, cacheOutputPrice, rateMultiplier float64) float64 {
	if inputPrice <= 0 {
		inputPrice = 5
	}
	if outputPrice <= 0 {
		outputPrice = 30
	}
	if cacheInputPrice <= 0 {
		cacheInputPrice = 0.5
	}
	if cacheOutputPrice <= 0 {
		cacheOutputPrice = inputPrice * 1.25
	}
	return billing.CalculateCredits(inputTokens, outputTokens, cacheRead, cacheWrite, billing.Prices{
		Input:          inputPrice,
		Output:         outputPrice,
		CacheRead:      cacheInputPrice,
		CacheWrite:     cacheOutputPrice,
		RateMultiplier: normalizeRateMultiplier(rateMultiplier),
	})
}

func normalizeRateMultiplier(rateMultiplier float64) float64 {
	if rateMultiplier <= 0 {
		return 1
	}
	return rateMultiplier
}
