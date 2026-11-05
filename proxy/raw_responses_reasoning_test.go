package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

func decodeRawTestBody(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return root
}

func TestRawResponsesFitsClientEffortToModelTiers(t *testing.T) {
	const body = `{"model":"m","input":"hi","reasoning":{"effort":"max","summary":"auto"}}`
	responses := rawResponsesTestModel("https://upstream.invalid", "openai", "/responses")
	responses.SupportedReasoningEfforts = []string{"low", "medium"}
	prepared, err := prepareRawResponsesUpstreamBody([]byte(body), nil, responses)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(decodeRawTestBody(t, prepared)["reasoning"]); got != `{"effort":"medium","summary":"auto"}` {
		t.Fatalf("Responses reasoning = %s", got)
	}
	chat := rawResponsesTestModel("https://upstream.invalid", "openai", "/chat/completions")
	chat.SupportedReasoningEfforts = []string{"low", "medium"}
	prepared, err = prepareRawResponsesUpstreamBody([]byte(body), nil, chat)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(decodeRawTestBody(t, prepared)["reasoning_effort"]); got != `"medium"` {
		t.Fatalf("Chat reasoning_effort = %s", got)
	}
}

func TestRawResponsesDisabledReasoningFieldStripsClientFields(t *testing.T) {
	const body = `{"model":"m","input":"hi","reasoning":{"effort":"high","summary":"auto"},"reasoning_effort":"high"}`
	for _, endpoint := range []string{"/responses", "/chat/completions"} {
		model := rawResponsesTestModel("https://upstream.invalid", "openai", endpoint)
		model.DisableReasoningField = true
		fixed := "max"
		autoPre := &Preflight{SourceModel: "auto", AutoReasoningEffort: &fixed, Conv: &converter.ParsedConversation{ModelID: "auto"}}
		for _, pre := range []*Preflight{nil, autoPre} {
			prepared, err := prepareRawResponsesUpstreamBody([]byte(body), pre, model)
			if err != nil {
				t.Fatal(err)
			}
			root := decodeRawTestBody(t, prepared)
			if _, ok := root["reasoning"]; ok {
				t.Fatalf("%s still sent reasoning: %s", endpoint, prepared)
			}
			if _, ok := root["reasoning_effort"]; ok {
				t.Fatalf("%s still sent reasoning_effort: %s", endpoint, prepared)
			}
		}
	}
}

func TestRawAnthropicUsesResolvedWireEffort(t *testing.T) {
	model := rawResponsesTestModel("https://upstream.invalid", "anthropic", "/messages")
	cases := []struct {
		name, body, defaultEffort string
		disabled                  bool
		supported                 []string
		thinking, outputConfig    string
	}{
		// 模型名 "m" 不是 Claude，按 budget 形态构造（与 Kiro 主路径共用 BuildAnthropicThinking）
		{"model default applies", `{"model":"m","input":"hi"}`, "medium", false, nil, `{"type":"enabled","budget_tokens":4096}`, ""},
		{"xhigh fits declared high", `{"model":"m","input":"hi","reasoning":{"effort":"xhigh"}}`, "", false, []string{"none", "low", "medium", "high"}, `{"type":"enabled","budget_tokens":8192}`, ""},
		{"none disables thinking", `{"model":"m","input":"hi","reasoning":{"effort":"none"}}`, "", false, nil, `{"type":"disabled"}`, ""},
		{"disabled field sends nothing", `{"model":"m","input":"hi","reasoning":{"effort":"high"}}`, "high", true, nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := *model
			candidate.ReasoningEffort, candidate.DisableReasoningField = tc.defaultEffort, tc.disabled
			candidate.SupportedReasoningEfforts = tc.supported
			prepared, err := prepareRawResponsesAnthropicBody([]byte(tc.body), nil, &candidate)
			if err != nil {
				t.Fatal(err)
			}
			root := decodeRawTestBody(t, prepared)
			if got := string(root["thinking"]); got != tc.thinking {
				t.Fatalf("thinking = %q, want %q (%s)", got, tc.thinking, prepared)
			}
			if got := string(root["output_config"]); got != tc.outputConfig {
				t.Fatalf("output_config = %q, want %q (%s)", got, tc.outputConfig, prepared)
			}
		})
	}
	if _, err := prepareRawResponsesAnthropicBody([]byte(`{"model":"m","input":"hi","reasoning":{"effort":7}}`), nil, model); err == nil {
		t.Fatal("non-string effort must still be rejected for Anthropic")
	}
}

