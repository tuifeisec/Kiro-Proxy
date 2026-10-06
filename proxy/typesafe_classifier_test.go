package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/license"
	"kiro-proxy/router"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

type typesafeClassifierTestLicense struct {
	result license.CheckResult
	err    error
	calls  atomic.Int32
}

func (f *typesafeClassifierTestLicense) CheckCached() (license.CheckResult, error) {
	f.calls.Add(1)
	return f.result, f.err
}

type typesafeClassifierTestEnv struct {
	server    *Server
	db        *store.DB
	stats     *store.Stats
	logger    *TrafficLogger
	logPath   string
	forwarder *Forwarder
	upstream  *httptest.Server
	hits      atomic.Int32
}

func newTypesafeClassifierTestEnv(t *testing.T, handler http.HandlerFunc) *typesafeClassifierTestEnv {
	t.Helper()
	env := &typesafeClassifierTestEnv{}
	env.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.hits.Add(1)
		handler(w, r)
	}))
	db, err := store.Open(filepath.Join(t.TempDir(), "typesafe-classifier.db"))
	if err != nil {
		env.upstream.Close()
		t.Fatal(err)
	}
	env.db = db
	env.stats = store.NewStats(db)
	env.logPath = filepath.Join(t.TempDir(), "traffic.log")
	env.logger, err = NewTrafficLogger(env.logPath, nil)
	if err != nil {
		env.stats.Close()
		env.db.Close()
		env.upstream.Close()
		t.Fatal(err)
	}
	env.forwarder = NewForwarder(env.stats, env.db)
	checker := &typesafeClassifierTestLicense{result: license.CheckResult{Authorized: true}}
	env.server = &Server{
		db: db, stats: env.stats, logger: env.logger, forwarder: env.forwarder,
		router: router.New(db), licenseClient: checker,
		conversationModels: make(map[string]conversationModelEntry),
	}
	t.Cleanup(func() {
		env.forwarder.CloseIdleConnections()
		env.logger.Close()
		env.stats.Close()
		env.db.Close()
		env.upstream.Close()
	})
	return env
}

func (env *typesafeClassifierTestEnv) setConfig(t *testing.T, enabled bool) {
	t.Helper()
	config := store.TypesafeClassifierConfig{
		Enabled: enabled, BaseURL: env.upstream.URL, APIKey: "typesafe-test-key",
		Model: "configured-model", TimeoutSeconds: 3, InputPricePerMillion: 0.042,
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.db.SetSetting(store.TypesafeClassifierSetting, string(raw)); err != nil {
		t.Fatal(err)
	}
}

func (env *typesafeClassifierTestEnv) setTimeout(t *testing.T, seconds int) {
	t.Helper()
	raw, err := env.db.GetSetting(store.TypesafeClassifierSetting)
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.ParseTypesafeClassifierConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	config.TimeoutSeconds = seconds
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.db.SetSetting(store.TypesafeClassifierSetting, string(encoded)); err != nil {
		t.Fatal(err)
	}
}

func readTypesafeClassifierLogs(t *testing.T, env *typesafeClassifierTestEnv) string {
	t.Helper()
	env.logger.Close()
	data, err := os.ReadFile(env.logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func typesafeClassifierTestPreflight(source, protocol string, classifier bool) *Preflight {
	conv := &converter.ParsedConversation{
		ModelID: source, ConversationID: "typesafe-test-conversation", IsClassifier: classifier,
		CurrentContent: "typesafe classifier test prompt",
	}
	return &Preflight{
		Conv: conv, Protocol: protocol, SourceModel: source, IsClassifier: classifier,
		ConversationID: conv.ConversationID, StartTime: time.Now(),
	}
}

func typesafeClassifierTestRequest() *http.Request {
	return httptest.NewRequest(http.MethodPost, "https://runtime.test/generateAssistantResponse", nil)
}

func typesafeClassifierTestRequestWithMode(mode string) *http.Request {
	req := typesafeClassifierTestRequest()
	if mode != "" {
		req.Header.Set("X-Amzn-Kiro-Agent-Mode", mode)
	}
	return req
}

const typesafeClassifierPromptMarker = "This is an intent classifier. Respond ONLY with a raw JSON object"

func TestTryTypesafeClassifierSkipsRequestsOutsideItsBoundary(t *testing.T) {
	SetProxyMode(ModeForward)
	t.Cleanup(func() { SetProxyMode(ModeForward) })
	env := newTypesafeClassifierTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unexpected Typesafe request", http.StatusInternalServerError)
	})
	env.setConfig(t, true)
	checker := env.server.licenseClient.(*typesafeClassifierTestLicense)

	tests := []struct {
		name    string
		pre     *Preflight
		mode    string
		capture bool
	}{
		{name: "capture", pre: typesafeClassifierTestPreflight("simple-task", "kiro", true), mode: "intent-classification", capture: true},
		{name: "manual main model", pre: typesafeClassifierTestPreflight("manual-model", "kiro", false)},
		{name: "auto main model", pre: typesafeClassifierTestPreflight("auto", "kiro", false)},
		{name: "non Kiro classifier", pre: typesafeClassifierTestPreflight("simple-task", "openai", true), mode: "intent-classification"},
		{name: "easter egg classifier", pre: typesafeClassifierTestPreflight("GPT-8.8", "kiro", true), mode: "intent-classification"},
		{name: "reserved simple task without intent signal", pre: typesafeClassifierTestPreflight("simple-task", "kiro", true)},
		{name: "session title with classifier marker", pre: func() *Preflight {
			pre := typesafeClassifierTestPreflight("simple-task", "kiro", true)
			pre.Conv.CurrentContent = typesafeClassifierPromptMarker
			return pre
		}(), mode: "session-title"},
		{name: "missing preflight"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.capture {
				SetProxyMode(ModeCapture)
			} else {
				SetProxyMode(ModeForward)
			}
			resp, handled := env.server.tryTypesafeClassifier(typesafeClassifierTestRequestWithMode(tt.mode), tt.pre)
			if resp != nil || handled {
				if resp != nil && resp.Body != nil {
					resp.Body.Close()
				}
				t.Fatalf("tryTypesafeClassifier() = (%v, %v), want (nil, false)", resp, handled)
			}
		})
	}
	if got := env.hits.Load(); got != 0 {
		t.Fatalf("Typesafe HTTP calls = %d, want 0", got)
	}
	if got := checker.calls.Load(); got != 0 {
		t.Fatalf("license checks = %d, want 0 for requests outside the classifier boundary", got)
	}
}

