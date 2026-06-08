// kiro_to_responses.go
// Kiro → OpenAI Responses API (/v1/responses) 格式转换
//
// Responses API 与 Chat Completions 的区别：
//   - 用 "input" 数组代替 "messages"
//   - input 里的 item 类型：message / function_call / function_call_output
//   - 有 "instructions" 字段代替 system message
//   - 流式响应事件类型完全不同（response.output_text.delta 等）

package converter

import (
	json "github.com/goccy/go-json"
	"strings"
)

// ResponsesRequest OpenAI Responses API 请求体
type ResponsesRequest struct {
	Model              string           `json:"model"`
	Instructions       string           `json:"instructions,omitempty"`
	Input              []ReqInput       `json:"input"`
	Tools              []ReqTool        `json:"tools,omitempty"`
	Reasoning          *OpenAIReasoning `json:"reasoning,omitempty"`
	Stream             bool             `json:"stream"`
	Store              bool             `json:"store"`
	PromptCacheKey     string           `json:"prompt_cache_key,omitempty"`
	PreviousResponseID string           `json:"previous_response_id,omitempty"`
	User               string           `json:"user,omitempty"`
}

type ReqInput struct {
	Type      string      `json:"type"`                // "message" / "function_call" / "function_call_output"
	Role      string      `json:"role,omitempty"`      // "user" / "assistant"
	Content   interface{} `json:"content,omitempty"`   // string 或 []ContentPart
	CallID    string      `json:"call_id,omitempty"`   // function_call / function_call_output 用
	Name      string      `json:"name,omitempty"`      // function_call 用
	Arguments string      `json:"arguments,omitempty"` // function_call 用
	Output    *string     `json:"output,omitempty"`    // function_call_output 用；空字符串也必须显式发送
}

func buildResponsesToolOutput(callID, output string) ReqInput {
	return ReqInput{
		Type:   "function_call_output",
		CallID: callID,
		Output: &output,
	}
}

