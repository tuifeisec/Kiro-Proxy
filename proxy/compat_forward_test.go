package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

const compatChatSuccessSSE = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"

func timeNowForCompatTest() time.Time { return time.Now() }

func compatKiroPre(t *testing.T, db *store.DB, conversationID string) *Preflight {
	t.Helper()
	return compatKiroPreWithInference(t, db, conversationID, `{"temperature":0.3}`)
}

func compatKiroPreWithInference(t *testing.T, db *store.DB, conversationID, inference string) *Preflight {
	t.Helper()
	body := []byte(`{"conversationState":{"conversationId":"` + conversationID + `","currentMessage":{"userInputMessage":{"content":"hello","modelId":"m"}}},"inferenceConfig":` + inference + `}`)
	conv, err := converter.ParseKiroRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	pre := &Preflight{Body: body, Conv: conv, Protocol: "kiro", StartTime: time.Now(), ConversationID: conversationID}
	pre.usage = newRequestUsageRecorder(db, pre.StartTime, nil, "/", "kiro")
	return pre
}

func compatChatModel(id, baseURL string) *store.Model {
	return &store.Model{ID: id, Name: id, ModelID: id, Provider: "openai", BaseURL: baseURL, Endpoint: "/chat/completions", APIKey: "test", Enabled: true, UpdatedAt: "2026-10-01T00:00:00Z"}
}

func compatGroupRoute(models ...*store.Model) *router.Route {
	group := &store.ModelGroup{ID: "compat-group", Name: "Compat", Enabled: true, MaxAttempts: len(models), FirstByteTimeoutMS: 5000}
	route := &router.Route{Model: models[0], Group: group, Candidates: models, CandidateLimits: map[string]int{}, CandidateGroups: map[string]*store.ModelGroup{}}
	for _, model := range models {
		group.Members = append(group.Members, store.ModelGroupMember{ModelID: model.ID})
		route.CandidateGroups[model.ID] = group
	}
	return route
}

type compatRecordingUpstream struct {
	mu     sync.Mutex
	bodies map[string][][]byte
}

func (u *compatRecordingUpstream) record(r *http.Request) (string, []byte) {
	body, _ := io.ReadAll(r.Body)
	var request struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &request)
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.bodies == nil {
		u.bodies = make(map[string][][]byte)
	}
	u.bodies[request.Model] = append(u.bodies[request.Model], body)
	return request.Model, body
}

func (u *compatRecordingUpstream) calls(model string) [][]byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.bodies[model]
}

func compatForward(t *testing.T, f *Forwarder, pre *Preflight, route *router.Route) string {
	t.Helper()
	resp, err := f.Forward(httptest.NewRequest(http.MethodPost, "http://runtime.test/", strings.NewReader(string(pre.Body))), pre, route, autoGroupMockLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return string(data)
}

func TestForwardKiroLearnsRejectedSamplingAndAppliesItBeforeNextSend(t *testing.T) {
	upstream := &compatRecordingUpstream{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, body := upstream.record(r)
		if strings.Contains(string(body), `"temperature"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Unsupported value: 'temperature' does not support 0.3 with this model. Only the default (1) value is supported.","param":"temperature","code":"unsupported_value"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, compatChatSuccessSSE)
	}))
	defer server.Close()
	db := rawResponsesTestDB(t)
	f, _ := rawResponsesTestForwarder(t, db)
	model := compatChatModel("reasoner", server.URL)
	route := &router.Route{Model: model, Candidates: []*store.Model{model}}

	first := compatKiroPre(t, db, "compat-1")
	compatForward(t, f, first, route)
	calls := upstream.calls("reasoner")
	if len(calls) != 2 || strings.Contains(string(calls[1]), `"temperature"`) {
		t.Fatalf("first request should retry once without temperature: calls=%d", len(calls))
	}
	if reason := compatUsageItem(first.usage).DispatchReason; !strings.Contains(reason, "compat_learned:sampling=omit") {
		t.Fatalf("first dispatch reason = %q", reason)
	}

	second := compatKiroPre(t, db, "compat-2")
	compatForward(t, f, second, route)
	if calls := upstream.calls("reasoner"); len(calls) != 3 || strings.Contains(string(calls[2]), `"temperature"`) {
		t.Fatalf("learned shape was not applied before the next send: calls=%d", len(calls))
	}
	if reason := compatUsageItem(second.usage).DispatchReason; !strings.Contains(reason, "compat_applied:sampling=omit") || strings.Contains(reason, "compat_retry") {
		t.Fatalf("second dispatch reason = %q", reason)
	}
}

func TestForwardKiroLearnedTokenFieldOverridesStaticHostRule(t *testing.T) {
	upstream := &compatRecordingUpstream{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, body := upstream.record(r)
		if strings.Contains(string(body), `"max_tokens"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			// magpie provider/token_field.go 记录的 OpenAI 报错；此处由非官方 host 的中转返回。
			_, _ = io.WriteString(w, compatTokenRefusal)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, compatChatSuccessSSE)
	}))
	defer server.Close()
	db := rawResponsesTestDB(t)
	f, _ := rawResponsesTestForwarder(t, db)
	model := compatChatModel("relay", server.URL)
	route := &router.Route{Model: model, Candidates: []*store.Model{model}}
	// 测试 host 不是 OpenAI 官方，静态规则 ChatTokenLimitField 选 max_tokens。
	compatForward(t, f, compatKiroPreWithInference(t, db, "token-1", `{"maxTokens":256}`), route)
	if calls := upstream.calls("relay"); len(calls) != 2 || !strings.Contains(string(calls[1]), `"max_completion_tokens":256`) {
		t.Fatalf("first request should retry once with max_completion_tokens: calls=%d", len(calls))
	}
	second := compatKiroPreWithInference(t, db, "token-2", `{"maxTokens":128}`)
	compatForward(t, f, second, route)
	calls := upstream.calls("relay")
	if len(calls) != 3 || !strings.Contains(string(calls[2]), `"max_completion_tokens":128`) || strings.Contains(string(calls[2]), `"max_tokens"`) {
		t.Fatalf("learned token field did not override the static host rule: calls=%d", len(calls))
	}
	if reason := compatUsageItem(second.usage).DispatchReason; !strings.Contains(reason, "compat_applied:token_field=max_completion_tokens") {
		t.Fatalf("dispatch reason = %q", reason)
	}
}

