package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

const nestedOpenAIChatBadRequest = `{"error":{"code":"no_healthy_account","message":"{\"code\":11129,\"msg\":\"invalid function call parameters\",\"requestId\":\"upstream-7722474e\",\"extError\":{\"StatusCode\":400,\"type\":\"invalid_request_error\",\"code\":\"invalid_request_error\"}}"}}`

func TestOpenAIChatNestedBadRequestStopsRetryAndKeepsBody(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, nestedOpenAIChatBadRequest)
	}))
	defer server.Close()

	logPath := filepath.Join(t.TempDir(), "chat-diagnostics.jsonl")
	logger, err := NewTrafficLogger(logPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	forwarder := NewForwarder(nil, nil)
	requestBody := []byte(`{"model":"deepseek","messages":[{"role":"user","content":"PRIVATE_PROMPT"}],"tools":[{"type":"function","function":{"name":"lookup","description":"PRIVATE_DESCRIPTION","parameters":{"type":"object","properties":{"id":{"type":"string"}}}}}]}`)
	resp, err := forwarder.sendWithRetryBudget(context.Background(), server.URL+"/v1/chat/completions", requestBody,
		&store.Model{ID: "chat-model", ModelID: "deepseek", Provider: "openai", APIKey: "secret-key"},
		logger, "conversation", nil, 5*time.Second)
	if err != nil {
		t.Fatalf("send OpenAI Chat request: %v", err)
	}
	gotBody, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	logger.Close()
	if readErr != nil {
		t.Fatalf("read preserved response: %v", readErr)
	}
	if resp.StatusCode != http.StatusServiceUnavailable || string(gotBody) != nestedOpenAIChatBadRequest {
		t.Fatalf("response status/body changed: status=%d body=%q", resp.StatusCode, gotBody)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("nested request error was retried %d times; want one attempt", got)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logData), "PRIVATE_PROMPT") || strings.Contains(string(logData), "PRIVATE_DESCRIPTION") || strings.Contains(string(logData), "secret-key") {
		t.Fatal("summary diagnostics leaked request content or API key")
	}
	var requestTrace, errorTrace string
	for _, line := range strings.Split(strings.TrimSpace(string(logData)), "\n") {
		var entry struct {
			Category string                 `json:"category"`
			Message  string                 `json:"message"`
			Metadata map[string]interface{} `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode log entry: %v", err)
		}
		if entry.Category != "openai_chat" {
			continue
		}
		if entry.Message == "OpenAI Chat 出站工具诊断" {
			requestTrace, _ = entry.Metadata["trace_id"].(string)
			summary, _ := entry.Metadata["tool_summary"].(map[string]interface{})
			if summary["tool_count"] != float64(1) {
				t.Fatalf("tool summary missing actual outbound tool: %#v", summary)
			}
		}
		if entry.Message == "OpenAI Chat 上游错误分类" {
			errorTrace, _ = entry.Metadata["trace_id"].(string)
			if entry.Metadata["inner_http_status"] != float64(400) || entry.Metadata["upstream_request_id"] != "upstream-7722474e" || entry.Metadata["retry"] != false {
				t.Fatalf("nested error diagnostics incomplete: %#v", entry.Metadata)
			}
		}
	}
	if requestTrace == "" || errorTrace != requestTrace {
		t.Fatalf("request and error trace IDs differ: request=%q error=%q", requestTrace, errorTrace)
	}
	captures, err := filepath.Glob(filepath.Join(filepath.Dir(logPath), "captures", "openai-chat-11129-*.json"))
	if err != nil || len(captures) != 1 {
		t.Fatalf("expected one captured tool schema, files=%v error=%v", captures, err)
	}
	captured, err := os.ReadFile(captures[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(captured), "PRIVATE_PROMPT") || strings.Contains(string(captured), "PRIVATE_DESCRIPTION") || strings.Contains(string(captured), "secret-key") || !strings.Contains(string(captured), `"name":"lookup"`) {
		t.Fatalf("capture has missing tool or leaked top-level request content: %s", captured)
	}
}

func TestOpenAIChatOrdinary503KeepsExistingRetry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	resp, err := NewForwarder(nil, nil).sendWithRetryBudget(context.Background(), server.URL+"/v1/chat/completions",
		[]byte(`{"model":"test","messages":[]}`), &store.Model{Provider: "openai", APIKey: "test"}, logger, "session", nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || requests.Load() != 2 {
		t.Fatalf("ordinary 503 retry changed: status=%d requests=%d", resp.StatusCode, requests.Load())
	}
}

func TestOpenAIChatFinalAttemptReportsNestedErrorWithoutChangingBody(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
		if requests.Add(1) <= maxRetries {
			_, _ = io.WriteString(w, `{"error":{"message":"busy"}}`)
			return
		}
		_, _ = io.WriteString(w, nestedOpenAIChatBadRequest)
	}))
	defer server.Close()
	logPath := filepath.Join(t.TempDir(), "last-attempt.jsonl")
	logger, err := NewTrafficLogger(logPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := NewForwarder(nil, nil).sendWithRetryBudget(context.Background(), server.URL+"/v1/chat/completions",
		[]byte(`{"model":"test","messages":[],"tools":[{"type":"function","function":{"name":"final_probe","parameters":{"type":"object","properties":{}}}}]}`), &store.Model{Provider: "openai", APIKey: "test"}, logger, "session", nil, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	logger.Close()
	if err != nil || string(body) != nestedOpenAIChatBadRequest || requests.Load() != maxRetries+1 {
		t.Fatalf("final response changed: body=%q readErr=%v requests=%d", body, err, requests.Load())
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(string(logData)), "\n") {
		var entry struct {
			Message  string                 `json:"message"`
			Metadata map[string]interface{} `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Message == "OpenAI Chat 上游响应" && entry.Metadata["upstream_request_id"] == "upstream-7722474e" {
			found = entry.Metadata["inner_http_status"] == float64(400) && entry.Metadata["attempt"] == float64(maxRetries+1)
		}
	}
	if !found {
		t.Fatal("final attempt did not log the nested request error")
	}
	captures, err := filepath.Glob(filepath.Join(filepath.Dir(logPath), "captures", "openai-chat-11129-*-attempt-4-*.json"))
	if err != nil || len(captures) != 1 {
		t.Fatalf("final attempt tool capture missing: files=%v error=%v", captures, err)
	}
}

