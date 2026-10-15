package proxy

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"kiro-proxy/store"
)

var benchmarkRawResponsesPrepareBody []byte

func BenchmarkRawResponsesPrepareCompatibility(b *testing.B) {
	for _, size := range []int{256 << 10, 1 << 20} {
		for _, endpoint := range []string{"/responses", "/chat/completions"} {
			b.Run(fmt.Sprintf("%s/%dKiB", strings.TrimPrefix(endpoint, "/"), size>>10), func(b *testing.B) {
				body, err := json.Marshal(map[string]any{
					"model": "local-alias", "stream": true, "instructions": "Synthetic benchmark",
					"input":    []map[string]any{{"role": "user", "content": strings.Repeat("a", size)}},
					"metadata": map[string]string{"kiro:conversation_id": "synthetic"},
					"tools": []map[string]any{{"type": "function", "name": "lookup", "parameters": map[string]any{
						"type": "object", "properties": map[string]any{"key": map[string]string{"type": "string"}},
					}}},
				})
				if err != nil {
					b.Fatal(err)
				}
				model := &store.Model{
					ModelID: "synthetic-model", Provider: "openai", BaseURL: "http://127.0.0.1:1/v1",
					Endpoint: endpoint, ReasoningEffort: "low",
				}
				pre := &Preflight{Body: body, Protocol: "kiro-runtime-responses"}
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					benchmarkRawResponsesPrepareBody, err = prepareRawResponsesUpstreamBody(body, pre, model)
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