func TestForwardKiroGroupSwitchesMemberOnFormatIncompatible(t *testing.T) {
	upstream := &compatRecordingUpstream{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if model, _ := upstream.record(r); model == "first" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Unknown parameter: 'stream_options'.","param":"stream_options","code":"unknown_parameter"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, compatChatSuccessSSE)
	}))
	defer server.Close()
	db := rawResponsesTestDB(t)
	f, _ := rawResponsesTestForwarder(t, db)
	first, second := compatChatModel("first", server.URL), compatChatModel("second", server.URL)
	pre := compatKiroPre(t, db, "compat-group")
	compatForward(t, f, pre, compatGroupRoute(first, second))
	if len(upstream.calls("first")) != 1 || len(upstream.calls("second")) != 1 {
		t.Fatalf("calls first=%d second=%d", len(upstream.calls("first")), len(upstream.calls("second")))
	}
	item := compatUsageItem(pre.usage)
	if item.FailoverReason != "format_incompatible" || item.SelectedModelConfigID != "second" || item.FailoverCount != 1 {
		t.Fatalf("usage after format failover = %+v", item)
	}
	if autoGroupHealthFailures(f, first) != 0 {
		t.Fatal("format incompatibility must be health neutral")
	}
}

func TestForwardKiroDoesNotSwitchMemberForNonFormatRejections(t *testing.T) {
	cases := map[string]string{
		"context too long": `{"error":{"message":"This model's maximum context length is 128000 tokens. However, your messages resulted in 130000 tokens.","code":"context_length_exceeded"}}`,
		"auth as 400":      `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}`,
	}
	for name, refusal := range cases {
		t.Run(name, func(t *testing.T) {
			upstream := &compatRecordingUpstream{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstream.record(r)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, refusal)
			}))
			defer server.Close()
			db := rawResponsesTestDB(t)
			f, _ := rawResponsesTestForwarder(t, db)
			pre := compatKiroPre(t, db, "compat-no-switch")
			compatForward(t, f, pre, compatGroupRoute(compatChatModel("first", server.URL), compatChatModel("second", server.URL)))
			if len(upstream.calls("first")) != 1 || len(upstream.calls("second")) != 0 {
				t.Fatalf("calls first=%d second=%d", len(upstream.calls("first")), len(upstream.calls("second")))
			}
			if reason := compatUsageItem(pre.usage).FailoverReason; reason != "" {
				t.Fatalf("failover reason = %q", reason)
			}
		})
	}
}

func TestForwardKiroSingleModelPassesFormatRejectionThrough(t *testing.T) {
	upstream := &compatRecordingUpstream{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.record(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"Unknown parameter: 'stream_options'.","code":"unknown_parameter"}}`)
	}))
	defer server.Close()
	db := rawResponsesTestDB(t)
	f, _ := rawResponsesTestForwarder(t, db)
	model := compatChatModel("only", server.URL)
	pre := compatKiroPre(t, db, "compat-single")
	output := compatForward(t, f, pre, &router.Route{Model: model, Candidates: []*store.Model{model}})
	if len(upstream.calls("only")) != 1 || !strings.Contains(compatKiroStreamText(output), "stream_options") {
		t.Fatalf("single-model rejection was not passed through: calls=%d text=%q", len(upstream.calls("only")), compatKiroStreamText(output))
	}
}

// compatKiroStreamText 拼接 Kiro 事件流中逐段发送的 content 文本。
func compatKiroStreamText(stream string) string {
	var builder strings.Builder
	for _, match := range compatKiroContentPattern.FindAllStringSubmatch(stream, -1) {
		var piece string
		if json.Unmarshal([]byte(`"`+match[1]+`"`), &piece) == nil {
			builder.WriteString(piece)
		}
	}
	return builder.String()
}

var compatKiroContentPattern = regexp.MustCompile(`\{"content":"((?:[^"\\]|\\.)*)"`)
