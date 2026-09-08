package proxy

import (
	"io"
	"kiro-proxy/upstreamproxy"
	"net/http"
	"strings"
	"sync"
	"time"
)

const clientPoolIdleTTL = 10 * time.Minute
const clientPoolMaxIdle = 16

// pooledTransport 跟踪整个响应体生命周期，回收只关闭空闲连接。
type pooledTransport struct {
	source    http.RoundTripper
	client    *http.Client
	mu        sync.Mutex
	active    int
	lastUsed  time.Time
	retired   bool
	onRelease func()
}

func (t *pooledTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.active++
	t.lastUsed = time.Now()
	t.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			t.mu.Lock()
			t.active--
			t.lastUsed = time.Now()
			retired := t.retired
			t.mu.Unlock()
			if retired {
				t.CloseIdleConnections()
			}
			if t.onRelease != nil {
				t.onRelease()
			}
		})
	}
	resp, err := t.source.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		release()
	} else {
		resp.Body = &poolResponseBody{ReadCloser: resp.Body, release: release}
	}
	return resp, err
}

func (t *pooledTransport) CloseIdleConnections() {
	if closer, ok := t.source.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

type poolResponseBody struct {
	io.ReadCloser
	release func()
}

func (b *poolResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.release()
	}
	return n, err
}
func (b *poolResponseBody) Close() error { err := b.ReadCloser.Close(); b.release(); return err }

func (f *Forwarder) clientForProxy(proxyAddr string) (*http.Client, error) {
	proxyAddr = strings.TrimSpace(proxyAddr)
	f.clientMu.Lock()
	defer f.clientMu.Unlock()
	if f.clients == nil {
		f.clients = make(map[string]*http.Client)
	}
	if f.clientEntries == nil {
		f.clientEntries = make(map[string]*pooledTransport)
	}
	if client := f.clients[proxyAddr]; client != nil {
		if entry := f.clientEntries[proxyAddr]; entry != nil {
			entry.mu.Lock()
			entry.lastUsed = time.Now()
			entry.mu.Unlock()
		}
		f.pruneClientsLocked(time.Now())
		return client, nil
	}
	client, err := upstreamproxy.NewClient(proxyAddr, 0)
	if err != nil {
		return nil, err
	}
	entry := &pooledTransport{source: client.Transport, client: client, lastUsed: time.Now()}
	entry.onRelease = func() { f.clientMu.Lock(); f.pruneClientsLocked(time.Now()); f.clientMu.Unlock() }
	client.Transport = entry
	f.clients[proxyAddr], f.clientEntries[proxyAddr] = client, entry
	f.pruneClientsLocked(time.Now())
	return client, nil
}

func (f *Forwarder) pruneClientsLocked(now time.Time) {
	// 活跃请求允许暂时超出空闲预算；其响应结束时再次执行清理。
	idle := 0
	for _, entry := range f.clientEntries {
		entry.mu.Lock()
		if entry.active == 0 {
			idle++
		}
		entry.mu.Unlock()
	}
	for {
		var key string
		var oldest *pooledTransport
		var oldestTime time.Time
		for k, entry := range f.clientEntries {
			if k == "" {
				continue
			}
			entry.mu.Lock()
			active, last := entry.active, entry.lastUsed
			entry.mu.Unlock()
			if active == 0 && (oldest == nil || last.Before(oldestTime)) {
				key, oldest, oldestTime = k, entry, last
			}
		}
		if oldest == nil || (idle <= clientPoolMaxIdle && now.Sub(oldestTime) < clientPoolIdleTTL) {
			return
		}
		oldest.mu.Lock()
		// 已取得 client 的请求可能在刚才的检查后开始 RoundTrip。
		if oldest.active != 0 {
			oldest.mu.Unlock()
			continue
		}
		oldest.retired = true
		oldest.mu.Unlock()
		if f.clients[key] == oldest.client {
			delete(f.clients, key)
		}
		delete(f.clientEntries, key)
		oldest.CloseIdleConnections()
		idle--
	}
}

func (f *Forwarder) CloseIdleConnections() {
	if f == nil {
		return
	}
	f.clientMu.Lock()
	defer f.clientMu.Unlock()
	for _, entry := range f.clientEntries {
		entry.mu.Lock()
		entry.retired = true
		entry.mu.Unlock()
	}
	for _, client := range f.clients {
		client.CloseIdleConnections()
	}
}
