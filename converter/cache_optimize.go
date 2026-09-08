// cache_optimize.go
// OpenAI Prompt Cache 优化：调整消息顺序，最大化缓存命中率
//
// OpenAI Prompt Caching 规则：
//   - 按消息顺序从头开始匹配前缀
//   - 稳定前缀有利于复用，实际命中还取决于上游模型、路由和缓存存活时间
//   - 一旦某条消息内容变了，后面的都无法缓存
//   - prompt_cache_key 与初始前缀 hash 共同影响缓存路由
//   - GPT-5.6 及后续模型的缓存最短生命周期当前为 30 分钟
//
// Kiro 的 system prompt 可能嵌入动态内容（如最后一条用户消息）。
// 动态尾部变化会缩短可复用前缀，但不代表此前的静态部分全部失效。
//
// 优化策略：
//   - 从 system prompt 里提取出动态尾部（"Here is the last user message:\n..."）
//   - 静态部分保留在 system 最前面（可缓存）
//   - 历史对话紧跟 system 之后（稳定，只会追加不会修改 → 可缓存）
//   - system 动态尾部移到历史之后
//   - prompt_cache_key 基于模型、静态 system 和 tools 生成，让共享前缀的请求复用路由
//   - 当前 user 保持 Kiro 原始内容，确保下一轮进入 history 后结构不变

package converter

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	json "github.com/goccy/go-json"
)

const lastUserMsgMarker = "Here is the last user message:\n"

func supportsPromptCacheBreakpoints(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
		model = model[slash+1:]
	}
	if !strings.HasPrefix(model, "gpt-") {
		return false
	}

	version := strings.TrimPrefix(model, "gpt-")
	major, rest, ok := parseLeadingVersionNumber(version)
	if !ok {
		return false
	}
	if major > 5 {
		return rest == "" || strings.HasPrefix(rest, ".") || strings.HasPrefix(rest, "-")
	}
	if major < 5 || !strings.HasPrefix(rest, ".") {
		return false
	}
	minor, suffix, ok := parseLeadingVersionNumber(rest[1:])
	return ok && minor >= 6 && (suffix == "" || strings.HasPrefix(suffix, "-"))
}

func parseLeadingVersionNumber(value string) (number int, rest string, ok bool) {
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, value, false
	}
	number, err := strconv.Atoi(value[:end])
	if err != nil {
		return 0, value, false
	}
	return number, value[end:], true
}

func openAIPromptCacheOptions() *PromptCacheOptions {
	return &PromptCacheOptions{Mode: "implicit", TTL: "30m"}
}

func promptCacheKey(conv *ParsedConversation, model, api string) string {
	if conv == nil {
		return ""
	}

	var providerTools []OpenAITool
	if isOpenAICacheAPI(api) && len(conv.RawTools) > 0 {
		providerTools = toOpenAITools(conv.RawTools)
	}
	return promptCacheKeyWithProviderTools(conv, model, api, providerTools)
}

// promptCacheKeyWithProviderTools builds the affinity key from the exact
// provider-visible tool list when one is available. Kiro's raw tool envelope
// contains fields that OpenAI never receives, so hashing the raw envelope can
// split an otherwise identical cache prefix.
func promptCacheKeyWithProviderTools(conv *ParsedConversation, model, api string, providerTools []OpenAITool) string {
	if conv == nil {
		return ""
	}
	toolsHash := conv.ToolsHash
	if isOpenAICacheAPI(api) && len(conv.RawTools) > 0 {
		if providerTools == nil {
			providerTools = toOpenAITools(conv.RawTools)
		}
		toolsHash = hashOpenAITools(providerTools)
	} else if toolsHash == "" && len(conv.RawTools) > 0 {
		toolsHash = hashTools(conv.RawTools)
	}
	return promptCacheKeyWithToolsHash(conv, model, api, toolsHash)
}

func promptCacheKeyWithToolsHash(conv *ParsedConversation, model, api, toolsHash string) string {
	if conv == nil {
		return ""
	}
	staticSystem, _ := optimizeSystemForCache(conv.SystemText)
	if staticSystem == "" && toolsHash == "" {
		if conv.PromptAffinityKey != "" {
			return conv.PromptAffinityKey
		}
		return conv.ConversationID
	}

	h := sha256.New()
	h.Write([]byte(strings.ToLower(strings.TrimSpace(api))))
	h.Write([]byte{0})
	h.Write([]byte(strings.ToLower(strings.TrimSpace(model))))
	h.Write([]byte{0})
	h.Write([]byte(staticSystem))
	h.Write([]byte{0})
	h.Write([]byte(toolsHash))
	return "kiro-" + strings.ToLower(strings.TrimSpace(api)) + "-" + hex.EncodeToString(h.Sum(nil)[:16])
}

func isOpenAICacheAPI(api string) bool {
	switch strings.ToLower(strings.TrimSpace(api)) {
	case "chat", "responses":
		return true
	default:
		return false
	}
}

