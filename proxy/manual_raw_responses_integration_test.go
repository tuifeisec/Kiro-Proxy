package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kiro-proxy/converter"
	"kiro-proxy/store"
)

type manualRawObservation struct {
	Model         string
	Authorization string
	Body          []byte
	Workflow      []string
}

func newManualRawUpstream(t *testing.T) (*httptest.Server, chan manualRawObservation) {
	t.Helper()
	observations := make(chan manualRawObservation, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request", http.StatusBadRequest)
			return
		}
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "decode request", http.StatusBadRequest)
			return
		}
		observations <- manualRawObservation{
			Model: payload.Model, Authorization: r.Header.Get("Authorization"),
			Body: body, Workflow: append([]string(nil), r.Header.Values(converter.WorkflowStageHeader)...),
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			http.Error(w, "decode request", http.StatusBadRequest)
			return
		}
		// 按严格兼容上游的边界拒绝 Kiro metadata 和原厂上下文字段，
		// 其他生成内容相同的请求仍然成功。
		if _, exists := fields["metadata"]; exists {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"unsupported_parameter","param":"metadata","message":"Unsupported parameter: metadata"}}`)
			return
		}
		for _, key := range []string{"origin", "profileArn"} {
			if _, exists := fields[key]; exists {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, fmt.Sprintf(`{"error":{"type":"invalid_request_error","code":"unsupported_parameter","param":%q,"message":%q}}`, key, "Unsupported parameter: "+key))
				return
			}
		}
		if reasoningRaw, exists := fields["reasoning"]; exists {
			var reasoning map[string]json.RawMessage
			if err := json.Unmarshal(reasoningRaw, &reasoning); err == nil {
				if _, exists := reasoning["type"]; exists {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"unsupported_parameter","param":"reasoning.type","message":"Unsupported parameter: reasoning.type"}}`)
					return
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"manual-response\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"manual-response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1000,\"output_tokens\":100,\"input_tokens_details\":{\"cached_tokens\":600}}}}\n\n")
	}))
	t.Cleanup(server.Close)
	return server, observations
}

func manualRawBody(model, conversation string) string {
	return fmt.Sprintf(`{"model":%q,"stream":true,"instructions":"stable policy","prompt_cache_key":"client-stable","input":[{"role":"user","content":"hello"}],"metadata":{"kiro:conversation_id":%q,"kiro:agent_mode":"vibe","kiro:proxy_continuation_id":"private-continuation","kiro:proxy_current_tool_results":0},"origin":"kiro","profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/example","reasoning":{"type":"enabled","effort":"medium","summary":"detailed"},"extension":{"precise":90071992547409931234}}`, model, conversation)
}

func manualRawRequest(model, conversation string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "http://runtime.us-east-1.kiro.dev/v1/responses", strings.NewReader(manualRawBody(model, conversation)))
}

func configureManualRawModel(t *testing.T, env *autoStageIntegrationEnv, id, name, modelID, provider, endpoint, baseURL, key string) *store.Model {
	t.Helper()
	model := &store.Model{
		ID: id, Name: name, ModelID: modelID, Provider: provider, Endpoint: endpoint,
		BaseURL: baseURL, APIKey: key, Enabled: true, RateMultiplier: 1,
		InputPrice: 1, OutputPrice: 2, CacheInputPrice: 0.1,
	}
	if err := env.db.CreateModel(model); err != nil {
		t.Fatal(err)
	}
	env.server.router.Reload()
	return model
}

