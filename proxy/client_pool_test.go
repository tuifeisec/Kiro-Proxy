package proxy

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type poolMockTransport struct{ closed atomic.Int32 }

func (t *poolMockTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("still streaming")), Request: r}, nil
}
func (t *poolMockTransport) CloseIdleConnections() { t.closed.Add(1) }

func TestClientPoolPreservesActiveBodyAndReapsIdle(t *testing.T) {
	f := NewForwarder(nil, nil)
	defer f.CloseIdleConnections()
	client, err := f.clientForProxy("http://127.0.0.1:1234")
	if err != nil {
		t.Fatal(err)
	}
	entry := client.Transport.(*pooledTransport)
	source := &poolMockTransport{}
	entry.source = source
	req, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	f.clientMu.Lock()
	f.pruneClientsLocked(time.Now().Add(clientPoolIdleTTL * 2))
	f.clientMu.Unlock()
	if f.clients["http://127.0.0.1:1234"] == nil || source.closed.Load() != 0 {
		t.Fatal("active response was evicted")
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil || string(data) != "still streaming" {
		t.Fatalf("body=%q err=%v", data, err)
	}
	_ = resp.Body.Close()
	f.clientMu.Lock()
	f.pruneClientsLocked(time.Now().Add(clientPoolIdleTTL * 2))
	f.clientMu.Unlock()
	if f.clients["http://127.0.0.1:1234"] != nil || source.closed.Load() == 0 {
		t.Fatal("idle client not retired")
	}
	// 已获取的旧 client 仍可完成请求，完成后关闭它新产生的空闲连接。
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if source.closed.Load() < 2 {
		t.Fatal("retired client leaked new idle connection")
	}
}

func TestClientPoolIdleEntryLimit(t *testing.T) {
	f := NewForwarder(nil, nil)
	defer f.CloseIdleConnections()
	for i := 0; i < clientPoolMaxIdle+5; i++ {
		_, err := f.clientForProxy("http://proxy-" + strings.Repeat("x", i+1) + ".invalid:3128")
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(f.clients) > clientPoolMaxIdle {
		t.Fatalf("clients=%d", len(f.clients))
	}
	if f.clients[""] == nil {
		t.Fatal("direct client should remain pooled")
	}
}
