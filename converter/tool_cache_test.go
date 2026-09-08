package converter

import (
	"fmt"
	json "github.com/goccy/go-json"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func cacheTestTools() []json.RawMessage {
	return []json.RawMessage{json.RawMessage(`{"toolSpecification":{"name":"read","description":"read a file","inputSchema":{"json":{"type":"object","properties":{"path":{"type":"string"}}}}}}`)}
}

func TestToolCacheLazyConversionAndResult(t *testing.T) {
	c := &ToolCache{}
	raw := cacheTestTools()
	if got := c.GetOpenAI("a", raw); !reflect.DeepEqual(got, toOpenAITools(raw)) {
		t.Fatalf("changed OpenAI tools: %+v", got)
	}
	if c.Snapshot().Misses != 1 || len(c.items) != 1 {
		t.Fatal("converted unused protocol")
	}
	c.GetOpenAI("a", raw)
	if got := c.GetAnthropic("a", raw); !reflect.DeepEqual(got, toAnthropicTools(raw)) {
		t.Fatalf("changed Anthropic tools: %+v", got)
	}
	s := c.Snapshot()
	if s.Misses != 2 || s.Hits != 1 || s.Entries != 2 || s.RetainedBytes <= 0 {
		t.Fatalf("stats: %+v", s)
	}
}

func TestToolCacheMissingKeyStillConverts(t *testing.T) {
	c := &ToolCache{}
	got, hash := c.GetOpenAIWithHash("", cacheTestTools())
	if !reflect.DeepEqual(got, toOpenAITools(cacheTestTools())) || hash == "" {
		t.Fatal("missing key must not discard tools")
	}
	if c.Snapshot().Entries != 0 {
		t.Fatal("missing key must not share a cache entry")
	}
}

func TestToolCacheCoalescesConcurrentMiss(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	c := &ToolCache{convert: func(provider string, raw []json.RawMessage) *toolCacheEntry {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return convertToolCacheEntry(provider, raw)
	}}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.GetOpenAI("same", cacheTestTools()) }()
	}
	<-started
	deadline := time.Now().Add(3 * time.Second)
	for c.Snapshot().Coalesced != 15 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 || c.Snapshot().Coalesced != 15 {
		t.Fatalf("calls=%d stats=%+v", calls.Load(), c.Snapshot())
	}
}

func TestToolCacheBudgetTTLAndLRU(t *testing.T) {
	now := time.Unix(100, 0)
	c := &ToolCache{maxBytes: 200, maxItems: 2, now: func() time.Time { return now }, convert: func(_ string, _ []json.RawMessage) *toolCacheEntry { return &toolCacheEntry{bytes: 80} }}
	raw := cacheTestTools()
	for _, key := range []string{"a", "b", "a", "c"} {
		now = now.Add(time.Second)
		c.GetOpenAI(key, raw)
	}
	if c.items["openai:b"] != nil || c.items["openai:a"] == nil || c.Snapshot().RetainedBytes > 200 {
		t.Fatalf("budget/LRU: %+v", c.Snapshot())
	}
	now = now.Add(toolCacheTTL)
	if s := c.Snapshot(); s.Entries != 0 || s.RetainedBytes != 0 {
		t.Fatalf("TTL: %+v", s)
	}

	oversized := &ToolCache{maxBytes: 1}
	if got := oversized.GetOpenAI("large", raw); !reflect.DeepEqual(got, toOpenAITools(raw)) {
		t.Fatal("oversized tools must still convert")
	}
	if s := oversized.Snapshot(); s.Oversized != 1 || s.Entries != 0 {
		t.Fatalf("oversized: %+v", s)
	}
}

func TestToolCacheConcurrentMixedKeysBounded(t *testing.T) {
	c := &ToolCache{maxItems: 4, maxBytes: 2048}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); c.GetOrConvert(fmt.Sprint(i%8), cacheTestTools()); c.Snapshot() }(i)
	}
	wg.Wait()
	if s := c.Snapshot(); s.Entries > 4 || s.RetainedBytes > 2048 || s.RetainedBytes < 0 {
		t.Fatalf("budget: %+v", s)
	}
}
