package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type rawResponsesScriptBody struct {
	mu             sync.Mutex
	data           []byte
	err            error
	errorReturned  bool
	blockAfterData bool
	closed         chan struct{}
	closeOnce      sync.Once
	closeCalls     atomic.Int32
}

type rawResponsesCloseFirstBody struct {
	data       []byte
	readOnce   sync.Once
	closeOnce  sync.Once
	readBlock  chan struct{}
	readResume chan struct{}
	closeCalls atomic.Int32
}

func (b *rawResponsesCloseFirstBody) Read(p []byte) (int, error) {
	read := false
	b.readOnce.Do(func() {
		read = true
	})
	if read {
		return copy(p, b.data), nil
	}
	b.closeOnce.Do(func() { close(b.readBlock) })
	<-b.readResume
	return 0, io.ErrClosedPipe
}

func (b *rawResponsesCloseFirstBody) Close() error {
	b.closeCalls.Add(1)
	return nil
}

func newRawResponsesScriptBody(data []byte, readErr error, blockAfterData bool) *rawResponsesScriptBody {
	return &rawResponsesScriptBody{data: append([]byte(nil), data...), err: readErr, blockAfterData: blockAfterData, closed: make(chan struct{})}
}

func (b *rawResponsesScriptBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if len(b.data) > 0 {
		n := copy(p, b.data)
		b.data = b.data[n:]
		b.mu.Unlock()
		return n, nil
	}
	if b.err != nil && !b.errorReturned {
		b.errorReturned = true
		err := b.err
		b.mu.Unlock()
		return 0, err
	}
	block := b.blockAfterData
	b.mu.Unlock()
	if block {
		<-b.closed
		return 0, io.ErrClosedPipe
	}
	return 0, io.EOF
}