// hashOpenAITools hashes the normalized tool list that is visible to OpenAI.
// The list is already sorted by toOpenAITools; preserving that order matters
// because tool order is part of the provider prompt prefix.
func hashOpenAITools(tools []OpenAITool) string {
	if len(tools) == 0 {
		return ""
	}

	h := sha256.New()
	for _, tool := range tools {
		encoded, err := json.Marshal(tool)
		if err != nil {
			return ""
		}
		canonical := canonicalToolJSON(encoded)
		if len(canonical) == 0 {
			return ""
		}
		h.Write(canonical)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// PromptCacheKeyForAudit exposes the same key calculation to the proxy log
// path without exposing the implementation details to other packages.
func PromptCacheKeyForAudit(conv *ParsedConversation, model, api string) string {
	return promptCacheKey(conv, model, api)
}

// PromptCacheKeyForAuditWithProviderToolsHash avoids re-converting the raw
// Kiro tools when the caller already has the normalized provider hash.
func PromptCacheKeyForAuditWithProviderToolsHash(conv *ParsedConversation, model, api, providerToolsHash string) string {
	return promptCacheKeyWithToolsHash(conv, model, api, providerToolsHash)
}

// ProviderToolsHashForAudit returns the hash of the normalized OpenAI tools
// list used for cache diagnostics.
func ProviderToolsHashForAudit(rawTools []json.RawMessage) string {
	return hashOpenAITools(toOpenAITools(rawTools))
}

func markOpenAIStablePrefixBreakpoint(messages []OpenAIMessage) {
	for i := range messages {
		if messages[i].Role != "system" {
			continue
		}
		content, ok := messages[i].Content.(string)
		if !ok || content == "" {
			return
		}
		messages[i].Content = []OpenAIContentPart{{
			Type:                  "text",
			Text:                  content,
			PromptCacheBreakpoint: &PromptCacheBreakpoint{Mode: "explicit"},
		}}
		return
	}
}

// optimizeSystemForCache 分离 system prompt 的静态/动态部分
// 返回: (staticSystem, dynamicTail)
// 如果不包含动态尾部标记，原样返回
func optimizeSystemForCache(system string) (staticPart, dynamicPart string) {
	idx := strings.LastIndex(system, lastUserMsgMarker)
	if idx < 0 {
		return system, ""
	}

	// 找到标记位置：标记之前是静态的，标记（含内容）是动态的
	staticPart = strings.TrimRight(system[:idx], "\n\r\t ")
	dynamicPart = system[idx:]
	return
}

// optimizeCurrentContentForCache 仅用于缓存审计，分析当前消息中的动态上下文。
// Provider 转换不能只拆 current，否则同一消息进入下一轮 history 后结构会变化。
func optimizeCurrentContentForCache(content string) (userQuestion, envContext string) {
	const envStart = "<EnvironmentContext>"
	const envEnd = "</EnvironmentContext>"

	startIdx := strings.Index(content, envStart)
	if startIdx < 0 {
		return content, ""
	}

	endIdx := strings.Index(content[startIdx:], envEnd)
	if endIdx < 0 {
		return content, ""
	}
	endIdx += startIdx + len(envEnd)

	// 用户问题 = 开头到 envStart 之前
	userQuestion = strings.TrimSpace(content[:startIdx])
	envContext = content[startIdx:endIdx]
	return
}

// applyOpenAICacheOptimization 对消息列表应用缓存优化。
// 优化点：
//  1. 拆分 system prompt 的动态尾部，避免每轮用户问题污染最前缀。
func applyOpenAICacheOptimization(msgs []OpenAIMessage, hasCurrentUser bool) []OpenAIMessage {
	if len(msgs) == 0 {
		return msgs
	}

	return splitOpenAISystemDynamicTail(msgs, hasCurrentUser)
}

func splitOpenAISystemDynamicTail(msgs []OpenAIMessage, hasCurrentUser bool) []OpenAIMessage {
	// 找 system 消息
	var systemIdx int = -1
	for i, m := range msgs {
		if m.Role == "system" {
			systemIdx = i
			break
		}
	}

	if systemIdx < 0 {
		return msgs
	}

	systemContent, ok := msgs[systemIdx].Content.(string)
	if !ok || systemContent == "" {
		return msgs
	}

	// 分离静态/动态部分
	staticPart, dynamicPart := optimizeSystemForCache(systemContent)
	if dynamicPart == "" {
		return msgs
	}

	// 将 system 内容替换为纯静态部分
	msgs[systemIdx].Content = staticPart

	// 把动态部分插入到最后一条 user 消息之前（即历史对话之后、当前消息之前）。
	// 这样 [static system] + [历史对话] 前缀保持稳定，最大化缓存命中。
	// 之前的实现放在 system 之后/历史之前，导致动态内容破坏了整个历史前缀的缓存。
	dynamicMsg := OpenAIMessage{
		Role:    "system",
		Content: dynamicPart,
	}

	// 找到最后一条 user 消息的位置（即当前用户输入），在其前面插入
	insertIdx := len(msgs)
	if hasCurrentUser {
		for i := len(msgs) - 1; i > systemIdx; i-- {
			if msgs[i].Role == "user" {
				insertIdx = i
				break
			}
		}
	}

	result := make([]OpenAIMessage, 0, len(msgs)+1)
	result = append(result, msgs[:insertIdx]...)
	result = append(result, dynamicMsg)
	result = append(result, msgs[insertIdx:]...)
	return result
}

// OptimizeSystemForCacheForAudit 暴露给代理层生成 prefix drift 日志，不改变请求体。
func OptimizeSystemForCacheForAudit(system string) (staticPart, dynamicPart string) {
	return optimizeSystemForCache(system)
}

// OptimizeCurrentContentForCacheForAudit 暴露给代理层生成 prefix drift 日志，不改变请求体。
func OptimizeCurrentContentForCacheForAudit(content string) (userQuestion, envContext string) {
	return optimizeCurrentContentForCache(content)
}
