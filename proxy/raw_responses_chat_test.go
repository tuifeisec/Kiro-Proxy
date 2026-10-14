package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

func TestRawResponsesUpstreamURLUsesOnlyConfiguredProtocol(t *testing.T) {
	tests := []struct {
		name     string
		model    *store.Model
		wantURL  string
		wantChat bool
		wantErr  bool
	}{
		{
			name:     "responses",
			model:    &store.Model{Provider: "openai", BaseURL: "https://api.example/v1", Endpoint: "/responses"},
			wantURL:  "https://api.example/v1/responses",
			wantChat: false,
		},
		{
			name:     "responses trailing slash is retained",
			model:    &store.Model{Provider: "openai", BaseURL: "https://api.example/v1", Endpoint: "/responses/"},
			wantURL:  "https://api.example/v1/responses/",
			wantChat: false,
		},
		{
			name:     "chat completions",
			model:    &store.Model{Provider: "openai", BaseURL: "https://api.example/v1", Endpoint: "/chat/completions"},
			wantURL:  "https://api.example/v1/chat/completions",
			wantChat: true,
		},
		{
			name:     "disable auto suffix uses complete base URL",
			model:    &store.Model{Provider: "openai", BaseURL: "https://api.example/v1/chat/completions?tenant=one", Endpoint: "/responses", DisableAutoSuffix: true},
			wantURL:  "https://api.example/v1/chat/completions?tenant=one",
			wantChat: true,
		},
		{
			name:    "unsupported endpoint",
			model:   &store.Model{Provider: "openai", BaseURL: "https://api.example/v1", Endpoint: "/chat"},
			wantErr: true,
		},
		{
			name:    "unsupported provider",
			model:   &store.Model{Provider: "anthropic", BaseURL: "https://api.example/v1", Endpoint: "/responses"},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotURL, gotChat, err := rawResponsesUpstreamURL(test.model)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected endpoint resolution error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve endpoint: %v", err)
			}
			if gotURL != test.wantURL || gotChat != test.wantChat {
				t.Fatalf("endpoint/chat = %q/%t, want %q/%t", gotURL, gotChat, test.wantURL, test.wantChat)
			}
		})
	}
}

