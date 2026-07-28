package proxy

import (
	"context"
	"kiro-proxy/store"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

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
		[]byte(`{"stream":true}`),
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
		[]byte(`{"stream":true}`),
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
