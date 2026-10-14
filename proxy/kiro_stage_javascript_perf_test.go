package proxy

import (
	"strings"
	"testing"
)

func BenchmarkKiroStageJSAnalyze(b *testing.B) {
	const unit = `(()=>{function process(input = fallback){const item=input;return item&&handlers.transform(item)}return process(seed)})()` + "\n"
	source := []byte(strings.Repeat(unit, 128))
	b.SetBytes(int64(len(source)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := analyzeKiroStageJS(source); err != nil {
			b.Fatal(err)
		}
	}
}
