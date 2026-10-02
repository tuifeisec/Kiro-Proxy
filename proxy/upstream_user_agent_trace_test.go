package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kiro-proxy/store"
)

func readUserAgentTraceEntry(t *testing.T, path string) LogEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entry LogEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

type userAgentTraceSnapshot struct {
	HeaderObserved bool     `json:"ua_header_observed"`
	UserAgents     []string `json:"encoded_user_agents"`
	Protocols      []string `json:"negotiated_protocols"`
	WriteResults   []bool   `json:"write_results"`
}

func readUserAgentTraceSnapshot(t *testing.T, entry LogEntry) userAgentTraceSnapshot {
	t.Helper()
	data, err := json.Marshal(entry.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot userAgentTraceSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestUpstreamUserAgentTracePreservesCallbacksAndSeparatesCallbackSequences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.jsonl")
	logger, err := NewTrafficLogger(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	previousWrites, previousHeaders := 0, 0
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		WroteRequest:     func(httptrace.WroteRequestInfo) { previousWrites++ },
		WroteHeaderField: func(string, []string) { previousHeaders++ },
	})
	ctx, observation := withUpstreamUserAgentTrace(ctx)
	trace := httptrace.ContextClientTrace(ctx)
	// Mock 两次传输事件，模拟 HTTP/2 写入失败后克隆请求并转 HTTP/1.1。
	trace.GotConn(httptrace.GotConnInfo{})
	trace.WroteHeaderField("authorization", []string{"Bearer test-secret"})
	trace.WroteHeaderField("cookie", []string{"test-cookie"})
	values := []string{"OpenAI/Go 3.56.0"}
	trace.WroteHeaderField("user-agent", values)
	values[0] = "mutated-after-callback"
	trace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("http2: test-write-error")})
	trace.GotConn(httptrace.GotConnInfo{})
	trace.WroteHeaderField("User-Agent", []string{"OpenAI/Go 3.56.0"})
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	resp := &http.Response{StatusCode: http.StatusOK, Proto: "HTTP/1.1", Header: http.Header{
		"X-Request-Id": {"receiver-request"}, "Request-Id": {"other-request"},
		"Set-Cookie": {"response-cookie"},
	}}
	observation.log(logger, &store.Model{Name: "deepseek-v4.1-flash"}, "https://test-user:test-password@relay.invalid/v1/chat/completions?key=query-secret", "local-trace", 2, resp, nil)
	logger.Close()
	entry := readUserAgentTraceEntry(t, path)
	snapshot := readUserAgentTraceSnapshot(t, entry)
	if !snapshot.HeaderObserved || len(snapshot.UserAgents) != 2 || len(snapshot.Protocols) != 2 || len(snapshot.WriteResults) != 2 {
		t.Fatalf("callback observations missing: %+v", snapshot)
	}
	for _, value := range snapshot.UserAgents {
		if value != "OpenAI/Go 3.56.0" {
			t.Fatalf("incorrect observed UA: %q", value)
		}
	}
	if snapshot.WriteResults[0] || !snapshot.WriteResults[1] {
		t.Fatalf("write callback outcomes merged: %+v", snapshot.WriteResults)
	}
	if previousWrites != 2 || previousHeaders != 4 {
		t.Fatalf("existing trace callbacks changed: writes=%d headers=%d", previousWrites, previousHeaders)
	}
	if entry.Category != "upstream_identity" || entry.Status != http.StatusOK || entry.Metadata["trace_id"] != "local-trace" || entry.Metadata["upstream_request_id"] != "receiver-request" || entry.Metadata["original_upstream_host"] != "relay.invalid" || entry.Metadata["original_endpoint"] != "/v1/chat/completions" {
		t.Fatalf("request correlation missing: %+v", entry)
	}
	data, _ := os.ReadFile(path)
	for _, secret := range []string{"test-secret", "test-cookie", "response-cookie", "test-user", "test-password", "query-secret", "mutated-after-callback"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("non-UA data retained: %q", secret)
		}
	}
}

