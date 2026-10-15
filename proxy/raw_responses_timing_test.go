package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A network delay moves the same clock without sleeping. If preparation timing
// ever extends past HTTP dispatch, the recorded duration will include an hour.
type rawPreparationTestClock struct {
	mu    sync.Mutex
	now   time.Time
	calls int
}

func (c *rawPreparationTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	value := c.now
	c.now = c.now.Add(17 * time.Millisecond)
	c.calls++
	return value
}

func (c *rawPreparationTestClock) networkDelay() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Hour)
	return c.calls
}

func rawPreparationRecordedMS(r *requestUsageRecorder) int64 {
	r.traceMu.Lock()
	defer r.traceMu.Unlock()
	return r.trace.conversionMS
}

func rawPreparationSuccessResponse(req *http.Request, endpoint string) *http.Response {
	resp := autoGroupMockResponse(req, http.StatusOK, io.NopCloser(strings.NewReader(autoGroupCompletedRaw)))
	if endpoint == "/chat/completions" {
		resp.Header.Set("Content-Type", "application/json")
		resp.Body = io.NopCloser(strings.NewReader(`{"id":"chat","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`))
	}
	return resp
}

func TestRawPreparationTimingSingleModel(t *testing.T) {
	for _, endpoint := range []string{"/responses", "/chat/completions"} {
		for _, existingUsage := range []bool{false, true} {
			name := endpoint + "/new_usage"
			if existingUsage {
				name = endpoint + "/existing_usage"
			}
			t.Run(name, func(t *testing.T) {
				db := rawResponsesTestDB(t)
				f, stats := rawResponsesTestForwarder(t, db)
				model := rawResponsesTestModel("https://timing.invalid", "openai", endpoint)
				const body = `{"model":"auto","input":"hello"}`
				req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(body))
				pre := rawResponsesTestPre(db, model, req.URL.Path, body)
				if !existingUsage {
					pre.usage = nil
				}
				clock := &rawPreparationTestClock{now: time.Unix(100, 0)}
				f.rawPreparationNow = clock.Now
				calls := 0
				f.clients[""] = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if got := clock.networkDelay(); got != 2 {
						t.Errorf("clock calls before HTTP = %d, want 2", got)
					}
					if pre.usage != nil && rawPreparationRecordedMS(pre.usage) != 17 {
						t.Error("preparation not recorded before HTTP")
					}
					return rawPreparationSuccessResponse(r, endpoint), nil
				})}
				resp, err := f.ForwardRawResponses(req, pre, rawResponsesTestRoute(model), nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadAll(resp.Body); err != nil {
					t.Fatal(err)
				}
				if err := resp.Body.Close(); err != nil {
					t.Fatal(err)
				}
				items := rawResponsesUsageItems(t, db)
				if len(items) != 1 || items[0].ConversionMS != 17 || !items[0].Success || items[0].SelectedModelConfigID != model.ID {
					t.Fatalf("usage = %+v, want one successful record with conversion_ms=17", items)
				}
				if calls != 1 || f.load.snapshot()[model.ID] != 0 {
					t.Fatal("HTTP count or lease changed")
				}
				if got := stats.Snapshot(); got.TotalRequests != 1 || got.TotalErrors != 0 {
					t.Fatalf("stats = %+v", got)
				}
			})
		}
	}
}