func assertManualRawForwarded(t *testing.T, env *autoStageIntegrationEnv, observations <-chan manualRawObservation, response *httptest.ResponseRecorder, selected *store.Model) manualRawObservation {
	t.Helper()
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "response.completed") || !strings.Contains(response.Body.String(), `"delta":"ok"`) {
		t.Fatalf("raw Responses response was changed or failed: status=%d body=%q", response.Code, response.Body.String())
	}
	got := <-observations
	if got.Model != selected.ModelID || got.Authorization != "Bearer "+selected.APIKey {
		t.Fatalf("wrong upstream model or configured credential: model=%q auth=%q selected=%+v", got.Model, got.Authorization, selected)
	}
	if len(got.Workflow) != 0 {
		t.Fatalf("proxy-private workflow header leaked upstream: %v", got.Workflow)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Body, &payload); err != nil {
		t.Fatal(err)
	}
	metadata, _ := payload["metadata"].(map[string]any)
	for key := range metadata {
		if strings.HasPrefix(key, "kiro:proxy_") {
			t.Fatalf("proxy-private metadata leaked upstream: %s", key)
		}
	}
	if payload["instructions"] != "stable policy" || payload["prompt_cache_key"] != "client-stable" || payload["extension"] == nil {
		t.Fatalf("raw request fields were not preserved: %s", got.Body)
	}
	if _, exists := payload["origin"]; exists {
		t.Fatalf("Kiro origin context leaked upstream: %s", got.Body)
	}
	if _, exists := payload["profileArn"]; exists {
		t.Fatalf("Kiro profile context leaked upstream: %s", got.Body)
	}
	if reasoningValue, exists := payload["reasoning"]; exists {
		reasoning, ok := reasoningValue.(map[string]any)
		if !ok {
			t.Fatalf("standard reasoning fields were not preserved: %#v", reasoningValue)
		}
		if _, exists := reasoning["type"]; exists {
			t.Fatalf("Kiro reasoning type leaked upstream: %s", got.Body)
		}
		if reasoning["effort"] != "medium" || reasoning["summary"] != "detailed" {
			t.Fatalf("standard reasoning fields were not preserved: %#v", reasoning)
		}
	}
	return got
}

func TestManualRawResponsesForwardsConfiguredModelWithoutWorkflowHeader(t *testing.T) {
	oldMode := GetProxyMode()
	SetProxyMode(ModeForward)
	t.Cleanup(func() { SetProxyMode(oldMode) })
	env := newAutoStageIntegrationEnv(t)
	upstream, observations := newManualRawUpstream(t)
	selected := configureManualRawModel(t, env, "manual-raw-uuid", "Manual display", "actual-model-id", "openai", "/responses", upstream.URL, "configured-secret")
	var officialHits int
	rawStageProxy(t, env, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		officialHits++
		http.Error(w, "unexpected official runtime", http.StatusBadGateway)
	}))

	// A genuinely minimal valid Responses request without metadata must route by local model ID.
	minimal := httptest.NewRequest(http.MethodPost, "http://runtime.us-east-1.kiro.dev/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q,"stream":true,"input":[{"role":"user","content":"minimal"}]}`, selected.ID)))
	response := serveRawStage(t, env, minimal)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "response.completed") {
		t.Fatalf("minimal raw Responses request failed: status=%d body=%q", response.Code, response.Body.String())
	}
	got := <-observations
	if got.Model != selected.ModelID || got.Authorization != "Bearer "+selected.APIKey || len(got.Workflow) != 0 {
		t.Fatalf("minimal request did not use configured model, auth, and private-header boundary: %+v", got)
	}
	var minimalPayload map[string]any
	if err := json.Unmarshal(got.Body, &minimalPayload); err != nil {
		t.Fatal(err)
	}
	if _, exists := minimalPayload["metadata"]; exists {
		t.Fatalf("minimal request unexpectedly gained metadata: %s", got.Body)
	}
	response = serveRawStage(t, env, manualRawRequest(selected.ID, "manual-with-routing-metadata"))
	assertManualRawForwarded(t, env, observations, response, selected)

	// Stage metadata is ignored for a manually selected model, including malformed metadata.
	for _, header := range []string{
		`{"version":1,"stage":"plan","conversationId":"manual-with-stage","agentMode":"vibe","agentContinuationId":""}`,
		"bad-json",
	} {
		req := rawStageRequest(t, selected.ID, "manual-with-stage", "plan", 0)
		req.Header.Set(converter.WorkflowStageHeader, header)
		response = serveRawStage(t, env, req)
		assertManualRawForwarded(t, env, observations, response, selected)
	}

	rows, _, err := env.db.QueryRequestUsage("", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, row := range rows {
		if row.RequestedModel == selected.ID {
			count++
			if row.SelectedModelConfigID != selected.ID || row.StatusCode != http.StatusOK || !row.Success || row.InputTokens != 1000 || row.OutputTokens != 100 || row.CacheReadTokens != 600 {
				t.Fatalf("usage did not record selected configuration and raw response tokens: %+v", row)
			}
		}
	}
	if count != 4 {
		t.Fatalf("manual usage rows=%d want=4", count)
	}
	if officialHits != 0 {
		t.Fatalf("configured manual requests hit official mock: %d", officialHits)
	}
}