func TestOpenAIChatGroupBodyObservationKeepsResponse(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "group-observation.jsonl")
	logger, err := NewTrafficLogger(logPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(nestedOpenAIChatBadRequest))}
	observeOpenAIChatResponse(resp, logger, &store.Model{Provider: "openai"}, "group-trace", 1,
		[]byte(`{"tools":[{"type":"function","function":{"name":"group_probe","parameters":{"type":"object","properties":{}}}}]}`))
	if detail := preserveResponseBody(resp); !strings.Contains(detail, "no_healthy_account") {
		t.Fatalf("group error detail lost: %q", detail)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	logger.Close()
	if readErr != nil || string(body) != nestedOpenAIChatBadRequest {
		t.Fatalf("group response changed: body=%q err=%v", body, readErr)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), `"trace_id":"group-trace"`) ||
		!strings.Contains(string(logData), `"upstream_request_id":"upstream-7722474e"`) {
		t.Fatalf("group diagnostic missing trace or upstream ID: %s", logData)
	}
	captures, err := filepath.Glob(filepath.Join(filepath.Dir(logPath), "captures", "openai-chat-11129-group-trace-attempt-1-*.json"))
	if err != nil || len(captures) != 1 {
		t.Fatalf("group tool capture missing: files=%v error=%v", captures, err)
	}
}

func TestOpenAIChatIncompleteErrorDoesNotCaptureTools(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "incomplete.jsonl")
	logger, err := NewTrafficLogger(logPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(nestedOpenAIChatBadRequest))}
	observeOpenAIChatResponse(resp, logger, &store.Model{Provider: "openai"}, "incomplete-trace", 1,
		[]byte(`{"tools":[{"type":"function","function":{"name":"probe","parameters":{"type":"object"}}}]}`))
	resp.Body.Close()
	logger.Close()
	captures, err := filepath.Glob(filepath.Join(filepath.Dir(logPath), "captures", "*.json"))
	if err != nil || len(captures) != 0 {
		t.Fatalf("incomplete response produced tool capture: files=%v error=%v", captures, err)
	}
}