func TestRawPreparationTimingGroupAccumulatesCandidates(t *testing.T) {
	for _, endpoint := range []string{"/responses", "/chat/completions"} {
		t.Run(endpoint, func(t *testing.T) {
			db := rawResponsesTestDB(t)
			f, stats := rawResponsesTestForwarder(t, db)
			first, second := autoGroupRawModels()
			first.Endpoint, second.Endpoint = endpoint, endpoint
			const body = `{"model":"auto","input":"hello"}`
			req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(body))
			pre := rawResponsesTestPre(db, nil, req.URL.Path, body)
			clock := &rawPreparationTestClock{now: time.Unix(100, 0)}
			f.rawPreparationNow = clock.Now
			var mu sync.Mutex
			calls := 0
			f.clients[""] = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				if got := clock.networkDelay(); got != calls*2 {
					t.Errorf("clock calls = %d, HTTP calls = %d", got, calls)
				}
				if got := rawPreparationRecordedMS(pre.usage); got != int64(calls*17) {
					t.Errorf("conversion_ms = %d before HTTP call %d", got, calls)
				}
				if r.URL.Host == "first.invalid" {
					return autoGroupMockResponse(r, http.StatusServiceUnavailable, io.NopCloser(strings.NewReader("unavailable"))), nil
				}
				return rawPreparationSuccessResponse(r, endpoint), nil
			})}
			resp, err := f.ForwardRawResponses(req, pre, autoGroupRawRoute(first, second), autoGroupMockLogger(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadAll(resp.Body); err != nil {
				t.Fatal(err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
			items := rawResponsesUsageItems(t, db)
			if len(items) != 1 || items[0].ConversionMS != 34 || !items[0].Success || items[0].SelectedModelConfigID != second.ID || items[0].FailoverCount != 1 {
				t.Fatalf("usage = %+v, want two preparations totaling 34ms and one failover", items)
			}
			mu.Lock()
			defer mu.Unlock()
			if calls != 2 || f.load.snapshot()[first.ID] != 0 || f.load.snapshot()[second.ID] != 0 {
				t.Fatal("HTTP count or leases changed")
			}
			if got := stats.Snapshot(); got.TotalRequests != 1 || got.TotalErrors != 0 {
				t.Fatalf("stats = %+v", got)
			}
		})
	}
}

func TestRawPreparationTimingFailurePreservesSettlement(t *testing.T) {
	for _, mode := range []string{"single_existing", "single_new", "group"} {
		t.Run(mode, func(t *testing.T) {
			db := rawResponsesTestDB(t)
			f, _ := rawResponsesTestForwarder(t, db)
			model := rawResponsesTestModel("https://timing.invalid", "openai", "/responses")
			route := rawResponsesTestRoute(model)
			if mode == "group" {
				route = autoGroupRawRoute(model)
			}
			const body = `{"input":"hello","metadata":[]}`
			req := httptest.NewRequest(http.MethodPost, "http://runtime.local/v1/responses", strings.NewReader(body))
			pre := rawResponsesTestPre(db, model, req.URL.Path, body)
			if mode == "single_new" {
				pre.usage = nil
			}
			clock := &rawPreparationTestClock{now: time.Unix(100, 0)}
			f.rawPreparationNow = clock.Now
			f.clients[""] = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				t.Error("invalid preparation reached HTTP")
				return rawPreparationSuccessResponse(r, "/responses"), nil
			})}
			_, err := f.ForwardRawResponses(req, pre, route, autoGroupMockLogger(t))
			if err == nil || !strings.Contains(err.Error(), "raw Responses metadata must be an object or null") {
				t.Fatalf("error = %v", err)
			}
			if got := f.load.snapshot()[model.ID]; got != 0 {
				t.Fatalf("lease leaked: %d", got)
			}
			if got := rawResponsesUsageItems(t, db); len(got) != 0 {
				t.Fatalf("preparation changed settlement owner: %+v", got)
			}
			if pre.usage != nil {
				if got := rawPreparationRecordedMS(pre.usage); got != 17 {
					t.Fatalf("failure conversion_ms = %d", got)
				}
				pre.usage.finish(http.StatusBadGateway, localCompatUsage{}, err.Error())
				pre.usage.finish(http.StatusBadGateway, localCompatUsage{}, err.Error())
				items := rawResponsesUsageItems(t, db)
				if len(items) != 1 || items[0].ConversionMS != 17 || items[0].Success {
					t.Fatalf("failure usage = %+v", items)
				}
			}
		})
	}
}