func TestManualRawResponsesResolvesGroupAndConfiguredIDBeforeDuplicateUpstreamModel(t *testing.T) {
	oldMode := GetProxyMode()
	SetProxyMode(ModeForward)
	t.Cleanup(func() { SetProxyMode(oldMode) })
	env := newAutoStageIntegrationEnv(t)
	upstream, observations := newManualRawUpstream(t)
	first := configureManualRawModel(t, env, "manual-first-uuid", "First display", "same-upstream-model", "openai", "/v1/responses", upstream.URL, "first-secret")
	second := configureManualRawModel(t, env, "manual-second-uuid", "Second display", "same-upstream-model", "openai", "/v1/responses", upstream.URL, "second-secret")
	third := configureManualRawModel(t, env, "manual-third-uuid", "Third display", "third-upstream-model", "openai", "/v1/responses", upstream.URL, "third-secret")
	group := &store.ModelGroup{ID: "manual-group-uuid", Name: "Manual model group", Enabled: true, MaxAttempts: 1, Members: []store.ModelGroupMember{{ModelID: first.ID, Priority: 0}}}
	if err := env.db.CreateModelGroup(group); err != nil {
		t.Fatal(err)
	}
	env.server.router.Reload()
	var officialHits int
	rawStageProxy(t, env, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		officialHits++
		http.Error(w, "unexpected official runtime", http.StatusBadGateway)
	}))

	// The exact local configuration UUID wins even though another config shares its upstream ModelID.
	resp := serveRawStage(t, env, manualRawRequest(second.ID, "exact-config-id"))
	assertManualRawForwarded(t, env, observations, resp, second)
	// Display names and group IDs are also values Kiro can send in model.
	resp = serveRawStage(t, env, manualRawRequest(first.Name, "display-name"))
	assertManualRawForwarded(t, env, observations, resp, first)
	resp = serveRawStage(t, env, manualRawRequest(third.ModelID, "upstream-model-id"))
	assertManualRawForwarded(t, env, observations, resp, third)
	resp = serveRawStage(t, env, manualRawRequest(group.ID, "group-id"))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "response.completed") {
		t.Fatalf("raw group response failed: %d %q", resp.Code, resp.Body.String())
	}
	got := <-observations
	if got.Model != first.ModelID || got.Authorization != "Bearer "+first.APIKey {
		t.Fatalf("group did not use compatible configured candidate: %+v", got)
	}
	if strings.Contains(string(got.Body), "kiro:proxy_") || len(got.Workflow) != 0 {
		t.Fatalf("group request leaked proxy-private metadata: %s headers=%v", got.Body, got.Workflow)
	}
	resp = serveRawStage(t, env, manualRawRequest(group.Name, "group-name"))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "response.completed") {
		t.Fatalf("raw group name response failed: %d %q", resp.Code, resp.Body.String())
	}
	if got := <-observations; got.Model != first.ModelID || got.Authorization != "Bearer "+first.APIKey {
		t.Fatalf("group name did not use configured candidate: %+v", got)
	}
	if officialHits != 0 {
		t.Fatalf("configured model/group requests hit official mock: %d", officialHits)
	}
}

func TestManualRawResponsesRejectsUnsupportedProviderWithoutOfficialFallback(t *testing.T) {
	oldMode := GetProxyMode()
	SetProxyMode(ModeForward)
	t.Cleanup(func() { SetProxyMode(oldMode) })
	env := newAutoStageIntegrationEnv(t)
	upstream, observations := newManualRawUpstream(t)
	selected := configureManualRawModel(t, env, "manual-anthropic", "Manual Anthropic", "claude-model", "anthropic", "/v1/messages", upstream.URL, "anthropic-secret")
	var officialHits int
	rawStageProxy(t, env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		officialHits++
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "official")
	}))

	resp := serveRawStage(t, env, manualRawRequest(selected.ID, "unsupported-provider"))
	if resp.Code != http.StatusBadGateway || !strings.Contains(strings.ToLower(resp.Body.String()), "openai") {
		t.Fatalf("unsupported provider did not return a clear gateway error: %d %q", resp.Code, resp.Body.String())
	}
	if officialHits != 0 {
		t.Fatalf("unsupported local provider fell back to Kiro official: %d hits", officialHits)
	}
	select {
	case got := <-observations:
		t.Fatalf("unsupported provider reached upstream: %+v", got)
	default:
	}
}