func (b *rawResponsesScriptBody) Close() error {
	b.closeCalls.Add(1)
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func rawResponsesTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func rawResponsesTestForwarder(t *testing.T, db *store.DB) (*Forwarder, *store.Stats) {
	t.Helper()
	stats := store.NewStats(db)
	t.Cleanup(stats.Close)
	return NewForwarder(stats, db), stats
}

func rawResponsesTestModel(serverURL, provider, endpoint string) *store.Model {
	return &store.Model{
		ID: "selected-model", Name: "Selected", ModelID: "gpt-selected", Provider: provider,
		BaseURL: serverURL + "/v1", Endpoint: endpoint, APIKey: "test-secret", ReasoningEffort: "low",
		InputPrice: 1, OutputPrice: 2, CacheInputPrice: 0.5, CacheOutputPrice: 0.25,
	}
}

func rawResponsesTestRoute(model *store.Model) *router.Route {
	return &router.Route{Model: model, AutoMode: store.AutoRoutingModeStageFixed, CandidateLimits: map[string]int{model.ID: 1}}
}

func rawResponsesTestPre(db *store.DB, model *store.Model, requestPath, body string) *Preflight {
	pre := &Preflight{Body: []byte(body), Protocol: "openai", StartTime: time.Now()}
	pre.usage = newRequestUsageRecorder(db, pre.StartTime, model, requestPath, pre.Protocol)
	return pre
}

func rawResponsesUsageItems(t *testing.T, db *store.DB) []store.RequestUsage {
	t.Helper()
	page, err := db.QueryRequestUsagePage(store.RequestUsageQuery{PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func TestForwardRawResponsesPreservesRawSSEAndObservesUsage(t *testing.T) {
	const requestBody = `{"model":"auto","input":[{"type":"function_call_output","call_id":"c1","output":"done"}],"tools":[{"type":"function","name":"first","parameters":{"type":"object","default":9007199254740993}},{"type":"function","name":"second","parameters":{"type":"object"}}],"reasoning":{"effort":"","summary":"auto"},"prompt_cache_key":"keep-this-key","unknown_extension":{"number":9007199254740993,"primitive":true},"metadata":{"kiro:conversation_id":"conversation-1","kiro:agent_mode":"spec","kiro:proxy_continuation_id":"private-id","kiro:proxy_current_tool_results":1}}`
	const responseBody = "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call-1\",\"name\":\"run\",\"arguments\":\"{\\\"x\\\":1}\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":17,\"output_tokens\":9,\"input_tokens_details\":{\"cached_tokens\":4},\"cache_creation_input_tokens\":2}}}\n\n"

	var outboundBody []byte
	var outboundAuthorization string
	var outboundStage string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outboundBody, _ = io.ReadAll(r.Body)
		outboundAuthorization = r.Header.Get("Authorization")
		outboundStage = r.Header.Get(converter.WorkflowStageHeader)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Upstream", "kept")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, responseBody)
	}))
	defer server.Close()

	db := rawResponsesTestDB(t)
	model := rawResponsesTestModel(server.URL, "openai", "/responses")
	f, stats := rawResponsesTestForwarder(t, db)
	req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(requestBody))
	req.Header.Set(converter.WorkflowStageHeader, "implement")
	pre := rawResponsesTestPre(db, model, req.URL.Path, requestBody)
	pre.Conv = &converter.ParsedConversation{ConversationID: "conversation-1", ReasoningEffort: "high"}
	resp, err := f.ForwardRawResponses(req, pre, rawResponsesTestRoute(model), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Upstream") != "kept" {
		t.Fatalf("upstream response metadata changed: status=%d headers=%v", resp.StatusCode, resp.Header)
	}
	if got := f.load.snapshot()[model.ID]; got != 1 {
		t.Fatalf("lease should remain held while response body is open, active=%d", got)
	}
	gotResponse, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotResponse, []byte(responseBody)) {
		t.Fatalf("SSE response bytes changed\n got: %q\nwant: %q", gotResponse, responseBody)
	}
	if outboundAuthorization != "Bearer test-secret" {
		t.Fatalf("upstream auth missing or changed: %q", outboundAuthorization)
	}
	if outboundStage != "" {
		t.Fatalf("private workflow header reached upstream: %q", outboundStage)
	}
	for _, forbidden := range []string{"metadata", "kiro:conversation_id", "kiro:agent_mode", "kiro:proxy_continuation_id", "kiro:proxy_current_tool_results", "private-id"} {
		if bytes.Contains(outboundBody, []byte(forbidden)) {
			t.Fatalf("private metadata leaked upstream (%s): %s", forbidden, outboundBody)
		}
	}
	for _, preserved := range []string{"9007199254740993", "keep-this-key", "first", "second", `"effort":"high"`, `"summary":"auto"`} {
		if !bytes.Contains(outboundBody, []byte(preserved)) {
			t.Fatalf("request field %q was not preserved or applied: %s", preserved, outboundBody)
		}
	}
	if got := f.load.snapshot()[model.ID]; got != 0 {
		t.Fatalf("lease not released after EOF: active=%d", got)
	}
	items := rawResponsesUsageItems(t, db)
	if len(items) != 1 || !items[0].Success || items[0].ModelID != statsModelID(model) || items[0].SelectedModelConfigID != model.ID || items[0].InputTokens != 17 || items[0].OutputTokens != 9 || items[0].CacheReadTokens != 4 || items[0].CacheWriteTokens != 2 {
		t.Fatalf("unexpected usage record: %+v", items)
	}
	snapshot := stats.Snapshot()
	wantCredits := store.CalculateCredits(17, 9, 4, 2, model)
	if snapshot.TotalRequests != 1 || snapshot.TotalErrors != 0 || snapshot.InputTokens != 17 || snapshot.OutputTokens != 9 || snapshot.CacheRead != 4 || snapshot.CacheWrite != 2 || snapshot.Credits != wantCredits {
		t.Fatalf("raw response stats mismatch: %+v want credits=%v", snapshot, wantCredits)
	}
}

func TestForwardRawResponsesReturnsFailedAndMissingTerminalSSEWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "response.failed",
			body: "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"usage\":{\"input_tokens\":4}},\"error\":{\"message\":\"upstream failed\"}}\n\n",
		},
		{
			name: "missing terminal",
			body: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			db := rawResponsesTestDB(t)
			model := rawResponsesTestModel(server.URL, "openai", "/responses")
			f, stats := rawResponsesTestForwarder(t, db)
			requestBody := `{"model":"auto","input":"test"}`
			req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(requestBody))
			pre := rawResponsesTestPre(db, model, req.URL.Path, requestBody)
			resp, err := f.ForwardRawResponses(req, pre, rawResponsesTestRoute(model), nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || !bytes.Equal(got, []byte(tc.body)) {
				t.Fatalf("raw SSE response changed: status=%d body=%q", resp.StatusCode, got)
			}
			if calls.Load() != 1 {
				t.Fatalf("raw SSE response was replayed %d times", calls.Load()-1)
			}
			items := rawResponsesUsageItems(t, db)
			if len(items) != 1 || items[0].Success || items[0].ErrorMessage == "" {
				t.Fatalf("failed stream was not recorded once: %+v", items)
			}
			if snapshot := stats.Snapshot(); snapshot.TotalRequests != 1 || snapshot.TotalErrors != 1 {
				t.Fatalf("failed stream stats should be recorded once: %+v", snapshot)
			}
		})
	}
}

