package proxy

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"
	"kiro-proxy/ua"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 覆盖真实 Kiro JSON、数据库路由快照和 HTTP 序列化，避免只验证 doRequest 的入参。
func TestKiroSessionReachesHTTPUpstreamThroughRouter(t *testing.T) {
	received := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
		}
		received <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-test\",\"status\":\"completed\",\"output\":[],\"usage\":{}}}\n\n")
	}))
	defer upstream.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "kiro-sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stats := store.NewStats(db)
	defer stats.Close()
	f := NewForwarder(stats, db)
	defer f.CloseIdleConnections()
	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	model := store.Model{ID: "kiro-model", Name: "kiro-model", ModelID: "deepseek-v4-flash", Provider: "openai", BaseURL: upstream.URL, Endpoint: "/v1/responses", Enabled: true}
	if err := db.CreateModel(&model); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateModelGroup(&store.ModelGroup{ID: "kiro-group", Name: "kiro-group", Enabled: true, Members: []store.ModelGroupMember{{ModelID: model.ID}}}); err != nil {
		t.Fatal(err)
	}
	routes := router.New(db)
	s := &Server{}
	for _, enabled := range []bool{false, true, false} {
		model.EnableOpenCodeSession = enabled
		if err := db.UpdateModel(&model); err != nil {
			t.Fatal(err)
		}
		routes.Reload()
		for _, routeID := range []string{model.ID, "kiro-group"} {
			for _, sessionID := range []string{"sess-test-a", "sess-test-a", "sess-test-b"} {
				body, err := json.Marshal(map[string]any{
					"conversationState": map[string]any{
						"conversationId": sessionID,
						"history": []any{
							map[string]any{"userInputMessage": map[string]any{"content": "Read the test file", "modelId": routeID}},
							map[string]any{"assistantResponseMessage": map[string]any{"content": "", "toolUses": []any{map[string]any{"toolUseId": "read-1", "name": "read_file", "input": map[string]any{"path": "test.go"}}}}},
						},
						"currentMessage": map[string]any{"userInputMessage": map[string]any{
							"content": "", "modelId": routeID,
							"userInputMessageContext": map[string]any{"toolResults": []any{map[string]any{"toolUseId": "read-1", "status": "success", "content": []any{map[string]any{"text": "package test"}}}}},
						}},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodPost, "http://kiro.invalid/", strings.NewReader(string(body)))
				req.Header.Set("User-Agent", "Go-http-client/1.1")
				pre, err := s.parseRequest(req, "kiro", nil)
				if err != nil {
					t.Fatal(err)
				}
				if pre.Conv == nil || len(pre.Conv.CurrentToolResults) == 0 {
					t.Fatal("fixture did not parse as a Kiro tool-result round")
				}
				if pre.SourceModel != routeID {
					t.Fatalf("parsed model = %q, want %q", pre.SourceModel, routeID)
				}
				route := routes.Resolve(pre.SourceModel)
				if route == nil || route.Model == nil || route.Model.ID != model.ID || (route.Group != nil) != (routeID == "kiro-group") {
					t.Fatalf("unexpected route for %q: %+v", routeID, route)
				}
				resp, err := f.Forward(req, pre, route, logger)
				if err != nil {
					t.Fatal(err)
				}
				_, readErr := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if readErr != nil {
					t.Fatal(readErr)
				}
				select {
				case headers := <-received:
					want := ""
					if enabled {
						want = sessionID
					}
					if headers.Get("x-opencode-session") != want || headers.Get("session_id") != sessionID || headers.Get("User-Agent") != ua.ProviderUserAgent("openai") {
						t.Fatalf("route=%s enabled=%v session=%s: unexpected session/UA headers: %v", routeID, enabled, sessionID, headers)
					}
				default:
					t.Fatal("HTTP upstream did not receive the Kiro request")
				}
			}
		}
	}
}

func TestLocalCompatRealSessionIdentity(t *testing.T) {
	for _, header := range []string{"x-opencode-session", "x-conversation-id", "x-session-id", "session_id", "conversation_id", "session-id"} {
		r := httptest.NewRequest("POST", "/v1/responses", nil)
		r.Header.Set(header, " conversation-a ")
		if got := localCompatSessionID(r, []byte(`{"conversation_id":"body-id"}`)); got != "conversation-a" {
			t.Fatalf("%s: got %q", header, got)
		}
		r.Header.Set("x-opencode-session", "explicit-opencode")
		if got := localCompatSessionID(r, nil); got != "explicit-opencode" {
			t.Fatalf("OpenCode header precedence: %q", got)
		}
	}
	for _, body := range []string{
		`{"prompt_cache_key":"shared","instructions":"same tools and system prompt"}`,
		`{"previous_response_id":"changes-every-turn","input":"question"}`,
		`{"conversation_id":123}`, `invalid`,
	} {
		if got := localCompatSessionID(nil, []byte(body)); got != "" {
			t.Fatalf("inferred a conversation from %s: %q", body, got)
		}
	}
	if got := localCompatSessionID(nil, []byte(`{"conversation_id":" body-a "}`)); got != "body-a" {
		t.Fatalf("body ID = %q", got)
	}
	rr := httptest.NewRecorder()
	writeLocalCORSHeaders(rr)
	if !strings.Contains(rr.Header().Get("Access-Control-Allow-Headers"), "x-opencode-session") {
		t.Fatal("browser preflight cannot send session header")
	}
}

func TestLocalCompatForwardsDistinctStableSessions(t *testing.T) {
	received := make(chan http.Header, 16)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/messages":
			io.WriteString(w, `{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{}}`)
		case "/v1/responses":
			io.WriteString(w, `{"id":"resp","status":"completed","output":[],"usage":{}}`)
		default:
			io.WriteString(w, `{"id":"chat","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{}}`)
		}
	}))
	defer upstream.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stats := store.NewStats(db)
	defer stats.Close()
	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	f := NewForwarder(stats, db)
	defer f.CloseIdleConnections()
	s := &Server{db: db, stats: stats, logger: logger, forwarder: f}
	for _, api := range []localCompatAPI{localCompatChat, localCompatResponses, localCompatMessages} {
		provider := "openai"
		endpoint := "/v1/chat/completions"
		body := `{"model":"model","stream":false,"messages":[{"role":"user","content":"fix a unit test"}]}`
		if api == localCompatResponses {
			endpoint = "/v1/responses"
			body = `{"model":"model","stream":false,"input":"fix a unit test","prompt_cache_key":"shared-prefix"}`
		}
		if api == localCompatMessages {
			provider, endpoint = "anthropic", "/v1/messages"
		}
		model := store.Model{ID: "model", Name: "model", ModelID: "upstream", Provider: provider, BaseURL: upstream.URL, Endpoint: endpoint, Enabled: true}
		if api == localCompatChat {
			err = db.CreateModel(&model)
		} else {
			err = db.UpdateModel(&model)
		}
		if err != nil {
			t.Fatal(err)
		}
		if api == localCompatChat {
			group := &store.ModelGroup{ID: "group", Name: "group", Enabled: true, Members: []store.ModelGroupMember{{ModelID: model.ID}}}
			if err := db.CreateModelGroup(group); err != nil {
				t.Fatal(err)
			}
		}
		for _, enabled := range []bool{false, true, false} {
			model.EnableOpenCodeSession = enabled
			if err := db.UpdateModel(&model); err != nil {
				t.Fatal(err)
			}
			s.router = router.New(db)
			for _, routeName := range []string{"model", "group"} {
				for _, session := range []string{"conversation-a", "conversation-a", "conversation-b", ""} {
					routeBody := strings.Replace(body, `"model":"model"`, `"model":"`+routeName+`"`, 1)
					r := httptest.NewRequest("POST", endpoint, strings.NewReader(routeBody))
					r.RemoteAddr = "127.0.0.1:12345"
					if session != "" {
						r.Header.Set("session_id", session)
					}
					rr := httptest.NewRecorder()
					s.handleLocalCompat(rr, r, api)
					if rr.Code != http.StatusOK {
						t.Fatalf("%s: HTTP %d: %s", api, rr.Code, rr.Body.String())
					}
					select {
					case headers := <-received:
						wantSession, wantUA := "", ua.ProviderUserAgent(provider)
						if enabled {
							wantSession = session
						}
						if got := headers.Get("x-opencode-session"); got != wantSession {
							t.Fatalf("%s enabled=%v: session = %q want %q", api, enabled, got, wantSession)
						}
						if got := headers.Get("session_id"); got != session {
							t.Fatalf("native session lost: %q", got)
						}
						if got := headers.Get("User-Agent"); got != wantUA {
							t.Fatalf("UA = %q", got)
						}
					default:
						t.Fatal("upstream request missing")
					}
				}
			}
		}
	}
}

