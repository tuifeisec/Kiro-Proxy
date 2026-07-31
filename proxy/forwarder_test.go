package proxy

import (
	"context"
	"io"
	"kiro-proxy/converter"
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

func TestModelCapabilityMismatchIsFailoverEligible(t *testing.T) {
	capabilityErrors := []string{
		`{"error":{"message":"Invalid content type. image_url is only supported by certain models"}}`,
		`{"error":{"message":"Model do not support image input"}}`,
		`{"error":{"message":"Model only support text input"}}`,
		`{"error":{"message":"Model only supports text input"}}`,
	}
	for _, body := range capabilityErrors {
		if !isModelCapabilityMismatch(http.StatusBadRequest, body) {
			t.Fatalf("capability mismatch should fail over to another group candidate: %s", body)
		}
	}
	if isModelCapabilityMismatch(http.StatusBadRequest, `{"error":{"message":"invalid request parameter"}}`) {
		t.Fatal("ordinary HTTP 400 should not fail over")
	}
	if isModelCapabilityMismatch(http.StatusUnprocessableEntity, capabilityErrors[0]) {
		t.Fatal("unexpected status should not be treated as an image capability mismatch")
	}
}

func TestImageCompatibleCandidatesUseConfigurationAndObservations(t *testing.T) {
	now := time.Now()
	configuredSupported := &store.Model{ID: "supported", ImageInputMode: store.ImageInputSupported}
	auto := &store.Model{ID: "auto", ImageInputMode: store.ImageInputAuto}
	configuredUnsupported := &store.Model{ID: "unsupported", ImageInputMode: store.ImageInputUnsupported}
	capabilities := newModelImageCapabilityStore()

	// 无观察：配置 unsupported 硬过滤，其余按原始顺序稳定保留
	candidates, skipped := imageCompatibleCandidates(
		[]*store.Model{auto, configuredUnsupported, configuredSupported},
		true,
		capabilities,
		now,
	)
	if len(candidates) != 2 || candidates[0] != auto || candidates[1] != configuredSupported {
		t.Fatalf("stable order: auto should precede supported (original order preserved): %+v", candidates)
	}
	if len(skipped) != 1 || skipped[0].model != configuredUnsupported || skipped[0].source != "config" {
		t.Fatalf("configured unsupported model was not skipped: %+v", skipped)
	}

	// 观察到 auto 不支持图片：软降级到末尾，仍保留在 candidates（允许复探）
	capabilities.observe(auto, modelImageUnsupported, now)
	candidates, skipped = imageCompatibleCandidates(
		[]*store.Model{auto, configuredUnsupported, configuredSupported},
		true,
		capabilities,
		now,
	)
	// configuredSupported 保持在前；auto 软降级到末尾但仍在列表中
	if len(candidates) != 2 || candidates[0] != configuredSupported || candidates[1] != auto {
		t.Fatalf("observed unsupported should be soft-demoted to end, not hard-skipped: %+v", candidates)
	}
	// only config-unsupported goes to skipped; observed-unsupported stays in candidates
	if len(skipped) != 1 || skipped[0].model != configuredUnsupported {
		t.Fatalf("only configured unsupported should be in skipped, got %+v", skipped)
	}

	// 无图片请求：忽略能力标签
	textCandidates, textSkipped := imageCompatibleCandidates(
		[]*store.Model{auto, configuredUnsupported, configuredSupported},
		false,
		capabilities,
		now,
	)
	if len(textCandidates) != 3 || len(textSkipped) != 0 {
		t.Fatalf("text routing must ignore image capability: candidates=%d skipped=%d", len(textCandidates), len(textSkipped))
	}
}

func TestConversationImageStatsIncludesHistory(t *testing.T) {
	conv := &converter.ParsedConversation{
		CurrentImages: []converter.KiroImage{{Source: converter.KiroImageSource{Bytes: "YWJj"}}},
		History: []converter.ParsedHistoryEntry{{
			Images: []converter.KiroImage{{Source: converter.KiroImageSource{Bytes: "YQ=="}}},
		}},
	}
	count, bytes := conversationImageStats(conv)
	if count != 2 || bytes != 4 {
		t.Fatalf("image stats = (%d, %d), want (2, 4)", count, bytes)
	}
}

func TestImageAwareFirstByteTimeoutUsesSingleCandidateBudget(t *testing.T) {
	now := time.Now()
	deadline := now.Add(2 * time.Minute)
	if got := imageAwareFirstByteTimeout(10*time.Second, deadline, now, false, true, 1); got != singleImageTimeout {
		t.Fatalf("single image candidate timeout = %v, want %v", got, singleImageTimeout)
	}
	if got := imageAwareFirstByteTimeout(10*time.Second, deadline, now, false, true, 2); got != 20*time.Second {
		t.Fatalf("multiple image candidate timeout = %v, want 20s", got)
	}
	if got := imageAwareFirstByteTimeout(10*time.Second, deadline, now, false, false, 1); got != 20*time.Second {
		t.Fatalf("text timeout = %v, want 20s", got)
	}
}

func TestSupportedInputTypesFollowImageConfiguration(t *testing.T) {
	if got := supportedInputTypesForModel(store.Model{ImageInputMode: store.ImageInputUnsupported}); len(got) != 1 || got[0] != "TEXT" {
		t.Fatalf("unsupported model inputs = %#v", got)
	}
	if got := supportedInputTypesForModel(store.Model{ImageInputMode: store.ImageInputAuto}); len(got) != 2 || got[1] != "IMAGE" {
		t.Fatalf("auto model inputs = %#v", got)
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
