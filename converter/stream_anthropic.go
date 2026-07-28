// stream_anthropic.go
// Anthropic Messages SSE 流 → Kiro AWS Event Stream 二进制帧
//
// 职责：
//   - 读取上游 Anthropic SSE 流
//   - 逐事件转换为 Kiro 二进制帧并写入 pipe
//   - 追踪 tool_use 状态（content_block_start 记录 id/name，delta 时回填）
//   - 流结束后追加 contextUsageEvent + meteringEvent

package converter

import (
	"bufio"
	json "github.com/goccy/go-json"
	"io"
	"log"
	"strings"
	"sync"
)

// AnthropicStreamConverter 将 Anthropic SSE 流转换为 Kiro AWS Event Stream
type AnthropicStreamConverter struct {
	pr        *io.PipeReader
	pw        *io.PipeWriter
	source    io.ReadCloser
	closeOnce sync.Once

	mu           sync.Mutex
	inputTokens  int
	outputTokens int
	cacheRead    int // cache_read_input_tokens
	cacheWrite   int // cache_creation_input_tokens
	outputChars  int
	toolUsed     bool
	outcome      StreamOutcome
	writeFailed  bool
	done         chan struct{}

	// 工具调用状态：key = content block index
	toolBlocks map[int]*anthropicToolBlock

	modelID                                                    string
	maxTokens                                                  int
	inputPrice, outputPrice, cacheInputPrice, cacheOutputPrice float64
	rateMultiplier                                             float64
}

type anthropicToolBlock struct {
	id   string
	name string
}