func TestPrepareRawResponsesChatBodyMapsReasoningAndToolChoice(t *testing.T) {
	body := []byte(`{"model":"local","instructions":"follow the tool result","input":[{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call-1","output":"found"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"lookup"},"reasoning":{"effort":"high","summary":"auto"},"stream":true}`)
	prepared, err := prepareRawResponsesChatBody(body, &store.Model{ModelID: "configured-model"})
	if err != nil {
		t.Fatalf("prepare Chat request: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(prepared, &got); err != nil {
		t.Fatalf("decode prepared request: %v", err)
	}
	var model, effort string
	if err := json.Unmarshal(got["model"], &model); err != nil || model != "configured-model" {
		t.Fatalf("model = %q, %v", model, err)
	}
	if err := json.Unmarshal(got["reasoning_effort"], &effort); err != nil || effort != "high" {
		t.Fatalf("reasoning_effort = %q, %v", effort, err)
	}
	var choice map[string]json.RawMessage
	if err := json.Unmarshal(got["tool_choice"], &choice); err != nil {
		t.Fatalf("decode tool_choice: %v", err)
	}
	var typ string
	var function map[string]string
	if err := json.Unmarshal(choice["type"], &typ); err != nil || typ != "function" {
		t.Fatalf("tool_choice type = %q, %v", typ, err)
	}
	if err := json.Unmarshal(choice["function"], &function); err != nil || function["name"] != "lookup" {
		t.Fatalf("tool_choice function = %#v, %v", function, err)
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(got["messages"], &messages); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("converted messages count = %d, want 3", len(messages))
	}
	var role string
	if err := json.Unmarshal(messages[1]["role"], &role); err != nil || role != "assistant" {
		t.Fatalf("tool call role = %q, %v", role, err)
	}
	var toolCallID string
	if err := json.Unmarshal(messages[2]["tool_call_id"], &toolCallID); err != nil || toolCallID != "call-1" {
		t.Fatalf("tool result id = %q, %v", toolCallID, err)
	}
	if !bytes.Contains(prepared, []byte(`"include_usage":true`)) {
		t.Fatalf("stream request is missing usage option: %s", prepared)
	}
}

func TestPrepareRawResponsesChatBodyRejectsUnsupportedToolsAndChoices(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "unsupported hosted tool",
			body: `{"input":"hello","tools":[{"type":"web_search_preview"}]}`,
		},
		{
			name: "unsupported allowed tools choice",
			body: `{"input":"hello","tool_choice":{"type":"allowed_tools","mode":"auto","tools":[]}}`,
		},
		{
			name: "function choice missing name",
			body: `{"input":"hello","tool_choice":{"type":"function"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := prepareRawResponsesChatBody([]byte(test.body), &store.Model{ModelID: "configured-model"}); err == nil {
				t.Fatal("expected explicit unsupported-shape error")
			}
		})
	}
}

func TestAdaptRawChatResponseConvertsJSONAndPreservesHTTPError(t *testing.T) {
	chatBody := `{"id":"chatcmpl_1","model":"configured-model","created":123,"choices":[{"message":{"role":"assistant","content":"hello","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":2}}}`
	source := &countingReadCloser{reader: strings.NewReader(chatBody)}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     {"application/json"},
			"Content-Length":   {"999"},
			"Content-Encoding": {"identity"},
			"Content-Md5":      {"old"},
			"X-Upstream":       {"kept"},
		},
		Body:             source,
		ContentLength:    999,
		TransferEncoding: []string{"chunked"},
	}
	adapted, err := adaptRawChatResponse(context.Background(), resp)
	if err != nil {
		t.Fatalf("adapt JSON response: %v", err)
	}
	if adapted != resp {
		t.Fatal("adapter replaced the response pointer")
	}
	if adapted.Header.Get("Content-Type") != "application/json" || adapted.Header.Get("X-Upstream") != "kept" {
		t.Fatalf("response headers = %v", adapted.Header)
	}
	if source.snapshot().reads != 0 {
		t.Fatal("JSON source was read before the downstream body was read")
	}
	for _, name := range []string{"Content-Encoding", "Content-Md5", "Transfer-Encoding", "Trailer"} {
		if adapted.Header.Get(name) != "" {
			t.Errorf("stale entity header %s = %q", name, adapted.Header.Get(name))
		}
	}
	payload, err := io.ReadAll(adapted.Body)
	if err != nil {
		t.Fatalf("read adapted response: %v", err)
	}
	_ = adapted.Body.Close()
	var response map[string]json.RawMessage
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("decode Responses JSON: %v", err)
	}
	var status, outputText string
	if err := json.Unmarshal(response["status"], &status); err != nil || status != "completed" {
		t.Fatalf("status = %q, %v", status, err)
	}
	if err := json.Unmarshal(response["output_text"], &outputText); err != nil || outputText != "hello" {
		t.Fatalf("output_text = %q, %v", outputText, err)
	}
	if adapted.ContentLength != -1 || adapted.Header.Get("Content-Length") != "" {
		t.Fatalf("content length = %d/header %q, payload length %d", adapted.ContentLength, adapted.Header.Get("Content-Length"), len(payload))
	}

	errorResponse := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": {"application/json"}, "X-Upstream": {"error"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"rate limit"}`)),
	}
	unchanged, err := adaptRawChatResponse(context.Background(), errorResponse)
	if err != nil || unchanged != errorResponse {
		t.Fatalf("HTTP error response changed: response=%p err=%v", unchanged, err)
	}
	unchangedBody, err := io.ReadAll(unchanged.Body)
	if err != nil || string(unchangedBody) != `{"error":"rate limit"}` || unchanged.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("HTTP error body/header changed: body=%q headers=%v err=%v", unchangedBody, unchanged.Header, err)
	}
}

