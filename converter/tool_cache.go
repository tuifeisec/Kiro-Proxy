package converter

import (
	json "github.com/goccy/go-json"
	"sync"
	"time"
	"unsafe"
)

// ToolCache 返回只读转换结果。字节预算统计保留数据，不代表进程 RSS。
type ToolCache struct {
	mu       sync.Mutex
	items    map[string]*toolCacheEntry
	loading  map[string]*toolCacheFlight
	stats    ToolCacheStats
	maxBytes int64
	maxItems int
	now      func() time.Time
	convert  func(string, []json.RawMessage) *toolCacheEntry
}

type toolCacheEntry struct {
	openai              []OpenAITool
	anthropic           []AnthropicTool
	createdAt, lastUsed time.Time
	bytes               int64
	providerHash        string
	report              ToolConversionReport
}

type toolCacheFlight struct {
	done  chan struct{}
	entry *toolCacheEntry
}

type ToolCacheStats struct {
	Hits          uint64 `json:"hits"`
	Misses        uint64 `json:"misses"`
	Coalesced     uint64 `json:"coalesced"`
	Evictions     uint64 `json:"evictions"`
	Oversized     uint64 `json:"oversized"`
	ConversionNS  uint64 `json:"conversion_ns"`
	RetainedBytes int64  `json:"retained_bytes"`
	Entries       int    `json:"entries"`
	MaxBytes      int64  `json:"max_bytes"`
}

const (
	toolCacheTTL      = 10 * time.Minute
	maxToolCacheItems = 100
	maxToolCacheBytes = 32 * 1024 * 1024
)

var globalToolCache = &ToolCache{}

func GlobalToolCacheStats() ToolCacheStats { return globalToolCache.Snapshot() }

func (c *ToolCache) Snapshot() ToolCacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.initLocked()
	c.evictLocked(c.now())
	s := c.stats
	s.Entries, s.MaxBytes = len(c.items), c.maxBytes
	return s
}

func (c *ToolCache) initLocked() {
	if c.items == nil {
		c.items = make(map[string]*toolCacheEntry)
	}
	if c.loading == nil {
		c.loading = make(map[string]*toolCacheFlight)
	}
	if c.maxBytes <= 0 {
		c.maxBytes = maxToolCacheBytes
	}
	if c.maxItems <= 0 {
		c.maxItems = maxToolCacheItems
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.convert == nil {
		c.convert = convertToolCacheEntry
	}
}

func (c *ToolCache) GetOpenAI(hash string, raw []json.RawMessage) []OpenAITool {
	return c.get("openai", hash, raw).openai
}

func (c *ToolCache) GetOpenAIWithHash(hash string, raw []json.RawMessage) ([]OpenAITool, string) {
	e := c.get("openai", hash, raw)
	return e.openai, e.providerHash
}

func CachedOpenAIToolAudit(hash string, raw []json.RawMessage) (string, ToolConversionReport) {
	if hash == "" {
		return ProviderToolsHashForAudit(raw), ToolConversionReportForAudit(raw, true)
	}
	e := globalToolCache.get("openai", hash, raw)
	return e.providerHash, e.report
}

func (c *ToolCache) GetAnthropic(hash string, raw []json.RawMessage) []AnthropicTool {
	return c.get("anthropic", hash, raw).anthropic
}

// GetOrConvert 保留原接口；请求热路径只转换所需协议。
func (c *ToolCache) GetOrConvert(hash string, raw []json.RawMessage) ([]OpenAITool, []AnthropicTool) {
	return c.GetOpenAI(hash, raw), c.GetAnthropic(hash, raw)
}

func (c *ToolCache) get(provider, hash string, raw []json.RawMessage) *toolCacheEntry {
	if len(raw) == 0 {
		return &toolCacheEntry{}
	}
	// 缺少缓存键只能跳过缓存，不能删除请求中的工具定义。
	if hash == "" {
		return convertToolCacheEntry(provider, raw)
	}
	key := provider + ":" + hash
	c.mu.Lock()
	c.initLocked()
	now := c.now()
	c.evictLocked(now)
	if e := c.items[key]; e != nil {
		e.lastUsed = now
		c.stats.Hits++
		c.mu.Unlock()
		return e
	}
	if flight := c.loading[key]; flight != nil {
		c.stats.Coalesced++
		c.mu.Unlock()
		<-flight.done
		return flight.entry
	}
	flight := &toolCacheFlight{done: make(chan struct{})}
	c.loading[key] = flight
	c.stats.Misses++
	convert := c.convert
	c.mu.Unlock()

	start := time.Now()
	e := convert(provider, raw)
	elapsed := time.Since(start)
	c.mu.Lock()
	e.createdAt, e.lastUsed = c.now(), c.now()
	e.bytes += int64(len(key))
	c.stats.ConversionNS += uint64(elapsed)
	if e.bytes <= c.maxBytes {
		c.items[key] = e
		c.stats.RetainedBytes += e.bytes
		c.evictLocked(e.createdAt)
	} else {
		c.stats.Oversized++
	}
	flight.entry = e
	delete(c.loading, key)
	close(flight.done)
	c.mu.Unlock()
	return e
}

func (c *ToolCache) evictLocked(now time.Time) {
	remove := func(key string, e *toolCacheEntry) {
		delete(c.items, key)
		c.stats.RetainedBytes -= e.bytes
		c.stats.Evictions++
	}
	for key, e := range c.items {
		if now.Sub(e.createdAt) >= toolCacheTTL {
			remove(key, e)
		}
	}
	for len(c.items) > c.maxItems || c.stats.RetainedBytes > c.maxBytes {
		var oldestKey string
		var oldest *toolCacheEntry
		for key, e := range c.items {
			if oldest == nil || e.lastUsed.Before(oldest.lastUsed) {
				oldestKey, oldest = key, e
			}
		}
		if oldest == nil {
			break
		}
		remove(oldestKey, oldest)
	}
}

func convertToolCacheEntry(provider string, raw []json.RawMessage) *toolCacheEntry {
	e := &toolCacheEntry{bytes: int64(unsafe.Sizeof(toolCacheEntry{}))}
	if provider == "openai" {
		e.openai = toOpenAITools(raw)
		e.providerHash = hashOpenAITools(e.openai)
		e.report = ToolConversionReportForAudit(raw, true)
		e.bytes += int64(len(e.providerHash))
		e.bytes += int64(cap(e.openai)) * int64(unsafe.Sizeof(OpenAITool{}))
		for _, t := range e.openai {
			e.bytes += int64(len(t.Type) + len(t.Function.Name) + len(t.Function.Description))
			if schema, ok := t.Function.Parameters.(json.RawMessage); ok {
				e.bytes += int64(cap(schema))
			}
		}
	} else {
		e.anthropic = toAnthropicTools(raw)
		e.bytes += int64(cap(e.anthropic)) * int64(unsafe.Sizeof(AnthropicTool{}))
		for _, t := range e.anthropic {
			e.bytes += int64(len(t.Name) + len(t.Description))
			if schema, ok := t.InputSchema.(json.RawMessage); ok {
				e.bytes += int64(cap(schema))
			}
		}
	}
	return e
}