func TestManualRawResponsesConfiguredUnavailableTargetsNeverFallBackOfficial(t *testing.T) {
	oldMode := GetProxyMode()
	SetProxyMode(ModeForward)
	t.Cleanup(func() { SetProxyMode(oldMode) })

	for _, tc := range []struct {
		name       string
		wantStatus int
		setup      func(*testing.T, *autoStageIntegrationEnv, string) string
	}{
		{
			name:       "enabled empty group",
			wantStatus: http.StatusServiceUnavailable,
			setup: func(t *testing.T, env *autoStageIntegrationEnv, _ string) string {
				group := &store.ModelGroup{ID: "empty-manual-group", Name: "Empty manual group", Enabled: true, MaxAttempts: 1}
				if err := env.db.CreateModelGroup(group); err != nil {
					t.Fatal(err)
				}
				return group.ID
			},
		},
		{
			name:       "disabled configured model",
			wantStatus: http.StatusServiceUnavailable,
			setup: func(t *testing.T, env *autoStageIntegrationEnv, baseURL string) string {
				model := configureManualRawModel(t, env, "disabled-manual-model", "Disabled manual model", "disabled-upstream-model", "openai", "/v1/responses", baseURL, "disabled-secret")
				model.Enabled = false
				if err := env.db.UpdateModel(model); err != nil {
					t.Fatal(err)
				}
				env.server.router.Reload()
				return model.ID
			},
		},
		{
			name:       "enabled group with only disabled member",
			wantStatus: http.StatusServiceUnavailable,
			setup: func(t *testing.T, env *autoStageIntegrationEnv, baseURL string) string {
				member := configureManualRawModel(t, env, "disabled-group-member", "Disabled group member", "disabled-group-upstream-model", "openai", "/v1/responses", baseURL, "disabled-group-secret")
				member.Enabled = false
				if err := env.db.UpdateModel(member); err != nil {
					t.Fatal(err)
				}
				group := &store.ModelGroup{ID: "disabled-member-group", Name: "Group with disabled member", Enabled: true, MaxAttempts: 1, Members: []store.ModelGroupMember{{ModelID: member.ID}}}
				if err := env.db.CreateModelGroup(group); err != nil {
					t.Fatal(err)
				}
				env.server.router.Reload()
				return group.ID
			},
		},
		{
			name:       "group with only incompatible provider",
			wantStatus: http.StatusBadGateway,
			setup: func(t *testing.T, env *autoStageIntegrationEnv, baseURL string) string {
				member := configureManualRawModel(t, env, "anthropic-group-member", "Anthropic group member", "claude-group-model", "anthropic", "/v1/messages", baseURL, "anthropic-group-secret")
				group := &store.ModelGroup{ID: "anthropic-only-group", Name: "Anthropic only group", Enabled: true, MaxAttempts: 1, Members: []store.ModelGroupMember{{ModelID: member.ID}}}
				if err := env.db.CreateModelGroup(group); err != nil {
					t.Fatal(err)
				}
				env.server.router.Reload()
				return group.ID
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newAutoStageIntegrationEnv(t)
			upstream, observations := newManualRawUpstream(t)
			requestedID := tc.setup(t, env, upstream.URL)
			env.server.router.Reload()
			var officialHits int
			rawStageProxy(t, env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				officialHits++
				_, _ = io.Copy(io.Discard, r.Body)
				_, _ = io.WriteString(w, "official")
			}))

			response := serveRawStage(t, env, manualRawRequest(requestedID, "unavailable-"+tc.name))
			if response.Code != tc.wantStatus {
				t.Fatalf("configured unavailable target returned status=%d want=%d body=%q", response.Code, tc.wantStatus, response.Body.String())
			}
			if officialHits != 0 {
				t.Fatalf("configured unavailable target fell back to official mock: %d hits", officialHits)
			}
			select {
			case got := <-observations:
				t.Fatalf("incompatible/unavailable target reached compatible upstream: %+v", got)
			default:
			}
		})
	}
}

