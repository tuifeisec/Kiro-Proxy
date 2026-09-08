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
)

func TestModelProbeUserAgentFollowsProvider(t *testing.T) {
	received := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	for _, provider := range []string{"openai", "anthropic", "gemini", "codex"} {
		for _, enabled := range []bool{false, true, false} {
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
			if got := headers.Get("x-opencode-session"); got != "" {
				t.Fatalf("standalone probe invented a session: %q", got)
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
