package converter

import (
	"strings"

	json "github.com/goccy/go-json"
)

// KiroToGemini 将解析好的会话转换为 Gemini generateContent 请求体
func KiroToGemini(conv *ParsedConversation, targetModel string, reasoningEffort string) ([]byte, error) {
	req := GeminiRequest{}

	// 系统提示 → systemInstruction（静态部分，利于隐式缓存）
	staticSystem, dynamicSystemTail := optimizeSystemForCache(conv.SystemText)
	if staticSystem != "" {
		req.SystemInstruction = &GeminiContent{
			Parts: []GeminiPart{{Text: staticSystem}},
		}
	}

	// 构建 contents（带角色交替合并 + 正确的 functionResponse name）
	req.Contents = buildGeminiContents(conv, dynamicSystemTail)

	// 工具定义
	if len(conv.RawTools) > 0 {
		decls := toGeminiTools(conv.RawTools)
		if len(decls) > 0 {
			req.Tools = []GeminiTool{{FunctionDeclarations: decls}}
		}
	}

	// 生成配置
	genConfig := &GeminiGenConfig{}
	hasConfig := false
	if conv.MaxTokens > 0 {
		genConfig.MaxOutputTokens = conv.MaxTokens
		hasConfig = true
	}
	if conv.Temperature != nil {
		genConfig.Temperature = conv.Temperature
		hasConfig = true
	}
	if conv.TopP != nil {
		genConfig.TopP = conv.TopP
		hasConfig = true
	}
	if thinking := geminiThinkingFromEffort(reasoningEffort); thinking != nil {
		genConfig.ThinkingConfig = thinking
		hasConfig = true
	}
	if hasConfig {
		req.GenerationConfig = genConfig
	}

	return json.Marshal(req)
}

// buildGeminiContents 构建 Gemini contents 数组，确保角色严格交替
func buildGeminiContents(conv *ParsedConversation, dynamicSystemTail string) []GeminiContent {
	// 建立 toolUseID → toolName 映射（Gemini functionResponse 需要 name 字段）
	toolNameMap := make(map[string]string)
	for _, entry := range conv.History {
		for _, tu := range entry.ToolUses {
			toolNameMap[tu.ToolUseID] = tu.Name
		}
	}

	raw := make([]GeminiContent, 0, len(conv.History)*2+4)

	for _, entry := range conv.History {
		if entry.IsSystem {
			continue
		}

		// 工具结果 → user role（functionResponse）
		if len(entry.ToolResults) > 0 {
			parts := make([]GeminiPart, 0, len(entry.ToolResults))
			for _, tr := range entry.ToolResults {
				name := toolNameMap[tr.ToolUseID]
				if name == "" {
					name = tr.ToolUseID
				}
				parts = append(parts, GeminiPart{
					FunctionResponse: &GeminiFunctionResp{
						Name:     name,
						Response: map[string]string{"result": tr.Content},
					},
				})
			}
			raw = append(raw, GeminiContent{Role: "user", Parts: parts})
		}

		// 用户消息
		if entry.UserContent != "" || len(entry.Images) > 0 {
			raw = append(raw, buildGeminiUserContent(entry.UserContent, entry.Images))
		}

		// 助手消息
		if entry.HasAssistant {
			if m := buildGeminiModelContent(entry.AssistantContent, entry.ToolUses); m != nil {
				raw = append(raw, *m)
			}
		}
	}

	// 当前消息的工具结果
	if len(conv.CurrentToolResults) > 0 {
		parts := make([]GeminiPart, 0, len(conv.CurrentToolResults))
		for _, tr := range conv.CurrentToolResults {
			name := toolNameMap[tr.ToolUseID]
			if name == "" {
				name = tr.ToolUseID
			}
			parts = append(parts, GeminiPart{
				FunctionResponse: &GeminiFunctionResp{
					Name:     name,
					Response: map[string]string{"result": tr.Content},
				},
			})
		}
		raw = append(raw, GeminiContent{Role: "user", Parts: parts})
	}

	// 当前用户消息（含动态系统尾部和 EnvironmentContext）
	userQuestion, envContext := optimizeCurrentContentForCache(conv.CurrentContent)
	var currentParts []GeminiPart
	if dynamicSystemTail != "" {
		currentParts = append(currentParts, GeminiPart{Text: dynamicSystemTail})
	}
	if envContext != "" {
		currentParts = append(currentParts, GeminiPart{Text: envContext})
	}
	if userQuestion != "" {
		currentParts = append(currentParts, GeminiPart{Text: userQuestion})
	}
	for _, img := range conv.CurrentImages {
		currentParts = append(currentParts, GeminiPart{
			InlineData: &GeminiInlineData{
				MimeType: ImageMediaType(img.Format),
				Data:     img.Source.Bytes,
			},
		})
	}
	if len(currentParts) > 0 {
		raw = append(raw, GeminiContent{Role: "user", Parts: currentParts})
	}

	// 合并相邻同角色消息（Gemini 要求严格交替）
	return mergeAdjacentRoles(raw)
}

