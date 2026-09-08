package converter

import (
	json "github.com/goccy/go-json"
	"strings"
)

// KiroToOpenAI 将解析好的会话转换为 OpenAI Chat Completions 请求体
func KiroToOpenAI(conv *ParsedConversation, targetModel string, reasoningEffort string) ([]byte, error) {
	model := targetModel
	if model == "" {
		model = conv.ModelID
	}

	messages := buildOpenAIMessages(conv)

	req := OpenAIRequest{
		Model:    model,
		Messages: messages,
		Stream:   true,
		StreamOptions: &OpenAIStreamOpts{
			IncludeUsage: true,
		},
	}

	if conv.MaxTokens > 0 {
		req.MaxTokens = conv.MaxTokens
	}
	if reasoningEffort = strings.TrimSpace(reasoningEffort); reasoningEffort != "" {
		req.ReasoningEffort = reasoningEffort
	}
	req.Temperature = conv.Temperature
	req.TopP = conv.TopP
	if supportsPromptCacheBreakpoints(model) {
		req.PromptCacheOpts = openAIPromptCacheOptions()
		markOpenAIStablePrefixBreakpoint(req.Messages)
	}

	// 相同模型、静态 system 和 tools 复用同一个 key，提高跨会话的缓存路由命中率。
	// 工具定义（走缓存）
	var openaiTools []OpenAITool
	if len(conv.RawTools) > 0 {
		openaiTools, _ = globalToolCache.GetOrConvert(conv.ToolsHash, conv.RawTools)
		req.Tools = openaiTools
	}
	if !conv.IsClassifier {
		if cacheKey := promptCacheKeyWithProviderTools(conv, model, "chat", openaiTools); cacheKey != "" {
			req.PromptCacheKey = cacheKey
		}
	}

	return json.Marshal(req)
}

// buildOpenAIMessages 将会话历史转换为 OpenAI messages 数组
// 自动应用 prompt cache 优化：静态前缀保持不变以最大化缓存命中
func buildOpenAIMessages(conv *ParsedConversation) []OpenAIMessage {
	msgs := make([]OpenAIMessage, 0, len(conv.History)*2+4)

	for _, entry := range conv.History {
		// 系统提示 → system role
		if entry.IsSystem {
			msgs = append(msgs, OpenAIMessage{
				Role:    "system",
				Content: entry.UserContent,
			})
			continue
		}

		// 工具结果 → tool role（必须在 user 消息之前）
		for _, tr := range entry.ToolResults {
			msgs = append(msgs, OpenAIMessage{
				Role:       "tool",
				ToolCallID: tr.ToolUseID,
				Content:    serializeToolResultForProvider(tr),
			})
		}

		// 用户消息
		if entry.UserContent != "" || len(entry.Images) > 0 {
			msgs = append(msgs, buildOpenAIUserMsg(entry.UserContent, entry.Images))
		}

		// 助手消息
		if entry.HasAssistant {
			if m := buildOpenAIAssistantMsg(entry.AssistantContent, entry.ToolUses, entry.Reasoning); m != nil {
				msgs = append(msgs, *m)
			}
		}
	}

	// 当前消息的工具结果
	for _, tr := range conv.CurrentToolResults {
		msgs = append(msgs, OpenAIMessage{
			Role:       "tool",
			ToolCallID: tr.ToolUseID,
			Content:    serializeToolResultForProvider(tr),
		})
	}

	// 当前用户消息
	if conv.CurrentContent != "" || len(conv.CurrentImages) > 0 {
		msgs = append(msgs, buildOpenAIUserMsg(conv.CurrentContent, conv.CurrentImages))
	}

	// 应用 OpenAI prompt cache 优化：拆分 system 里的动态尾部
	msgs = applyOpenAICacheOptimization(msgs, conv.CurrentContent != "" || len(conv.CurrentImages) > 0)

	return msgs
}

func buildOpenAIUserMsg(content string, images []KiroImage) OpenAIMessage {
	if len(images) == 0 {
		return OpenAIMessage{Role: "user", Content: content}
	}
	parts := make([]OpenAIContentPart, 0, len(images)+1)
	for _, img := range images {
		parts = append(parts, OpenAIContentPart{
			Type:     "image_url",
			ImageURL: &OpenAIImageURL{URL: ImageDataURI(img)},
		})
	}
	if content != "" {
		parts = append(parts, OpenAIContentPart{Type: "text", Text: content})
	}
	return OpenAIMessage{Role: "user", Content: parts}
}

func buildOpenAIAssistantMsg(content string, toolUses []ParsedToolUse, reasoning *ReasoningTrace) *OpenAIMessage {
	msg := &OpenAIMessage{Role: "assistant"}
	if content != "" {
		msg.Content = content
	}
	if len(toolUses) > 0 {
		tcs := make([]OpenAIToolCall, 0, len(toolUses))
		for _, tu := range toolUses {
			tcs = append(tcs, OpenAIToolCall{
				ID:   tu.ToolUseID,
				Type: "function",
				Function: OpenAIFuncCall{
					Name:      tu.Name,
					Arguments: string(tu.Input),
				},
			})
		}
		msg.ToolCalls = tcs
	}
	if reasoning != nil && (reasoning.Present || strings.TrimSpace(reasoning.Text) != "") && len(msg.ToolCalls) > 0 {
		text := reasoning.Text
		switch reasoning.WireField {
		case "reasoning_text":
			msg.ReasoningText = &text
		case "reasoning":
			msg.Reasoning = &text
		default:
			msg.ReasoningContent = &text
		}
	}
	if msg.Content == nil && len(msg.ToolCalls) == 0 {
		return nil
	}
	return msg
}

// ── OpenAI → Kiro 响应转换（非流式，用于测试） ────────────────────

// OpenAIResponseToKiroText 从 OpenAI 非流式响应中提取文本内容
func OpenAIResponseToKiroText(body []byte) (string, error) {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if len(resp.Choices) > 0 {
		return resp.Choices[0].Message.Content, nil
	}
	return "", nil
}

// ── 模型名映射 ────────────────────────────────────────────────────

// MapModelID 将 Kiro modelId 映射到目标模型名
// 如果 targetModel 非空则直接使用，否则做内置映射
func MapModelID(kiroModel, targetModel string) string {
	if strings.TrimSpace(targetModel) != "" {
		return strings.TrimSpace(targetModel)
	}
	// 内置映射：GPT 兼容名 → Claude
	gptMap := map[string]string{
		"gpt-4":         "claude-sonnet-4-5",
		"gpt-4o":        "claude-sonnet-4-5",
		"gpt-4-turbo":   "claude-sonnet-4-5",
		"gpt-3.5-turbo": "claude-sonnet-4-5",
		"gpt-4o-mini":   "claude-haiku-4-5",
	}
	lower := strings.ToLower(strings.TrimSpace(kiroModel))
	if mapped, ok := gptMap[lower]; ok {
		return mapped
	}
	return kiroModel
}