func TestForwardRawResponsesPreservesProvidedEffortAndRejectsConversationReferences(t *testing.T) {
	db := rawResponsesTestDB(t)
	model := rawResponsesTestModel("https://unused.example", "openai", "/responses")
	pre := &Preflight{Protocol: "openai", Conv: &converter.ParsedConversation{ReasoningEffort: "high"}}
	prepared, err := prepareRawResponsesBody([]byte(`{"model":"auto","reasoning":{"effort":"medium","summary":"detailed"}}`), pre, model)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(prepared, []byte(`"effort":"medium"`)) || !bytes.Contains(prepared, []byte(`"summary":"detailed"`)) {
		t.Fatalf("provided effort or other reasoning fields changed: %s", prepared)
	}
	for _, body := range []string{
		`{"model":"auto","previous_response_id":"resp-1"}`,
		`{"model":"auto","conversation":{"id":"conv-1"}}`,
	} {
		if _, err := prepareRawResponsesBody([]byte(body), pre, model); err == nil {
			t.Fatalf("expected unsupported conversation reference to fail: %s", body)
		}
	}
	nullReasoning, err := prepareRawResponsesBody([]byte(`{"model":"auto","reasoning":null}`), pre, model)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(nullReasoning, []byte(`"reasoning":{"effort":"high"}`)) {
		t.Fatalf("reasoning:null should use the resolved effort default: %s", nullReasoning)
	}
	_ = db
}

func TestRawResponsesErrorsDoNotExposeEndpointCredentials(t *testing.T) {
	upstreamURL := "https://alice:password@provider.example/v1/responses?api_key=secret"
	err := redactRawResponsesEndpointError(fmt.Errorf("Post %q: connection refused", upstreamURL), upstreamURL)
	for _, secret := range []string{"alice", "password", "api_key", "secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked endpoint credentials (%s): %v", secret, err)
		}
	}
}

