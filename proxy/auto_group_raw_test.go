package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"
)

const autoGroupCompletedRaw = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n"

func autoGroupRawRoute(models ...*store.Model) *router.Route {
	group := &store.ModelGroup{ID: "raw-group", Name: "GPT group", Enabled: true, MaxAttempts: len(models), FirstByteTimeoutMS: 1000}
	route := &router.Route{Model: models[0], Group: group, Candidates: models,
		AutoMode: store.AutoRoutingModeDutyFixed, AutoReason: "duty_fixed", AutoEffortFixed: true, AutoEffort: "high",
		CandidateLimits: map[string]int{}, CandidateGroups: map[string]*store.ModelGroup{}}
	for index, model := range models {
		group.Members = append(group.Members, store.ModelGroupMember{ModelID: model.ID, Priority: index, MaxConcurrency: 1})
		route.CandidateLimits[model.ID] = 1
		route.CandidateGroups[model.ID] = group
	}
	return route
}

func autoGroupRawModels() (*store.Model, *store.Model) {
	first := rawResponsesTestModel("https://first.invalid", "openai", "/responses")
	first.ID, first.ModelID = "first", "gpt-first"
	second := rawResponsesTestModel("https://second.invalid", "openai", "/responses")
	second.ID, second.ModelID = "second", "gpt-second"
	return first, second
}

func autoGroupMockLogger(t *testing.T) *TrafficLogger {
	t.Helper()
	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logger.Close)
	return logger
}

func autoGroupMockResponse(req *http.Request, status int, body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Upstream": {"raw"}}, Body: body, Request: req, ContentLength: -1}
}

func autoGroupHealthFailures(f *Forwarder, model *store.Model) int {
	for _, status := range f.health.snapshot(time.Now()) {
		if status.ModelConfigID == model.ID {
			return status.ConsecutiveFailures
		}
	}
	return 0
}

func TestAutoGroupRawCapacityAndHTTPFallbackPreserveBody(t *testing.T) {
	for _, reason := range []string{"capacity", "http_503"} {
		t.Run(reason, func(t *testing.T) {
			db := rawResponsesTestDB(t)
			f, stats := rawResponsesTestForwarder(t, db)
			first, second := autoGroupRawModels()
			route := autoGroupRawRoute(first, second)
			// Different stages prove fallback is not reordered into the primary group.
			fallback := &store.ModelGroup{ID: "fallback", Name: "Fallback", Enabled: true, MaxAttempts: 1, FirstByteTimeoutMS: 1000}
			route.CandidateGroups[second.ID] = fallback
			route.Stages = []router.RouteStage{{GroupID: route.Group.ID, GroupName: route.Group.Name, Candidates: []*store.Model{first}}, {GroupID: fallback.ID, GroupName: fallback.Name, Candidates: []*store.Model{second}}}
			var blocker *modelLease
			if reason == "capacity" {
				var acquired bool
				blocker, acquired = f.load.acquire(first.ID, 1)
				if !acquired {
					t.Fatal("cannot occupy first candidate")
				}
				defer blocker.release()
			}
			var calls []string
			var mu sync.Mutex
			var outgoing []byte
			f.clients[""] = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				mu.Lock()
				defer mu.Unlock()
				calls = append(calls, req.URL.Host)
				if req.URL.Host == "first.invalid" {
					return autoGroupMockResponse(req, http.StatusServiceUnavailable, io.NopCloser(strings.NewReader("temporarily unavailable"))), nil
				}
				outgoing, _ = io.ReadAll(req.Body)
				return autoGroupMockResponse(req, http.StatusOK, io.NopCloser(strings.NewReader(autoGroupCompletedRaw))), nil
			})}
			const body = `{"model":"auto","input":[{"type":"function_call_output","call_id":"c1","output":"done"}],"reasoning":{"effort":"max","summary":"auto"},"prompt_cache_key":"stable","extension":{"number":9007199254740993}}`
			req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(body))
			pre := rawResponsesTestPre(db, nil, req.URL.Path, body)
			pre.SourceModel = "auto"
			pre.Conv = &converter.ParsedConversation{ModelID: "auto", ConversationID: "raw-conversation", ReasoningEffort: "max"}
			resp, err := f.ForwardRawResponses(req, pre, route, autoGroupMockLogger(t))
			if err != nil {
				t.Fatal(err)
			}
			if route.Model.ID != second.ID || pre.Protocol != "openai" {
				t.Fatal("group selection or caller protocol changed incorrectly")
			}
			if got := f.affinity.get(route.Group.ID, "raw-conversation", time.Now()); got != "" {
				t.Fatal("headers alone established affinity")
			}
			if got := f.load.snapshot()[second.ID]; got != 1 {
				t.Fatalf("active lease = %d", got)
			}
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			_ = resp.Body.Close()
			if string(data) != autoGroupCompletedRaw {
				t.Fatal("raw response changed")
			}
			for _, field := range []string{`"model":"gpt-second"`, `"effort":"high"`, `"summary":"auto"`, `"prompt_cache_key":"stable"`, `9007199254740993`, `"function_call_output"`} {
				if !bytes.Contains(outgoing, []byte(field)) {
					t.Fatalf("missing raw field %s in %s", field, outgoing)
				}
			}
			mu.Lock()
			if len(calls) == 0 || calls[len(calls)-1] != "second.invalid" {
				t.Fatal("second candidate was not called")
			}
			if reason == "capacity" && len(calls) != 1 {
				t.Fatal("full candidate was contacted")
			}
			for _, host := range calls[:len(calls)-1] {
				if host != "first.invalid" {
					t.Fatal("fallback ran before primary exhausted")
				}
			}
			mu.Unlock()
			if f.load.snapshot()[second.ID] != 0 || len(rawResponsesUsageItems(t, db)) != 1 {
				t.Fatal("lease or usage finalized incorrectly")
			}
			if got := stats.Snapshot(); got.TotalRequests != 1 || got.TotalErrors != 0 {
				t.Fatalf("stats = %+v", got)
			}
			if got := f.affinity.get(route.Group.ID, "raw-conversation", time.Now()); got != second.ID {
				t.Fatal("completion did not establish affinity")
			}
		})
	}
}