// NewAnthropicStreamConverter 创建转换器并启动后台 goroutine
func NewAnthropicStreamConverter(source io.ReadCloser, modelID string, maxTokens int, inputPrice, outputPrice, cacheInputPrice, cacheOutputPrice, rateMultiplier float64) *AnthropicStreamConverter {
	pr, pw := io.Pipe()
	c := &AnthropicStreamConverter{
		pr:               pr,
		pw:               pw,
		source:           source,
		outcome:          StreamOutcomePending,
		done:             make(chan struct{}),
		toolBlocks:       make(map[int]*anthropicToolBlock),
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

func (c *AnthropicStreamConverter) Read(p []byte) (int, error) {
	return c.pr.Read(p)
}

func (c *AnthropicStreamConverter) Close() error {
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

func (c *AnthropicStreamConverter) Outcome() StreamOutcome {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.outcome
}

func (c *AnthropicStreamConverter) Succeeded() bool {
	return c.Outcome() == StreamOutcomeCompleted
}

func (c *AnthropicStreamConverter) Error() error {
	return streamOutcomeError(c.Outcome())
}

func (c *AnthropicStreamConverter) Usage() (int, int) {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inputTokens + c.cacheRead + c.cacheWrite, c.outputTokens
}

// CacheTokens 返回缓存命中/写入的 token 数
func (c *AnthropicStreamConverter) CacheTokens() (cacheRead, cacheWrite int) {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cacheRead, c.cacheWrite
}

func (c *AnthropicStreamConverter) process(source io.ReadCloser) {
	defer close(c.done)
	defer source.Close()
	defer c.pw.Close()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[stream_anthropic] panic recovered: %v", r)
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
				log.Printf("[stream_anthropic] 读取错误: %v", err)
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

	c.mu.Lock()
	uncachedIn, out := c.inputTokens, c.outputTokens
	cacheR, cacheW := c.cacheRead, c.cacheWrite
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

	// 项目内统一约定 inputTokens 是总输入；Anthropic 原始 input_tokens 仅是未缓存尾部。
	totalIn := uncachedIn + cacheR + cacheW
	if !c.write(MetadataStopFrame(stopReason)) {
		return
	}
	if !c.write(ContextUsageFrame(totalIn+out, c.maxTokens)) {
		return
	}
	c.write(MeteringFrame(calcCredits(totalIn, out, cacheR, cacheW, c.inputPrice, c.outputPrice, c.cacheInputPrice, c.cacheOutputPrice, c.rateMultiplier)))
}

func (c *AnthropicStreamConverter) handleLine(line string) {
	if len(line) < 6 || line[0] != 'd' || line[4] != ':' {
		return
	}
	if line[:5] != "data:" {
		return
	}
	data := strings.TrimSpace(line[5:])
	if data == "" {
		return
	}

	var event map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return
	}

	eventTypeRaw, ok := event["type"]
	if !ok {
		return
	}
	var eventType string
	json.Unmarshal(eventTypeRaw, &eventType)

	switch eventType {
	case "message_start":
		c.handleMessageStart(event)
	case "content_block_start":
		c.handleContentBlockStart(event)
	case "content_block_delta":
		c.handleContentBlockDelta(event)
	case "content_block_stop":
		c.handleContentBlockStop(event)
	case "message_delta":
		c.handleMessageDelta(event)
	case "message_stop":
		c.mu.Lock()
		if c.outcome == StreamOutcomePending {
			c.outcome = StreamOutcomeCompleted
		}
		c.mu.Unlock()
	case "error":
		c.mu.Lock()
		if c.outcome == StreamOutcomePending {
			c.outcome = StreamOutcomeUpstreamProtocolFailure
		}
		c.mu.Unlock()
		log.Printf("[stream_anthropic] 上游错误: %s", data)
	}
}

func (c *AnthropicStreamConverter) handleMessageStart(event map[string]json.RawMessage) {
	var msg struct {
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if msgRaw, ok := event["message"]; ok {
		json.Unmarshal(msgRaw, &msg)
		c.mu.Lock()
		if msg.Usage.InputTokens > 0 {
			c.inputTokens = msg.Usage.InputTokens
		}
		if msg.Usage.CacheReadInputTokens > 0 {
			c.cacheRead = msg.Usage.CacheReadInputTokens
		}
		if msg.Usage.CacheCreationInputTokens > 0 {
			c.cacheWrite = msg.Usage.CacheCreationInputTokens
		}
		c.mu.Unlock()
	}
}

func (c *AnthropicStreamConverter) handleContentBlockStart(event map[string]json.RawMessage) {
	idx := extractIndex(event)

	var block struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if blockRaw, ok := event["content_block"]; ok {
		json.Unmarshal(blockRaw, &block)
	}

	if block.Type == "tool_use" {
		c.mu.Lock()
		c.toolUsed = true
		c.mu.Unlock()
		c.toolBlocks[idx] = &anthropicToolBlock{id: block.ID, name: block.Name}
		c.write(ToolUseStartFrame(block.ID, block.Name))
	}
}

func (c *AnthropicStreamConverter) handleContentBlockDelta(event map[string]json.RawMessage) {
	idx := extractIndex(event)

	var delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
	}
	if deltaRaw, ok := event["delta"]; ok {
		json.Unmarshal(deltaRaw, &delta)
	}

	switch delta.Type {
	case "text_delta":
		if delta.Text != "" {
			c.mu.Lock()
			c.outputChars += len(delta.Text)
			c.mu.Unlock()
			c.write(AssistantResponseFrame(delta.Text, c.modelID))
		}
	case "input_json_delta":
		if delta.PartialJSON != "" {
			tb := c.toolBlocks[idx]
			id, name := "", ""
			if tb != nil {
				id, name = tb.id, tb.name
			}
			c.write(ToolUseInputFrame(id, name, delta.PartialJSON))
		}
		// thinking_delta / signature_delta 忽略
	}
}

func (c *AnthropicStreamConverter) handleContentBlockStop(event map[string]json.RawMessage) {
	idx := extractIndex(event)
	tb, ok := c.toolBlocks[idx]
	if !ok {
		return
	}
	delete(c.toolBlocks, idx)
	c.write(ToolUseStopFrame(tb.id, tb.name))
}

func (c *AnthropicStreamConverter) handleMessageDelta(event map[string]json.RawMessage) {
	var delta struct {
		StopReason string `json:"stop_reason"`
	}
	var usage struct {
		InputTokens              *int `json:"input_tokens"`
		OutputTokens             *int `json:"output_tokens"`
		CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	}

	if deltaRaw, ok := event["delta"]; ok {
		json.Unmarshal(deltaRaw, &delta)
	}
	if delta.StopReason == "tool_use" {
		c.mu.Lock()
		c.toolUsed = true
		c.mu.Unlock()
	}
	if usageRaw, ok := event["usage"]; ok {
		json.Unmarshal(usageRaw, &usage)
		c.mu.Lock()
		if usage.InputTokens != nil {
			c.inputTokens = *usage.InputTokens
		}
		if usage.CacheReadInputTokens != nil {
			c.cacheRead = *usage.CacheReadInputTokens
		}
		if usage.CacheCreationInputTokens != nil {
			c.cacheWrite = *usage.CacheCreationInputTokens
		}
		if usage.OutputTokens != nil && *usage.OutputTokens > 0 {
			c.outputTokens = *usage.OutputTokens
		}
		c.mu.Unlock()
	}

	// 如果 stop_reason=tool_use 但还有未关闭的 tool block，补发 stop 帧
	if delta.StopReason == "tool_use" {
		for idx, tb := range c.toolBlocks {
			c.write(ToolUseStopFrame(tb.id, tb.name))
			delete(c.toolBlocks, idx)
		}
	}
}

func (c *AnthropicStreamConverter) write(frame []byte) bool {
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
			log.Printf("[stream_anthropic] downstream closed: %v", err)
		}
		_ = c.source.Close()
		return false
	}
	return true
}

// extractIndex 从事件 map 中提取 index 字段
func extractIndex(event map[string]json.RawMessage) int {
	if raw, ok := event["index"]; ok {
		var idx int
		if err := json.Unmarshal(raw, &idx); err == nil {
			return idx
		}
	}
	return 0
}
