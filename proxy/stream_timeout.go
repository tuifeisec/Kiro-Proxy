package proxy

import (
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

var errUpstreamReadIdleTimeout = errors.New("upstream response read idle timeout")

type upstreamReadIdleTimeoutBody struct {
	source    io.ReadCloser
	timeout   time.Duration
	closeOnce sync.Once
	timedOut  atomic.Bool
}

func wrapUpstreamReadIdleTimeout(resp *http.Response, timeout time.Duration) {
	if resp == nil || resp.Body == nil || timeout <= 0 {
		return
	}
	resp.Body = &upstreamReadIdleTimeoutBody{source: resp.Body, timeout: timeout}
}

func (b *upstreamReadIdleTimeoutBody) Read(p []byte) (int, error) {
	timer := time.AfterFunc(b.timeout, func() {
		b.timedOut.Store(true)
		b.closeSource()
	})
	n, err := b.source.Read(p)
	timer.Stop()
	if b.timedOut.Load() {
		return n, errUpstreamReadIdleTimeout
	}
	return n, err
}

func (b *upstreamReadIdleTimeoutBody) Close() error {
	return b.closeSource()
}

func (b *upstreamReadIdleTimeoutBody) closeSource() error {
	var err error
	b.closeOnce.Do(func() {
		err = b.source.Close()
	})
	return err
}