func TestAutoGroupRaw200FailureNeverReplaysAnotherMember(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		readErr    error
	}{
		{name: "failed", data: "data: {\"type\":\"response.failed\",\"response\":{\"usage\":{\"input_tokens\":2}}}\n\n"},
		{name: "empty"},
		{name: "incomplete", data: "data: {\"type\":\"response.incomplete\"}\n\n"},
		{name: "missing terminal", data: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"},
		{name: "read failure", data: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", readErr: errors.New("scripted read failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := rawResponsesTestDB(t)
			f, stats := rawResponsesTestForwarder(t, db)
			first, second := autoGroupRawModels()
			route := autoGroupRawRoute(first, second)
			calls := 0
			source := newRawResponsesScriptBody([]byte(tc.data), tc.readErr, false)
			f.clients[""] = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return autoGroupMockResponse(req, http.StatusOK, source), nil
			})}
			const body = `{"model":"auto","input":"test","stream":true}`
			req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(body))
			pre := rawResponsesTestPre(db, nil, req.URL.Path, body)
			pre.Conv = &converter.ParsedConversation{ConversationID: "stream-conversation"}
			resp, err := f.ForwardRawResponses(req, pre, route, autoGroupMockLogger(t))
			if err != nil {
				t.Fatal(err)
			}
			if source.errorReturned || len(source.data) != len(tc.data) {
				t.Fatal("raw stream was read before client read")
			}
			if autoGroupHealthFailures(f, first) != 0 {
				t.Fatal("headers changed stream health")
			}
			got, readErr := io.ReadAll(resp.Body)
			if tc.readErr == nil && readErr != nil {
				t.Fatal(readErr)
			}
			if tc.readErr != nil && readErr == nil {
				t.Fatal("read failure was swallowed")
			}
			_ = resp.Body.Close()
			_ = resp.Body.Close()
			if calls != 1 || route.Model.ID != first.ID || string(got) != tc.data {
				t.Fatal("200 stream was replayed or changed")
			}
			if f.load.snapshot()[first.ID] != 0 || autoGroupHealthFailures(f, first) != 1 {
				t.Fatal("lease or failure health did not settle once")
			}
			if got := f.affinity.get(route.Group.ID, "stream-conversation", time.Now()); got != "" {
				t.Fatal("failed stream established affinity")
			}
			items := rawResponsesUsageItems(t, db)
			if len(items) != 1 || items[0].Success {
				t.Fatalf("usage = %+v", items)
			}
			if got := stats.Snapshot(); got.TotalRequests != 1 || got.TotalErrors != 1 {
				t.Fatalf("stats = %+v", got)
			}
		})
	}
}

