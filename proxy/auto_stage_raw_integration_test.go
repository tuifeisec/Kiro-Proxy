package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"kiro-proxy/converter"
	"kiro-proxy/license"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/elazarl/goproxy"
)

func rawStageRequest(t *testing.T, model, conversation, stage string, currentTools int) *http.Request {
	t.Helper()
	// 完整历史里始终有旧工具结果；只有 metadata 标明的当轮结果影响锁。
	body := fmt.Sprintf(`{"model":%q,"stream":true,"instructions":"stable policy","prompt_cache_key":"client-stable","input":[{"type":"function_call","call_id":"old-call","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"old-call","output":"done"},{"role":"user","content":"new task"}],"metadata":{"kiro:conversation_id":%q,"kiro:agent_mode":"vibe","kiro:proxy_continuation_id":"","kiro:proxy_current_tool_results":%d},"extension":{"precise":90071992547409931234}}`, model, conversation, currentTools)
	req := httptest.NewRequest(http.MethodPost, "http://runtime.us-east-1.kiro.dev/v1/responses", strings.NewReader(body))
	if stage != "" {
		header, _ := json.Marshal(map[string]interface{}{"version": 1, "stage": stage, "conversationId": conversation, "agentMode": "vibe", "agentContinuationId": ""})
		req.Header.Set(converter.WorkflowStageHeader, string(header))
	}
	return req
}

func rawStageProxy(t *testing.T, env *autoStageIntegrationEnv, passthrough http.Handler) {
	t.Helper()
	env.server.forwarder = env.forward
	env.server.licenseClient = &typesafeClassifierTestLicense{result: license.CheckResult{Authorized: true}}
	env.server.proxy = goproxy.NewProxyHttpServer()
	if passthrough != nil {
		mock := httptest.NewServer(passthrough)
		t.Cleanup(mock.Close)
		parsed, _ := url.Parse(mock.URL)
		env.server.proxy.Tr.Proxy = nil
		env.server.proxy.Tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, parsed.Host)
		}
	}
	env.server.setupInterceptor()
}

func serveRawStage(t *testing.T, env *autoStageIntegrationEnv, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	env.server.proxy.ServeHTTP(recorder, req)
	return recorder
}

func TestStageRawRuntimeInterceptorRoutesAndFollowsStageBoundaries(t *testing.T) {
	oldMode := GetProxyMode()
	SetProxyMode(ModeForward)
	t.Cleanup(func() { SetProxyMode(oldMode) })
	env := newAutoStageIntegrationEnv(t)
	rawStageProxy(t, env, nil)
	for _, tc := range []struct {
		stage, want string
		tools       int
	}{
		{"run", "upstream-run", 0}, {"plan", "upstream-shared-responses", 0},
		{"implement", "upstream-implement", 0}, {"review", "upstream-shared-responses", 0},
		{"finalize", "upstream-finalize", 0}, {"investigate", "upstream-default", 0},
		{"plan", "upstream-shared-responses", 0}, {"implement", "upstream-implement", 1},
		{"implement", "upstream-implement", 0},
	} {
		resp := serveRawStage(t, env, rawStageRequest(t, "auto", "raw-same-thread", tc.stage, tc.tools))
		if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "response.completed") || strings.Contains(resp.Body.String(), "contentBlockDelta") {
			t.Fatalf("raw response was transformed/failed: %d %s", resp.Code, resp.Body.String())
		}
		got := <-env.requests
		if got.Path != "/v1/responses" || got.Model != tc.want || got.PromptCacheKey != "client-stable" || len(got.WorkflowHeaderValues) != 0 {
			t.Fatalf("unexpected outgoing route: %+v", got)
		}
	}
	rows, _, err := env.db.QueryRequestUsage("", "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 9 {
		t.Fatalf("raw usage missing: %d", len(rows))
	}
}

func TestStageRawRuntimeBoundaryAndPrivateMetadataRemoval(t *testing.T) {
	oldMode := GetProxyMode()
	SetProxyMode(ModeForward)
	t.Cleanup(func() { SetProxyMode(oldMode) })
	env := newAutoStageIntegrationEnv(t)
	var observedBody []byte
	var observedHeader http.Header
	hits := 0
	rawStageProxy(t, env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		observedBody, _ = io.ReadAll(r.Body)
		observedHeader = r.Header.Clone()
		_, _ = io.WriteString(w, "passthrough")
	}))
	for _, tc := range []struct {
		name    string
		req     *http.Request
		capture bool
	}{
		{"no header", rawStageRequest(t, "auto", "raw-boundary", "", 0), false},
		{"manual", rawStageRequest(t, "manual", "raw-boundary", "plan", 0), false},
		{"capture", rawStageRequest(t, "auto", "raw-boundary", "plan", 0), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.capture {
				SetProxyMode(ModeCapture)
			} else {
				SetProxyMode(ModeForward)
			}
			resp := serveRawStage(t, env, tc.req)
			if resp.Code != 200 || resp.Body.String() != "passthrough" {
				t.Fatalf("boundary changed: %d %s", resp.Code, resp.Body.String())
			}
			if len(observedHeader.Values(converter.WorkflowStageHeader)) > 0 {
				t.Fatal("private header leaked")
			}
			if tc.name != "no header" && strings.Contains(string(observedBody), "kiro:proxy_") {
				t.Fatalf("private metadata leaked: %s", observedBody)
			}
		})
	}
	if hits != 3 {
		t.Fatalf("passthrough calls=%d", hits)
	}
	select {
	case got := <-env.requests:
		t.Fatalf("unexpected stage upstream: %+v", got)
	default:
	}
	SetProxyMode(ModeForward)
	for _, modify := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set(converter.WorkflowStageHeader, "bad-json") },
		func(r *http.Request) {
			r.Header.Add(converter.WorkflowStageHeader, r.Header.Get(converter.WorkflowStageHeader))
		},
		func(r *http.Request) {
			r.Header.Set(converter.WorkflowStageHeader, `{"version":1,"stage":"plan","conversationId":"other","agentMode":"vibe","agentContinuationId":""}`)
		},
	} {
		req := rawStageRequest(t, "auto", "raw-boundary", "plan", 0)
		modify(req)
		resp := serveRawStage(t, env, req)
		if resp.Code != 400 {
			t.Fatalf("invalid candidate not rejected: %d %s", resp.Code, resp.Body.String())
		}
	}
	if hits != 3 {
		t.Fatalf("invalid stage request fell through to official Mock: %d", hits)
	}
}