func TestManualRawResponsesBoundariesAndClearsAutoLockWithoutApplyingStage(t *testing.T) {
	oldMode := GetProxyMode()
	SetProxyMode(ModeForward)
	t.Cleanup(func() { SetProxyMode(oldMode) })
	env := newAutoStageIntegrationEnv(t)
	upstream, observations := newManualRawUpstream(t)
	selected := configureManualRawModel(t, env, "manual-lock-uuid", "Manual lock model", "manual-lock-model", "openai", "/v1/responses", upstream.URL, "manual-lock-secret")
	var officialHits int
	rawStageProxy(t, env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		officialHits++
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "passthrough")
	}))

	conversation := "manual-clears-auto-scope"
	first := serveRawStage(t, env, rawStageRequest(t, "auto", conversation, "plan", 0))
	if first.Code != http.StatusOK {
		t.Fatalf("initial Auto stage request failed: %d %q", first.Code, first.Body.String())
	}
	if got := <-env.requests; got.Model != "upstream-shared-responses" {
		t.Fatalf("initial request did not use plan stage model: %+v", got)
	}
	manual := rawStageRequest(t, selected.ID, conversation, "run", 0)
	manual.Header.Set(converter.WorkflowStageHeader, "malformed-but-ignored")
	resp := serveRawStage(t, env, manual)
	assertManualRawForwarded(t, env, observations, resp, selected)
	continued := serveRawStage(t, env, rawStageRequest(t, "auto", conversation, "implement", 1))
	if continued.Code != http.StatusOK {
		t.Fatalf("continued Auto request failed: %d %q", continued.Code, continued.Body.String())
	}
	if got := <-env.requests; got.Model != "upstream-implement" {
		t.Fatalf("Auto request reused stale/manual stage policy: %+v", got)
	}
	if officialHits != 0 {
		t.Fatalf("configured local models unexpectedly hit official mock: %d", officialHits)
	}

	unknown := serveRawStage(t, env, manualRawRequest("not-a-local-model", "unknown"))
	if unknown.Code != http.StatusOK || unknown.Body.String() != "passthrough" || officialHits != 1 {
		t.Fatalf("unknown model fallback behavior changed: status=%d body=%q hits=%d", unknown.Code, unknown.Body.String(), officialHits)
	}
	autoNoStage := serveRawStage(t, env, rawStageRequest(t, "auto", "auto-without-stage", "", 0))
	if autoNoStage.Code != http.StatusOK || autoNoStage.Body.String() != "passthrough" || officialHits != 2 {
		t.Fatalf("Auto without stage did not pass through: status=%d body=%q hits=%d", autoNoStage.Code, autoNoStage.Body.String(), officialHits)
	}
	withReference := httptest.NewRequest(http.MethodPost, "http://runtime.us-east-1.kiro.dev/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q,"stream":true,"input":[{"role":"user","content":"referenced"}],"previous_response_id":"prior-response"}`, selected.ID)))
	rejectedReference := serveRawStage(t, env, withReference)
	if rejectedReference.Code != http.StatusBadRequest {
		t.Fatalf("known model with previous_response_id was not rejected: status=%d body=%q", rejectedReference.Code, rejectedReference.Body.String())
	}
	if officialHits != 2 {
		t.Fatalf("unsupported continuation reference hit official mock: %d hits", officialHits)
	}
	select {
	case got := <-observations:
		t.Fatalf("unsupported continuation reference reached configured upstream: %+v", got)
	default:
	}
	SetProxyMode(ModeCapture)
	captured := serveRawStage(t, env, manualRawRequest(selected.ID, "capture"))
	if captured.Code != http.StatusOK || captured.Body.String() != "passthrough" || officialHits != 3 {
		t.Fatalf("capture mode did not pass through: status=%d body=%q hits=%d", captured.Code, captured.Body.String(), officialHits)
	}
	SetProxyMode(ModeForward)
	nonPost := manualRawRequest(selected.ID, "non-post")
	nonPost.Method = http.MethodGet
	getResp := serveRawStage(t, env, nonPost)
	if getResp.Code != http.StatusOK || getResp.Body.String() != "passthrough" || officialHits != 4 {
		t.Fatalf("non-POST request did not pass through: status=%d body=%q hits=%d", getResp.Code, getResp.Body.String(), officialHits)
	}
	select {
	case got := <-observations:
		t.Fatalf("capture/non-POST/unknown request reached configured upstream: %+v", got)
	default:
	}
}