func TestAutoGroupRawCancellationIsNeutralAndFinalizesOnce(t *testing.T) {
	db := rawResponsesTestDB(t)
	f, stats := rawResponsesTestForwarder(t, db)
	first, second := autoGroupRawModels()
	route := autoGroupRawRoute(first, second)
	calls := 0
	source := newRawResponsesScriptBody(nil, nil, true)
	f.clients[""] = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return autoGroupMockResponse(req, http.StatusOK, source), nil
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", nil).WithContext(ctx)
	pre := rawResponsesTestPre(db, nil, req.URL.Path, `{"model":"auto","input":"test"}`)
	resp, err := f.ForwardRawResponses(req, pre, route, autoGroupMockLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = resp.Body.Close()
	_ = resp.Body.Close()
	if calls != 1 || f.load.snapshot()[first.ID] != 0 || autoGroupHealthFailures(f, first) != 0 {
		t.Fatal("cancel replayed, leaked a lease or failed health")
	}
	if len(rawResponsesUsageItems(t, db)) != 1 {
		t.Fatal("cancel did not finalize usage once")
	}
	if got := stats.Snapshot(); got.TotalRequests != 1 || got.TotalErrors != 0 {
		t.Fatalf("stats = %+v", got)
	}
}

func TestAutoGroupRawFiltersProtocolAndImageCandidatesBeforeLoad(t *testing.T) {
	for _, images := range []bool{false, true} {
		t.Run(map[bool]string{false: "protocol", true: "images"}[images], func(t *testing.T) {
			first, second := autoGroupRawModels()
			if images {
				first.ImageInputMode = store.ImageInputUnsupported
			} else {
				first.Provider = "anthropic"
			}
			route := autoGroupRawRoute(first, second)
			f := NewForwarder(nil, nil)
			calls := 0
			f.clients[""] = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Host != "second.invalid" {
					t.Fatal("incompatible candidate sent")
				}
				data, _ := io.ReadAll(req.Body)
				if images && !bytes.Contains(data, []byte(`"image_url":"data:image/png;base64,AAAA"`)) {
					t.Fatal("raw image body was projected")
				}
				return autoGroupMockResponse(req, http.StatusOK, io.NopCloser(strings.NewReader(autoGroupCompletedRaw))), nil
			})}
			pre := &Preflight{Body: []byte(`{"model":"auto","input":[{"content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`), Conv: &converter.ParsedConversation{RawResponsesHasImages: images}}
			req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", nil)
			resp, err := f.ForwardRawResponses(req, pre, route, autoGroupMockLogger(t))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if calls != 1 || route.Model.ID != second.ID || autoGroupHealthFailures(f, first) != 0 || f.load.snapshot()[first.ID] != 0 {
				t.Fatal("filter mutated incompatible candidate health/load")
			}
		})
	}
	first, _ := autoGroupRawModels()
	first.Endpoint = "/messages"
	pre := &Preflight{Body: []byte(`{"model":"auto","input":[]}`)}
	resp, err := NewForwarder(nil, nil).ForwardRawResponses(httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", nil), pre, autoGroupRawRoute(first), nil)
	if err == nil || resp != nil || !strings.Contains(err.Error(), "/responses") {
		t.Fatal("group with no compatible candidate was accepted")
	}
}

func TestAutoGroupRawNon2xxBodyAndHeadersRemainComplete(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			db := rawResponsesTestDB(t)
			f, stats := rawResponsesTestForwarder(t, db)
			first, _ := autoGroupRawModels()
			route := autoGroupRawRoute(first)
			payload := strings.Repeat("raw-error-body", int(upstreamErrorBodyLimit)/len("raw-error-body")+128)
			f.clients[""] = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				resp := autoGroupMockResponse(req, status, io.NopCloser(strings.NewReader(payload)))
				resp.ContentLength = int64(len(payload))
				resp.Header.Set("Content-Type", "application/json")
				resp.Header.Set("Content-Length", "original-value")
				return resp, nil
			})}
			req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", nil)
			resp, err := f.ForwardRawResponses(req, rawResponsesTestPre(db, nil, req.URL.Path, `{"model":"auto","input":[]}`), route, autoGroupMockLogger(t))
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if string(got) != payload || resp.StatusCode != status || resp.ContentLength != int64(len(payload)) || resp.Header.Get("Content-Length") != "original-value" {
				t.Fatal("raw HTTP error response was truncated or rebuilt")
			}
			if f.load.snapshot()[first.ID] != 0 || len(rawResponsesUsageItems(t, db)) != 1 {
				t.Fatal("HTTP error lease/usage did not finalize once")
			}
			wantFailures := 1
			if status < http.StatusInternalServerError {
				wantFailures = 0
			}
			if got := autoGroupHealthFailures(f, first); got != wantFailures {
				t.Fatalf("HTTP health failures = %d, want %d", got, wantFailures)
			}
			if got := stats.Snapshot(); got.TotalRequests != 1 || got.TotalErrors != 1 {
				t.Fatalf("stats = %+v", got)
			}
		})
	}
}

