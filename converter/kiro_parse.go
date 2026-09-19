package converter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	json "github.com/goccy/go-json"
)

// ParseKiroRequest 将原始请求体一次性解析为结构化会话。
// 后续所有转换（→OpenAI / →Anthropic）都基于返回的 ParsedConversation，
// 不再重复 json.Unmarshal。
func ParseKiroRequest(body []byte) (*ParsedConversation, error) {
	var req KiroRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}

	cs := req.ConversationState
	cur := cs.CurrentMessage.UserInputMessage

	conv := &ParsedConversation{
		ModelID:              cur.ModelID,
		ConversationID:       cs.ConversationID,
		ProfileArn:           strings.TrimSpace(req.ProfileArn),
		ChatTriggerType:      strings.TrimSpace(cs.ChatTriggerType),
		AgentTaskType:        strings.TrimSpace(cs.AgentTaskType),
		AgentMode:            strings.TrimSpace(req.AgentMode),
		AgentContinuationID:  strings.TrimSpace(cs.AgentContinuationID),
		Origin:               strings.TrimSpace(cur.Origin),
		PromptAffinityKey:    cs.ConversationID,
		ContinuationStateKey: buildContinuationStateKey(cs.ConversationID, req.AgentMode, cs.AgentContinuationID),
		IsClassifier:         looksLikeClassifierPrompt(cur.Content),
	}
	if conv.ModelID == "" {
		conv.ModelID = "claude-sonnet-4-5"
	}

	// 推理配置
	if req.InferenceConfig != nil {
		conv.MaxTokens = req.InferenceConfig.MaxTokens
		conv.Temperature = req.InferenceConfig.Temperature
		conv.TopP = req.InferenceConfig.TopP
	}
	if fields := req.AdditionalModelRequestFields; fields != nil {
		effortValue := fields.ReasoningEffort
		if fields.Reasoning != nil && strings.TrimSpace(fields.Reasoning.Effort) != "" {
			effortValue = fields.Reasoning.Effort
		}
		effort, err := NormalizeReasoningEffort(effortValue)
		if err != nil {
			return nil, err
		}
		conv.ReasoningEffort = effort
	}

	// 当前消息
	conv.CurrentContent = cur.Content
	conv.CurrentImages = cur.Images
	if cur.UserInputMessageContext != nil {
		conv.CurrentToolResults = parseToolResults(cur.UserInputMessageContext.ToolResults)
		conv.RawTools = cur.UserInputMessageContext.Tools
		conv.ToolsHash = hashTools(cur.UserInputMessageContext.Tools)
	}

	// 历史消息
	for i, h := range cs.History {
		entry := ParsedHistoryEntry{}

		if h.UserInputMessage != nil {
			entry.UserContent = h.UserInputMessage.Content
			entry.Images = h.UserInputMessage.Images
			if h.UserInputMessage.UserInputMessageContext != nil {
				entry.ToolResults = parseToolResults(h.UserInputMessage.UserInputMessageContext.ToolResults)
			}

			// 第一条历史消息：检测是否是系统提示
			if i == 0 && looksLikeSystemPrompt(entry.UserContent) && !keepBootstrapAsUser(conv.AgentMode) {
				entry.IsSystem = true
				conv.SystemText = entry.UserContent
			}
		}

		if h.AssistantResponseMsg != nil {
			entry.HasAssistant = true
			entry.AssistantContent = h.AssistantResponseMsg.Content
			for _, tu := range h.AssistantResponseMsg.ToolUses {
				inputRaw, _ := json.Marshal(tu.Input)
				entry.ToolUses = append(entry.ToolUses, ParsedToolUse{
					ToolUseID: tu.ToolUseID,
					Name:      tu.Name,
					Input:     json.RawMessage(inputRaw),
				})
			}
		}

		conv.History = append(conv.History, entry)
	}

	return conv, nil
}

// NormalizeReasoningEffort validates the per-request reasoning override.
func NormalizeReasoningEffort(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}

	switch value {
	case "low", "medium", "high", "xhigh":
		return value, nil
	default:
		return "", fmt.Errorf("unsupported reasoning effort %q", value)
	}
}

func keepBootstrapAsUser(agentMode string) bool {
	mode := strings.ToLower(strings.TrimSpace(agentMode))
	return mode != "" && mode != "vibe"
}

func buildContinuationStateKey(conversationID, agentMode, continuationID string) string {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return ""
	}

	mode := strings.ToLower(strings.TrimSpace(agentMode))
	if mode == "" || mode == "vibe" {
		return conversationID
	}

	continuationID = strings.TrimSpace(continuationID)
	return "branch|" + lengthPrefixed(conversationID) + lengthPrefixed(mode) + lengthPrefixed(continuationID)
}

func lengthPrefixed(value string) string {
	return strconv.Itoa(len(value)) + ":" + value
}

// ── 辅助函数 ─────────────────────────────────────────────────────

func parseToolResults(trs []KiroToolResult) []ParsedToolResult {
	if len(trs) == 0 {
		return nil
	}
	result := make([]ParsedToolResult, 0, len(trs))
	for _, tr := range trs {
		text := ""
		blocks := make([]ParsedToolResultBlock, 0, len(tr.Content))
		for _, c := range tr.Content {
			text += c.Text
			block := parseToolResultBlock(c)
			blocks = append(blocks, block)
		}
		text = appendProcessOutputPlaceholderHint(text)
		result = append(result, ParsedToolResult{
			ToolUseID: tr.ToolUseID,
			Content:   text,
			Blocks:    blocks,
			IsError:   tr.Status == "error",
		})
	}
	return result
}