func TestUpstreamUserAgentTraceDoesNotInventEvidence(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"mock_without_encoding_events", nil},
		{"connection_failure", errors.New("test-secret-original-error")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "identity.jsonl")
			logger, err := NewTrafficLogger(path, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, observation := withUpstreamUserAgentTrace(context.Background())
			var resp *http.Response
			if tt.err == nil {
				// 自定义 transport 返回带有预期请求头的响应，仍不能伪造编码事件。
				resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Request-Id": {"fallback-request-id"}}, Request: &http.Request{Header: http.Header{"User-Agent": {"OpenAI/Go 3.56.0"}}}}
			}
			observation.log(logger, &store.Model{Name: "test"}, "https://relay.invalid/v1/responses", "trace", 1, resp, tt.err)
			logger.Close()
			entry := readUserAgentTraceEntry(t, path)
			if got := readUserAgentTraceSnapshot(t, entry); got.HeaderObserved || len(got.UserAgents) != 0 || len(got.Protocols) != 0 || len(got.WriteResults) != 0 {
				t.Fatalf("invented encoding evidence: %+v", got)
			}
			if entry.Metadata["request_error"] != (tt.err != nil) {
				t.Fatalf("request outcome=%v", entry.Metadata["request_error"])
			}
			if resp != nil && entry.Metadata["upstream_request_id"] != "fallback-request-id" {
				t.Fatalf("Request-Id fallback missing: %+v", entry.Metadata)
			}
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), "test-secret-original-error") || strings.Contains(string(data), "OpenAI/Go 3.56.0") {
				t.Fatal("unobserved or unrelated data retained")
			}
		})
	}
}

func TestUpstreamUserAgentTraceSnapshotDistinguishesPendingAndFailedWrite(t *testing.T) {
	ctx, observation := withUpstreamUserAgentTrace(context.Background())
	trace := httptrace.ContextClientTrace(ctx)
	trace.GotConn(httptrace.GotConnInfo{})
	trace.WroteHeaderField("user-agent", []string{"OpenAI/Go 3.56.0"})
	for _, failed := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "identity.jsonl")
		logger, err := NewTrafficLogger(path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if failed {
			// HTTP/2 可先返回响应头，写入回调稍后到达；用 Mock 稳定回放该顺序。
			trace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("late write failure")})
		}
		observation.log(logger, &store.Model{Name: "test"}, "https://relay.invalid/v1/chat/completions", "trace", 1, &http.Response{StatusCode: 200}, nil)
		logger.Close()
		snapshot := readUserAgentTraceSnapshot(t, readUserAgentTraceEntry(t, path))
		wantWrites := 0
		if failed {
			wantWrites = 1
		}
		if !snapshot.HeaderObserved || len(snapshot.UserAgents) != 1 || len(snapshot.WriteResults) != wantWrites {
			t.Fatalf("failed=%v: pending and failed writes confused: %+v", failed, snapshot)
		}
		if failed && snapshot.WriteResults[0] {
			t.Fatal("failed write callback logged as success")
		}

	}
}