func TestOpenCodeSessionIsExplicitPerModel(t *testing.T) {
	f := NewForwarder(nil, nil)
	defer f.CloseIdleConnections()
	var headers http.Header
	f.clients[""] = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		headers = r.Header.Clone()
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})}
	for _, url := range []string{"https://opencode.ai/zen/go/v1/responses", "https://custom-relay.invalid/v1/chat/completions"} {
		for _, provider := range []string{"openai", "anthropic", "gemini", "codex"} {
			for _, sessionID := range []string{"same-session", ""} {
				for _, enabled := range []bool{false, true, false} {
					model := &store.Model{Provider: provider, EnableOpenCodeSession: enabled, APIKey: "test-token", AccessTokenExpiresAt: time.Now().Add(time.Hour).Unix()}
					resp, err := f.doRequest(context.Background(), url, []byte(`{}`), model, sessionID, nil)
					if err != nil {
						t.Fatal(err)
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					want := ""
					if enabled {
						want = sessionID
					}
					if got := headers.Get("x-opencode-session"); got != want {
						t.Fatalf("url=%s enabled=%v: session=%q want=%q", url, enabled, got, want)
					}
					wantUA := ua.ProviderUserAgent(provider)
					if provider == "codex" {
						wantUA = "Codex Desktop/260202.0859 (darwin; arm64)"
						if sessionID == "" {
							if _, err := uuid.Parse(headers.Get("x-client-request-id")); err != nil {
								t.Fatalf("Codex empty-session request ID is not a UUID: %v", err)
							}
						}
					}
					if got := headers.Get("User-Agent"); got != wantUA {
						t.Fatalf("provider=%s enabled=%v: UA=%q want=%q", provider, enabled, got, wantUA)
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
	}
}

func TestKiroClassifierAndRetryKeepRealSession(t *testing.T) {
	var seen []string
	f := NewForwarder(nil, nil)
	defer f.CloseIdleConnections()
	f.clients[""] = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.Header.Get("x-opencode-session"))
		if got := r.Header.Get("User-Agent"); got != "OpenAI/Go 3.56.0" {
			t.Errorf("attempt %d: User-Agent = %q, want OpenAI/Go 3.56.0", len(seen), got)
		}
		status := http.StatusOK
		if len(seen) == 1 {
			status = http.StatusServiceUnavailable
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"0"}, "Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))}, nil
	})}
	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	model := &store.Model{Provider: "openai", ModelID: "test", BaseURL: "http://upstream.invalid", Endpoint: "/v1/chat/completions", EnableOpenCodeSession: true}
	for _, classifier := range []bool{false, true} {
		pre := &Preflight{Protocol: "kiro", Conv: &converter.ParsedConversation{ConversationID: "kiro-conversation", IsClassifier: classifier, CurrentContent: "fix a test"}}
		resp, _, err := f.forwardCandidate(context.Background(), pre, model, logger, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if len(seen) != 3 {
		t.Fatalf("attempts = %d, want main + retry + classifier", len(seen))
	}
	for _, id := range seen {
		if id != "kiro-conversation" {
			t.Fatalf("session changed: %v", seen)
		}
	}
}
