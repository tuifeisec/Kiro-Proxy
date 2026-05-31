package converter

import (
	json "github.com/goccy/go-json"
	"sync"
	"time"
)

// ToolCache 缓存工具定义的转换结果。
// Kiro 每次请求都携带完整工具定义（~300KB），但工具定义几乎不变。
// 用内容哈希做 key，避免每次重复转换。
type ToolCache struct {
	mu    sync.RWMutex
	items map[string]*toolCacheEntry
}

type toolCacheEntry struct {
	openai    []OpenAITool
	anthropic []AnthropicTool
	createdAt time.Time
}

const (
	toolCacheTTL      = 10 * time.Minute
	maxToolCacheItems = 100
)

var globalToolCache = &ToolCache{
	items: make(map[string]*toolCacheEntry),
}

// GetOrConvert 返回缓存的转换结果，未命中时调用 tool_convert.go 执行转换并缓存
func (c *ToolCache) GetOrConvert(hash string, rawTools []json.RawMessage) ([]OpenAITool, []AnthropicTool) {
	if hash == "" || len(rawTools) == 0 {
		return nil, nil
	}

	c.mu.RLock()
	entry, ok := c.items[hash]
	c.mu.RUnlock()

	if ok && time.Since(entry.createdAt) < toolCacheTTL {
		return entry.openai, entry.anthropic
	}

	// 缓存未命中，转换逻辑在 tool_convert.go
	openaiTools := toOpenAITools(rawTools)
	anthropicTools := toAnthropicTools(rawTools)

	c.mu.Lock()
	c.items[hash] = &toolCacheEntry{
		openai:    openaiTools,
		anthropic: anthropicTools,
		createdAt: time.Now(),
	}
	if len(c.items) > maxToolCacheItems {
		c.evictLocked()
	}
	c.mu.Unlock()

	return openaiTools, anthropicTools
}

func (c *ToolCache) evictLocked() {
	now := time.Now()
	for k, v := range c.items {
		if now.Sub(v.createdAt) > toolCacheTTL {
			delete(c.items, k)
		}
	}

	// 硬上限保护：如果短时间内出现大量不同 tools hash，过期淘汰可能删不掉。
	// 继续删除最旧项，避免并发/异常输入导致缓存 map 静默增长。
	for len(c.items) > maxToolCacheItems {
		oldestKey := ""
		var oldest time.Time
		for k, v := range c.items {
			if oldestKey == "" || v.createdAt.Before(oldest) {
				oldestKey = k
				oldest = v.createdAt
			}
		}
		if oldestKey == "" {
			return
		}
		delete(c.items, oldestKey)
	}
}
