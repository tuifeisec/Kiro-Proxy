package converter

import (
	"io"
	"strings"
	"testing"
)

func runOpenAIPipeline(sse string) *OpenAIStreamConverter {
	sc := NewOpenAIStreamConverter(io.NopCloser(strings.NewReader(sse)), "gpt-5.5", 200000, 1.0)
	_, _ = io.ReadAll(sc)
	return sc
}

func TestOpenAIStream_DeepSeekPromptCacheUsage(t *testing.T) {
	sse := `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_cache_hit_tokens":900,"prompt_cache_miss_tokens":100,"completion_tokens":7}}

data: [DONE]

`
	sc := runOpenAIPipeline(sse)
	in, out := sc.Usage()
	cacheRead, cacheWrite := sc.CacheTokens()

	if in != 1000 {
		t.Fatalf("input tokens = %d, want 1000", in)
	}
	if out != 7 {
		t.Fatalf("output tokens = %d, want 7", out)
	}
	if cacheRead != 900 || cacheWrite != 0 {
		t.Fatalf("cache tokens = (%d,%d), want (900,0)", cacheRead, cacheWrite)
	}
}

func TestOpenAIStream_OpenAICachedTokensStillSupported(t *testing.T) {
	sse := `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":1200,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":768}}}

data: [DONE]

`
	sc := runOpenAIPipeline(sse)
	in, out := sc.Usage()
	cacheRead, _ := sc.CacheTokens()

	if in != 1200 || out != 8 || cacheRead != 768 {
		t.Fatalf("usage mismatch: in=%d out=%d cache=%d", in, out, cacheRead)
	}
}