func TestUpstreamUserAgentTraceSeparatesOriginalAndRedirectedResponse(t *testing.T) {
	receivedFinalUA := make(chan string, 1)
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		receivedFinalUA <- r.UserAgent()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "final-receiver-request")
		io.WriteString(w, `{"choices":[]}`)
	}))
	defer final.Close()
	original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("X-Request-Id", "redirect-response-request")
		w.Header().Set("Location", final.URL+"/redirected?key=final-query-secret")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer original.Close()
	path := filepath.Join(t.TempDir(), "identity.jsonl")
	logger, err := NewTrafficLogger(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	f := NewForwarder(nil, nil)
	defer f.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := f.sendWithRetryBudget(ctx, original.URL+"/v1/chat/completions?key=original-query-secret", []byte(`{}`), &store.Model{Provider: "openai", Name: "test"}, logger, "", nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	logger.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries []LogEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Category == "upstream_identity" {
			entries = append(entries, entry)
		}
	}
	if len(entries) != 1 {
		t.Fatalf("identity logs=%d, want one client.Do snapshot", len(entries))
	}
	entry := entries[0]
	if entry.Metadata["original_upstream_host"] != strings.TrimPrefix(original.URL, "http://") || entry.Metadata["original_endpoint"] != "/v1/chat/completions" || entry.Metadata["response_host"] != strings.TrimPrefix(final.URL, "http://") || entry.Metadata["response_endpoint"] != "/redirected" || entry.Metadata["upstream_request_id"] != "final-receiver-request" {
		t.Fatalf("redirect identities mixed: %+v", entry.Metadata)
	}
	var finalUA string
	select {
	case finalUA = <-receivedFinalUA:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if finalUA != "OpenAI/Go 3.56.0" {
		t.Fatalf("redirect receiver UA=%q", finalUA)
	}
	snapshot := readUserAgentTraceSnapshot(t, entry)
	if !snapshot.HeaderObserved || len(snapshot.UserAgents) != 2 || len(snapshot.Protocols) != 2 {
		t.Fatalf("redirect callback observations missing: %+v", snapshot)
	}
	for _, value := range snapshot.UserAgents {
		if value != finalUA {
			t.Fatalf("redirect encoded UA=%q", value)
		}
	}
	// 不断言响应返回时已经收到全部 WroteRequest 回调。
	if !strings.Contains(entry.Message, finalUA) || !strings.Contains(entry.Message, "final-receiver-request") {
		t.Fatalf("UA and response ID unavailable in log message: %q", entry.Message)
	}
	for _, secret := range []string{"original-query-secret", "final-query-secret"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("query retained in log: %q", secret)
		}
	}
}

func TestUpstreamUserAgentTraceDoesNotPairInterleavedCallbacks(t *testing.T) {
	ctx, observation := withUpstreamUserAgentTrace(context.Background())
	trace := httptrace.ContextClientTrace(ctx)
	// A 的写入结果在 B 的连接与编码事件之后到达；不能把它归到 B。
	trace.GotConn(httptrace.GotConnInfo{})
	trace.WroteHeaderField("User-Agent", []string{"first-UA"})
	trace.GotConn(httptrace.GotConnInfo{})
	trace.WroteHeaderField("user-agent", []string{"second-UA"})
	trace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("late A write failure")})
	path := filepath.Join(t.TempDir(), "identity.jsonl")
	logger, err := NewTrafficLogger(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	observation.log(logger, &store.Model{Name: "test"}, "https://relay.invalid/v1/chat/completions", "trace", 1, &http.Response{StatusCode: 200}, nil)
	logger.Close()
	snapshot := readUserAgentTraceSnapshot(t, readUserAgentTraceEntry(t, path))
	if len(snapshot.Protocols) != 2 || len(snapshot.UserAgents) != 2 || snapshot.UserAgents[0] != "first-UA" || snapshot.UserAgents[1] != "second-UA" || len(snapshot.WriteResults) != 1 || snapshot.WriteResults[0] {
		t.Fatalf("independent observations mixed: %+v", snapshot)
	}
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	trace.WroteHeaderField("user-agent", []string{"late-UA"})
	// 晚到事件不会篡改已记录的快照，也不会强制与连接数量对齐。
	old := readUserAgentTraceSnapshot(t, readUserAgentTraceEntry(t, path))
	if len(old.UserAgents) != 2 || len(old.WriteResults) != 1 {
		t.Fatal("late callback changed prior snapshot")
	}
	path = filepath.Join(t.TempDir(), "identity.jsonl")
	logger, err = NewTrafficLogger(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	observation.log(logger, &store.Model{Name: "test"}, "https://relay.invalid/v1/chat/completions", "trace", 1, nil, nil)
	logger.Close()
	snapshot = readUserAgentTraceSnapshot(t, readUserAgentTraceEntry(t, path))
	if len(snapshot.UserAgents) != 3 || snapshot.UserAgents[2] != "late-UA" || len(snapshot.Protocols) != 2 || len(snapshot.WriteResults) != 2 || snapshot.WriteResults[0] || !snapshot.WriteResults[1] {
		t.Fatalf("late callback outcomes mixed: %+v", snapshot)
	}
}