func TestAutoGroupRawTypedTransportFailureKeepsClassification(t *testing.T) {
	first, second := autoGroupRawModels()
	first.BaseURL = "https://alice:password@first.invalid/v1/responses?api_key=secret"
	first.DisableAutoSuffix = true
	f := NewForwarder(nil, nil)
	transportErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("scripted transport failure")}
	calls := 0
	f.clients[""] = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host == "first.invalid" {
			return nil, transportErr
		}
		return autoGroupMockResponse(req, http.StatusOK, io.NopCloser(strings.NewReader(autoGroupCompletedRaw))), nil
	})}
	endpoint, err := rawResponsesCandidateURL(first)
	if err != nil {
		t.Fatal(err)
	}
	safeErr := redactRawResponsesEndpointError(&url.Error{Op: "Post", URL: endpoint, Err: transportErr}, endpoint)
	for _, secret := range []string{"alice", "password", "api_key", "secret"} {
		if strings.Contains(safeErr.Error(), secret) {
			t.Fatalf("typed error exposed %s: %v", secret, safeErr)
		}
	}
	var typed *net.OpError
	if !errors.Is(safeErr, transportErr) || !errors.As(safeErr, &typed) || !isUpstreamTransportFailure(safeErr) {
		t.Fatal("redaction destroyed transport error classification")
	}
	pre := &Preflight{Body: []byte(`{"model":"auto","input":[]}`)}
	route := autoGroupRawRoute(first, second)
	resp, err := f.ForwardRawResponses(httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", nil), pre, route, autoGroupMockLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if calls != 2 || route.Model.ID != second.ID || autoGroupHealthFailures(f, first) != 1 {
		t.Fatal("typed transport failure was not classified by the group scheduler")
	}
}

func TestAutoGroupRawCancelClosesSavedHTTPFailureLease(t *testing.T) {
	first, second := autoGroupRawModels()
	f := NewForwarder(nil, nil)
	// Expired credential circuit enters half-open when the scheduler selects it.
	f.health.failure(second, modelFailureCredential, 0, time.Now().Add(-modelOpenDuration-time.Second))
	priorBody := newRawResponsesScriptBody([]byte("original 500 response"), nil, false)
	started := make(chan struct{})
	f.clients[""] = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "first.invalid" {
			return autoGroupMockResponse(req, http.StatusInternalServerError, priorBody), nil
		}
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", nil).WithContext(ctx)
	result := make(chan error, 1)
	logger := autoGroupMockLogger(t)
	go func() {
		_, err := f.ForwardRawResponses(req, &Preflight{Body: []byte(`{"model":"auto","input":[]}`)}, autoGroupRawRoute(first, second), logger)
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("second candidate did not start")
	}
	if f.load.snapshot()[first.ID] != 1 {
		t.Fatal("saved response lease was not held")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled dispatch did not return")
	}
	if f.load.snapshot()[first.ID] != 0 || f.load.snapshot()[second.ID] != 0 || priorBody.closeCalls.Load() != 1 {
		t.Fatal("saved HTTP response or candidate lease leaked on cancel")
	}
	if autoGroupHealthFailures(f, first) != 1 || autoGroupHealthFailures(f, second) != 1 {
		t.Fatal("cancel changed HTTP failure or neutral candidate health")
	}
	if allowed, state := f.health.allow(second, time.Now()); !allowed || state != "half-open" {
		t.Fatalf("cancel retained half-open reservation: allowed=%v state=%s", allowed, state)
	}
	f.health.neutral(second)
}

func TestAutoGroupRawNilBodyFinalizesAndReleasesOnce(t *testing.T) {
	db := rawResponsesTestDB(t)
	f, stats := rawResponsesTestForwarder(t, db)
	first, _ := autoGroupRawModels()
	lease, ok := f.load.acquire(first.ID, 1)
	if !ok {
		t.Fatal("cannot acquire candidate lease")
	}
	usage := newRequestUsageRecorder(db, time.Now(), first, "/v1/responses", "kiro-runtime-responses")
	outcomes := 0
	_, err := f.finalizeRawResponses(context.Background(), &http.Response{StatusCode: http.StatusOK}, usage, first, first.Endpoint, lease, func(outcome converter.StreamOutcome) {
		outcomes++
		if outcome != converter.StreamOutcomeMissingTerminal {
			t.Fatalf("nil body outcome = %s", outcome)
		}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.load.snapshot()[first.ID] != 0 || outcomes != 1 || len(rawResponsesUsageItems(t, db)) != 1 {
		t.Fatal("nil body did not settle once")
	}
	if got := stats.Snapshot(); got.TotalRequests != 1 || got.TotalErrors != 1 {
		t.Fatalf("stats = %+v", got)
	}
}