func TestForwardRawResponsesPreservesNon2xxResponseAndFinishesOnce(t *testing.T) {
	const responseBody = `{"error":{"message":"original upstream error"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", "failure")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, responseBody)
	}))
	defer server.Close()
	db := rawResponsesTestDB(t)
	model := rawResponsesTestModel(server.URL, "openai", "/responses")
	f, stats := rawResponsesTestForwarder(t, db)
	body := `{"model":"auto","input":"test"}`
	req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(body))
	pre := rawResponsesTestPre(db, model, req.URL.Path, body)
	resp, err := f.ForwardRawResponses(req, pre, rawResponsesTestRoute(model), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("X-Upstream") != "failure" || !bytes.Equal(got, []byte(responseBody)) {
		t.Fatalf("non-2xx response changed: status=%d header=%q body=%s", resp.StatusCode, resp.Header.Get("X-Upstream"), got)
	}
	if len(rawResponsesUsageItems(t, db)) != 1 {
		t.Fatalf("expected exactly one usage record, got %d", len(rawResponsesUsageItems(t, db)))
	}
	if got := f.load.snapshot()[model.ID]; got != 0 {
		t.Fatalf("lease not released: active=%d", got)
	}
	if snapshot := stats.Snapshot(); snapshot.TotalRequests != 1 || snapshot.TotalErrors != 1 {
		t.Fatalf("non-2xx stats should be recorded once as an error: %+v", snapshot)
	}
}

func TestForwardRawResponsesReadErrorAndEarlyCloseReleaseLease(t *testing.T) {
	t.Run("read error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("httptest response writer does not support hijacking")
			}
			conn, rw, err := hijacker.Hijack()
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(rw, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"partial\":true}")
			_ = rw.Flush()
			_ = conn.Close()
		}))
		defer server.Close()
		db := rawResponsesTestDB(t)
		model := rawResponsesTestModel(server.URL, "openai", "/responses")
		f, stats := rawResponsesTestForwarder(t, db)
		body := `{"model":"auto","input":"test"}`
		req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(body))
		pre := rawResponsesTestPre(db, model, req.URL.Path, body)
		resp, err := f.ForwardRawResponses(req, pre, rawResponsesTestRoute(model), nil)
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr == nil || !bytes.Equal(got, []byte(`{"partial":true}`)) {
			t.Fatalf("expected preserved partial bytes and read error, body=%q err=%v", got, readErr)
		}
		if got := f.load.snapshot()[model.ID]; got != 0 {
			t.Fatalf("lease not released after read error: active=%d", got)
		}
		items := rawResponsesUsageItems(t, db)
		if len(items) != 1 || items[0].Success || items[0].ErrorMessage == "" {
			t.Fatalf("read error usage not recorded once as failure: %+v", items)
		}
		if snapshot := stats.Snapshot(); snapshot.TotalRequests != 1 || snapshot.TotalErrors != 1 {
			t.Fatalf("read error stats should be recorded once: %+v", snapshot)
		}
	})

	t.Run("early close", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"usage":{"input_tokens":5,"output_tokens":2},"data":"long body"}`)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		}))
		defer server.Close()
		db := rawResponsesTestDB(t)
		model := rawResponsesTestModel(server.URL, "openai", "/responses")
		f, stats := rawResponsesTestForwarder(t, db)
		body := `{"model":"auto","input":"test"}`
		req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(body))
		pre := rawResponsesTestPre(db, model, req.URL.Path, body)
		resp, err := f.ForwardRawResponses(req, pre, rawResponsesTestRoute(model), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if got := f.load.snapshot()[model.ID]; got != 0 {
			t.Fatalf("lease not released on early close: active=%d", got)
		}
		items := rawResponsesUsageItems(t, db)
		if len(items) != 1 || items[0].Success || items[0].ErrorMessage != context.Canceled.Error() {
			t.Fatalf("early close usage not recorded once as client cancellation: %+v", items)
		}
		if snapshot := stats.Snapshot(); snapshot.TotalRequests != 1 || snapshot.TotalErrors != 0 {
			t.Fatalf("early close should not count as an upstream error: %+v", snapshot)
		}
	})
}

func TestForwardRawResponsesRejectsUnsupportedRoutesWithoutSending(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		endpoint string
	}{
		{name: "anthropic", provider: "anthropic", endpoint: "/v1/responses"},
		{name: "gemini", provider: "gemini", endpoint: "/v1/responses"},
		{name: "codex", provider: "codex", endpoint: "/v1/responses"},
		{name: "wrong endpoint", provider: "openai", endpoint: "/messages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := rawResponsesTestModel("https://should-not-be-contacted.invalid", tc.provider, tc.endpoint)
			body := `{"model":"auto","input":"test"}`
			req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(body))
			resp, err := NewForwarder(nil, nil).ForwardRawResponses(req, &Preflight{Body: []byte(body), Protocol: "openai"}, rawResponsesTestRoute(model), nil)
			if err == nil || resp != nil {
				t.Fatalf("unsupported route was accepted: resp=%v err=%v", resp, err)
			}
		})
	}
}

