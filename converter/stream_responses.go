// stream_responses.go
// OpenAI Responses API SSE 流 → Kiro AWS Event Stream 二进制帧（一段式直转）
//
// Responses API SSE 事件类型（与 Chat Completions 完全不同）：
//   - response.output_text.delta              → assistantResponseEvent（文本增量）
//   - response.output_item.added(function_call) → toolUseEvent（工具调用开始）
//   - response.function_call_arguments.delta  → toolUseEvent（工具参数增量）
//   - response.output_item.done(function_call)  → toolUseEvent（工具调用结束）
//   - response.completed                       → usage 提取，流结束
//
// 设计要点：
//   - 直接复用 event_frame.go 的高性能帧构建器，避免中间 chat SSE 的解析/再序列化
//   - 单 goroutine 读 SSE、写 pipe，零额外拷贝
//   - 工具调用同时按 item_id / call_id 建立索引，兼容不同上游的事件字段差异
//   - 结尾补 metadata + contextUsage + metering 三帧（Kiro IDE 判定响应完成所需）

package converter

import (
	"bufio"
	"io"
	"log"
	"strings"
	"sync"

	json "github.com/goccy/go-json"
)

// ResponsesStreamConverter 将 Responses API SSE 流转换为 Kiro AWS Event Stream
type ResponsesStreamConverter struct {
	pr        *io.PipeReader
	pw        *io.PipeWriter
	closeOnce sync.Once

	mu           sync.Mutex
	inputTokens  int
	outputTokens int
	cacheRead    int
	outputChars  int
	responseID   string // 上游返回的 response ID（用于续传）
	completed    bool
	failed       bool
	done         chan struct{}

	// 活跃工具调用：按 item_id 与 call_id 双索引，保证不同上游事件都能回填
	toolsByItem map[string]*respToolState
	toolsByCall map[string]*respToolState
	toolOrder   []*respToolState

	modelID                                                    string
	maxTokens                                                  int
	inputPrice, outputPrice, cacheInputPrice, cacheOutputPrice float64
	rateMultiplier                                             float64
}

type respToolState struct {
	callID  string
	name    string
	gotArgs bool
}

func NewResponsesStreamConverter(source io.ReadCloser, modelID string, maxTokens int, inputPrice, outputPrice, cacheInputPrice, cacheOutputPrice, rateMultiplier float64) *ResponsesStreamConverter {
	pr, pw := io.Pipe()
	c := &ResponsesStreamConverter{
		pr:               pr,
		pw:               pw,
		done:             make(chan struct{}),
		toolsByItem:      make(map[string]*respToolState, 4),
		toolsByCall:      make(map[string]*respToolState, 4),
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

func (c *ResponsesStreamConverter) Read(p []byte) (int, error) {
	return c.pr.Read(p)
}

func (c *ResponsesStreamConverter) Close() error {
	c.closeOnce.Do(func() { c.pr.Close() })
	return nil
}

// Usage 等待流结束后返回 token 用量（阻塞直到 done）
func (c *ResponsesStreamConverter) Usage() (inputTokens, outputTokens int) {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inputTokens, c.outputTokens
}

// CacheTokens 返回缓存命中/写入 token 数
func (c *ResponsesStreamConverter) CacheTokens() (cacheRead, cacheWrite int) {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cacheRead, 0
}

// ResponseID 返回上游的 response ID（用于 previous_response_id 续传）
func (c *ResponsesStreamConverter) ResponseID() string {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.responseID
}

// Succeeded 表示 Responses 流明确完成且未收到错误/失败事件。
func (c *ResponsesStreamConverter) Succeeded() bool {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.completed && !c.failed
}

func (c *ResponsesStreamConverter) process(source io.ReadCloser) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[stream_responses] panic recovered: %v", r)
		}
	}()
	defer close(c.done)
	defer source.Close()
	defer c.pw.Close()

	reader := bufio.NewReaderSize(source, 64*1024)
	var currentEventType string

	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")

		if len(line) > 0 {
			if len(line) > 6 && line[0] == 'e' && line[:6] == "event:" {
				currentEventType = strings.TrimSpace(line[6:])
			} else if len(line) > 5 && line[0] == 'd' && line[:5] == "data:" {
				data := strings.TrimSpace(line[5:])
				if data != "" && data != "[DONE]" {
					c.handleEvent(currentEventType, data)
				}
				currentEventType = ""
			}
		}

		if err != nil {
			if err != io.EOF {
				log.Printf("[stream_responses] 读取错误: %v", err)
				c.mu.Lock()
				c.failed = true
				c.mu.Unlock()
			}
			break
		}
	}

	// 结尾帧：与 OpenAI chat 路径保持一致（Kiro IDE 依赖这些帧判定完成）
	c.mu.Lock()
	in, out := c.inputTokens, c.outputTokens
	cacheR := c.cacheRead
	if out == 0 && c.outputChars > 0 {
		out = c.outputChars / 4
		if out == 0 {
			out = 1
		}
		c.outputTokens = out
	}
	c.mu.Unlock()

	c.write(MetadataFrame(in, out, cacheR, 0))
	c.write(ContextUsageFrame(in+out, c.maxTokens))
	c.write(MeteringFrame(calcCredits(in, out, cacheR, 0, c.inputPrice, c.outputPrice, c.cacheInputPrice, c.cacheOutputPrice, c.rateMultiplier)))
}

