package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"kiro-proxy/converter"
	"kiro-proxy/router"
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

type fixedErrorBody struct {
	err error
}

type blockingCloseBody struct {
	closed     chan struct{}
	closeCalls atomic.Int32
}

func (b *fixedErrorBody) Read([]byte) (int, error) { return 0, b.err }
func (b *fixedErrorBody) Close() error             { return nil }

func (b *blockingCloseBody) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingCloseBody) Close() error {
	b.closeCalls.Add(1)
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

func (b *closeTrackingBody) Close() error {
	b.closed.Store(true)
	return nil
}

func TestReadUpstreamErrorBodyWithLimits(t *testing.T) {
	t.Run("small body", func(t *testing.T) {
		body := &closeTrackingBody{Reader: strings.NewReader("upstream error")}
		got, err := readUpstreamErrorBodyWithLimits(body, 1024, time.Second)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(got) != "upstream error" {
			t.Fatalf("unexpected body: %q", got)
		}
		if !body.closed.Load() {
			t.Fatal("body was not closed")
		}
	})

	t.Run("size limit", func(t *testing.T) {
		body := &closeTrackingBody{Reader: strings.NewReader("123456")}
		got, err := readUpstreamErrorBodyWithLimits(body, 5, time.Second)
		if !errors.Is(err, errUpstreamErrorBodyTooLarge) {
			t.Fatalf("expected size error, got %v", err)
		}
		if string(got) != "12345" {
			t.Fatalf("unexpected truncated body: %q", got)
		}
	})

	t.Run("timeout closes body", func(t *testing.T) {
		body := &blockingCloseBody{closed: make(chan struct{})}
		started := time.Now()
		_, err := readUpstreamErrorBodyWithLimits(body, 1024, 20*time.Millisecond)
		if !errors.Is(err, errUpstreamErrorBodyTimeout) {
			t.Fatalf("expected timeout error, got %v", err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("timeout did not unblock read promptly: %v", elapsed)
		}
		if calls := body.closeCalls.Load(); calls != 1 {
			t.Fatalf("body close called %d times", calls)
		}
	})
}

func TestReadAndRestoreMITMRequestBodyWithLimit(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://example.com", strings.NewReader("123456"))
	body, err := readAndRestoreMITMRequestBodyWithLimit(req, 5)
	if !errors.Is(err, errMITMRequestBodyTooLarge) {
		t.Fatalf("expected request body size error, got %v", err)
	}
	if string(body) != "123456" {
		t.Fatalf("unexpected captured body: %q", body)
	}
	restored, readErr := io.ReadAll(req.Body)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(restored) != "123456" {
		t.Fatalf("request body was not restored: %q", restored)
	}
}

func TestIsKiroConversationBody(t *testing.T) {
	body := []byte(`{"conversationState":{"currentMessage":{"userInputMessage":{"content":"hello"}}}}`)
	if !isKiroConversationBody(body) {
		t.Fatal("valid Kiro runtime request was not detected")
	}
	if isKiroConversationBody([]byte(`{"conversationState":{}}`)) {
		t.Fatal("incomplete runtime request was detected as a conversation")
	}
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

func TestBuildKiroExceptionStreamResponseUsesFailureEvent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://q.us-east-1.amazonaws.com/generateAssistantResponse", nil)
	resp := buildKiroExceptionStreamResponse(req, "upstream_forward_failed", "upstream unavailable")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read exception stream: %v", err)
	}
	if !bytes.Contains(body, []byte("exceptionEvent")) || !bytes.Contains(body, []byte("upstream_forward_failed")) {
		t.Fatalf("exception event missing: %q", body)
	}
	if bytes.Contains(body, []byte("assistantResponseEvent")) || bytes.Contains(body, []byte("metadataEvent")) {
		t.Fatalf("failure stream contains normal completion frames: %q", body)
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

func TestConversationImageStateTracksProjectableAndPendingHistory(t *testing.T) {
	conv := &converter.ParsedConversation{
		History: []converter.ParsedHistoryEntry{
			{Images: []converter.KiroImage{{Source: converter.KiroImageSource{Bytes: "YQ=="}}}},
			{HasAssistant: true, ToolUses: []converter.ParsedToolUse{{ToolUseID: "call-1", Name: "read_file"}}},
			{ToolResults: []converter.ParsedToolResult{{ToolUseID: "call-1", Content: "result"}}},
			{HasAssistant: true, AssistantContent: "first image handled"},
			{Images: []converter.KiroImage{{Source: converter.KiroImageSource{Bytes: "YmM="}}}},
			{HasAssistant: true, ToolUses: []converter.ParsedToolUse{{ToolUseID: "call-2", Name: "inspect"}}},
		},
	}

	state := analyzeConversationImages(conv)
	if state.projectableHistoryCount != 1 || state.projectableHistoryBytes != 1 {
		t.Fatalf("projectable history = (%d, %d), want (1, 1)", state.projectableHistoryCount, state.projectableHistoryBytes)
	}
	if state.pendingHistoryCount != 1 || state.pendingHistoryBytes != 2 {
		t.Fatalf("pending history = (%d, %d), want (1, 2)", state.pendingHistoryCount, state.pendingHistoryBytes)
	}
	if !state.requiresVision() {
		t.Fatal("unfinished image tool chain must still require vision")
	}
}

func TestConversationImageStateProjectsSupersededImages(t *testing.T) {
	tests := map[string]*converter.ParsedConversation{
		"later history turn": {
			History: []converter.ParsedHistoryEntry{
				{Images: []converter.KiroImage{{Source: converter.KiroImageSource{Bytes: "YQ=="}}}},
				{UserContent: "unrelated text question"},
				{HasAssistant: true, AssistantContent: "unrelated text answer"},
			},
		},
		"mixed history turn": {
			History: []converter.ParsedHistoryEntry{
				{Images: []converter.KiroImage{{Source: converter.KiroImageSource{Bytes: "YQ=="}}}},
				{
					UserContent: "new question with a tool result",
					ToolResults: []converter.ParsedToolResult{{ToolUseID: "call-1", Content: "result"}},
				},
			},
		},
		"current user turn": {
			History: []converter.ParsedHistoryEntry{
				{Images: []converter.KiroImage{{Source: converter.KiroImageSource{Bytes: "YQ=="}}}},
			},
			CurrentContent:     "unrelated current question",
			CurrentToolResults: []converter.ParsedToolResult{{ToolUseID: "call-2", Content: "result"}},
		},
	}

	for name, conv := range tests {
		t.Run(name, func(t *testing.T) {
			state := analyzeConversationImages(conv)
			if state.projectableHistoryCount != 1 || state.pendingHistoryCount != 0 {
				t.Fatalf("superseded image state = %+v, want one projectable historical image", state)
			}
			if state.requiresVision() {
				t.Fatal("a superseded image must not create a permanent vision requirement")
			}
		})
	}
}

func TestCandidateConversationForTextModelProjectsResolvedHistoryWithoutMutation(t *testing.T) {
	const imageBytes = "aGlzdG9yeS1pbWFnZQ=="
	conv := &converter.ParsedConversation{
		ConversationID: "projection-conversation",
		History: []converter.ParsedHistoryEntry{
			{Images: []converter.KiroImage{{Format: "png", Source: converter.KiroImageSource{Bytes: imageBytes}}}},
			{HasAssistant: true, AssistantContent: "the image contains settings"},
		},
		CurrentContent: "continue with text",
	}
	pre := &Preflight{Protocol: "kiro", Conv: conv}

	state := analyzeConversationImages(conv)
	view := candidateConversationForModel(pre, state, modelImageUnsupported)
	if view.historyView != "text_projection" || view.projectedImageCount != 1 || view.imageCount != 0 {
		t.Fatalf("projection view = %+v", view)
	}
	if view.pre == pre || view.pre.Conv == conv {
		t.Fatal("text projection must use candidate-specific copies")
	}
	if len(view.pre.Conv.History[0].Images) != 0 {
		t.Fatal("projected history still contains the image")
	}
	if view.pre.Conv.History[0].UserContent != historicalImageOmissionMarker {
		t.Fatalf("image-only history marker = %q", view.pre.Conv.History[0].UserContent)
	}
	if len(conv.History[0].Images) != 1 || conv.History[0].UserContent != "" {
		t.Fatal("candidate projection mutated the original conversation")
	}

	fullView := candidateConversationForModel(pre, state, modelImageSupported)
	if fullView.pre != pre || fullView.historyView != "full" || fullView.imageCount != 1 {
		t.Fatalf("vision model did not retain the full history: %+v", fullView)
	}
}

func TestPendingHistoryImageIsNotProjectedForTextModel(t *testing.T) {
	conv := &converter.ParsedConversation{
		History: []converter.ParsedHistoryEntry{
			{Images: []converter.KiroImage{{Format: "png", Source: converter.KiroImageSource{Bytes: "YQ=="}}}},
			{HasAssistant: true, ToolUses: []converter.ParsedToolUse{{ToolUseID: "call-1", Name: "inspect"}}},
		},
		CurrentToolResults: []converter.ParsedToolResult{{ToolUseID: "call-1", Content: "result"}},
	}
	pre := &Preflight{Protocol: "kiro", Conv: conv}

	state := analyzeConversationImages(conv)
	view := candidateConversationForModel(pre, state, modelImageUnsupported)
	if !state.requiresVision() || view.pre != pre || view.historyView != "full" || view.imageCount != 1 {
		t.Fatalf("pending image was incorrectly projected: state=%+v view=%+v", state, view)
	}
}

func TestObservedUnsupportedAutoModelUsesTextProjection(t *testing.T) {
	conv := &converter.ParsedConversation{
		History: []converter.ParsedHistoryEntry{
			{Images: []converter.KiroImage{{Format: "png", Source: converter.KiroImageSource{Bytes: "YQ=="}}}},
			{HasAssistant: true, AssistantContent: "image answer"},
		},
		CurrentContent: "continue with text",
	}
	pre := &Preflight{Protocol: "kiro", Conv: conv}
	model := &store.Model{ID: "auto-text-model", ImageInputMode: store.ImageInputAuto}
	capabilities := newModelImageCapabilityStore()
	now := time.Now()
	capabilities.observe(model, modelImageUnsupported, now)
	capability, source := capabilities.effective(model, now)

	view := candidateConversationForModel(pre, analyzeConversationImages(conv), capability)
	if source != "observed" || view.historyView != "text_projection" || view.imageCount != 0 {
		t.Fatalf("observed unsupported auto view = %+v source=%s", view, source)
	}
}

func TestTextProjectionFeedsAllKiroConvertersWithoutImages(t *testing.T) {
	const imageBytes = "aGlzdG9yeS1pbWFnZQ=="
	conv := &converter.ParsedConversation{
		ConversationID: "all-providers",
		SystemText:     "You are a test assistant.",
		History: []converter.ParsedHistoryEntry{
			{UserContent: "inspect this", Images: []converter.KiroImage{{Format: "jpeg", Source: converter.KiroImageSource{Bytes: imageBytes}}}},
			{HasAssistant: true, AssistantContent: "the image shows a form"},
		},
		CurrentContent: "what should I enter?",
	}
	pre := &Preflight{Protocol: "kiro", Conv: conv}
	state := analyzeConversationImages(conv)
	models := []store.Model{
		{ID: "chat", ModelID: "chat-model", Provider: "openai", BaseURL: "https://example.test", Endpoint: "/chat/completions", ImageInputMode: store.ImageInputUnsupported},
		{ID: "responses", ModelID: "responses-model", Provider: "openai", BaseURL: "https://example.test", Endpoint: "/responses", ImageInputMode: store.ImageInputUnsupported},
		{ID: "anthropic", ModelID: "anthropic-model", Provider: "anthropic", BaseURL: "https://example.test", Endpoint: "/v1/messages", ImageInputMode: store.ImageInputUnsupported},
		{ID: "gemini", ModelID: "gemini-model", Provider: "gemini", BaseURL: "https://example.test", Endpoint: "/v1beta/models/{model}:streamGenerateContent", ImageInputMode: store.ImageInputUnsupported},
	}

	for index := range models {
		model := &models[index]
		t.Run(model.ID, func(t *testing.T) {
			view := candidateConversationForModel(pre, state, modelImageUnsupported)
			body, _, err := convertRequest(view.pre, model)
			if err != nil {
				t.Fatalf("convert projected request: %v", err)
			}
			if bytes.Contains(body, []byte(imageBytes)) {
				t.Fatal("text-only provider request still contains historical image bytes")
			}
			if !bytes.Contains(body, []byte(historicalImageOmissionMarker)) {
				t.Fatalf("provider request lost the deterministic image marker: %s", body)
			}
		})
	}
}

func TestResponsesContinuationRemainsAppendOnlyWithTextProjection(t *testing.T) {
	const imageBytes = "aGlzdG9yeS1pbWFnZQ=="
	model := &store.Model{
		ID: "responses-text", ModelID: "responses-model", Provider: "openai",
		BaseURL: "https://example.test", Endpoint: "/responses", ImageInputMode: store.ImageInputUnsupported,
	}
	firstConversation := &converter.ParsedConversation{
		ConversationID: "projection-continuation",
		History: []converter.ParsedHistoryEntry{
			{Images: []converter.KiroImage{{Format: "png", Source: converter.KiroImageSource{Bytes: imageBytes}}}},
			{HasAssistant: true, AssistantContent: "image answer"},
		},
		CurrentContent: "first follow-up",
	}
	firstPre := &Preflight{Protocol: "kiro", Conv: firstConversation}
	firstView := candidateConversationForModel(firstPre, analyzeConversationImages(firstConversation), modelImageUnsupported)
	firstBody, _, err := convertRequest(firstView.pre, model)
	if err != nil {
		t.Fatalf("convert first request: %v", err)
	}
	firstResult, err := converter.ApplyResponsesContinuation(firstBody, nil)
	if err != nil {
		t.Fatalf("capture first continuation state: %v", err)
	}

	secondConversation := &converter.ParsedConversation{
		ConversationID: "projection-continuation",
		History: []converter.ParsedHistoryEntry{
			{Images: []converter.KiroImage{{Format: "png", Source: converter.KiroImageSource{Bytes: imageBytes}}}},
			{HasAssistant: true, AssistantContent: "image answer"},
			{UserContent: "first follow-up"},
			{HasAssistant: true, AssistantContent: "first follow-up answer"},
		},
		CurrentContent: "second follow-up",
	}
	secondPre := &Preflight{Protocol: "kiro", Conv: secondConversation}
	secondView := candidateConversationForModel(secondPre, analyzeConversationImages(secondConversation), modelImageUnsupported)
	secondBody, _, err := convertRequest(secondView.pre, model)
	if err != nil {
		t.Fatalf("convert second request: %v", err)
	}
	secondResult, err := converter.ApplyResponsesContinuation(secondBody, &converter.ResponsesContinuationState{
		ResponseID: "resp_previous",
		InputJSON:  firstResult.FullInputJSON,
		ItemCount:  firstResult.InputItemCount,
	})
	if err != nil {
		t.Fatalf("apply projected continuation: %v", err)
	}
	if !secondResult.UsedContinuation {
		t.Fatal("stable text projection should preserve the Responses append-only prefix")
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

func TestModelGroupRetryBudgetScalesWithConfiguredFirstByteTimeout(t *testing.T) {
	group := &store.ModelGroup{ID: "group", FirstByteTimeoutMS: 180000, MaxAttempts: 2}
	route := &router.Route{
		Group: group,
		Stages: []router.RouteStage{{
			GroupID:    group.ID,
			Candidates: []*store.Model{{ID: "one"}, {ID: "two"}},
		}},
	}
	if got, want := modelGroupRetryBudget(route), 10*time.Minute; got != want {
		t.Fatalf("retry budget = %v, want %v", got, want)
	}
	if singleModelFirstByteTimeout <= time.Minute {
		t.Fatalf("single model first-byte timeout = %v, want more than one minute", singleModelFirstByteTimeout)
	}
}

func TestRequestWithProtocolFallbackRetriesHTTP2PrecommitFailure(t *testing.T) {
	var attempts atomic.Int32
	send := func(ctx context.Context) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       &fixedErrorBody{err: errors.New("http2: response body closed")},
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("event: response.created\ndata: {\"type\":\"response.created\"}\n\n")),
		}, nil
	}

	resp, _, err := requestWithProtocolFallback(context.Background(), time.Second, send, func(ctx context.Context, resp *http.Response, timeout time.Duration) error {
		return probeSSEPrecommit(ctx, resp, "openai", "/responses", timeout, 0)
	})
	if err != nil {
		t.Fatalf("protocol fallback failed: %v", err)
	}
	defer resp.Body.Close()
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
}

func TestStreamIdleTimeoutClosesStalledBody(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   reader,
	}
	wrapUpstreamReadIdleTimeout(resp, 20*time.Millisecond)

	result := make(chan error, 1)
	go func() {
		_, err := resp.Body.Read(make([]byte, 1))
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, errUpstreamReadIdleTimeout) {
			t.Fatalf("read error = %v, want stream idle timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled stream was not closed")
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

func TestForwardSingleModelPreservesTransientStatusRetry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "temporary upstream failure", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()

	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	defer logger.Close()
	model := &store.Model{
		ID:       "single-model",
		ModelID:  "upstream-model",
		Provider: "openai",
		BaseURL:  server.URL,
		Endpoint: "/responses",
		APIKey:   "test",
		Enabled:  true,
	}
	forwarder := NewForwarder(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "http://proxy.test/responses", nil)
	resp, err := forwarder.Forward(req, &Preflight{
		Body:     []byte(`{"model":"requested","stream":true}`),
		Protocol: "responses",
	}, &router.Route{Model: model, Candidates: []*store.Model{model}}, logger)
	if err != nil {
		t.Fatalf("single-model forward failed: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read forwarded response: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("request count = %d, want 2", requests.Load())
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

func TestSendWithRetryCommitsAfterResponsesMetadata(t *testing.T) {
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
	if requests.Load() != 1 {
		t.Fatalf("request count = %d, want 1", requests.Load())
	}
	if !strings.Contains(string(stream), "resp_started") || !strings.Contains(string(stream), "late failure") || strings.Contains(string(stream), "resp_ok") {
		t.Fatalf("probe did not commit the first live stream: %s", stream)
	}
}

func TestResponsesOrCodexRequestUsesActualUpstreamURL(t *testing.T) {
	model := &store.Model{Provider: "openai", Endpoint: "/responses"}
	body := []byte(`{"stream":true}`)
	if responsesOrCodexRequest(model, body, "https://upstream.example/v1/chat/completions") {
		t.Fatal("forced Chat request must not use Responses retry policy")
	}
	if !responsesOrCodexRequest(model, body, "https://upstream.example/v1/responses") {
		t.Fatal("Responses URL must use Responses retry policy")
	}
	codexModel := &store.Model{Provider: "codex", Endpoint: "/chat/completions"}
	if !responsesOrCodexRequest(codexModel, body, "https://upstream.example/v1/chat/completions") {
		t.Fatal("Codex must retain Responses-like retry policy")
	}
}

func TestRequestWithProtocolFallbackDoesNotReplayWrittenResponsesRequest(t *testing.T) {
	var attempts atomic.Int32
	model := &store.Model{Provider: "openai", Endpoint: "/responses"}
	body := []byte(`{"stream":true}`)
	send := func(ctx context.Context) (*http.Response, error) {
		attempts.Add(1)
		operation := upstreamRequestOperationFromContext(ctx)
		attempt := &upstreamAttemptState{}
		attempt.mark(upstreamSendWritten)
		operation.recordAttempt(model, body, "https://upstream.example/v1/responses", attempt)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       &fixedErrorBody{err: errors.New("http2: response body closed")},
		}, nil
	}

	_, _, err := requestWithProtocolFallback(context.Background(), time.Second, send, func(ctx context.Context, resp *http.Response, timeout time.Duration) error {
		return probeSSEPrecommit(ctx, resp, "openai", "/responses", timeout, 0)
	})
	if err == nil {
		t.Fatal("expected written Responses request to fail without replay")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}
