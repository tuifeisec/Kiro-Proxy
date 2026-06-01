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
)

// OpenAIStreamConverter 将 OpenAI SSE 流转换为 Kiro AWS Event Stream
type OpenAIStreamConverter struct {
	pr        *io.PipeReader
	pw        *io.PipeWriter
	closeOnce sync.Once

	// 统计（流结束后可读）
	mu           sync.Mutex
	inputTokens  int
	outputTokens int
	cacheRead    int // prompt_tokens_details.cached_tokens
	cacheWrite   int
	outputChars  int // 输出字符累计，用于上游无 usage 时估算
	done         chan struct{}

	// 工具调用状态跟踪
	// key = tool_calls[].index，value = {id, name}
	activeTools map[int]*toolCallState

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
	id   string
	name string
}

// NewOpenAIStreamConverter 创建转换器并在后台启动转换 goroutine
func NewOpenAIStreamConverter(source io.ReadCloser, modelID string, maxTokens int, inputPrice, outputPrice, cacheInputPrice, cacheOutputPrice, rateMultiplier float64) *OpenAIStreamConverter {
	pr, pw := io.Pipe()
	c := &OpenAIStreamConverter{
		pr:               pr,
		pw:               pw,
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
	c.closeOnce.Do(func() { c.pr.Close() })
	return nil
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

// process 在独立 goroutine 中运行，读取 SSE 并写入 pipe
func (c *OpenAIStreamConverter) process(source io.ReadCloser) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[stream_openai] panic recovered: %v", r)
		}
	}()
	defer close(c.done)
	defer source.Close()
	defer c.pw.Close()

	reader := bufio.NewReaderSize(source, 64*1024)

	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")

		if line != "" {
			c.handleLine(line)
		}

		if err != nil {
			if err != io.EOF {
				log.Printf("[stream_openai] 读取错误: %v", err)
			}
			break
		}
	}

	// 流结束：写入 Kiro 必需的结尾帧
	c.mu.Lock()
	in, out := c.inputTokens, c.outputTokens
	cacheR, cacheW := c.cacheRead, c.cacheWrite
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
	c.write(MetadataFrame(in, out, cacheR, cacheW))
	c.write(ContextUsageFrame(in+out, c.maxTokens))
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
	if len(data) == 0 || data == "[DONE]" {
		return
	}

	var chunk openAIChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
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
			c.cacheRead = chunk.Usage.PromptTokensDetails.CachedTokens
		}
		// DeepSeek/OpenAI-compatible: prompt_cache_hit_tokens / miss_tokens
		if chunk.Usage.PromptCacheHitTokens > 0 {
			c.cacheRead = chunk.Usage.PromptCacheHitTokens
			if c.inputTokens == 0 {
				c.inputTokens = chunk.Usage.PromptCacheHitTokens + chunk.Usage.PromptCacheMissTokens
			}
		}
		if c.cacheRead > c.inputTokens && c.inputTokens > 0 {
			log.Printf("[stream_openai] cache_read(%d) > input_tokens(%d)，已按 input_tokens 限制", c.cacheRead, c.inputTokens)
			c.cacheRead = c.inputTokens
		}
		c.mu.Unlock()
	}

	if len(chunk.Choices) == 0 {
		return
	}

	choice := chunk.Choices[0]
	delta := choice.Delta
	finishReason := choice.FinishReason

	// 文本内容
	if delta.Content != "" {
		c.mu.Lock()
		c.outputChars += len(delta.Content)
		c.mu.Unlock()
		c.write(AssistantResponseFrame(delta.Content, c.modelID))
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

		// 首次出现该 index：记录 id/name，发送开始帧
		if tc.ID != "" || tc.Function.Name != "" {
			state := &toolCallState{id: tc.ID, name: tc.Function.Name}
			c.activeTools[idx] = state
			c.write(ToolUseStartFrame(tc.ID, tc.Function.Name))
		}

		// arguments 增量
		if tc.Function.Arguments != "" {
			state := c.activeTools[idx]
			id, name := "", ""
			if state != nil {
				id, name = state.id, state.name
			}
			c.write(ToolUseInputFrame(id, name, tc.Function.Arguments))
		}
	}
}

func (c *OpenAIStreamConverter) writeToolStopFrames() {
	if len(c.activeTools) == 0 {
		c.write(ToolUseStopFrame("", ""))
		return
	}
	for _, state := range c.activeTools {
		c.write(ToolUseStopFrame(state.id, state.name))
	}
	c.activeTools = make(map[int]*toolCallState)
}

func (c *OpenAIStreamConverter) write(frame []byte) {
	if len(frame) == 0 {
		return
	}
	if _, err := c.pw.Write(frame); err != nil {
		log.Printf("[stream_openai] 写入帧失败: %v", err)
	}
}

// ── OpenAI SSE 数据结构 ───────────────────────────────────────────

type openAIChunk struct {
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage"`
}

type openAIChoice struct {
	Delta        openAIDelta `json:"delta"`
	FinishReason string      `json:"finish_reason"`
}

type openAIDelta struct {
	Content   string                `json:"content"`
	ToolCalls []openAIToolCallDelta `json:"tool_calls"`
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
	CachedTokens int `json:"cached_tokens"`
}

// calcCredits 根据 token 用量和每百万 token 单价计算 credit 消耗。
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
		cacheOutputPrice = outputPrice
	}
	regularInput := inputTokens - cacheRead - cacheWrite
	if regularInput < 0 {
		regularInput = 0
	}
	return ((float64(regularInput)*inputPrice +
		float64(outputTokens)*outputPrice +
		float64(cacheRead)*cacheInputPrice +
		float64(cacheWrite)*cacheOutputPrice) / 1000000) * normalizeRateMultiplier(rateMultiplier)
}

func normalizeRateMultiplier(rateMultiplier float64) float64 {
	if rateMultiplier <= 0 {
		return 1
	}
	return rateMultiplier
}