func TestForwardRawResponsesRequestCanceledReleasesLease(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"input_tokens":3}}`)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	db := rawResponsesTestDB(t)
	model := rawResponsesTestModel(server.URL, "openai", "/responses")
	f, stats := rawResponsesTestForwarder(t, db)
	body := `{"model":"auto","input":"test"}`
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(body)).WithContext(ctx)
	pre := rawResponsesTestPre(db, model, req.URL.Path, body)
	resp, err := f.ForwardRawResponses(req, pre, rawResponsesTestRoute(model), nil)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	readResult := make(chan error, 1)
	readReturnedData := make(chan struct{})
	go func() {
		buffer := make([]byte, 8)
		_, readErr := resp.Body.Read(buffer)
		close(readReturnedData)
		if readErr == nil {
			_, readErr = io.Copy(io.Discard, resp.Body)
		}
		readResult <- readErr
	}()
	<-readReturnedData
	cancel()
	select {
	case <-readResult:
	case <-time.After(time.Second):
		t.Fatal("response read did not stop after request cancellation")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && f.load.snapshot()[model.ID] != 0 {
		time.Sleep(time.Millisecond)
	}
	_ = resp.Body.Close()
	if got := f.load.snapshot()[model.ID]; got != 0 {
		t.Fatalf("lease not released after request cancellation: active=%d", got)
	}
	if got := len(rawResponsesUsageItems(t, db)); got != 1 {
		t.Fatalf("request cancellation should finish exactly one usage record, got %d", got)
	}
	items := rawResponsesUsageItems(t, db)
	if items[0].Success || items[0].ErrorMessage != context.Canceled.Error() {
		t.Fatalf("request cancellation should remain an unsuccessful usage row with context error: %+v", items[0])
	}
	if snapshot := stats.Snapshot(); snapshot.TotalRequests != 1 || snapshot.TotalErrors != 0 {
		t.Fatalf("client cancellation should be counted once without an upstream error: %+v", snapshot)
	}
}

func TestRawResponsesUsageObserverPreservesFailedAndMissingTerminalStreams(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "response failed",
			body: "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"usage\":{\"input_tokens\":4}},\"error\":{\"message\":\"upstream failed\"}}\n\n",
		},
		{
			name: "missing terminal",
			body: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := rawResponsesTestDB(t)
			model := &store.Model{ID: "selected-model", Name: "Selected", ModelID: "gpt-selected", Provider: "openai"}
			f, stats := rawResponsesTestForwarder(t, db)
			usage := newRequestUsageRecorder(db, time.Now(), model, "/v1/responses", "openai")
			lease, ok := f.load.acquire(model.ID, 1)
			if !ok {
				t.Fatal("failed to acquire model lease")
			}
			observer := newRawResponsesUsageBody(io.NopCloser(bytes.NewReader([]byte(tc.body))), usage, http.StatusOK, true)
			observer.lease = lease
			observer.stats = stats
			observer.model = model
			got, err := io.ReadAll(observer)
			if err != nil {
				t.Fatal(err)
			}
			_ = observer.Close()
			if !bytes.Equal(got, []byte(tc.body)) {
				t.Fatalf("observer changed the SSE body: got=%q want=%q", got, tc.body)
			}
			if f.load.snapshot()[model.ID] != 0 {
				t.Fatal("observer did not release lease")
			}
			items := rawResponsesUsageItems(t, db)
			if len(items) != 1 || items[0].Success || items[0].ErrorMessage == "" {
				t.Fatalf("stream failure was not recorded exactly once: %+v", items)
			}
			if snapshot := stats.Snapshot(); snapshot.TotalRequests != 1 || snapshot.TotalErrors != 1 {
				t.Fatalf("stream failure stats should be recorded once: %+v", snapshot)
			}
		})
	}
}

func rawResponsesObserverForFinishTest(t *testing.T, db *store.DB, stats *store.Stats, model *store.Model, ctx context.Context, data []byte, readErr error, blockAfterData bool) (*rawResponsesUsageBody, *rawResponsesScriptBody, *Forwarder) {
	t.Helper()
	f := NewForwarder(stats, db)
	usage := newRequestUsageRecorder(db, time.Now(), model, "/v1/responses", "openai")
	underlying := newRawResponsesScriptBody(data, readErr, blockAfterData)
	response := &http.Response{Body: underlying}
	usage.wrapResponse(response)
	lease, ok := f.load.acquire(model.ID, 1)
	if !ok {
		t.Fatal("failed to acquire model lease")
	}
	observed := newRawResponsesUsageBody(response.Body, usage, http.StatusOK, true)
	observed.lease = lease
	observed.stats = stats
	observed.model = model
	observed.ctx = ctx
	observed.watchContext(ctx)
	return observed, underlying, f
}

const rawResponsesUsageEvent = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":11,\"output_tokens\":7,\"input_tokens_details\":{\"cached_tokens\":3},\"cache_creation_input_tokens\":2}}}\n\n"

func assertRawResponsesFinishAccounting(t *testing.T, db *store.DB, stats *store.Stats, f *Forwarder, model *store.Model, wantSuccess bool, wantStatsError bool, wantErrorMessage string) {
	t.Helper()
	items := rawResponsesUsageItems(t, db)
	if len(items) != 1 {
		t.Fatalf("expected one request usage row, got %+v", items)
	}
	item := items[0]
	if item.Success != wantSuccess || item.ErrorMessage != wantErrorMessage {
		t.Fatalf("unexpected request usage outcome: success=%v error=%q", item.Success, item.ErrorMessage)
	}
	if item.InputTokens != 11 || item.OutputTokens != 7 || item.CacheReadTokens != 3 || item.CacheWriteTokens != 2 {
		t.Fatalf("request usage tokens were not retained: %+v", item)
	}
	wantCredits := store.CalculateCredits(11, 7, 3, 2, model)
	if item.Credits != wantCredits {
		t.Fatalf("request usage credits=%v want=%v", item.Credits, wantCredits)
	}
	snapshot := stats.Snapshot()
	wantErrors := int64(0)
	if wantStatsError {
		wantErrors = 1
	}
	if snapshot.TotalRequests != 1 || snapshot.TotalErrors != wantErrors || snapshot.InputTokens != 11 || snapshot.OutputTokens != 7 || snapshot.CacheRead != 3 || snapshot.CacheWrite != 2 || snapshot.Credits != wantCredits {
		t.Fatalf("stats were not recorded exactly once: %+v want errors=%d credits=%v", snapshot, wantErrors, wantCredits)
	}
	if active := f.load.snapshot()[model.ID]; active != 0 {
		t.Fatalf("model lease was not released: active=%d", active)
	}
}

func TestRawResponsesReadErrorSettlesBeforeLaterCancelAndClose(t *testing.T) {
	db := rawResponsesTestDB(t)
	stats := store.NewStats(db)
	t.Cleanup(stats.Close)
	model := rawResponsesTestModel("", "openai", "/responses")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readErr := errors.New("mock upstream read failed")
	observed, underlying, f := rawResponsesObserverForFinishTest(t, db, stats, model, ctx, []byte(rawResponsesUsageEvent), readErr, false)
	buffer := make([]byte, len(rawResponsesUsageEvent)+16)
	if n, err := observed.Read(buffer); err != nil || n != len(rawResponsesUsageEvent) {
		t.Fatalf("first read = (%d, %v), want payload and no error", n, err)
	}
	if n, err := observed.Read(buffer); n != 0 || !errors.Is(err, readErr) {
		t.Fatalf("second read = (%d, %v), want the scripted upstream error", n, err)
	}
	// The read error has already settled usage and stats before cancellation.
	cancel()
	_ = observed.Close()
	_ = observed.Close()
	assertRawResponsesFinishAccounting(t, db, stats, f, model, false, true, readErr.Error())
	if got := underlying.closeCalls.Load(); got != 1 {
		t.Fatalf("underlying body Close calls=%d want 1", got)
	}
}

func TestRawResponsesCloseSettlesBeforeBlockedReadAndLaterCancel(t *testing.T) {
	db := rawResponsesTestDB(t)
	stats := store.NewStats(db)
	t.Cleanup(stats.Close)
	model := rawResponsesTestModel("", "openai", "/responses")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := NewForwarder(stats, db)
	usage := newRequestUsageRecorder(db, time.Now(), model, "/v1/responses", "openai")
	underlying := &rawResponsesCloseFirstBody{
		data:      []byte(strings.Replace(rawResponsesUsageEvent, "response.completed", "response.in_progress", 2)),
		readBlock: make(chan struct{}), readResume: make(chan struct{}),
	}
	response := &http.Response{Body: underlying}
	usage.wrapResponse(response)
	lease, ok := f.load.acquire(model.ID, 1)
	if !ok {
		t.Fatal("failed to acquire model lease")
	}
	observed := newRawResponsesUsageBody(response.Body, usage, http.StatusOK, true)
	observed.lease = lease
	observed.stats = stats
	observed.model = model
	observed.ctx = ctx
	observed.watchContext(ctx)
	readDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, len(underlying.data)+16)
		if _, err := observed.Read(buffer); err != nil {
			readDone <- err
			return
		}
		_, err := observed.Read(buffer)
		readDone <- err
	}()
	<-underlying.readBlock
	if err := observed.Close(); err != nil {
		t.Fatal(err)
	}
	// Close has synchronously settled the row while the next Read stays blocked.
	assertRawResponsesFinishAccounting(t, db, stats, f, model, false, false, context.Canceled.Error())
	cancel()
	close(underlying.readResume)
	if err := <-readDone; !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("blocked read returned %v, want closed-pipe after release", err)
	}
	_ = observed.Close()
	assertRawResponsesFinishAccounting(t, db, stats, f, model, false, false, context.Canceled.Error())
	if got := underlying.closeCalls.Load(); got != 1 {
		t.Fatalf("underlying body Close calls=%d want 1", got)
	}
}

func TestRawResponsesTerminalCloseBeforeEOFSettlesSuccessfully(t *testing.T) {
	db := rawResponsesTestDB(t)
	stats := store.NewStats(db)
	t.Cleanup(stats.Close)
	model := rawResponsesTestModel("", "openai", "/responses")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := NewForwarder(stats, db)
	usage := newRequestUsageRecorder(db, time.Now(), model, "/v1/responses", "openai")
	underlying := newRawResponsesScriptBody([]byte(rawResponsesUsageEvent), nil, true)
	response := &http.Response{Body: underlying}
	usage.wrapResponse(response)
	lease, ok := f.load.acquire(model.ID, 1)
	if !ok {
		t.Fatal("failed to acquire model lease")
	}
	observed := newRawResponsesUsageBody(response.Body, usage, http.StatusOK, true)
	observed.lease = lease
	observed.stats = stats
	observed.model = model
	observed.ctx = ctx
	buffer := make([]byte, len(rawResponsesUsageEvent)+16)
	if n, err := observed.Read(buffer); err != nil || n != len(rawResponsesUsageEvent) {
		t.Fatalf("terminal read = (%d, %v), want payload and no error", n, err)
	}
	if err := observed.Close(); err != nil {
		t.Fatal(err)
	}
	assertRawResponsesFinishAccounting(t, db, stats, f, model, true, false, "")
	_ = observed.Close()
	assertRawResponsesFinishAccounting(t, db, stats, f, model, true, false, "")
	if got := underlying.closeCalls.Load(); got != 1 {
		t.Fatalf("underlying body Close calls=%d want 1", got)
	}
}

func TestRawResponsesCanceledContextReadCloseFinishOrder(t *testing.T) {
	t.Run("read error finishes first", func(t *testing.T) {
		db := rawResponsesTestDB(t)
		stats := store.NewStats(db)
		t.Cleanup(stats.Close)
		model := rawResponsesTestModel("", "openai", "/responses")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f := NewForwarder(stats, db)
		usage := newRequestUsageRecorder(db, time.Now(), model, "/v1/responses", "openai")
		readErr := errors.New("mock upstream read failed")
		underlying := newRawResponsesScriptBody([]byte(rawResponsesUsageEvent), readErr, false)
		response := &http.Response{Body: underlying}
		usage.wrapResponse(response)
		lease, ok := f.load.acquire(model.ID, 1)
		if !ok {
			t.Fatal("failed to acquire model lease")
		}
		observed := newRawResponsesUsageBody(response.Body, usage, http.StatusOK, true)
		observed.lease = lease
		observed.stats = stats
		observed.model = model
		observed.ctx = ctx

		// Do not register AfterFunc: this test explicitly schedules the real
		// Read and Close methods after context cancellation to control ordering.
		cancel()
		buffer := make([]byte, len(rawResponsesUsageEvent)+16)
		if n, err := observed.Read(buffer); err != nil || n != len(rawResponsesUsageEvent) {
			t.Fatalf("first read = (%d, %v), want the usage event", n, err)
		}
		if n, err := observed.Read(buffer); n != 0 || !errors.Is(err, readErr) {
			t.Fatalf("second read = (%d, %v), want scripted upstream read error", n, err)
		}
		assertRawResponsesFinishAccounting(t, db, stats, f, model, false, false, context.Canceled.Error())
		_ = observed.Close()
		assertRawResponsesFinishAccounting(t, db, stats, f, model, false, false, context.Canceled.Error())
		if got := underlying.closeCalls.Load(); got != 1 {
			t.Fatalf("underlying body Close calls=%d want 1", got)
		}
	})

	t.Run("close finishes before blocked read", func(t *testing.T) {
		db := rawResponsesTestDB(t)
		stats := store.NewStats(db)
		t.Cleanup(stats.Close)
		model := rawResponsesTestModel("", "openai", "/responses")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f := NewForwarder(stats, db)
		usage := newRequestUsageRecorder(db, time.Now(), model, "/v1/responses", "openai")
		progress := strings.Replace(rawResponsesUsageEvent, "response.completed", "response.in_progress", 2)
		underlying := &rawResponsesCloseFirstBody{
			data: []byte(progress), readBlock: make(chan struct{}), readResume: make(chan struct{}),
		}
		response := &http.Response{Body: underlying}
		usage.wrapResponse(response)
		lease, ok := f.load.acquire(model.ID, 1)
		if !ok {
			t.Fatal("failed to acquire model lease")
		}
		observed := newRawResponsesUsageBody(response.Body, usage, http.StatusOK, true)
		observed.lease = lease
		observed.stats = stats
		observed.model = model
		observed.ctx = ctx
		readDone := make(chan error, 1)
		go func() {
			buffer := make([]byte, len(progress)+16)
			if _, err := observed.Read(buffer); err != nil {
				readDone <- err
				return
			}
			_, err := observed.Read(buffer)
			readDone <- err
		}()
		<-underlying.readBlock
		cancel()
		if err := observed.Close(); err != nil {
			t.Fatal(err)
		}
		assertRawResponsesFinishAccounting(t, db, stats, f, model, false, false, context.Canceled.Error())
		close(underlying.readResume)
		if err := <-readDone; !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("blocked read returned %v, want closed-pipe after release", err)
		}
		_ = observed.Close()
		assertRawResponsesFinishAccounting(t, db, stats, f, model, false, false, context.Canceled.Error())
		if got := underlying.closeCalls.Load(); got != 1 {
			t.Fatalf("underlying body Close calls=%d want 1", got)
		}
	})
}

func TestRawResponsesCancellationAndDeadlineSettleAsClientCanceled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		deadline bool
	}{
		{name: "cancel"},
		{name: "deadline", deadline: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := rawResponsesTestDB(t)
			stats := store.NewStats(db)
			t.Cleanup(stats.Close)
			model := rawResponsesTestModel("", "openai", "/responses")
			var ctx context.Context
			var cancel context.CancelFunc
			if tc.deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			progress := strings.Replace(rawResponsesUsageEvent, "response.completed", "response.in_progress", 2)
			observed, underlying, f := rawResponsesObserverForFinishTest(t, db, stats, model, ctx, []byte(progress), nil, true)
			buffer := make([]byte, len(progress)+16)
			if n, err := observed.Read(buffer); err != nil || n != len(progress) {
				t.Fatalf("progress read = (%d, %v), want payload and no error", n, err)
			}
			if tc.deadline {
				<-ctx.Done()
			} else {
				cancel()
				<-ctx.Done()
			}
			_ = observed.Close()
			_ = observed.Close()
			assertRawResponsesFinishAccounting(t, db, stats, f, model, false, false, ctx.Err().Error())
			if got := underlying.closeCalls.Load(); got != 1 {
				t.Fatalf("underlying body Close calls=%d want 1", got)
			}
		})
	}
}

func TestRawResponsesObservedUpstreamFailureWinsOverCancellation(t *testing.T) {
	db := rawResponsesTestDB(t)
	stats := store.NewStats(db)
	t.Cleanup(stats.Close)
	model := rawResponsesTestModel("", "openai", "/responses")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failureEvent := strings.Replace(rawResponsesUsageEvent, "response.completed", "response.failed", 2)
	observed, underlying, f := rawResponsesObserverForFinishTest(t, db, stats, model, ctx, []byte(failureEvent), nil, true)
	buffer := make([]byte, len(failureEvent)+16)
	if n, err := observed.Read(buffer); err != nil || n != len(failureEvent) {
		t.Fatalf("failure read = (%d, %v), want payload and no error", n, err)
	}
	cancel()
	<-ctx.Done()
	_ = observed.Close()
	assertRawResponsesFinishAccounting(t, db, stats, f, model, false, true, "raw Responses stream reported failure (event=response.failed)")
	if got := underlying.closeCalls.Load(); got != 1 {
		t.Fatalf("underlying body Close calls=%d want 1", got)
	}
}

func TestRawResponsesEOFSettlementIsUnchangedByLaterCancellation(t *testing.T) {
	db := rawResponsesTestDB(t)
	stats := store.NewStats(db)
	t.Cleanup(stats.Close)
	model := rawResponsesTestModel("", "openai", "/responses")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed, underlying, f := rawResponsesObserverForFinishTest(t, db, stats, model, ctx, []byte(rawResponsesUsageEvent), nil, false)
	if _, err := io.ReadAll(observed); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-ctx.Done()
	_ = observed.Close()
	_ = observed.Close()
	assertRawResponsesFinishAccounting(t, db, stats, f, model, true, false, "")
	if got := underlying.closeCalls.Load(); got != 1 {
		t.Fatalf("underlying body Close calls=%d want 1", got)
	}
}
