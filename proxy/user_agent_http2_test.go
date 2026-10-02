package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/store"
)

func TestForwardCandidateSendsOpenAIUserAgentOverHTTP2(t *testing.T) {
	tests := []struct {
		name         string
		endpoint     string
		responseBody string
	}{
		{
			name:         "chat",
			endpoint:     "/v1/chat/completions",
			responseBody: "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n",
		},
		{
			name:         "responses",
			endpoint:     "/v1/responses",
			responseBody: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-test\"}}\n\n",
		},
	}

	type receivedRequest struct {
		protoMajor int
		userAgent  string
		sessionID  string
		requestID  string
		path       string
		body       []byte
		err        error
	}
	type identityLog struct {
		Category string `json:"category"`
		Metadata struct {
			TraceID           string   `json:"trace_id"`
			Attempt           int      `json:"attempt"`
			HeaderObserved    bool     `json:"ua_header_observed"`
			UserAgents        []string `json:"encoded_user_agents"`
			Protocols         []string `json:"negotiated_protocols"`
			UpstreamRequestID string   `json:"upstream_request_id"`
			ResponseProtocol  string   `json:"response_protocol"`
		} `json:"metadata"`
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			received := make(chan receivedRequest, 3)
			var requestCount atomic.Int32
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, readErr := io.ReadAll(r.Body)
				attempt := requestCount.Add(1)
				received <- receivedRequest{
					protoMajor: r.ProtoMajor,
					userAgent:  r.Header.Get("User-Agent"),
					sessionID:  r.Header.Get("session_id"),
					requestID:  "receiver-attempt-" + strconv.Itoa(int(attempt)),
					path:       r.URL.Path,
					body:       body,
					err:        readErr,
				}

				w.Header().Set("X-Request-Id", "receiver-attempt-"+strconv.Itoa(int(attempt)))
				if attempt == 1 {
					w.Header().Set("Retry-After", "0")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, `{}`)
					return
				}

				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, test.responseBody)
			}))
			upstream.EnableHTTP2 = true
			upstream.StartTLS()
			defer upstream.Close()

			forwarder := NewForwarder(nil, nil)
			forwarder.clients[""] = upstream.Client()
			defer forwarder.CloseIdleConnections()

			logPath := filepath.Join(t.TempDir(), "upstream-identity.jsonl")
			logger, err := NewTrafficLogger(logPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer logger.Close()

			model := &store.Model{
				Provider: "openai",
				ModelID:  "cn:deepseek-v4.1-flash",
				BaseURL:  upstream.URL,
				Endpoint: test.endpoint,
			}
			pre := &Preflight{
				Protocol: "kiro",
				Conv: &converter.ParsedConversation{
					ConversationID: "kiro-session-http2",
					CurrentContent: "check the outbound request",
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			var requestIDs []string
			for call := 0; call < 2; call++ {
				resp, _, err := forwarder.forwardCandidate(ctx, pre, model, logger, 5*time.Second)
				if err != nil {
					t.Fatalf("forwardCandidate call %d: %v", call+1, err)
				}
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("forwardCandidate call %d returned status %d, want %d", call+1, resp.StatusCode, http.StatusOK)
				}
				if _, err := io.Copy(io.Discard, resp.Body); err != nil {
					t.Fatalf("read response from call %d: %v", call+1, err)
				}
				if err := resp.Body.Close(); err != nil {
					t.Fatalf("close response from call %d: %v", call+1, err)
				}

				wantRequests := 2
				if call == 1 {
					wantRequests = 1
				}
				for attempt := 0; attempt < wantRequests; attempt++ {
					var got receivedRequest
					select {
					case got = <-received:
					case <-ctx.Done():
						t.Fatalf("waiting for HTTP/2 request: %v", ctx.Err())
					}
					if got.err != nil {
						t.Fatalf("read request body: %v", got.err)
					}
					requestIDs = append(requestIDs, got.requestID)
					if got.protoMajor != 2 {
						t.Errorf("request protocol = HTTP/%d, want HTTP/2", got.protoMajor)
					}
					if got.userAgent != "OpenAI/Go 3.56.0" {
						t.Errorf("User-Agent = %q, want %q", got.userAgent, "OpenAI/Go 3.56.0")
					}
					if got.sessionID != "kiro-session-http2" {
						t.Errorf("session_id = %q, want %q", got.sessionID, "kiro-session-http2")
					}
					if got.path != test.endpoint {
						t.Errorf("path = %q, want %q", got.path, test.endpoint)
					}

					var requestBody struct {
						Model string `json:"model"`
					}
					if err := json.Unmarshal(got.body, &requestBody); err != nil {
						t.Errorf("decode outbound body %q: %v", got.body, err)
					} else if requestBody.Model != "cn:deepseek-v4.1-flash" {
						t.Errorf("outbound model = %q, want %q", requestBody.Model, "cn:deepseek-v4.1-flash")
					}
				}
			}

			if got := requestCount.Load(); got != 3 {
				t.Fatalf("HTTP requests = %d, want 3 (first request, retry, and reused-client request)", got)
			}
			logger.Close()
			logData, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read sender identity log: %v", err)
			}
			var identityLogs []identityLog
			for _, line := range strings.Split(strings.TrimSpace(string(logData)), "\n") {
				var entry identityLog
				if err := json.Unmarshal([]byte(line), &entry); err != nil {
					t.Fatalf("decode sender log: %v", err)
				}
				if entry.Category == "upstream_identity" {
					identityLogs = append(identityLogs, entry)
				}
			}
			if len(identityLogs) != 3 {
				t.Fatalf("upstream_identity logs = %d, want 3", len(identityLogs))
			}
			for i, entry := range identityLogs {
				if !entry.Metadata.HeaderObserved || len(entry.Metadata.UserAgents) != 1 || entry.Metadata.UserAgents[0] != "OpenAI/Go 3.56.0" {
					t.Errorf("attempt %d encoded UA observations=%+v", i+1, entry.Metadata.UserAgents)
				}
				if len(entry.Metadata.Protocols) != 1 || entry.Metadata.Protocols[0] != "h2" {
					t.Errorf("attempt %d connection protocol observations=%+v", i+1, entry.Metadata.Protocols)
				}
				// 响应可先于 WroteRequest 回调返回；写入结果由 Mock 独立验证。
				if entry.Metadata.ResponseProtocol != "HTTP/2.0" {
					t.Errorf("attempt %d response_protocol = %q, want HTTP/2.0", i+1, entry.Metadata.ResponseProtocol)
				}
				if entry.Metadata.UpstreamRequestID != requestIDs[i] {
					t.Errorf("attempt %d upstream_request_id = %q, receiver ID = %q", i+1, entry.Metadata.UpstreamRequestID, requestIDs[i])
				}
				wantAttempt := i + 1
				if i == 2 {
					wantAttempt = 1
				}
				if entry.Metadata.Attempt != wantAttempt {
					t.Errorf("identity attempt number = %d, want %d", entry.Metadata.Attempt, wantAttempt)
				}
			}
			if identityLogs[0].Metadata.TraceID == "" || identityLogs[0].Metadata.TraceID != identityLogs[1].Metadata.TraceID {
				t.Errorf("retry trace IDs differ or are empty: %q and %q", identityLogs[0].Metadata.TraceID, identityLogs[1].Metadata.TraceID)
			}
			if identityLogs[2].Metadata.TraceID == identityLogs[0].Metadata.TraceID {
				t.Errorf("subsequent request reused prior trace ID %q", identityLogs[2].Metadata.TraceID)
			}
		})
	}
}
