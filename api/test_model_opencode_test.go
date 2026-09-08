package api

import (
	"io"
	"kiro-proxy/store"
	"kiro-proxy/ua"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestModelProbeUserAgentFollowsProvider(t *testing.T) {
	received := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	seenSessions := make(map[string]bool)
	for _, provider := range []string{"openai", "anthropic", "gemini", "codex"} {
		for _, enabled := range []bool{false, true, true, false} {
			model := &store.Model{Provider: provider, BaseURL: upstream.URL, Endpoint: "/v1/chat/completions", EnableOpenCodeSession: enabled, APIKey: "test-token", AccessTokenExpiresAt: time.Now().Add(time.Hour).Unix()}
			result := runModelTest(model, "test")
			if !result.OK {
				t.Fatal(result.Error)
			}
			headers := <-received
			wantUA := ua.ProviderUserAgent(provider)
			if provider == "codex" {
				wantUA = "Codex Desktop/260202.0859 (darwin; arm64)"
			}
			if got := headers.Get("User-Agent"); got != wantUA {
				t.Fatalf("enabled=%v: UA=%q want=%q", enabled, got, wantUA)
			}
			if got := headers.Get("x-opencode-session"); enabled {
				if _, err := uuid.Parse(got); err != nil {
					t.Fatalf("enabled probe session is not a UUID: %q", got)
				}
				if seenSessions[got] {
					t.Fatalf("independent probes reused a session: %q", got)
				}
				seenSessions[got] = true
			} else if got != "" {
				t.Fatalf("disabled probe sent a session: %q", got)
			}
			for name, values := range headers {
				for _, value := range values {
					if strings.Contains(value, "kiro-proxy") {
						t.Fatalf("project identity in %s: %q", name, value)
					}
				}
			}
		}
	}
}

func TestModelProbeAgainstSessionRequiredUpstream(t *testing.T) {
	const missingSession = `{"type":"error","error":{"type":"MissingSessionID","message":"Request is missing x-opencode-session"}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-opencode-session") == "" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, missingSession)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"delta\":\"ok\"}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/responses"} {
		for _, enabled := range []bool{false, true, false} {
			model := &store.Model{Provider: "openai", BaseURL: upstream.URL, Endpoint: endpoint, EnableOpenCodeSession: enabled}
			result := runModelTest(model, "test")
			if enabled {
				if !result.OK {
					t.Fatalf("%s enabled probe failed: %s", endpoint, result.Error)
				}
			} else if result.OK || result.Error != "HTTP 400: "+missingSession {
				t.Fatalf("%s disabled probe did not preserve the upstream error: %+v", endpoint, result)
			}
		}
	}
}
