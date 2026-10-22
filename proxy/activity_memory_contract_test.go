package proxy

import (
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

type activityMemoryFillReader struct {
	remaining int64
	read      int64
}

func (r *activityMemoryFillReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	for i := int64(0); i < n; i++ {
		p[i] = 'a'
	}
	r.remaining -= n
	r.read += n
	return int(n), nil
}

func TestActivityOversizedTokenAllocationDoesNotScaleWithBody(t *testing.T) {
	measure := func(size int64) uint64 {
		t.Helper()
		fill := &activityMemoryFillReader{remaining: size}
		body := io.MultiReader(strings.NewReader(`{"padding":"`), fill, strings.NewReader(`"}`))
		req := &http.Request{Body: io.NopCloser(body)}
		server := &Server{}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, parseErr, consumeErr := server.discardKiroActivityRequestBody(req)
		runtime.ReadMemStats(&after)
		if !errors.Is(parseErr, errKiroActivityTokenTooLarge) || consumeErr != nil || fill.read != size {
			t.Fatalf("size=%d parsed=%v consume=%v consumed=%d", size, parseErr, consumeErr, fill.read)
		}
		allocated := after.TotalAlloc - before.TotalAlloc
		t.Logf("body=%d MiB, allocated=%d KiB", size>>20, allocated>>10)
		return allocated
	}
	small, large := measure(4<<20), measure(40<<20)
	// 允许 lexer 的 token 缓冲扩容、固定 drain 缓冲及少量后台运行开销；
	// 旧实现对 40 MiB token 分配约 168 MiB，不能通过此实际分配边界。
	if large > 16<<20 || large > small+(4<<20) {
		t.Fatalf("allocation scales with body: 4MiB=%d, 40MiB=%d", small, large)
	}
}