// 分组选 xhigh 时 anthropic 候选原来被整体跳过；现在按自身有效档位（此处声明到 high）映射为 high 并实际发送。
func TestModelGroupMapsEffortPerCandidateInsteadOfSkipping(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string][]byte{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		if _, seen := bodies[r.URL.Path]; !seen {
			bodies[r.URL.Path] = body
		}
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/messages") {
			http.Error(w, `{"error":{"message":"overloaded"}}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	anthropic := &store.Model{ID: "claude", ModelID: "claude-upstream", Provider: "anthropic", BaseURL: upstream.URL, Endpoint: "/v1/messages", APIKey: "k", Enabled: true,
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high"}}
	openai := &store.Model{ID: "gpt", ModelID: "gpt-upstream", Provider: "openai", BaseURL: upstream.URL, Endpoint: "/v1/chat/completions", APIKey: "k", Enabled: true}
	group := &store.ModelGroup{ID: "mixed", Name: "mixed", Enabled: true, MaxAttempts: 2, FirstByteTimeoutMS: 10000, ReasoningEffort: "xhigh"}
	route := &router.Route{Model: anthropic, Group: group, Candidates: []*store.Model{anthropic, openai},
		CandidateLimits: map[string]int{}, CandidateGroups: map[string]*store.ModelGroup{anthropic.ID: group, openai.ID: group}}
	pre := reasoningTestPre("", group.ReasoningEffort)
	pre.StartTime = time.Now()
	pre.usage = newRequestUsageRecorder(nil, pre.StartTime, nil, "/", "kiro")
	db, err := store.Open(filepath.Join(t.TempDir(), "group-effort.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stats := store.NewStats(db)
	defer stats.Close()
	f := NewForwarder(stats, db)
	defer f.CloseIdleConnections()
	resp, err := f.Forward(httptest.NewRequest(http.MethodPost, "http://runtime.test/", nil), pre, route, autoGroupMockLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(string(bodies["/v1/messages"]), `"thinking":{"type":"enabled","budget_tokens":8192}`) {
		t.Fatalf("anthropic candidate was skipped or not mapped to high: %s", bodies["/v1/messages"])
	}
	if !strings.Contains(string(bodies["/v1/chat/completions"]), `"reasoning_effort":"xhigh"`) {
		t.Fatalf("openai candidate did not keep xhigh: %s", bodies["/v1/chat/completions"])
	}
	if pre.usage.item.ReasoningEffort != "xhigh" || pre.usage.item.RequestedReasoningEffort != "xhigh" || pre.usage.item.ReasoningEffortSource != "group" {
		t.Fatalf("usage for final candidate = %+v", pre.usage.item)
	}
}

func TestModelGroupStillFailsWhenNoCandidateCanExpressEffort(t *testing.T) {
	onlyNone := &store.Model{ID: "none-only", ModelID: "x", Provider: "openai", BaseURL: "https://upstream.invalid", Endpoint: "/v1/chat/completions",
		APIKey: "k", Enabled: true, SupportedReasoningEfforts: []string{"none"}}
	group := &store.ModelGroup{ID: "g", Name: "none-group", Enabled: true, MaxAttempts: 1, FirstByteTimeoutMS: 10000, ReasoningEffort: "high"}
	route := &router.Route{Model: onlyNone, Group: group, Candidates: []*store.Model{onlyNone},
		CandidateLimits: map[string]int{}, CandidateGroups: map[string]*store.ModelGroup{onlyNone.ID: group}}
	pre := reasoningTestPre("", group.ReasoningEffort)
	_, err := NewForwarder(nil, nil).Forward(httptest.NewRequest(http.MethodPost, "http://runtime.test/", nil), pre, route, autoGroupMockLogger(t))
	if err == nil || !strings.Contains(err.Error(), "无法使用所选思考强度") {
		t.Fatalf("expected the unsupported-effort group error, got %v", err)
	}
}
