package converter

import (
	json "github.com/goccy/go-json"
)

// KiroToAnthropic 将解析好的会话转换为 Anthropic Messages 请求体
func KiroToAnthropic(conv *ParsedConversation, targetModel string, reasoningEffort string, opts AnthropicRequestOptions) ([]byte, error) {
	model := targetModel
	if model == "" {
		model = conv.ModelID
	}

	staticSystem, dynamicSystemTail := optimizeSystemForCache(conv.SystemText)
	replayRequired := conv.ReasoningReplayRequired || UpstreamRequiresReasoningReplay(opts.UpstreamHost, model)
	messages := buildAnthropicMessagesCacheOptimized(conv, dynamicSystemTail, replayRequired)

	req := AnthropicRequest{
		Model:     model,
		Messages:  messages,
		MaxTokens: 16384,
		Stream:    true,
		// 顶层 cache_control：Anthropic 自动将 breakpoint 滚动到最后一个可缓存 block。
		// 多轮对话时每次只有最后一轮是新增的，之前所有历史自动从缓存读取。
		// 与 system/tools 上的显式 breakpoint 兼容（占用 4 个 slot 中的 1 个）。
		CacheControl: &CacheControl{Type: "ephemeral"},
	}
	if conv.MaxTokens > 0 {
		req.MaxTokens = conv.MaxTokens
	}
	thinking, err := BuildAnthropicThinking(AnthropicThinkingInput{
		ModelID:       model,
		UpstreamHost:  opts.UpstreamHost,
		ThinkingStyle: opts.ThinkingStyle,
		Effort:        reasoningEffort,
		MaxTokens:     req.MaxTokens,
	})
	if err != nil {
		return nil, err
	}
	req.Thinking = thinking.Thinking
	req.OutputConfig = thinking.OutputConfig

	// 系统提示只放静态部分，并添加 cache_control。
	// Kiro 的 system prompt 可能包含 "Here is the last user message:" 动态尾部；
	// 若整块放入 system，会导致 Anthropic system/tools 缓存前缀每轮变化。
	if staticSystem != "" {
		req.System = []AnthropicBlock{
			{
				Type:         "text",
				Text:         staticSystem,
				CacheControl: &CacheControl{Type: "ephemeral"},
			},
		}
	}

	// 工具定义（走本地转换缓存）+ 最后一个工具添加 cache_control。
	// breakpoint 在最后一个工具上，可缓存稳定的 system + tools 前缀。
	if len(conv.RawTools) > 0 {
		anthropicTools := globalToolCache.GetAnthropic(conv.ToolsHash, conv.RawTools)
		if len(anthropicTools) > 0 {
			tools := make([]AnthropicTool, len(anthropicTools))
			copy(tools, anthropicTools)
			tools[len(tools)-1].CacheControl = &CacheControl{Type: "ephemeral"}
			req.Tools = tools
		} else {
			req.Tools = anthropicTools
		}
	}

	return json.Marshal(req)
}

func buildAnthropicMessages(conv *ParsedConversation) []AnthropicMessage {
	return buildAnthropicMessagesCacheOptimized(conv, "", false)
}

// buildAnthropicMessagesCacheOptimized 构造 Anthropic messages。
// dynamicSystemTail 是从 system prompt 中拆出的动态尾部，应放到最后一轮用户消息中，
// 避免污染 Anthropic 可缓存的 system/tools 前缀。
// replayRequired 时，工具调用轮与工具结果之后的回复缺 thinking 会补占位（见 anthropicReasoningBlocks）。
func buildAnthropicMessagesCacheOptimized(conv *ParsedConversation, dynamicSystemTail string, replayRequired bool) []AnthropicMessage {
	msgs := make([]AnthropicMessage, 0, len(conv.History)*2+2)
	afterToolResults := false

	for _, entry := range conv.History {
		if entry.IsSystem {
			continue // 系统提示已放到 system 字段
		}
		afterToolResults = followsToolResults(afterToolResults, entry)

		// 用户消息（含工具结果）
		if m := buildAnthropicUserMsg(entry.UserContent, entry.ToolResults, entry.Images); m != nil {
			msgs = append(msgs, *m)
		}

		// 助手消息
		if entry.HasAssistant {
			reasoning := anthropicReasoningBlocks(entry.Reasoning, replayRequired, len(entry.ToolUses) > 0 || afterToolResults)
			if m := buildAnthropicAssistantMsg(entry.AssistantContent, entry.ToolUses, reasoning); m != nil {
				msgs = append(msgs, *m)
			}
		}
	}

	// 当前消息：把 system 动态尾部留在最后，
	// 不进入 system/tools cache breakpoint。
	if m := buildAnthropicCurrentMsgOptimized(conv, dynamicSystemTail); m != nil {
		msgs = append(msgs, *m)
	}
	markAnthropicRollingCacheBreakpoint(msgs)

	return msgs
}