func parseToolResultBlock(content KiroToolContent) ParsedToolResultBlock {
	raw := bytes.TrimSpace(content.Raw)
	if len(raw) == 0 {
		raw, _ = json.Marshal(map[string]string{"text": content.Text})
	}

	kind := toolResultBlockKind(raw, content.Text)
	block := ParsedToolResultBlock{
		Kind: kind,
		Text: content.Text,
	}
	if kind != "text" {
		block.Raw = append(block.Raw[:0], raw...)
	}
	return block
}

func toolResultBlockKind(raw []byte, text string) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		if text != "" {
			return "text"
		}
		return "unknown"
	}

	if rawType, ok := fields["type"]; ok {
		var kind string
		if json.Unmarshal(rawType, &kind) == nil && strings.TrimSpace(kind) != "" {
			if kind == "text" {
				if isTextBlockFields(fields) {
					return "text"
				}
				return "unknown"
			}
			return kind
		}
	}
	if isTextBlockFields(fields) {
		return "text"
	}
	return "unknown"
}

func isTextBlockFields(fields map[string]json.RawMessage) bool {
	if _, ok := fields["text"]; !ok {
		return false
	}
	for key := range fields {
		if key != "text" && key != "type" {
			return false
		}
	}
	return true
}

func looksLikeSystemPrompt(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	markers := []string{
		"<identity>",
		"<key_kiro_features>",
		"You are Kiro",
		"You are an intent classifier",
		"IMPORTANT: Respond ONLY with a raw JSON object",
		// Sub-agent system prompts
		"# Context Gathering Agent",
		"# Efficient Context Gathering Agent",
		"<progress_reporting>",
		"<response_requirement>",
		"You are a context gathering specialist",
		"## Your Mission",
		"subagentResponse",
	}
	for _, m := range markers {
		if strings.Contains(trimmed, m) {
			return true
		}
	}

	// 通用启发式：如果第一条消息很长（>2000字符）且紧跟 assistant 回复 "I will follow these instructions"
	// 大概率是系统提示
	if len(trimmed) > 2000 {
		// 检测是否包含典型的 agent 指令结构
		agentPatterns := []string{
			"## Available Context",
			"## Tool Usage Guidelines",
			"## Critical Rules",
			"## Output Format",
			"## Step ",
			"You MUST use the",
			"You MUST call the",
		}
		matchCount := 0
		for _, p := range agentPatterns {
			if strings.Contains(trimmed, p) {
				matchCount++
			}
		}
		// 命中 2 个以上模式就认为是 system prompt
		if matchCount >= 2 {
			return true
		}
	}

	return false
}

func looksLikeClassifierPrompt(content string) bool {
	// classifier 请求有非常独特的特征：要求只返回 JSON {"chat":x,"do":x,"spec":x}
	// 必须同时满足多个条件才判定为 classifier，避免误判主请求
	required := []string{
		"intent classifier",
		"Respond ONLY with a raw JSON object",
	}
	for _, m := range required {
		if !strings.Contains(content, m) {
			return false
		}
	}
	return true
}

// hashTools 计算工具定义的内容哈希，用于缓存命中判断。
// Kiro/插件侧有时会改变 JSON 字段顺序或空白；直接 hash 原始 bytes 会造成
// 语义相同的工具集合反复 miss。本函数先做 canonical JSON，并按工具名排序，
// 让本地工具转换缓存和上游 tools 前缀尽量稳定。
func hashTools(tools []json.RawMessage) string {
	if len(tools) == 0 {
		return ""
	}

	canonical := make([][]byte, 0, len(tools))
	for _, t := range tools {
		b := canonicalToolJSON(t)
		if len(b) > 0 {
			canonical = append(canonical, b)
		}
	}
	if len(canonical) == 0 {
		return ""
	}

	sort.Slice(canonical, func(i, j int) bool {
		return bytes.Compare(canonical[i], canonical[j]) < 0
	})

	h := sha256.New()
	for _, b := range canonical {
		h.Write(b)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func canonicalToolJSON(raw json.RawMessage) []byte {
	// unmarshal → marshal 做 key 排序归一化，确保语义相同但字段顺序不同的 JSON 产生相同 hash
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return bytes.TrimSpace(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return bytes.TrimSpace(raw)
	}
	return b
}

// ImageMediaType 和 ImageDataURI 在 image.go 中定义

func appendProcessOutputPlaceholderHint(text string) string {
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "no background process found with id") {
		return text
	}

	var terminalID string
	if strings.Contains(lower, `"invalid"`) {
		terminalID = "invalid"
	} else if strings.Contains(lower, `"dummy"`) {
		terminalID = "dummy"
	} else {
		return text
	}

	if strings.Contains(lower, "do not call get_process_output with placeholder terminalid") {
		return text
	}

	return text + "\n\nHint: The terminalId \"" + terminalID + "\" looks like a placeholder, not a real background process id. Do not call get_process_output with placeholder terminalId values such as invalid or dummy. Use list_processes first, or use a terminalId returned by control_pwsh_process."
}