type ReqTool struct {
	Type        string      `json:"type"` // "function"
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// KiroToResponses 将解析好的会话转换为 OpenAI Responses API 请求体
//
// 缓存优化策略（针对 OpenAI Prompt Caching）：
//   - 不使用 instructions 字段（社区反馈 instructions 会破坏前缀缓存）
//   - 将 system prompt 作为 input 数组的第一条 developer 消息
//   - 分离 system prompt 中的动态尾部（"Here is the last user message"），移到末尾
//   - 保持前缀稳定：[developer(静态system)] + [历史对话] + [动态内容(末尾)]
//   - 工具定义放在 tools 字段（OpenAI 自动缓存 tools schema）
func KiroToResponses(conv *ParsedConversation, targetModel string, reasoningEffort string) ([]byte, error) {
	model := targetModel
	if model == "" {
		model = conv.ModelID
	}

	req := ResponsesRequest{
		Model:  model,
		Stream: true,
		Store:  true, // 必须 true，否则中转站无法缓存 prompt（previous_response_id 续传依赖此字段）
	}
	if reasoningEffort = strings.TrimSpace(reasoningEffort); reasoningEffort != "" {
		req.Reasoning = &OpenAIReasoning{Effort: reasoningEffort}
	}

	// prompt_cache_key 帮助中转站关联同一会话的请求，用于缓存调度
	if conv.ConversationID != "" {
		req.PromptCacheKey = conv.ConversationID
		// user 参数影响 OpenAI 缓存路由：相同 user 值的请求路由到同一机器，
		// 提高该机器上 KV cache 的命中率，避免 5 分钟超时丢缓存。
		req.User = conv.ConversationID
	}

	// 构建 input 数组（带缓存优化）
	input := buildResponsesInputCacheOptimized(conv)
	req.Input = input

	// 工具定义
	if len(conv.RawTools) > 0 {
		openaiTools, _ := globalToolCache.GetOrConvert(conv.ToolsHash, conv.RawTools)
		tools := make([]ReqTool, 0, len(openaiTools))
		for _, t := range openaiTools {
			tools = append(tools, ReqTool{
				Type:        "function",
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			})
		}
		req.Tools = tools
	}

	return json.Marshal(req)
}

// buildResponsesInput 构建基础 input（不含缓存优化，用于测试对比）
func buildResponsesInput(conv *ParsedConversation) []ReqInput {
	input := make([]ReqInput, 0, len(conv.History)*2+4)

	// system → developer
	if conv.SystemText != "" {
		input = append(input, ReqInput{
			Type:    "message",
			Role:    "developer",
			Content: conv.SystemText,
		})
	}

	for _, entry := range conv.History {
		if entry.IsSystem {
			continue
		}

		for _, tr := range entry.ToolResults {
			input = append(input, buildResponsesToolOutput(tr.ToolUseID, tr.Content))
		}

		if entry.UserContent != "" || len(entry.Images) > 0 {
			input = append(input, buildResponsesUserMsg(entry.UserContent, entry.Images))
		}

		if entry.HasAssistant {
			if entry.AssistantContent != "" {
				input = append(input, ReqInput{
					Type:    "message",
					Role:    "assistant",
					Content: entry.AssistantContent,
				})
			}
			for _, tu := range entry.ToolUses {
				input = append(input, ReqInput{
					Type:      "function_call",
					CallID:    tu.ToolUseID,
					Name:      tu.Name,
					Arguments: string(tu.Input),
				})
			}
		}
	}

	for _, tr := range conv.CurrentToolResults {
		input = append(input, buildResponsesToolOutput(tr.ToolUseID, tr.Content))
	}

	if conv.CurrentContent != "" || len(conv.CurrentImages) > 0 {
		input = append(input, buildResponsesUserMsg(conv.CurrentContent, conv.CurrentImages))
	}

	return input
}

func buildResponsesUserMsg(content string, images []KiroImage) ReqInput {
	if len(images) == 0 {
		return ReqInput{Type: "message", Role: "user", Content: content}
	}

	parts := make([]map[string]string, 0, len(images)+1)
	for _, img := range images {
		parts = append(parts, map[string]string{
			"type":      "input_image",
			"image_url": ImageDataURI(img),
		})
	}
	if content != "" {
		parts = append(parts, map[string]string{
			"type": "input_text",
			"text": content,
		})
	}

	return ReqInput{Type: "message", Role: "user", Content: parts}
}

// buildResponsesInputCacheOptimized 构建带缓存优化的 input 数组
//
// OpenAI Prompt Caching 核心规则：
//  1. 自动匹配请求的最长前缀（从第一个 token 开始）
//  2. 前缀必须 >= 1024 tokens 才能触发缓存
//  3. 以 128 token 为增量递增缓存范围
//  4. 前缀中任何一个 byte 变了，后面的全部无法命中缓存
//
// 优化策略：
//   - system prompt 中的静态部分作为 developer 消息放在最前面（稳定前缀）
//   - system prompt 中的动态尾部（"Here is the last user message:\n..."）分离到末尾
//   - EnvironmentContext（每轮可能变化）从用户消息中分离到末尾
//   - 历史对话保持原序（老消息不变，天然可缓存）
//   - 当前用户消息（每次必变）放在最后
func buildResponsesInputCacheOptimized(conv *ParsedConversation) []ReqInput {
	input := make([]ReqInput, 0, len(conv.History)*2+6)

	// ── 第一部分：静态 system prompt（developer 消息） ──
	var dynamicSystemTail string
	if conv.SystemText != "" {
		staticSystem, dynamicTail := optimizeSystemForCache(conv.SystemText)
		dynamicSystemTail = dynamicTail

		if staticSystem != "" {
			input = append(input, ReqInput{
				Type:    "message",
				Role:    "developer",
				Content: staticSystem,
			})
		}
	}

	// ── 第二部分：历史对话（稳定，可缓存） ──
	for _, entry := range conv.History {
		if entry.IsSystem {
			continue
		}

		// 工具结果 → function_call_output
		for _, tr := range entry.ToolResults {
			input = append(input, buildResponsesToolOutput(tr.ToolUseID, tr.Content))
		}

		// 用户消息
		if entry.UserContent != "" || len(entry.Images) > 0 {
			input = append(input, buildResponsesUserMsg(entry.UserContent, entry.Images))
		}

		// 助手消息
		if entry.HasAssistant {
			if entry.AssistantContent != "" {
				input = append(input, ReqInput{
					Type:    "message",
					Role:    "assistant",
					Content: entry.AssistantContent,
				})
			}
			// 工具调用 → function_call
			for _, tu := range entry.ToolUses {
				input = append(input, ReqInput{
					Type:      "function_call",
					CallID:    tu.ToolUseID,
					Name:      tu.Name,
					Arguments: string(tu.Input),
				})
			}
		}
	}

	// ── 第三部分：动态内容（每次都变，放在最后，不影响前缀缓存） ──

	// 当前工具结果
	for _, tr := range conv.CurrentToolResults {
		input = append(input, buildResponsesToolOutput(tr.ToolUseID, tr.Content))
	}

	// system prompt 的动态尾部
	if dynamicSystemTail != "" {
		input = append(input, ReqInput{
			Type:    "message",
			Role:    "developer",
			Content: dynamicSystemTail,
		})
	}

	// 当前用户消息（分离 EnvironmentContext）
	if conv.CurrentContent != "" || len(conv.CurrentImages) > 0 {
		userQuestion, envContext := optimizeCurrentContentForCache(conv.CurrentContent)

		if envContext != "" {
			input = append(input, ReqInput{
				Type:    "message",
				Role:    "user",
				Content: envContext,
			})
		}

		if userQuestion != "" || len(conv.CurrentImages) > 0 {
			input = append(input, buildResponsesUserMsg(userQuestion, conv.CurrentImages))
		}
	}

	return input
}
