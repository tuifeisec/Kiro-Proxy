package upstreamproxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestAttemptObserverCountsProtocolAndSubsequentStatusRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	var calls, fallbacks atomic.Int32
	ctx = WithAttemptObserver(ctx, func(fallback bool) {
		calls.Add(1)
		if fallback {
			fallbacks.Add(1)
		}
	})
	statusCalls := 0
	transport := &protocolFallbackTransport{
		primary: roundTripFunc(func(req *http.Request) (*http.Response, error) { return nil, errors.New("http2: stream error") }),
		fallback: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got, _ := req.Context().Deadline(); !got.Equal(deadline) {
				t.Fatal("fallback reset deadline")
			}
			statusCalls++
			status := http.StatusServiceUnavailable
			if statusCalls > 1 {
				status = http.StatusOK
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(nil))}, nil
		}),
	}
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid/v1/responses", bytes.NewBufferString("same body"))
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	if calls.Load() != 3 || fallbacks.Load() != 1 {
		t.Fatalf("wire attempts=%d protocol fallbacks=%d", calls.Load(), fallbacks.Load())
	}
}