// markAnthropicRollingCacheBreakpoint caches the growing conversation prefix.
// Some Anthropic-compatible relays ignore top-level automatic cache_control,
// so keep an explicit breakpoint on the last user content block as well.
func markAnthropicRollingCacheBreakpoint(msgs []AnthropicMessage) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}

		switch content := msgs[i].Content.(type) {
		case string:
			if content == "" {
				continue
			}
			msgs[i].Content = []AnthropicBlock{{
				Type:         "text",
				Text:         content,
				CacheControl: &CacheControl{Type: "ephemeral"},
			}}
			return
		case []AnthropicBlock:
			if len(content) == 0 {
				continue
			}
			blocks := make([]AnthropicBlock, len(content))
			copy(blocks, content)
			blocks[len(blocks)-1].CacheControl = &CacheControl{Type: "ephemeral"}
			msgs[i].Content = blocks
			return
		}
	}
}

func buildAnthropicUserMsg(content string, trs []ParsedToolResult, images []KiroImage) *AnthropicMessage {
	return buildAnthropicUserMsgWithExtraTexts(content, trs, images, nil)
}

func buildAnthropicUserMsgWithExtraTexts(content string, trs []ParsedToolResult, images []KiroImage, extraTexts []string) *AnthropicMessage {
	if len(trs) == 0 && len(images) == 0 && len(extraTexts) == 0 {
		if content == "" {
			return nil
		}
		return &AnthropicMessage{
			Role: "user",
			Content: []AnthropicBlock{{
				Type: "text",
				Text: content,
			}},
		}
	}

	blocks := make([]AnthropicBlock, 0, len(trs)+len(images)+len(extraTexts)+1)

	// tool_result 必须在其他 block 之前
	for _, tr := range trs {
		b := AnthropicBlock{
			Type:      "tool_result",
			ToolUseID: tr.ToolUseID,
			Content:   serializeToolResultForProvider(tr),
		}
		if tr.IsError {
			b.IsError = true
		}
		blocks = append(blocks, b)
	}

	for _, img := range images {
		blocks = append(blocks, AnthropicBlock{
			Type: "image",
			Source: &AnthropicImageSource{
				Type:      "base64",
				MediaType: ImageMediaType(img.Format),
				Data:      img.Source.Bytes,
			},
		})
	}

	for _, text := range extraTexts {
		if text != "" {
			blocks = append(blocks, AnthropicBlock{Type: "text", Text: text})
		}
	}

	if content != "" {
		blocks = append(blocks, AnthropicBlock{Type: "text", Text: content})
	}

	if len(blocks) == 0 {
		return nil
	}
	return &AnthropicMessage{Role: "user", Content: blocks}
}

func buildAnthropicCurrentMsg(conv *ParsedConversation) *AnthropicMessage {
	return buildAnthropicCurrentMsgOptimized(conv, "")
}

func buildAnthropicCurrentMsgOptimized(conv *ParsedConversation, dynamicSystemTail string) *AnthropicMessage {
	if dynamicSystemTail == "" {
		return buildAnthropicUserMsg(conv.CurrentContent, conv.CurrentToolResults, conv.CurrentImages)
	}

	return buildAnthropicUserMsgWithExtraTexts(
		conv.CurrentContent,
		conv.CurrentToolResults,
		conv.CurrentImages,
		[]string{dynamicSystemTail},
	)
}

// buildAnthropicAssistantMsg 组装 assistant 消息；reasoning 是 anthropicReasoningBlocks 的结果，
// 必须放在 text/tool_use 之前。
func buildAnthropicAssistantMsg(content string, toolUses []ParsedToolUse, reasoning []AnthropicBlock) *AnthropicMessage {
	if len(toolUses) == 0 && len(reasoning) == 0 {
		if content == "" {
			return &AnthropicMessage{Role: "assistant", Content: " "}
		}
		return &AnthropicMessage{Role: "assistant", Content: content}
	}

	blocks := make([]AnthropicBlock, 0, len(reasoning)+len(toolUses)+1)
	blocks = append(blocks, reasoning...)
	if content == "" && len(toolUses) == 0 {
		// 只有补齐的 thinking 时保留与纯文本回复一致的空格正文，避免 assistant 只剩推理块
		content = " "
	}
	if content != "" {
		blocks = append(blocks, AnthropicBlock{Type: "text", Text: content})
	}
	for _, tu := range toolUses {
		blocks = append(blocks, AnthropicBlock{
			Type:  "tool_use",
			ID:    tu.ToolUseID,
			Name:  tu.Name,
			Input: tu.Input,
		})
	}
	return &AnthropicMessage{Role: "assistant", Content: blocks}
}
