package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestForwardRawResponsesRetriesDeveloperRoleAndRemembersSystemRole(t *testing.T) {
	var calls atomic.Int32
	var lastBody atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		lastBody.Store(string(body))
		if strings.Contains(string(body), `"developer"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			// magpie codex_backend.go developerItemsAsSystem 记录的非 OpenAI Responses 后端报错。
			_, _ = io.WriteString(w, `{"error":{"message":"developer is not one of ['system', 'assistant', 'user', 'tool', 'function']"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, autoGroupCompletedRaw)
	}))
	defer server.Close()
	db := rawResponsesTestDB(t)
	f, _ := rawResponsesTestForwarder(t, db)
	model := rawResponsesTestModel(server.URL, "openai", "/responses")
	const requestBody = `{"model":"auto","input":[{"type":"message","role":"developer","content":"rules"},{"type":"message","role":"user","content":"hi"}]}`
	forward := func() int {
		req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(requestBody))
		resp, err := f.ForwardRawResponses(req, rawResponsesTestPre(db, model, req.URL.Path, requestBody), rawResponsesTestRoute(model), autoGroupMockLogger(t))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if status := forward(); status != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("first raw request status=%d calls=%d, want one compat retry", status, calls.Load())
	}
	if status := forward(); status != http.StatusOK || calls.Load() != 3 {
		t.Fatalf("learned role was not applied before send: status=%d calls=%d", status, calls.Load())
	}
	if body := lastBody.Load().(string); !strings.Contains(body, `"role":"system"`) || strings.Contains(body, `"developer"`) {
		t.Fatalf("second outbound body = %s", body)
	}
	reasons := ""
	for _, item := range rawResponsesUsageItems(t, db) {
		reasons += item.DispatchReason + "\n"
	}
	if !strings.Contains(reasons, "compat_learned:developer_role=system") || !strings.Contains(reasons, "compat_applied:developer_role=system") {
		t.Fatalf("dispatch reasons = %q", reasons)
	}
}

func TestAutoGroupRawSwitchesMemberOnFormatIncompatible(t *testing.T) {
	db := rawResponsesTestDB(t)
	f, _ := rawResponsesTestForwarder(t, db)
	first, second := autoGroupRawModels()
	var firstCalls, secondCalls atomic.Int32
	f.clients[""] = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "first.invalid" {
			firstCalls.Add(1)
			resp := autoGroupMockResponse(req, http.StatusBadRequest, io.NopCloser(strings.NewReader(`{"error":{"message":"Unsupported parameter: 'prompt_cache_key'","code":"unsupported_parameter"}}`)))
			resp.Header.Set("Content-Type", "application/json")
			return resp, nil
		}
		secondCalls.Add(1)
		return autoGroupMockResponse(req, http.StatusOK, io.NopCloser(strings.NewReader(autoGroupCompletedRaw))), nil
	})}
	req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", nil)
	resp, err := f.ForwardRawResponses(req, rawResponsesTestPre(db, nil, req.URL.Path, `{"model":"auto","input":[]}`), autoGroupRawRoute(first, second), autoGroupMockLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("status=%d first=%d second=%d", resp.StatusCode, firstCalls.Load(), secondCalls.Load())
	}
	if autoGroupHealthFailures(f, first) != 0 {
		t.Fatal("format incompatibility must be health neutral")
	}
	items := rawResponsesUsageItems(t, db)
	if len(items) != 1 || items[0].FailoverReason != "format_incompatible" || items[0].SelectedModelConfigID != second.ID {
		t.Fatalf("usage = %+v", items)
	}
}