func TestAdaptRawChatResponseRejectsJSONFailureEnvelope(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"upstream failed"}}`,
		`{"choices":[]}`,
		`{"choices":[{}]}`,
	} {
		resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
		adapted, err := adaptRawChatResponse(context.Background(), resp)
		if err != nil {
			t.Fatalf("adapt response before body read: %v", err)
		}
		if _, err := io.ReadAll(adapted.Body); err == nil {
			t.Errorf("body %s was accepted as a completed response", body)
		}
		_ = adapted.Body.Close()
	}
}

func TestAdaptRawChatResponseStreamsLazilyWithUsageAndToolEvents(t *testing.T) {
	stream := "data: {\"id\":\"chatcmpl_2\",\"model\":\"configured-model\",\"choices\":[{\"delta\":{\"content\":\"hello\",\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\"}}]}}]}\n\n" +
		"data: {\"id\":\"chatcmpl_2\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"x\\\"}\"}}],\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl_2\",\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":3,\"prompt_tokens_details\":{\"cached_tokens\":2}}}\n\n" +
		"data: [DONE]\n\n"
	source := &countingReadCloser{reader: strings.NewReader(stream)}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     {"text/event-stream; charset=utf-8"},
			"Content-Length":   {"123"},
			"Content-Encoding": {"gzip"},
			"X-Upstream":       {"kept"},
		},
		Body:          source,
		ContentLength: 123,
	}
	adapted, err := adaptRawChatResponse(context.Background(), resp)
	if err != nil {
		t.Fatalf("adapt stream response: %v", err)
	}
	if source.snapshot().reads != 0 {
		t.Fatal("stream source was read before the downstream body was read")
	}
	if adapted.Header.Get("Content-Type") != "text/event-stream" || adapted.Header.Get("Content-Length") != "" || adapted.Header.Get("Content-Encoding") != "" || adapted.Header.Get("X-Upstream") != "kept" {
		t.Fatalf("adapted stream headers = %v", adapted.Header)
	}
	payload, err := io.ReadAll(adapted.Body)
	if err != nil {
		t.Fatalf("read adapted stream: %v", err)
	}
	_ = adapted.Body.Close()
	text := string(payload)
	for _, expected := range []string{
		"response.output_text.delta", "response.function_call_arguments.delta", "response.output_item.done", "response.completed",
		`"input_tokens":9`, `"output_tokens":3`, `"cached_tokens":2`,
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("adapted stream missing %q: %s", expected, text)
		}
	}
	if snapshot := source.snapshot(); snapshot.reads == 0 || !snapshot.closed {
		t.Fatalf("source read/closed = %d/%t", snapshot.reads, snapshot.closed)
	}
}

func TestAdaptRawChatResponseCancellationClosesBlockedSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := newBlockingReadCloser()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: source}
	adapted, err := adaptRawChatResponse(ctx, resp)
	if err != nil {
		t.Fatalf("adapt stream response: %v", err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, readErr := adapted.Body.Read(make([]byte, 128))
		readDone <- readErr
	}()
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("stream conversion did not start reading source")
	}
	cancel()
	select {
	case <-source.closed:
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not close source body")
	}
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("stream read remained blocked after cancellation")
	}
	_ = adapted.Body.Close()
}

func TestForwardRawResponsesSingleChatOnlyEndpoint(t *testing.T) {
	var chatCalls, responsesCalls atomic.Int32
	var outboundBody []byte
	var outboundAuthorization string
	var outboundMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			chatCalls.Add(1)
		default:
			responsesCalls.Add(1)
			http.Error(w, "Responses endpoint is unavailable", http.StatusNotFound)
			return
		}
		capturedBody, _ := io.ReadAll(r.Body)
		outboundMu.Lock()
		outboundBody = capturedBody
		outboundAuthorization = r.Header.Get("Authorization")
		outboundMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_single","model":"gpt-configured","created":123,"choices":[{"message":{"role":"assistant","content":"single reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":3}}}`)
	}))
	defer server.Close()

	db := rawResponsesTestDB(t)
	f, _ := rawResponsesTestForwarder(t, db)
	model := rawResponsesTestModel(server.URL, "openai", "/chat/completions")
	model.ModelID = "gpt-configured"
	requestBody := `{"model":"local-alias","instructions":"be concise","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"lookup"},"reasoning":{"effort":"medium"},"metadata":{"kiro:conversation_id":"single-conversation"}}`
	req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(requestBody))
	pre := rawResponsesTestPre(db, model, req.URL.Path, requestBody)
	pre.Conv = &converter.ParsedConversation{ConversationID: "single-conversation", ReasoningEffort: "medium"}
	resp, err := f.ForwardRawResponses(req, pre, rawResponsesTestRoute(model), nil)
	if err != nil {
		t.Fatalf("forward single Chat-configured Responses request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := f.load.snapshot()[model.ID]; got != 1 {
		t.Fatalf("lease should remain held before response read, active=%d", got)
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read adapted JSON: %v", err)
	}
	_ = resp.Body.Close()
	if got := f.load.snapshot()[model.ID]; got != 0 {
		t.Fatalf("lease remains held after response read, active=%d", got)
	}
	if chatCalls.Load() != 1 || responsesCalls.Load() != 0 {
		t.Fatalf("Chat/Responses calls = %d/%d, want exactly 1/0", chatCalls.Load(), responsesCalls.Load())
	}
	outboundMu.Lock()
	capturedBody := append([]byte(nil), outboundBody...)
	capturedAuthorization := outboundAuthorization
	outboundMu.Unlock()
	if capturedAuthorization != "Bearer test-secret" {
		t.Fatalf("Authorization = %q", capturedAuthorization)
	}
	for _, forbidden := range []string{`"input"`, `"tool_choice":{"type":"function","name"`, "metadata", "kiro:"} {
		if bytes.Contains(capturedBody, []byte(forbidden)) {
			t.Errorf("Responses-only/private field %q reached Chat endpoint: %s", forbidden, capturedBody)
		}
	}
	for _, expected := range []string{`"model":"gpt-configured"`, `"reasoning_effort":"medium"`, `"name":"lookup"`, `"messages"`} {
		if !bytes.Contains(capturedBody, []byte(expected)) {
			t.Errorf("adapted Chat request missing %q: %s", expected, capturedBody)
		}
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode adapted response: %v", err)
	}
	var outputText string
	if err := json.Unmarshal(response["output_text"], &outputText); err != nil || outputText != "single reply" {
		t.Fatalf("output_text = %q, %v", outputText, err)
	}
	items := rawResponsesUsageItems(t, db)
	if len(items) != 1 || !items[0].Success || items[0].InputTokens != 11 || items[0].OutputTokens != 4 || items[0].CacheReadTokens != 3 {
		t.Fatalf("unexpected usage record: %+v", items)
	}
}

