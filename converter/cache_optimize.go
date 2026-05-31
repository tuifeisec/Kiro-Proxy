// cache_optimize.go
// OpenAI Prompt Cache 优化：调整消息顺序，最大化缓存命中率
//
// OpenAI Prompt Caching 规则：
//   - 按消息顺序从头开始匹配前缀
//   - 只要前缀内容完全一致，就能命中缓存
//   - 一旦某条消息内容变了，后面的都无法缓存
//   - 缓存以请求前 256 tokens + user 参数做路由 hash，决定打到哪台机器
//   - 同一台机器上 KV cache 5~10 分钟不活跃就被清理
//
// Kiro 的问题：
//   1. system prompt 里嵌入了动态内容（如 "Here is the last user message:\n你好"）
//      → 每次用户说不同的话，system 就变了 → 如果放在前缀里，缓存全废
//   2. EnvironmentContext 每次可能不同（打开的文件变了）
//   3. 不同会话路由到不同机器 → 5 分钟后缓存丢失
//
// 优化策略：
//   - 从 system prompt 里提取出动态尾部（"Here is the last user message:\n..."）
//   - 静态部分保留在 system 最前面（可缓存）
//   - 历史对话紧跟 system 之后（稳定，只会追加不会修改 → 可缓存）
//   - 动态部分（system 尾部 + EnvironmentContext）移到最后，不影响前缀缓存
//   - 通过 user 参数设置 conversation_id，让同一会话的请求路由到同一机器，
//     避免 5 分钟超时因路由到不同机器而丢缓存
//   - 最终顺序：[static system] [history...] [dynamic tail] [env context] [current user]

package converter

import "strings"

const lastUserMsgMarker = "Here is the last user message:\n"

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

// optimizeCurrentContentForCache 从当前用户消息中分离出 EnvironmentContext
// EnvironmentContext 每轮可能变化（打开的文件不同），但核心用户问题不变
// 返回: (userQuestion, envContext)
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
//  2. 拆分最后一条当前 user 消息中的 EnvironmentContext，将易变 IDE 上下文
//     作为独立 user 消息放在真实用户问题之前，便于历史前缀继续命中缓存。
func applyOpenAICacheOptimization(msgs []OpenAIMessage) []OpenAIMessage {
	if len(msgs) == 0 {
		return msgs
	}

	optimized := splitOpenAISystemDynamicTail(msgs)
	optimized = splitOpenAICurrentEnvironmentContext(optimized)
	return optimized
}

func splitOpenAISystemDynamicTail(msgs []OpenAIMessage) []OpenAIMessage {
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
		Role:    "user",
		Content: dynamicPart,
	}

	// 找到最后一条 user 消息的位置（即当前用户输入），在其前面插入
	insertIdx := len(msgs) // 默认放末尾
	for i := len(msgs) - 1; i > systemIdx; i-- {
		if msgs[i].Role == "user" {
			insertIdx = i
			break
		}
	}

	result := make([]OpenAIMessage, 0, len(msgs)+1)
	result = append(result, msgs[:insertIdx]...)
	result = append(result, dynamicMsg)
	result = append(result, msgs[insertIdx:]...)
	return result
}

func splitOpenAICurrentEnvironmentContext(msgs []OpenAIMessage) []OpenAIMessage {
	// 只处理最后一条普通文本 user 消息，即当前用户输入。历史 user 消息保持原样，
	// 避免改变已缓存的 append-only log。
	idx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		if _, ok := msgs[i].Content.(string); ok {
			idx = i
		}
		break
	}
	if idx < 0 {
		return msgs
	}

	content := msgs[idx].Content.(string)
	userQuestion, envContext := optimizeCurrentContentForCache(content)
	if envContext == "" {
		return msgs
	}

	replacement := make([]OpenAIMessage, 0, 2)
	if envContext != "" {
		replacement = append(replacement, OpenAIMessage{Role: "user", Content: envContext})
	}
	if userQuestion != "" {
		replacement = append(replacement, OpenAIMessage{Role: "user", Content: userQuestion})
	}
	if len(replacement) == 0 {
		return msgs
	}

	result := make([]OpenAIMessage, 0, len(msgs)+len(replacement)-1)
	result = append(result, msgs[:idx]...)
	result = append(result, replacement...)
	result = append(result, msgs[idx+1:]...)
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