func TestTypesafeIntentClassifierUsesHistoryMarkerOrExplicitHeader(t *testing.T) {
	SetProxyMode(ModeForward)
	env := newTypesafeClassifierTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "local mock rejection", http.StatusUnauthorized)
	})
	env.setConfig(t, true)
	tests := []struct {
		name string
		pre  *Preflight
		mode string
	}{
		{name: "history marker", pre: func() *Preflight {
			pre := typesafeClassifierTestPreflight("simple-task", "kiro", false)
			pre.Conv.History = []converter.ParsedHistoryEntry{{UserContent: typesafeClassifierPromptMarker}}
			return pre
		}()},
		{name: "header independent of parser mark", pre: typesafeClassifierTestPreflight("manual-model", "kiro", false), mode: "intent-classification"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, handled := env.server.tryTypesafeClassifier(typesafeClassifierTestRequestWithMode(tt.mode), tt.pre)
			if !handled || resp == nil {
				t.Fatalf("classifier recognition returned (%v, %v), want handled", resp, handled)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("response status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
			}
		})
	}
	if got := env.hits.Load(); got != int32(len(tests)) {
		t.Fatalf("Typesafe HTTP calls = %d, want %d", got, len(tests))
	}
}

func TestTypesafeClassifierMapsChoiceUsageAndAccounting(t *testing.T) {
	SetProxyMode(ModeForward)
	responses := []struct {
		choice        string
		probabilities map[string]float64
		confidence    float64
	}{
		{choice: "chat", probabilities: map[string]float64{"chat": 0.64, "do": 0.29, "spec": 0.07}, confidence: 0.38},
		{choice: "do", probabilities: map[string]float64{"chat": 0.1, "do": 0.8, "spec": 0.1}, confidence: 0.31},
		{choice: "spec", probabilities: map[string]float64{"chat": 0.1, "do": 0.2, "spec": 0.7}, confidence: 0.99},
	}
	var responseIndex atomic.Int32
	var capturedRequest atomic.Value
	env := newTypesafeClassifierTestEnv(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/v1/systemone" {
			t.Errorf("Typesafe request = %s %s, want POST /v1/systemone", req.Method, req.URL.Path)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer typesafe-test-key" {
			t.Errorf("Authorization = %q, want configured bearer key", got)
		}
		var request struct {
			Model     string                    `json:"model"`
			State     map[string]interface{}    `json:"state"`
			Questions map[string]map[string]any `json:"questions"`
		}
		if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
			t.Errorf("decode Typesafe request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		capturedRequest.Store(request)
		index := int(responseIndex.Add(1) - 1)
		if index >= len(responses) {
			t.Errorf("unexpected Typesafe request number %d", index+1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		answer := responses[index]
		body, err := json.Marshal(map[string]interface{}{
			"model": "actual-model",
			"usage": map[string]int{"input_tokens": 1000, "output_tokens": 500},
			"answers": map[string]interface{}{"intent": map[string]interface{}{
				"type": "choice", "choice": answer.choice,
				"probabilities": answer.probabilities, "confidence": answer.confidence,
			}},
		})
		if err != nil {
			t.Errorf("encode Typesafe response: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	env.setConfig(t, true)

	if err := env.db.CreateModel(&store.Model{ID: "manual-model", Name: "Manual", ModelID: "manual-upstream", Provider: "openai", BaseURL: "https://manual.example", Endpoint: "/chat/completions", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := env.db.CreateModel(&store.Model{ID: "auto-target", Name: "Auto target", ModelID: "auto-upstream", Provider: "openai", BaseURL: "https://auto.example", Endpoint: "/chat/completions", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	autoConfig, err := json.Marshal(store.AutoRoutingConfig{
		Mode: store.AutoRoutingModeStageFixed, Enabled: true, DefaultModelID: "manual-model",
		StageModels: store.AutoStageModels{Run: "manual-model", Plan: "auto-target", Implement: "auto-target", Review: "manual-model", Finalize: "manual-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.db.SetSetting(store.AutoRoutingSetting, string(autoConfig)); err != nil {
		t.Fatal(err)
	}
	env.server.router = router.New(env.db)

	mainPre := typesafeClassifierTestPreflight("auto", "kiro", false)
	stageScopeApplyHeader(t, mainPre.Conv, "plan")
	mainRouteBefore, err := env.server.resolveMainRoute(mainPre)
	if err != nil || mainRouteBefore == nil || mainRouteBefore.Model.ID != "auto-target" {
		t.Fatalf("main Auto route before classification = %+v, error=%v", mainRouteBefore, err)
	}
	env.server.rememberMainModel(mainPre, mainRouteBefore)
	if got := env.server.mainModelForConversation(mainPre.ConversationID); got != "auto-target" {
		t.Fatalf("remembered main model before classification = %q, want auto-target", got)
	}

	for _, answer := range responses {
		pre := typesafeClassifierTestPreflight("simple-task", "kiro", true)
		pre.Conv.SystemText = "Kiro category definitions"
		pre.Conv.CurrentContent = "classify this current user message"
		pre.Conv.History = []converter.ParsedHistoryEntry{
			{IsSystem: true, UserContent: "system content should not be repeated"},
			{UserContent: "earlier user turn", HasAssistant: true, AssistantContent: "earlier assistant turn"},
		}
		resp, handled := env.server.tryTypesafeClassifier(typesafeClassifierTestRequestWithMode("intent-classification"), pre)
		if !handled || resp == nil {
			t.Fatalf("choice %s returned (%v, %v), want a handled response", answer.choice, resp, handled)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("choice %s status = %d, want %d", answer.choice, resp.StatusCode, http.StatusOK)
		}
		frames, err := readTypesafeKiroFrames(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("choice %s response frames: %v", answer.choice, err)
		}
		wantEvents := []string{"initial-response", "assistantResponseEvent", "messageMetadataEvent", "metadataEvent", "meteringEvent"}
		if len(frames) != len(wantEvents) {
			t.Fatalf("choice %s event count = %d, want %d", answer.choice, len(frames), len(wantEvents))
		}
		for i, frame := range frames {
			if frame.event != wantEvents[i] {
				t.Fatalf("choice %s event[%d] = %q, want %q", answer.choice, i, frame.event, wantEvents[i])
			}
		}
		if frames[0].payload["conversationId"] != pre.ConversationID {
			t.Fatalf("initial response conversationId = %v, want %q", frames[0].payload["conversationId"], pre.ConversationID)
		}
		assistant := frames[1].payload
		if assistant["modelId"] != "actual-model" {
			t.Fatalf("assistant modelId = %v, want actual-model", assistant["modelId"])
		}
		var gotProbabilities map[string]float64
		if err := json.Unmarshal([]byte(assistant["content"].(string)), &gotProbabilities); err != nil {
			t.Fatalf("decode classifier probabilities: %v", err)
		}
		if len(gotProbabilities) != len(answer.probabilities) {
			t.Fatalf("probabilities = %v, want %v", gotProbabilities, answer.probabilities)
		}
		for key, want := range answer.probabilities {
			if gotProbabilities[key] != want {
				t.Fatalf("probability[%s] = %v, want %v (confidence=%v)", key, gotProbabilities[key], want, answer.confidence)
			}
		}
		tokenUsage, ok := frames[2].payload["tokenUsage"].(map[string]interface{})
		if !ok || tokenUsage["uncachedInputTokens"] != float64(1000) || tokenUsage["outputTokens"] != float64(500) {
			t.Fatalf("token usage frame = %v", frames[2].payload)
		}
		if frames[3].payload["stopReason"] != "END_TURN" {
			t.Fatalf("stop reason = %v, want END_TURN", frames[3].payload["stopReason"])
		}
		if got, want := frames[4].payload["usage"].(float64), 0.00105; math.Abs(got-want) > 1e-12 {
			t.Fatalf("metering credits = %.12f, want %.12f", got, want)
		}
	}

	if got := responseIndex.Load(); got != int32(len(responses)) {
		t.Fatalf("Typesafe HTTP calls = %d, want %d", got, len(responses))
	}
	request, ok := capturedRequest.Load().(struct {
		Model     string                    `json:"model"`
		State     map[string]interface{}    `json:"state"`
		Questions map[string]map[string]any `json:"questions"`
	})
	if !ok {
		t.Fatal("Typesafe request was not captured")
	}
	if request.Model != "configured-model" {
		t.Fatalf("requested classifier model = %q, want configured-model", request.Model)
	}
	current, ok := request.State["current_message"].(map[string]interface{})
	if !ok || current["content"] != "classify this current user message" || request.State["classifier_context"] != "Kiro category definitions" {
		t.Fatalf("Typesafe state did not preserve current message and classifier context: %+v", request.State)
	}
	history, ok := request.State["history"].([]interface{})
	if !ok || len(history) != 2 {
		t.Fatalf("Typesafe history = %v, want two non-system entries", request.State["history"])
	}
	if question, ok := request.Questions["intent"]; !ok || question["type"] != "choice" {
		t.Fatalf("intent question = %v, want a choice", request.Questions)
	}

	page, err := env.db.QueryRequestUsagePage(store.RequestUsageQuery{PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != int64(len(responses)) {
		t.Fatalf("request usage rows = %d, want %d", page.Total, len(responses))
	}
	for _, item := range page.Items {
		if item.ModelID != "Typesafe classifier / actual-model" || item.Provider != "typesafe" || item.RequestedModel != "simple-task" || item.DispatchReason != "classifier:typesafe" || item.SelectedModelConfigID != "typesafe-classifier" {
			t.Fatalf("classifier request usage identity = %+v", item)
		}
		if !item.Success || item.StatusCode != http.StatusOK || item.InputTokens != 1000 || item.OutputTokens != 500 || math.Abs(item.InputCost-0.000042) > 1e-12 || item.OutputCost != 0 || math.Abs(item.Credits-0.00105) > 1e-12 {
			t.Fatalf("classifier request usage = %+v", item)
		}
	}
	wantCredits := float64(len(responses)) * 0.00105
	if got := env.stats.Snapshot(); got.TotalRequests != int64(len(responses)) || got.TotalErrors != 0 || got.InputTokens != int64(len(responses)*1000) || got.OutputTokens != int64(len(responses)*500) || math.Abs(got.Credits-wantCredits) > 1e-12 {
		t.Fatalf("classifier aggregate stats = %+v, want credits %.12f", got, wantCredits)
	}
	today := store.StatsDay(time.Now())
	daily := env.stats.GetDailyStats(today, today)
	if len(daily) != 1 || daily[0].ModelID != "Typesafe classifier / actual-model" || daily[0].Requests != int64(len(responses)) || math.Abs(daily[0].Credits-wantCredits) > 1e-12 {
		t.Fatalf("daily classifier stats = %+v", daily)
	}
	if got := env.server.mainModelForConversation(mainPre.ConversationID); got != "auto-target" {
		t.Fatalf("main model memory after classifier = %q, want auto-target", got)
	}
	mainRouteAfter, err := env.server.resolveMainRoute(mainPre)
	if err != nil || mainRouteAfter == nil || mainRouteAfter.Model.ID != mainRouteBefore.Model.ID || mainRouteAfter.AutoReason != mainRouteBefore.AutoReason {
		t.Fatalf("main Auto route changed after classifier: before=%+v after=%+v error=%v", mainRouteBefore, mainRouteAfter, err)
	}
}

type typesafeKiroFrame struct {
	event   string
	payload map[string]interface{}
}

func readTypesafeKiroFrames(body io.ReadCloser) ([]typesafeKiroFrame, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	frames := make([]typesafeKiroFrame, 0, 5)
	for len(data) > 0 {
		if len(data) < 16 {
			return nil, io.ErrUnexpectedEOF
		}
		total := int(binary.BigEndian.Uint32(data[:4]))
		headersLength := int(binary.BigEndian.Uint32(data[4:8]))
		if total < 16 || total > len(data) || headersLength > total-16 {
			return nil, io.ErrUnexpectedEOF
		}
		headers := data[12 : 12+headersLength]
		event := ""
		for offset := 0; offset < len(headers); {
			nameLength := int(headers[offset])
			offset++
			if nameLength > len(headers)-offset {
				return nil, io.ErrUnexpectedEOF
			}
			name := string(headers[offset : offset+nameLength])
			offset += nameLength
			if offset >= len(headers) {
				return nil, io.ErrUnexpectedEOF
			}
			typeCode := headers[offset]
			offset++
			if typeCode != 7 || len(headers)-offset < 2 {
				return nil, io.ErrUnexpectedEOF
			}
			valueLength := int(binary.BigEndian.Uint16(headers[offset : offset+2]))
			offset += 2
			if valueLength > len(headers)-offset {
				return nil, io.ErrUnexpectedEOF
			}
			if name == ":event-type" {
				event = string(headers[offset : offset+valueLength])
			}
			offset += valueLength
		}
		payload := make(map[string]interface{})
		if err := json.Unmarshal(data[12+headersLength:total-4], &payload); err != nil {
			return nil, err
		}
		frames = append(frames, typesafeKiroFrame{event: event, payload: payload})
		data = data[total:]
	}
	return frames, nil
}

func TestTryTypesafeClassifierPreservesHTTPStatusesAndDoesNotLogResponseBodies(t *testing.T) {
	SetProxyMode(ModeForward)
	tests := []struct {
		name   string
		status int
	}{
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "unprocessable", status: http.StatusUnprocessableEntity},
		{name: "rate limited", status: http.StatusTooManyRequests},
		{name: "upstream overloaded", status: 529},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := "PRIVATE-CLASSIFIER-PROMPT"
			apiKey := "typesafe-test-key"
			env := newTypesafeClassifierTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, "upstream body echoes "+apiKey+" and "+prompt)
			})
			env.setConfig(t, true)
			pre := typesafeClassifierTestPreflight("simple-task", "kiro", true)
			pre.Conv.CurrentContent = prompt
			resp, handled := env.server.tryTypesafeClassifier(typesafeClassifierTestRequestWithMode("intent-classification"), pre)
			if !handled || resp == nil {
				t.Fatalf("upstream HTTP %d returned (%v, %v), want handled error", tt.status, resp, handled)
			}
			responseBody, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tt.status {
				t.Fatalf("response status = %d, want upstream %d", resp.StatusCode, tt.status)
			}
			if strings.Contains(string(responseBody), apiKey) || strings.Contains(string(responseBody), prompt) {
				t.Fatalf("error response exposed a secret or prompt: %s", responseBody)
			}
			if got := env.hits.Load(); got != 1 {
				t.Fatalf("Typesafe HTTP calls = %d, want one call without retries", got)
			}
			page, err := env.db.QueryRequestUsagePage(store.RequestUsageQuery{PageSize: 10})
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != 1 || len(page.Items) != 1 {
				t.Fatalf("request usage page = %+v, want one row", page)
			}
			item := page.Items[0]
			if item.StatusCode != tt.status || item.Success || item.InputTokens != 0 || item.OutputTokens != 0 || item.Credits != 0 || item.DispatchReason != "classifier:typesafe;usage_unavailable" || !strings.Contains(item.ErrorMessage, "HTTP") {
				t.Fatalf("HTTP error usage row = %+v", item)
			}
			if got := env.stats.Snapshot(); got.TotalRequests != 1 || got.TotalErrors != 1 || got.InputTokens != 0 || got.OutputTokens != 0 || got.Credits != 0 {
				t.Fatalf("HTTP error stats = %+v", got)
			}
			logs := readTypesafeClassifierLogs(t, env)
			if strings.Contains(logs, apiKey) || strings.Contains(logs, prompt) {
				t.Fatalf("classifier logs exposed a secret or prompt: %s", logs)
			}
			if !strings.Contains(logs, `"usage_available":false`) || !strings.Contains(logs, `"error_kind":"http_`+strconv.Itoa(tt.status)+`"`) {
				t.Fatalf("logs did not identify missing usage and upstream status: %s", logs)
			}
		})
	}
}

func TestTryTypesafeClassifierInvalidSuccessResponseStillMetersReportedUsage(t *testing.T) {
	SetProxyMode(ModeForward)
	env := newTypesafeClassifierTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"model":"actual-invalid-model","usage":{"input_tokens":17,"output_tokens":4},"answers":{"intent":{"type":"choice","choice":"other","probabilities":{"chat":0.2,"do":0.7,"spec":0.1},"confidence":0.3}}}`)
	})
	env.setConfig(t, true)
	pre := typesafeClassifierTestPreflight("simple-task", "kiro", true)
	resp, handled := env.server.tryTypesafeClassifier(typesafeClassifierTestRequestWithMode("intent-classification"), pre)
	if !handled || resp == nil {
		t.Fatalf("invalid response returned (%v, %v), want handled error", resp, handled)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("invalid response status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
	page, err := env.db.QueryRequestUsagePage(store.RequestUsageQuery{PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("request usage rows = %d, want 1", len(page.Items))
	}
	item := page.Items[0]
	wantCredits := 17 * 0.042 / 1_000_000 / 0.04
	if item.ModelID != "Typesafe classifier / actual-invalid-model" || item.InputTokens != 17 || item.OutputTokens != 4 || item.DispatchReason != "classifier:typesafe" || math.Abs(item.InputCost-17*0.042/1_000_000) > 1e-15 || item.OutputCost != 0 || math.Abs(item.Credits-wantCredits) > 1e-15 {
		t.Fatalf("invalid response usage was not preserved: %+v", item)
	}
	if got := env.stats.Snapshot(); got.TotalRequests != 1 || got.TotalErrors != 1 || got.InputTokens != 17 || got.OutputTokens != 4 || math.Abs(got.Credits-wantCredits) > 1e-15 {
		t.Fatalf("invalid response stats = %+v, want credits %.15g", got, wantCredits)
	}
	logs := readTypesafeClassifierLogs(t, env)
	if !strings.Contains(logs, `"usage_available":true`) {
		t.Fatalf("invalid response log did not retain usage availability: %s", logs)
	}
}

func TestTryTypesafeClassifierHandlesCanceledAndTimedOutContexts(t *testing.T) {
	SetProxyMode(ModeForward)
	t.Run("canceled before send", func(t *testing.T) {
		env := newTypesafeClassifierTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unexpected canceled request", http.StatusInternalServerError)
		})
		env.setConfig(t, true)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := typesafeClassifierTestRequestWithMode("intent-classification").WithContext(ctx)
		resp, handled := env.server.tryTypesafeClassifier(req, typesafeClassifierTestPreflight("simple-task", "kiro", true))
		if !handled || resp == nil {
			t.Fatalf("canceled request returned (%v, %v), want handled error", resp, handled)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK || env.hits.Load() != 0 {
			t.Fatalf("canceled request status=%d, upstream calls=%d", resp.StatusCode, env.hits.Load())
		}
		page, err := env.db.QueryRequestUsagePage(store.RequestUsageQuery{PageSize: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].Success || page.Items[0].InputTokens != 0 || page.Items[0].Credits != 0 {
			t.Fatalf("canceled request usage = %+v", page.Items)
		}
	})
	t.Run("client timeout", func(t *testing.T) {
		env := newTypesafeClassifierTestEnv(t, func(_ http.ResponseWriter, req *http.Request) {
			_, _ = io.Copy(io.Discard, req.Body)
			<-req.Context().Done()
		})
		env.setConfig(t, true)
		env.setTimeout(t, 1)
		resp, handled := env.server.tryTypesafeClassifier(typesafeClassifierTestRequestWithMode("intent-classification"), typesafeClassifierTestPreflight("simple-task", "kiro", true))
		if !handled || resp == nil {
			t.Fatalf("timed out request returned (%v, %v), want handled error", resp, handled)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusGatewayTimeout || env.hits.Load() != 1 {
			t.Fatalf("timeout status=%d, upstream calls=%d; want 504 and one attempt", resp.StatusCode, env.hits.Load())
		}
		logs := readTypesafeClassifierLogs(t, env)
		if !strings.Contains(logs, `"error_kind":"timeout"`) {
			t.Fatalf("timeout log missing timeout kind: %s", logs)
		}
	})
}

func TestTryTypesafeClassifierDisabledRestoresLegacyClassifierSetting(t *testing.T) {
	SetProxyMode(ModeForward)
	env := newTypesafeClassifierTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unexpected Typesafe request", http.StatusInternalServerError)
	})
	env.setConfig(t, false)
	if err := env.db.SetSetting("classifier_model_id", "__disabled__"); err != nil {
		t.Fatal(err)
	}
	checker := env.server.licenseClient.(*typesafeClassifierTestLicense)

	resp, handled := env.server.tryTypesafeClassifier(typesafeClassifierTestRequestWithMode("intent-classification"), typesafeClassifierTestPreflight("simple-task", "kiro", true))
	if resp != nil || handled {
		t.Fatalf("disabled Typesafe classifier returned (%v, %v), want (nil, false)", resp, handled)
	}
	if !env.server.isClassifierDisabled() {
		t.Fatal("legacy classifier disabled setting was not preserved")
	}
	if env.hits.Load() != 0 || checker.calls.Load() != 0 {
		t.Fatalf("Typesafe calls=%d, license checks=%d; both should remain zero when disabled", env.hits.Load(), checker.calls.Load())
	}
}

func TestTryTypesafeClassifierOverridesLegacyDisabledSetting(t *testing.T) {
	SetProxyMode(ModeForward)
	env := newTypesafeClassifierTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "local mock rejection", http.StatusUnauthorized)
	})
	env.setConfig(t, true)
	if err := env.db.SetSetting("classifier_model_id", "__disabled__"); err != nil {
		t.Fatal(err)
	}

	resp, handled := env.server.tryTypesafeClassifier(typesafeClassifierTestRequestWithMode("intent-classification"), typesafeClassifierTestPreflight("simple-task", "kiro", true))
	if !handled || resp == nil {
		t.Fatalf("enabled Typesafe classifier returned (%v, %v), want a handled response", resp, handled)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("response status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	if env.hits.Load() != 1 {
		t.Fatalf("Typesafe HTTP calls = %d, want 1", env.hits.Load())
	}
}

func TestTryTypesafeClassifierAuthorizationRejectionDoesNotSendOrMeter(t *testing.T) {
	SetProxyMode(ModeForward)
	env := newTypesafeClassifierTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unexpected Typesafe request", http.StatusInternalServerError)
	})
	env.setConfig(t, true)
	checker := env.server.licenseClient.(*typesafeClassifierTestLicense)
	checker.result.Authorized = false

	resp, handled := env.server.tryTypesafeClassifier(typesafeClassifierTestRequestWithMode("intent-classification"), typesafeClassifierTestPreflight("simple-task", "kiro", true))
	if !handled || resp == nil {
		t.Fatalf("authorization rejection returned (%v, %v), want a handled response", resp, handled)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("response status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	if env.hits.Load() != 0 || checker.calls.Load() != 1 {
		t.Fatalf("Typesafe calls=%d, license checks=%d; want 0 and 1", env.hits.Load(), checker.calls.Load())
	}
	if got := env.stats.Snapshot(); got.TotalRequests != 0 || got.TotalErrors != 0 || got.InputTokens != 0 || got.OutputTokens != 0 || got.Credits != 0 {
		t.Fatalf("authorization rejection was metered: %+v", got)
	}
	page, err := env.db.QueryRequestUsagePage(store.RequestUsageQuery{PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("request usage rows after authorization rejection = %d, want 0", page.Total)
	}
}
