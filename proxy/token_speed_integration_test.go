package proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"kiro-proxy/converter"
	"kiro-proxy/store"
)

// Each scripted read is an upstream arrival, independent of consumer timing.
type tokenSpeedScriptBody struct {
	chunks     []string
	index      int
	beforeRead func(int)
}

func (b *tokenSpeedScriptBody) Read(p []byte) (int, error) {
	if b.index >= len(b.chunks) {
		return 0, io.EOF
	}
	if b.beforeRead != nil {
		b.beforeRead(b.index)
	}
	n := copy(p, b.chunks[b.index])
	b.chunks[b.index] = b.chunks[b.index][n:]
	if b.chunks[b.index] == "" {
		b.index++
	}
	return n, nil
}

func (*tokenSpeedScriptBody) Close() error { return nil }

func TestTokenSpeedObservesProbeOnceAndPersistsGPTVisibleUsage(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/speed.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	start := time.Now().Add(-5 * time.Second)
	model := &store.Model{ID: "gpt-config", ModelID: "gpt-test", Provider: "openai", Endpoint: "/v1/responses"}
	recorder := newRequestUsageRecorder(db, start, model, "/v1/responses", "responses")
	chunks := []string{
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\" world\"}\n\n",
		"event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"text\":\"hello world\"}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":1186,\"output_tokens_details\":{\"reasoning_tokens\":1024}}}}\n\n",
	}
	wantBody := strings.Join(chunks, "")
	now := start
	source := &tokenSpeedScriptBody{chunks: chunks, beforeRead: func(index int) { now = start.Add(time.Duration(index+1) * time.Second) }}
	forwarder := &Forwarder{clients: map[string]*http.Client{"": {Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: source, Request: req}, nil
	})}}}
	resp, err := forwarder.doRequest(context.Background(), "https://upstream.invalid/v1/responses", []byte(`{"stream":true}`), model, "", recorder)
	if err != nil {
		t.Fatal(err)
	}
	metrics := recorder.content
	if metrics == nil {
		t.Fatal("upstream body was not observed before probing")
	}
	metrics.now = func() time.Time { return now }
	if err := probeSSEPrecommit(context.Background(), resp, model.Provider, model.Endpoint, time.Second, 0); err != nil {
		t.Fatal(err)
	}
	firstAt := metrics.snapshot().firstContentAt
	calls := 0
	recorder.wrapResponseForModel(resp, model, model.Endpoint, func(at time.Time, kind string) {
		calls++
		if at != firstAt || kind != contentMetricText {
			t.Fatalf("latency hook was retimed: %v %s", at, kind)
		}
	})
	if recorder.content != metrics || calls != 1 {
		t.Fatal("finalization replaced probe measurement or lost hook")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if string(body) != wantBody {
		t.Fatal("probe changed stream bytes")
	}
	if calls != 1 {
		t.Fatalf("first-content hook called %d times", calls)
	}
	recorder.finish(http.StatusOK, localCompatUsage{OutputTokens: 1186}, "")
	page, err := db.QueryRequestUsagePage(store.RequestUsageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items=%d", len(page.Items))
	}
	item := page.Items[0]
	if item.OutputTokens != 1186 || item.SpeedOutputTokens != 162 || item.SpeedDurationNS != int64(time.Second) || item.TokensPerSecond == nil || *item.TokensPerSecond != 161 || item.TokenRateStatus != "ok" {
		t.Fatalf("wrong GPT speed after probe and persistence: %+v", item)
	}
	rows, err := db.QueryDailyStats(item.RequestDate, item.RequestDate)
	if err != nil || len(rows) != 1 || rows[0].GenerationSpeedSum != 161 || rows[0].GenerationSpeedSamples != 1 {
		t.Fatalf("daily=%+v err=%v", rows, err)
	}
}

func TestTokenSpeedLocalChatRequestsUsageWithoutOverwritingOptions(t *testing.T) {
	model := &store.Model{ID: "chat", ModelID: "gpt-test", Provider: "openai", BaseURL: "https://upstream.invalid", Endpoint: "/v1/chat/completions"}
	for _, tc := range []struct {
		name, options string
		want          bool
	}{
		{"missing", "", true},
		{"other options", `,"stream_options":{"include_obfuscation":false}`, true},
		{"explicit false", `,"stream_options":{"include_usage":false}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"client","stream":true,"messages":[]` + tc.options + `}`)
			prepared, err := (&Server{}).prepareLocalCompatCandidate(body, localCompatChat, model, true, "")
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				StreamOptions struct {
					IncludeUsage       bool  `json:"include_usage"`
					IncludeObfuscation *bool `json:"include_obfuscation"`
				} `json:"stream_options"`
			}
			if err := json.Unmarshal(prepared.upstreamBody, &got); err != nil {
				t.Fatal(err)
			}
			if got.StreamOptions.IncludeUsage != tc.want {
				t.Fatalf("include_usage=%v", got.StreamOptions.IncludeUsage)
			}
			if tc.name == "other options" && (got.StreamOptions.IncludeObfuscation == nil || *got.StreamOptions.IncludeObfuscation) {
				t.Fatal("lost obfuscation option")
			}
		})
	}
}