// mergeAdjacentRoles 合并相邻同角色的 GeminiContent
func mergeAdjacentRoles(contents []GeminiContent) []GeminiContent {
	if len(contents) == 0 {
		return contents
	}
	merged := make([]GeminiContent, 0, len(contents))
	merged = append(merged, contents[0])
	for i := 1; i < len(contents); i++ {
		last := &merged[len(merged)-1]
		if last.Role == contents[i].Role {
			last.Parts = append(last.Parts, contents[i].Parts...)
		} else {
			merged = append(merged, contents[i])
		}
	}
	return merged
}

func buildGeminiUserContent(content string, images []KiroImage) GeminiContent {
	parts := make([]GeminiPart, 0, 1+len(images))
	for _, img := range images {
		parts = append(parts, GeminiPart{
			InlineData: &GeminiInlineData{
				MimeType: ImageMediaType(img.Format),
				Data:     img.Source.Bytes,
			},
		})
	}
	if content != "" {
		parts = append(parts, GeminiPart{Text: content})
	}
	return GeminiContent{Role: "user", Parts: parts}
}

func buildGeminiModelContent(content string, toolUses []ParsedToolUse) *GeminiContent {
	parts := make([]GeminiPart, 0, 1+len(toolUses))
	if content != "" {
		parts = append(parts, GeminiPart{Text: content})
	}
	for _, tu := range toolUses {
		var args interface{}
		if len(tu.Input) > 0 {
			_ = json.Unmarshal(tu.Input, &args)
		}
		parts = append(parts, GeminiPart{
			FunctionCall: &GeminiFunctionCall{
				Name: tu.Name,
				Args: args,
			},
		})
	}
	if len(parts) == 0 {
		return nil
	}
	return &GeminiContent{Role: "model", Parts: parts}
}

// toGeminiTools 将 Kiro 原始工具定义转换为 Gemini functionDeclarations
func toGeminiTools(rawTools []json.RawMessage) []GeminiFuncDecl {
	specs := sortedToolSpecs(rawTools, false)
	result := make([]GeminiFuncDecl, 0, len(specs))
	for _, spec := range specs {
		var params interface{}
		if len(spec.schema) > 0 {
			_ = json.Unmarshal(spec.schema, &params)
		}
		result = append(result, GeminiFuncDecl{
			Name:        spec.name,
			Description: truncateDesc(spec.description),
			Parameters:  params,
		})
	}
	return result
}

func geminiThinkingFromEffort(effort string) *GeminiThinkingConfig {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low":
		return &GeminiThinkingConfig{ThinkingBudget: 1024}
	case "medium":
		return &GeminiThinkingConfig{ThinkingBudget: 4096}
	case "high":
		return &GeminiThinkingConfig{ThinkingBudget: 8192}
	default:
		return nil
	}
}
