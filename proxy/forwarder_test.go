package proxy

import (
	"context"
	"io"
	"kiro-proxy/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type closeTrackingBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *closeTrackingBody) Close() error {
	b.closed.Store(true)
	return nil
}

func TestProbeSSEPrecommitClosesBodyWhenContextAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := &closeTrackingBody{Reader: strings.NewReader("data: {}\n\n")}
	resp := &http.Response{Body: body}

	err := probeSSEPrecommit(ctx, resp, "openai", "/responses", time.Second, 0)
	if err == nil {
		t.Fatal("expected context cancellation")
	}
	if !body.closed.Load() {
		t.Fatal("probe did not close response body")
	}
}

func TestIsPreviousResponseRejected(t *testing.T) {
	cases := []string{
		`{"error":{"code":"previous_response_not_found","message":"previous_response_id not found"}}`,
		`{"error":{"message":"Unknown parameter: previous_response_id"}}`,
		`{"error":{"message":"invalid previous response id"}}`,
		`{"error":{"message":"previous_response_id is unsupported"}}`,
	}
	for _, body := range cases {
		if !isPreviousResponseRejected(body) {
			t.Fatalf("should reject: %s", body)
		}
	}

	if isPreviousResponseRejected(`{"error":{"message":"invalid api key"}}`) {
		t.Fatal("unrelated error should not be treated as previous_response_id rejection")
	}
}

func TestMergeUsageObjectReadsOpenAICacheWrites(t *testing.T) {
	var usage localCompatUsage
	mergeUsageObject(&usage, []byte(`{
		"input_tokens": 1400,
		"output_tokens": 9,
		"input_tokens_details": {
			"cached_tokens": 200,
			"cache_write_tokens": 1000
		}
	}`))
	if usage.InputTokens != 1400 || usage.OutputTokens != 9 || usage.CacheReadTokens != 200 || usage.CacheWriteTokens != 1000 {
		t.Fatalf("usage mismatch: %+v", usage)
	}
}

func TestUpstreamStatusRetryDelay(t *testing.T) {
	if delay := upstreamStatusRetryDelay(1, ""); delay != time.Second {
		t.Fatalf("first retry delay = %v, want 1s", delay)
	}
	if delay := upstreamStatusRetryDelay(3, ""); delay != 4*time.Second {
		t.Fatalf("third retry delay = %v, want 4s", delay)
	}
	if delay := upstreamStatusRetryDelay(1, "3"); delay != 3*time.Second {
		t.Fatalf("Retry-After delay = %v, want 3s", delay)
	}
	if delay := upstreamStatusRetryDelay(1, "60"); delay != maxRetryDelay {
		t.Fatalf("capped Retry-After delay = %v, want %v", delay, maxRetryDelay)
	}
}

func TestInternalServerErrorIsRetryableAndFailoverEligible(t *testing.T) {
	if !shouldRetryUpstreamStatus(http.StatusInternalServerError) {
		t.Fatal("HTTP 500 should be retried for a single model")
	}
	if !shouldFailoverModelStatus(http.StatusInternalServerError) {
		t.Fatal("HTTP 500 should fail over to another group candidate")
	}
}

func TestSendWithRetryRecoversAfterInternalServerError(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	defer logger.Close()

	forwarder := NewForwarder(nil, nil)
	resp, err := forwarder.sendWithRetry(
		context.Background(),
		server.URL,
		[]byte(`{"stream":false}`),
		&store.Model{Provider: "openai", APIKey: "test"},
		logger,
		"retry-500-test",
		nil,
	)
	if err != nil {
		t.Fatalf("sendWithRetry returned error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
}

func TestSendWithRetryRecoversAfterThreeServiceUnavailableResponses(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) <= 3 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	defer logger.Close()

	forwarder := NewForwarder(nil, nil)
	resp, err := forwarder.sendWithRetry(
		context.Background(),
		server.URL,
		[]byte(`{"stream":false}`),
		&store.Model{Provider: "openai", APIKey: "test"},
		logger,
		"retry-test",
		nil,
	)
	if err != nil {
		t.Fatalf("sendWithRetry returned error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := requests.Load(); got != 4 {
		t.Fatalf("request count = %d, want 4", got)
	}
}

func TestSendWithRetryBudgetEndsAfterResponseHeaders(t *testing.T) {
	requestContext := make(chan context.Context, 1)
	forwarder := NewForwarder(nil, nil)
	forwarder.clients[""] = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestContext <- req.Context()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(http.NoBody),
			Request:    req,
		}, nil
	})}

	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	defer logger.Close()

	ctx, cancel := context.WithCancel(context.Background())
	resp, err := forwarder.sendWithRetry(
		ctx,
		"http://upstream.test/v1/responses",
		[]byte(`{"stream":false}`),
		&store.Model{Provider: "openai", APIKey: "test"},
		logger,
		"retry-context-test",
		nil,
	)
	if err != nil {
		t.Fatalf("sendWithRetry returned error: %v", err)
	}
	defer resp.Body.Close()

	upstreamCtx := <-requestContext
	if deadline, ok := upstreamCtx.Deadline(); ok {
		t.Fatalf("accepted response context retains retry deadline: %v", deadline)
	}
	select {
	case <-upstreamCtx.Done():
		t.Fatalf("upstream context canceled before client cancellation: %v", upstreamCtx.Err())
	default:
	}

	cancel()
	select {
	case <-upstreamCtx.Done():
		if upstreamCtx.Err() != context.Canceled {
			t.Fatalf("upstream context error = %v, want context canceled", upstreamCtx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("client cancellation was not propagated to upstream context")
	}
}

func TestSendWithRetryRetriesSSEFailureBeforeFirstEvent(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"message\":\"upstream closed\"}\n\n")
			return
		}
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\"}}\n\n")
	}))
	defer server.Close()

	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	defer logger.Close()
	forwarder := NewForwarder(nil, nil)
	resp, err := forwarder.sendWithRetry(context.Background(), server.URL+"/responses", []byte(`{"stream":true}`), &store.Model{Provider: "openai", Endpoint: "/responses", APIKey: "test"}, logger, "precommit-retry", nil)
	if err != nil {
		t.Fatalf("sendWithRetry returned error: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read accepted response: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("request count = %d, want 2", requests.Load())
	}
	if !strings.Contains(string(body), "response.completed") || strings.Contains(string(body), "upstream closed") {
		t.Fatalf("unexpected accepted stream: %s", body)
	}
}

func TestSendWithRetryRetriesFailureAfterResponsesMetadata(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_started\"}}\n\n")
			_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"message\":\"late failure\"}\n\n")
			return
		}
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_ok\"}}\n\n")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\"}}\n\n")
	}))
	defer server.Close()

	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	defer logger.Close()
	forwarder := NewForwarder(nil, nil)
	resp, err := forwarder.sendWithRetry(context.Background(), server.URL+"/responses", []byte(`{"stream":true}`), &store.Model{Provider: "openai", Endpoint: "/responses", APIKey: "test"}, logger, "precommit-progress", nil)
	if err != nil {
		t.Fatalf("sendWithRetry returned error: %v", err)
	}
	defer resp.Body.Close()
	stream, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read committed response: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("request count = %d, want 2", requests.Load())
	}
	if !strings.Contains(string(stream), "resp_ok") || !strings.Contains(string(stream), "response.output_text.delta") || strings.Contains(string(stream), "late failure") {
		t.Fatalf("probe did not replay the complete stream: %s", stream)
	}
}
