package proxy

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"kiro-proxy/store"
)

const compatTokenRefusal = `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","param":"max_tokens","code":"unsupported_parameter"}}`

func compatTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}

type compatScriptSender struct {
	sent      [][]byte
	responses []*http.Response
}

func (s *compatScriptSender) sender() compatSender {
	return compatSender{send: func(body []byte) (*http.Response, error) {
		s.sent = append(s.sent, append([]byte(nil), body...))
		next := s.responses[0]
		s.responses = s.responses[1:]
		return next, nil
	}}
}

func compatTestSession(t *testing.T, caps *modelCapabilityStore, model *store.Model, protocol compatProtocol) (*compatSession, *requestUsageRecorder) {
	t.Helper()
	usage := newRequestUsageRecorder(nil, timeNowForCompatTest(), model, "/", "kiro")
	return newCompatSession(caps, model, protocol, "", autoGroupMockLogger(t), usage), usage
}

func compatUsageItem(usage *requestUsageRecorder) store.RequestUsage {
	usage.itemMu.Lock()
	defer usage.itemMu.Unlock()
	return usage.item
}

func TestCompatSessionRetriesOnceAndLearnsAfterSuccess(t *testing.T) {
	caps := newModelCapabilityStore()
	model := capabilityTestModel()
	session, usage := compatTestSession(t, caps, model, compatProtocolChat)
	script := &compatScriptSender{responses: []*http.Response{compatTestResponse(http.StatusOK, `{}`)}}
	resp, err := session.retry(compatTestResponse(http.StatusBadRequest, compatTokenRefusal), []byte(`{"max_tokens":64,"messages":[]}`), script.sender())
	if err != nil || resp.StatusCode != http.StatusOK || len(script.sent) != 1 {
		t.Fatalf("retry status=%v sends=%d err=%v", resp.StatusCode, len(script.sent), err)
	}
	assertCompatJSONEqual(t, script.sent[0], `{"max_completion_tokens":64,"messages":[]}`)
	if got, ok := caps.lookup(model, compatProtocolChat, capabilityTokenField, timeNowForCompatTest()); !ok || got.value != compatCompletionTokField || !strings.Contains(got.evidence, "max_completion_tokens") {
		t.Fatalf("observation after successful retry = %+v ok=%v", got, ok)
	}
	if reason := compatUsageItem(usage).DispatchReason; !strings.Contains(reason, "compat_retry:token_field=max_completion_tokens") || !strings.Contains(reason, "compat_learned:token_field=max_completion_tokens") {
		t.Fatalf("dispatch reason = %q", reason)
	}

	next, nextUsage := compatTestSession(t, caps, model, compatProtocolChat)
	body, err := next.applyLearned([]byte(`{"max_tokens":32,"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	assertCompatJSONEqual(t, body, `{"max_completion_tokens":32,"messages":[]}`)
	if reason := compatUsageItem(nextUsage).DispatchReason; !strings.Contains(reason, "compat_applied:token_field=max_completion_tokens") {
		t.Fatalf("applied dispatch reason = %q", reason)
	}
	other, _ := compatTestSession(t, caps, model, compatProtocolResponses)
	if body, _ := other.applyLearned([]byte(`{"max_tokens":32}`)); !strings.Contains(string(body), `"max_tokens"`) {
		t.Fatal("chat observation was applied to another protocol")
	}
}

func TestCompatSessionFailedRetryPassesThroughAndDoesNotLearn(t *testing.T) {
	caps := newModelCapabilityStore()
	model := capabilityTestModel()
	session, usage := compatTestSession(t, caps, model, compatProtocolResponses)
	refusal := `{"error":{"message":"Unsupported value: 'high' is not supported with this model. Supported values are: 'low' and 'medium'.","param":"reasoning.effort","code":"unsupported_value"}}`
	second := `{"error":{"message":"Unsupported value: 'medium' is not supported with this model. Supported values are: 'low' and 'high'.","param":"reasoning.effort"}}`
	script := &compatScriptSender{responses: []*http.Response{compatTestResponse(http.StatusBadRequest, second)}}
	resp, err := session.retry(compatTestResponse(http.StatusBadRequest, refusal), []byte(`{"reasoning":{"effort":"high"},"input":[]}`), script.sender())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest || string(got) != second || resp.ContentLength != int64(len(second)) {
		t.Fatalf("last upstream error was not passed through: status=%d body=%s", resp.StatusCode, got)
	}
	if len(script.sent) != 1 {
		t.Fatalf("same kind retried %d times, want once", len(script.sent))
	}
	if _, ok := caps.lookup(model, compatProtocolResponses, capabilityReasoningEffort, timeNowForCompatTest()); ok {
		t.Fatal("failed retry must not record an observation")
	}
	item := compatUsageItem(usage)
	if !strings.Contains(item.DispatchReason, "compat_retry_failed:http_400") || item.ReasoningEffort != "medium" {
		t.Fatalf("usage after failed retry = reason %q effort %q", item.DispatchReason, item.ReasoningEffort)
	}
}

func TestCompatSessionChainsDifferentKindsAndLearnsAll(t *testing.T) {
	caps := newModelCapabilityStore()
	model := capabilityTestModel()
	session, _ := compatTestSession(t, caps, model, compatProtocolChat)
	sampling := `{"error":{"message":"Unsupported value: 'temperature' does not support 0.2 with this model.","param":"temperature"}}`
	script := &compatScriptSender{responses: []*http.Response{compatTestResponse(http.StatusBadRequest, compatTokenRefusal), compatTestResponse(http.StatusOK, `{}`)}}
	resp, err := session.retry(compatTestResponse(http.StatusBadRequest, sampling), []byte(`{"temperature":0.2,"max_tokens":8,"messages":[]}`), script.sender())
	if err != nil || resp.StatusCode != http.StatusOK || len(script.sent) != 2 {
		t.Fatalf("chain status=%d sends=%d err=%v", resp.StatusCode, len(script.sent), err)
	}
	assertCompatJSONEqual(t, script.sent[1], `{"max_completion_tokens":8,"messages":[]}`)
	for _, kind := range []modelCapabilityKind{capabilitySampling, capabilityTokenField} {
		if _, ok := caps.lookup(model, compatProtocolChat, kind, timeNowForCompatTest()); !ok {
			t.Fatalf("%s was not learned after the chain succeeded", kind)
		}
	}
}

func TestCompatSessionConfiguredCapabilityPassesThrough(t *testing.T) {
	caps := newModelCapabilityStore()
	model := capabilityTestModel()
	caps.observe(model, compatProtocolChat, capabilityReasoningEffort, "low", "earlier observation", timeNowForCompatTest())
	// 用户随后声明了支持档位：配置优先，已有观测不再应用，被拒也不学习。
	model.SupportedReasoningEfforts = []string{"high"}
	session, _ := compatTestSession(t, caps, model, compatProtocolChat)
	body, err := session.applyLearned([]byte(`{"reasoning_effort":"high","messages":[]}`))
	if err != nil || !strings.Contains(string(body), `"high"`) {
		t.Fatalf("configured capability was rewritten from observation: %s err=%v", body, err)
	}
	refusal := `{"error":{"message":"gpt-x does not support reasoning effort \"high\"; use low or medium"}}`
	script := &compatScriptSender{}
	resp, err := session.retry(compatTestResponse(http.StatusBadRequest, refusal), body, script.sender())
	got, _ := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusBadRequest || string(got) != refusal || len(script.sent) != 0 {
		t.Fatalf("configured capability must pass the refusal through: status=%d sends=%d err=%v", resp.StatusCode, len(script.sent), err)
	}
}

func TestCompatSessionExhaustedBudgetAndUnknownRefusalPassThrough(t *testing.T) {
	session, _ := compatTestSession(t, newModelCapabilityStore(), capabilityTestModel(), compatProtocolChat)
	script := &compatScriptSender{}
	sender := script.sender()
	sender.budget = func() bool { return false }
	resp, err := session.retry(compatTestResponse(http.StatusBadRequest, compatTokenRefusal), []byte(`{"max_tokens":8}`), sender)
	if err != nil || resp.StatusCode != http.StatusBadRequest || len(script.sent) != 0 {
		t.Fatalf("exhausted budget sent %d requests", len(script.sent))
	}
	unknown := `{"error":{"message":"Unknown parameter: 'prompt_cache_options'."}}`
	resp, err = session.retry(compatTestResponse(http.StatusBadRequest, unknown), []byte(`{"max_tokens":8}`), script.sender())
	got, _ := io.ReadAll(resp.Body)
	if err != nil || string(got) != unknown || len(script.sent) != 0 {
		t.Fatalf("unrecognized refusal was not passed through intact: %s", got)
	}
}

func TestCompatSessionSyncsOutboundEffortForThinking(t *testing.T) {
	caps := newModelCapabilityStore()
	model := capabilityTestModel()
	model.Provider = "anthropic"
	offDecision := reasoningEffortDecision{Requested: "none", Wire: "none", Source: reasoningEffortSourceRequest}
	session, usage := compatTestSession(t, caps, model, compatProtocolAnthropic)
	usage.setReasoningEffortDecision(offDecision)
	refusal := `{"error":{"message":"The model always engages in thinking and the thinking cannot be disabled"}}`
	script := &compatScriptSender{responses: []*http.Response{compatTestResponse(http.StatusOK, `{}`)}}
	if _, err := session.retry(compatTestResponse(http.StatusBadRequest, refusal), []byte(`{"thinking":{"type":"disabled"},"max_tokens":4096,"messages":[]}`), script.sender()); err != nil {
		t.Fatal(err)
	}
	assertCompatJSONEqual(t, script.sent[0], `{"thinking":{"type":"enabled","budget_tokens":1024},"max_tokens":4096,"messages":[]}`)
	if item := compatUsageItem(usage); item.ReasoningEffort != "low" || item.RequestedReasoningEffort != "none" || item.ReasoningEffortSource != "request" {
		t.Fatalf("usage effort after rewrite = wire %q requested %q source %q", item.ReasoningEffort, item.RequestedReasoningEffort, item.ReasoningEffortSource)
	}

	next, nextUsage := compatTestSession(t, caps, model, compatProtocolAnthropic)
	nextUsage.setReasoningEffortDecision(offDecision)
	body, err := next.applyLearned([]byte(`{"thinking":{"type":"disabled"},"max_tokens":4096,"messages":[]}`))
	if err != nil || compatThinkingType(body) != "enabled" || compatUsageItem(nextUsage).ReasoningEffort != "low" {
		t.Fatalf("learned thinking shape: body=%s effort=%q err=%v", body, compatUsageItem(nextUsage).ReasoningEffort, err)
	}
	styled := *model
	styled.ThinkingStyle = "budget"
	configured, _ := compatTestSession(t, caps, &styled, compatProtocolAnthropic)
	if body, _ := configured.applyLearned([]byte(`{"thinking":{"type":"disabled"},"max_tokens":4096}`)); compatThinkingType(body) != "disabled" {
		t.Fatal("ThinkingStyle-configured model must not get learned thinking shapes")
	}
}