func TestOpenAIChatCandidateErrorCapturesTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, nestedOpenAIChatBadRequest)
	}))
	defer server.Close()
	logPath := filepath.Join(t.TempDir(), "candidate-error.jsonl")
	logger, err := NewTrafficLogger(logPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := withUpstreamRequestOperation(context.Background(), newUpstreamRequestOperation())
	requestBody := []byte(`{"model":"test","messages":[],"tools":[{"type":"function","function":{"name":"candidate_probe","parameters":{"type":"object"}}}]}`)
	resp, err := NewForwarder(nil, nil).sendCandidateRequest(ctx, server.URL+"/v1/chat/completions", requestBody,
		&store.Model{Provider: "openai", APIKey: "test"}, logger, "session", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	gotBody, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	logger.Close()
	if readErr != nil || string(gotBody) != nestedOpenAIChatBadRequest {
		t.Fatalf("candidate response changed: body=%q error=%v", gotBody, readErr)
	}
	captures, err := filepath.Glob(filepath.Join(filepath.Dir(logPath), "captures", "openai-chat-11129-*-attempt-1-*.json"))
	if err != nil || len(captures) != 1 {
		t.Fatalf("candidate tool capture missing: files=%v error=%v", captures, err)
	}
}

func TestOpenAIChatCandidateReplayLogsEachOutboundBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	logPath := filepath.Join(t.TempDir(), "replay.jsonl")
	logger, err := NewTrafficLogger(logPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := withUpstreamRequestOperation(context.Background(), newUpstreamRequestOperation())
	forwarder := NewForwarder(nil, nil)
	model := &store.Model{Provider: "openai", APIKey: "test"}
	for _, name := range []string{"first_tool", "replay_tool"} {
		requestBody := []byte(`{"model":"test","messages":[],"tools":[{"type":"function","function":{"name":"` + name + `","parameters":{"type":"object"}}}]}`)
		resp, err := forwarder.sendCandidateRequest(ctx, server.URL+"/v1/chat/completions", requestBody, model, logger, "session", nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	logger.Close()
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var attempts []int
	var names []string
	var traceIDs []string
	for _, line := range strings.Split(strings.TrimSpace(string(logData)), "\n") {
		var entry struct {
			Message  string                 `json:"message"`
			Metadata map[string]interface{} `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Message != "OpenAI Chat 出站工具诊断" {
			continue
		}
		attempts = append(attempts, int(entry.Metadata["attempt"].(float64)))
		traceIDs = append(traceIDs, entry.Metadata["trace_id"].(string))
		summary := entry.Metadata["tool_summary"].(map[string]interface{})
		tools := summary["tools"].([]interface{})
		names = append(names, tools[0].(map[string]interface{})["function_name"].(string))
	}
	if len(attempts) != 2 || attempts[0] != 1 || attempts[1] != 2 ||
		len(names) != 2 || names[0] != "first_tool" || names[1] != "replay_tool" ||
		traceIDs[0] == "" || traceIDs[0] != traceIDs[1] {
		t.Fatalf("replay diagnostics incomplete: attempts=%v names=%v traceIDs=%v", attempts, names, traceIDs)
	}
}

func TestOpenAIChatNestedErrorMessageNamesBothStatuses(t *testing.T) {
	info := parseOpenAIChatGatewayError(http.StatusServiceUnavailable, nil, []byte(nestedOpenAIChatBadRequest))
	message := openAIChatErrorMessage(http.StatusServiceUnavailable, "503 Service Unavailable", []byte(nestedOpenAIChatBadRequest), info)
	if !strings.Contains(message, "HTTP 503") || !strings.Contains(message, "HTTP 400") || !strings.Contains(message, "upstream-7722474e") {
		t.Fatalf("nested error message hides original status or request ID: %q", message)
	}
	if strings.Contains(message, "稍后重试") {
		t.Fatalf("definitive request error was described as transient: %q", message)
	}
}

func TestNestedBadRequestDoesNotChangeResponsesRetry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, nestedOpenAIChatBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	resp, err := NewForwarder(nil, nil).sendWithRetryBudget(context.Background(), server.URL+"/v1/responses",
		[]byte(`{"model":"test","stream":false}`), &store.Model{Provider: "openai", Endpoint: "/responses", APIKey: "test"}, logger, "session", nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || requests.Load() != 2 {
		t.Fatalf("Responses retry changed: status=%d requests=%d", resp.StatusCode, requests.Load())
	}
}

func TestNestedBadRequestDoesNotChangeOtherProviderRetry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, nestedOpenAIChatBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	resp, err := NewForwarder(nil, nil).sendWithRetryBudget(context.Background(), server.URL+"/v1/chat/completions",
		[]byte(`{"model":"test","stream":false}`), &store.Model{Provider: "anthropic", APIKey: "test"}, logger, "session", nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || requests.Load() != 2 {
		t.Fatalf("other provider retry changed: status=%d requests=%d", resp.StatusCode, requests.Load())
	}
}