func (c *ResponsesStreamConverter) handleEvent(eventType, data string) {
	switch eventType {
	case "response.output_text.delta":
		var ev struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal([]byte(data), &ev) == nil && ev.Delta != "" {
			c.mu.Lock()
			c.outputChars += len(ev.Delta)
			c.mu.Unlock()
			c.write(AssistantResponseFrame(ev.Delta, c.modelID))
		}

	case "response.output_item.added":
		var ev struct {
			Item struct {
				ID        string `json:"id"`
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil || ev.Item.Type != "function_call" {
			return
		}
		st := &respToolState{callID: ev.Item.CallID, name: ev.Item.Name}
		if ev.Item.ID != "" {
			c.toolsByItem[ev.Item.ID] = st
		}
		if ev.Item.CallID != "" {
			c.toolsByCall[ev.Item.CallID] = st
		}
		c.toolOrder = append(c.toolOrder, st)
		c.write(ToolUseStartFrame(ev.Item.CallID, ev.Item.Name))
		// 个别上游在 added 时即给出完整 arguments（非增量），直接透传
		if ev.Item.Arguments != "" {
			st.gotArgs = true
			c.write(ToolUseInputFrame(ev.Item.CallID, ev.Item.Name, ev.Item.Arguments))
		}

	case "response.function_call_arguments.delta":
		var ev struct {
			Delta  string `json:"delta"`
			ItemID string `json:"item_id"`
			CallID string `json:"call_id"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil || ev.Delta == "" {
			return
		}
		st := c.findTool(ev.ItemID, ev.CallID)
		callID, name := ev.CallID, ""
		if st != nil {
			st.gotArgs = true
			callID, name = st.callID, st.name
		}
		c.write(ToolUseInputFrame(callID, name, ev.Delta))

	case "response.output_item.done":
		var ev struct {
			Item struct {
				ID        string `json:"id"`
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil || ev.Item.Type != "function_call" {
			return
		}
		st := c.findTool(ev.Item.ID, ev.Item.CallID)
		name := ev.Item.Name
		callID := ev.Item.CallID
		if st != nil {
			if name == "" {
				name = st.name
			}
			if callID == "" {
				callID = st.callID
			}
		}
		// 若全程没有 arguments 增量，但 done 带了完整 arguments，则补一帧 input
		if (st == nil || !st.gotArgs) && ev.Item.Arguments != "" {
			c.write(ToolUseInputFrame(callID, name, ev.Item.Arguments))
		}
		c.write(ToolUseStopFrame(callID, name))
		c.removeTool(ev.Item.ID, callID)

	case "response.completed", "response.incomplete":
		var ev struct {
			Response struct {
				ID    string `json:"id"`
				Usage struct {
					InputTokens           int `json:"input_tokens"`
					OutputTokens          int `json:"output_tokens"`
					PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
					PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
					InputTokensDetails    struct {
						CachedTokens int `json:"cached_tokens"`
					} `json:"input_tokens_details"`
				} `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(data), &ev) == nil {
			c.mu.Lock()
			if eventType == "response.completed" {
				c.completed = true
			} else {
				c.failed = true
			}
			if ev.Response.Usage.InputTokens > 0 {
				c.inputTokens = ev.Response.Usage.InputTokens
			}
			if ev.Response.Usage.OutputTokens > 0 {
				c.outputTokens = ev.Response.Usage.OutputTokens
			}
			if ev.Response.Usage.InputTokensDetails.CachedTokens > 0 {
				c.cacheRead = ev.Response.Usage.InputTokensDetails.CachedTokens
			}
			// DeepSeek/OpenAI-compatible usage fields.
			if ev.Response.Usage.PromptCacheHitTokens > 0 {
				c.cacheRead = ev.Response.Usage.PromptCacheHitTokens
				if c.inputTokens == 0 {
					c.inputTokens = ev.Response.Usage.PromptCacheHitTokens + ev.Response.Usage.PromptCacheMissTokens
				}
			}
			if c.cacheRead > c.inputTokens && c.inputTokens > 0 {
				log.Printf("[stream_responses] cache_read(%d) > input_tokens(%d)，已按 input_tokens 限制", c.cacheRead, c.inputTokens)
				c.cacheRead = c.inputTokens
			}
			if ev.Response.ID != "" {
				c.responseID = ev.Response.ID
			}
			c.mu.Unlock()
		}

	case "error":
		c.mu.Lock()
		c.failed = true
		c.mu.Unlock()
		log.Printf("[stream_responses] 上游错误: %s", data)
	}
}

// findTool 按 item_id → call_id → 唯一活跃工具 的顺序查找
func (c *ResponsesStreamConverter) findTool(itemID, callID string) *respToolState {
	if itemID != "" {
		if st := c.toolsByItem[itemID]; st != nil {
			return st
		}
	}
	if callID != "" {
		if st := c.toolsByCall[callID]; st != nil {
			return st
		}
	}
	if len(c.toolOrder) == 1 {
		return c.toolOrder[0]
	}
	return nil
}

func (c *ResponsesStreamConverter) removeTool(itemID, callID string) {
	var target *respToolState
	if itemID != "" {
		target = c.toolsByItem[itemID]
		delete(c.toolsByItem, itemID)
	}
	if callID != "" {
		if target == nil {
			target = c.toolsByCall[callID]
		}
		delete(c.toolsByCall, callID)
	}
	if target != nil {
		for i, st := range c.toolOrder {
			if st == target {
				c.toolOrder = append(c.toolOrder[:i], c.toolOrder[i+1:]...)
				break
			}
		}
	}
}

func (c *ResponsesStreamConverter) write(frame []byte) {
	if len(frame) == 0 {
		return
	}
	if _, err := c.pw.Write(frame); err != nil {
		log.Printf("[stream_responses] 写入帧失败: %v", err)
	}
}