func TestForwardRawResponsesGroupChatOnlyEndpointStreamsAndSettlesCallback(t *testing.T) {
	var chatCalls, responsesCalls atomic.Int32
	var outboundBody []byte
	var outboundAuthorization string
	var outboundMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			chatCalls.Add(1)
		default:
			responsesCalls.Add(1)
			http.Error(w, "Responses endpoint is unavailable", http.StatusNotFound)
			return
		}
		capturedBody, _ := io.ReadAll(r.Body)
		outboundMu.Lock()
		outboundBody = capturedBody
		outboundAuthorization = r.Header.Get("Authorization")
		outboundMu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"data: {\"id\":\"chatcmpl_group\",\"model\":\"gpt-configured\",\"choices\":[{\"delta\":{\"content\":\"tool result accepted\"}}]}\n\n"+
				"data: {\"id\":\"chatcmpl_group\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
				"data: {\"id\":\"chatcmpl_group\",\"choices\":[],\"usage\":{\"prompt_tokens\":13,\"completion_tokens\":5,\"prompt_tokens_details\":{\"cached_tokens\":2}}}\n\n"+
				"data: [DONE]\n\n")
	}))
	defer server.Close()

	db := rawResponsesTestDB(t)
	f, _ := rawResponsesTestForwarder(t, db)
	model := rawResponsesTestModel(server.URL, "openai", "/chat/completions")
	model.ModelID = "gpt-configured"
	requestBody := `{"model":"local-alias","input":[{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call-1","output":"done"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"reasoning":{"effort":"high"},"stream":true,"metadata":{"kiro:conversation_id":"group-conversation"}}`
	req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(requestBody))
	pre := rawResponsesTestPre(db, model, req.URL.Path, requestBody)
	pre.Conv = &converter.ParsedConversation{ConversationID: "group-conversation", ReasoningEffort: "high"}
	route := autoGroupRawRoute(model)
	resp, err := f.ForwardRawResponses(req, pre, route, autoGroupMockLogger(t))
	if err != nil {
		t.Fatalf("forward grouped Chat-configured Responses request: %v", err)
	}
	defer resp.Body.Close()
	if got := f.load.snapshot()[model.ID]; got != 1 {
		t.Fatalf("group lease should remain held before response read, active=%d", got)
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read adapted stream: %v", err)
	}
	_ = resp.Body.Close()
	if got := f.load.snapshot()[model.ID]; got != 0 {
		t.Fatalf("group lease remains held after response read, active=%d", got)
	}
	if chatCalls.Load() != 1 || responsesCalls.Load() != 0 {
		t.Fatalf("Chat/Responses calls = %d/%d, want exactly 1/0", chatCalls.Load(), responsesCalls.Load())
	}
	outboundMu.Lock()
	capturedBody := append([]byte(nil), outboundBody...)
	capturedAuthorization := outboundAuthorization
	outboundMu.Unlock()
	if capturedAuthorization != "Bearer test-secret" {
		t.Fatalf("Authorization = %q", capturedAuthorization)
	}
	for _, forbidden := range []string{`"input"`, "metadata", "kiro:"} {
		if bytes.Contains(capturedBody, []byte(forbidden)) {
			t.Errorf("Responses-only/private field %q reached Chat endpoint: %s", forbidden, capturedBody)
		}
	}
	for _, expected := range []string{`"model":"gpt-configured"`, `"reasoning_effort":"high"`, `"tool_call_id":"call-1"`, `"messages"`} {
		if !bytes.Contains(capturedBody, []byte(expected)) {
			t.Errorf("adapted Chat request missing %q: %s", expected, capturedBody)
		}
	}
	stream := string(responseBody)
	if !strings.Contains(stream, "response.output_text.delta") || !strings.Contains(stream, "response.completed") {
		t.Fatalf("Chat stream did not become a completed Responses stream: %s", stream)
	}
	items := rawResponsesUsageItems(t, db)
	if len(items) != 1 || !items[0].Success || items[0].InputTokens != 13 || items[0].OutputTokens != 5 || items[0].CacheReadTokens != 2 {
		t.Fatalf("unexpected group usage record: %+v", items)
	}
	if got := f.affinity.get(route.Group.ID, "group-conversation", time.Now()); got != model.ID {
		t.Fatalf("group completion callback did not bind affinity: got %q, want %q", got, model.ID)
	}
}

type countingReadCloser struct {
	reader io.Reader
	reads  int
	closed bool
	mu     sync.Mutex
}

func (r *countingReadCloser) snapshot() (snapshot struct {
	reads  int
	closed bool
}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot.reads = r.reads
	snapshot.closed = r.closed
	return snapshot
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	r.mu.Lock()
	r.reads++
	r.mu.Unlock()
	return r.reader.Read(p)
}

func (r *countingReadCloser) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return nil
}

type blockingReadCloser struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{started: make(chan struct{}), closed: make(chan struct{})}
}

func (r *blockingReadCloser) Read([]byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.closed
	return 0, io.ErrClosedPipe
}

func (r *blockingReadCloser) Close() error {
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	return nil
}