func TestTokenSpeedCustomURLUsesConvertedProtocolBeforeProbe(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		classifier, compatibilityRetry bool
		wantProtocol                   contentMetricProtocol
	}{
		{"Responses at custom URL", false, false, contentMetricProtocolResponses},
		{"classifier Chat at custom URL", true, false, contentMetricProtocolChat},
		{"Responses retries through Chat", false, true, contentMetricProtocolChat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(t.TempDir() + "/speed.db")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			start := time.Now().Add(-5 * time.Second)
			model := &store.Model{ID: "custom", ModelID: "gpt-test", Provider: "openai", BaseURL: "https://upstream.invalid/custom", Endpoint: "/responses", DisableAutoSuffix: true}
			pre := &Preflight{Protocol: "kiro", StartTime: start, IsClassifier: tc.classifier, Conv: &converter.ParsedConversation{IsClassifier: tc.classifier, ConversationID: "speed-test", CurrentContent: "hello"}}
			if tc.compatibilityRetry {
				pre.Conv.History = []converter.ParsedHistoryEntry{{HasAssistant: true, ToolUses: []converter.ParsedToolUse{{ToolUseID: "call_1", Name: "lookup", Input: json.RawMessage(`{}`)}}}}
			}
			pre.usage = newRequestUsageRecorder(db, start, model, "/", "kiro")
			now, calls := start, 0
			forwarder := NewForwarder(store.NewStats(db), db)
			forwarder.clients[""] = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				requestBody, err := io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(requestBody, &payload); err != nil {
					return nil, err
				}
				wantRequestProtocol := tc.wantProtocol
				if tc.compatibilityRetry && calls == 1 {
					wantRequestProtocol = contentMetricProtocolResponses
				}
				if wantRequestProtocol == contentMetricProtocolResponses && payload["input"] == nil || wantRequestProtocol == contentMetricProtocolChat && payload["messages"] == nil {
					t.Fatalf("actual request does not match %s: %s", wantRequestProtocol, requestBody)
				}
				if tc.compatibilityRetry && calls == 1 {
					return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"The reasoning_text in the thinking mode must be passed back to the API"}}`)), Request: req}, nil
				}
				chunks := []string{
					"data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n",
					"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\n",
					"data: {\"choices\":[],\"usage\":{\"completion_tokens\":10,\"completion_tokens_details\":{\"reasoning_tokens\":0}}}\n\ndata: [DONE]\n\n",
				}
				if tc.wantProtocol == contentMetricProtocolResponses {
					chunks = []string{
						"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"a\"}\n\n",
						"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"b\"}\n\n",
						"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":10,\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n",
					}
				}
				source := &tokenSpeedScriptBody{chunks: chunks, beforeRead: func(index int) { now = start.Add(time.Duration(index+1) * time.Second) }}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: source, Request: req}, nil
			})}
			logger, err := NewTrafficLogger("", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, _, err := forwarder.forwardCandidate(context.Background(), pre, model, logger, 0)
			if err != nil {
				t.Fatal(err)
			}
			metrics := pre.usage.content
			if metrics == nil || metrics.protocol != tc.wantProtocol {
				t.Fatalf("observer protocol=%+v want %s", metrics, tc.wantProtocol)
			}
			metrics.now = func() time.Time { return now }
			if err := probeSSEPrecommit(context.Background(), resp, model.Provider, model.BaseURL, time.Second, 0); err != nil {
				t.Fatal(err)
			}
			pre.usage.wrapResponseForModel(resp, model, model.Endpoint, nil)
			if _, err := io.Copy(io.Discard, resp.Body); err != nil {
				t.Fatal(err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
			pre.usage.finish(http.StatusOK, localCompatUsage{OutputTokens: 10}, "")
			page, err := db.QueryRequestUsagePage(store.RequestUsageQuery{})
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("page=%+v error=%v", page, err)
			}
			if item := page.Items[0]; item.TokensPerSecond == nil || *item.TokensPerSecond != 9 || item.SpeedOutputTokens != 10 || item.SpeedDurationNS != int64(time.Second) {
				t.Fatalf("wrong custom URL speed: %+v", item)
			}
			wantCalls := 1
			if tc.compatibilityRetry {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d want=%d", calls, wantCalls)
			}
		})
	}
}

func TestTokenSpeedLocalAPIDeterminesProtocolForCustomURL(t *testing.T) {
	model := &store.Model{ID: "custom", ModelID: "gpt-test", Provider: "openai", BaseURL: "https://upstream.invalid/custom", Endpoint: "/responses", DisableAutoSuffix: true}
	for _, api := range []localCompatAPI{localCompatChat, localCompatResponses} {
		body := []byte(`{"stream":true,"messages":[]}`)
		want := contentMetricProtocolChat
		if api == localCompatResponses {
			body = []byte(`{"stream":true,"input":"hello"}`)
			want = contentMetricProtocolResponses
		}
		prepared, err := (&Server{}).prepareLocalCompatCandidate(body, api, model, true, "")
		if err != nil {
			t.Fatal(err)
		}
		if prepared.contentProtocol != want || prepared.upstreamURL != model.BaseURL {
			t.Fatalf("api=%s prepared=%+v", api, prepared)
		}
	}
}