func TestManualRawResponsesCapturePreservesUnparseableBodies(t *testing.T) {
	oldMode := GetProxyMode()
	SetProxyMode(ModeCapture)
	t.Cleanup(func() { SetProxyMode(oldMode) })
	env := newAutoStageIntegrationEnv(t)
	localUpstream, localObservations := newManualRawUpstream(t)
	configureManualRawModel(t, env, "capture-local-model", "Capture local model", "capture-upstream-model", "openai", "/v1/responses", localUpstream.URL, "capture-secret")
	var officialBodies [][]byte
	var officialWorkflowHeaders [][]string
	rawStageProxy(t, env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		officialBodies = append(officialBodies, body)
		officialWorkflowHeaders = append(officialWorkflowHeaders, append([]string(nil), r.Header.Values(converter.WorkflowStageHeader)...))
		_, _ = io.WriteString(w, "capture passthrough")
	}))

	malformedBody := `{"model":`
	malformed := httptest.NewRequest(http.MethodPost, "http://runtime.us-east-1.kiro.dev/v1/responses", strings.NewReader(malformedBody))
	malformedResponse := serveRawStage(t, env, malformed)
	if malformedResponse.Code != http.StatusOK || malformedResponse.Body.String() != "capture passthrough" {
		t.Fatalf("capture rejected malformed JSON instead of passing it through: status=%d body=%q", malformedResponse.Code, malformedResponse.Body.String())
	}

	privateMetadataBody := `{"model":123,"stream":true,"input":[{"role":"user","content":"capture"}],"origin":"kiro","profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/example","reasoning":{"type":"enabled","effort":"medium","summary":"detailed"},"metadata":{"kiro:conversation_id":"capture-scope","kiro:proxy_continuation_id":"private-id","kiro:proxy_current_tool_results":1,"custom":"keep"}}`
	privateMetadata := httptest.NewRequest(http.MethodPost, "http://runtime.us-east-1.kiro.dev/v1/responses", strings.NewReader(privateMetadataBody))
	privateMetadata.Header.Set(converter.WorkflowStageHeader, `{"version":1,"stage":"plan"}`)
	privateResponse := serveRawStage(t, env, privateMetadata)
	if privateResponse.Code != http.StatusOK || privateResponse.Body.String() != "capture passthrough" {
		t.Fatalf("capture parsed/routed non-string model instead of passing it through: status=%d body=%q", privateResponse.Code, privateResponse.Body.String())
	}

	if len(officialBodies) != 2 || string(officialBodies[0]) != malformedBody {
		t.Fatalf("capture did not preserve malformed body exactly: bodies=%q", officialBodies)
	}
	var forwarded map[string]any
	if err := json.Unmarshal(officialBodies[1], &forwarded); err != nil {
		t.Fatalf("capture produced invalid JSON after metadata cleanup: %v body=%q", err, officialBodies[1])
	}
	if model, ok := forwarded["model"].(float64); !ok || model != 123 {
		t.Fatalf("capture changed non-string model field: %#v", forwarded["model"])
	}
	metadata, _ := forwarded["metadata"].(map[string]any)
	if metadata["custom"] != "keep" || metadata["kiro:conversation_id"] != "capture-scope" {
		t.Fatalf("capture changed caller-owned metadata: %#v", metadata)
	}
	if _, exists := metadata["kiro:proxy_continuation_id"]; exists {
		t.Fatalf("capture leaked private continuation metadata: %#v", metadata)
	}
	if _, exists := metadata["kiro:proxy_current_tool_results"]; exists {
		t.Fatalf("capture leaked private tool-result metadata: %#v", metadata)
	}
	if forwarded["origin"] != "kiro" || forwarded["profileArn"] != "arn:aws:codewhisperer:us-east-1:123456789012:profile/example" {
		t.Fatalf("capture changed native Kiro context: %#v", forwarded)
	}
	reasoning, _ := forwarded["reasoning"].(map[string]any)
	if reasoning["type"] != "enabled" || reasoning["effort"] != "medium" || reasoning["summary"] != "detailed" {
		t.Fatalf("capture changed native reasoning context: %#v", reasoning)
	}
	if len(officialWorkflowHeaders) != 2 || len(officialWorkflowHeaders[0]) != 0 || len(officialWorkflowHeaders[1]) != 0 {
		t.Fatalf("capture leaked workflow header: %v", officialWorkflowHeaders)
	}
	select {
	case got := <-localObservations:
		t.Fatalf("capture request reached local upstream: %+v", got)
	default:
	}
}
