package proxy

import (
	"encoding/binary"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

func TestUsageLimitsPreservesFractionalMonthlyCredits(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "limits.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stats := store.NewStats(db)
	defer stats.Close()
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	for _, row := range []store.DailyModelStats{
		{Date: store.StatsDay(monthStart.AddDate(0, 0, -1)), ModelID: "m", Credits: 100},
		{Date: store.StatsDay(monthStart), ModelID: "m", Credits: 3},
	} {
		if err := db.UpsertDailyStats(&row); err != nil {
			t.Fatal(err)
		}
	}
	stats.RecordRequestFullWithCredits(0, 0, 0, 0, .52, "m", false)
	s := &Server{stats: stats}
	resp := s.buildUsageLimitsResponse(httptest.NewRequest(http.MethodGet, "/getUsageLimits", nil))
	defer resp.Body.Close()
	var body struct {
		NextDateReset int64 `json:"nextDateReset"`
		Usage         []struct {
			Current       int64   `json:"currentUsage"`
			Precise       float64 `json:"currentUsageWithPrecision"`
			Rate          float64 `json:"overageRate"`
			NextDateReset int64   `json:"nextDateReset"`
		} `json:"usageBreakdownList"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Usage) != 1 || body.Usage[0].Current != 3 || body.Usage[0].Precise != 3.52 || body.Usage[0].Rate != .04 {
		t.Fatalf("usage limits=%+v", body)
	}
	reset := monthStart.AddDate(0, 1, 0).Unix()
	if body.NextDateReset != reset || body.Usage[0].NextDateReset != reset {
		t.Fatalf("reset=%+v want=%d", body, reset)
	}
}

func TestUsageLimitsReportsMonthlyReadError(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "closed-limits.db"))
	if err != nil {
		t.Fatal(err)
	}
	stats := store.NewStats(db)
	stats.Close()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	s := &Server{stats: stats, logger: logger}
	resp := s.buildUsageLimitsResponse(httptest.NewRequest(http.MethodGet, "/getUsageLimits", nil))
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "sql: database is closed") {
		t.Fatalf("monthly failure was hidden: status=%d body=%s", resp.StatusCode, body)
	}
}

func TestModelListIncludesLocalAutoWithTargetCapabilities(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	model := &store.Model{ID: "a", Name: "A", ModelID: "upstream-a", Provider: "openai", BaseURL: "https://example.invalid", Endpoint: "/chat/completions", Enabled: true, ContextWindow: 64000, MaxTokens: 12000, ImageInputMode: store.ImageInputUnsupported}
	if err := db.CreateModel(model); err != nil {
		t.Fatal(err)
	}
	s := &Server{router: router.New(db)}
	resp := s.buildModelsResponse(httptest.NewRequest(http.MethodPost, "/ListAvailableModels", nil))
	defer resp.Body.Close()
	var body struct {
		Default struct {
			ModelID string `json:"modelId"`
		} `json:"defaultModel"`
		Models []struct {
			ID     string   `json:"modelId"`
			Name   string   `json:"modelName"`
			Types  []string `json:"supportedInputTypes"`
			Limits struct {
				Input  int `json:"maxInputTokens"`
				Output int `json:"maxOutputTokens"`
			} `json:"tokenLimits"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Default.ModelID != model.ID {
		t.Fatalf("existing default changed: %+v", body.Default)
	}
	count := 0
	for _, entry := range body.Models {
		if entry.ID != "auto" {
			continue
		}
		count++
		if entry.Name != "Auto（默认路由）" || entry.Limits.Input != 64000 || entry.Limits.Output != 12000 || len(entry.Types) != 1 || entry.Types[0] != "TEXT" {
			t.Fatalf("auto capabilities=%+v", entry)
		}
	}
	if count != 1 {
		t.Fatalf("auto entries=%d", count)
	}
}

func TestAutoFailoverMetersActualCandidate(t *testing.T) {
	var rejected, completed atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var input struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if input.Model == "first" {
			rejected.Add(1)
			http.Error(w, `{"error":{"message":"quota exhausted"}}`, http.StatusTooManyRequests)
			return
		}
		completed.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":100,\"prompt_tokens_details\":{\"cached_tokens\":600}}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "failover.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	models := []*store.Model{
		{ID: "first", Name: "first", ModelID: "first", Provider: "openai", BaseURL: upstream.URL, Endpoint: "/chat/completions", APIKey: "test", Enabled: true, InputPrice: 100, OutputPrice: 100, CacheInputPrice: 100, CacheOutputPrice: 100},
		{ID: "second", Name: "second", ModelID: "second", Provider: "openai", BaseURL: upstream.URL, Endpoint: "/chat/completions", APIKey: "test", Enabled: true, InputPrice: 2, OutputPrice: 10, CacheInputPrice: .5, CacheOutputPrice: 2.5, RateMultiplier: 2},
	}
	for _, model := range models {
		if err := db.CreateModel(model); err != nil {
			t.Fatal(err)
		}
	}
	group := &store.ModelGroup{ID: "auto-group", Name: "Auto", Enabled: true, MaxAttempts: 2, Members: []store.ModelGroupMember{{ModelID: "first"}, {ModelID: "second"}}}
	if err := db.CreateModelGroup(group); err != nil {
		t.Fatal(err)
	}
	r := router.New(db)
	route := r.Resolve("auto")
	stats := store.NewStats(db)
	defer stats.Close()
	logPath := filepath.Join(t.TempDir(), "traffic.log")
	logger, err := NewTrafficLogger(logPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	body := []byte(`{"conversationState":{"conversationId":"test-auto","currentMessage":{"userInputMessage":{"content":"hello","modelId":"auto"}}}}`)
	conv, err := converter.ParseKiroRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	pre := &Preflight{Body: body, Conv: conv, Protocol: "kiro", SourceModel: "auto", StartTime: time.Now(), ConversationID: "test-auto"}
	pre.usage = newRequestUsageRecorder(db, pre.StartTime, route.Model, "/", "kiro")
	pre.usage.setRoute(route)
	fwd := NewForwarder(stats, db)
	resp, err := fwd.Forward(httptest.NewRequest(http.MethodPost, "http://runtime.test/", strings.NewReader(string(body))), pre, route, logger)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	const wantCredits = .105 // ((400*2 + 100*10 + 600*.5)/1e6 * 2) / .04
	foundMeter := false
	for len(frames) >= 16 {
		n := int(binary.BigEndian.Uint32(frames[:4]))
		h := int(binary.BigEndian.Uint32(frames[4:8]))
		if n > len(frames) || n < 16+h {
			t.Fatal("invalid event frame")
		}
		if strings.Contains(string(frames[12:12+h]), "meteringEvent") {
			var meter struct {
				Usage float64 `json:"usage"`
			}
			if err := json.Unmarshal(frames[12+h:n-4], &meter); err != nil {
				t.Fatal(err)
			}
			if math.Abs(meter.Usage-wantCredits) > 1e-12 {
				t.Fatalf("wire credits=%v", meter.Usage)
			}
			foundMeter = true
		}
		frames = frames[n:]
	}
	if !foundMeter || rejected.Load() != 1 || completed.Load() != 1 {
		t.Fatalf("meter=%v calls=%d/%d", foundMeter, rejected.Load(), completed.Load())
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		page, err := db.QueryRequestUsagePage(store.RequestUsageQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 0 {
			item := page.Items[0]
			if item.RequestedModel != "auto" || item.SelectedModelConfigID != "second" || item.ModelID != "second" || item.FailoverCount != 1 || math.Abs(item.Credits-wantCredits) > 1e-12 || math.Abs(item.InputCost+item.OutputCost+item.CacheReadCost+item.CacheWriteCost-.0042) > 1e-12 {
				t.Fatalf("usage=%+v", item)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("usage was not recorded")
		}
		time.Sleep(time.Millisecond)
	}
	if got := stats.Snapshot(); got.TotalRequests != 1 || math.Abs(got.Credits-wantCredits) > 1e-12 {
		t.Fatalf("stats=%+v", got)
	}
	s := &Server{router: r, conversationModels: make(map[string]conversationModelEntry)}
	s.rememberMainModel(pre, route)
	classifier := s.resolveClassifierRoute(&Preflight{ConversationID: pre.ConversationID, SourceModel: "simple-task", IsClassifier: true})
	if classifier == nil || classifier.Group == nil || classifier.Group.ID != group.ID || classifier.RequestedName != "auto" || len(classifier.Candidates) != 2 {
		t.Fatalf("classifier lost Auto group after failover: %+v", classifier)
	}
	for {
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, line := range strings.Split(string(data), "\n") {
			var entry LogEntry
			if json.Unmarshal([]byte(line), &entry) == nil && strings.Contains(entry.Message, "流式完成") {
				if entry.Model != "second" || math.Abs(entry.Credits-wantCredits) > 1e-12 {
					t.Fatalf("completion log=%+v", entry)
				}
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual candidate was not logged")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestClassifierRemembersExplicitModelWithoutAutoFallback(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "classifier.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	model := &store.Model{ID: "model", Name: "Manual", ModelID: "upstream-manual", Provider: "openai", BaseURL: "https://example.invalid", Endpoint: "/chat/completions", Enabled: true}
	if err := db.CreateModel(model); err != nil {
		t.Fatal(err)
	}
	s := &Server{router: router.New(db), conversationModels: make(map[string]conversationModelEntry)}
	pre := &Preflight{SourceModel: model.ID, ConversationID: "manual"}
	s.rememberMainModel(pre, s.router.Resolve(model.ID))
	route := s.resolveClassifierRoute(&Preflight{SourceModel: "simple-task", ConversationID: "manual", IsClassifier: true})
	if route == nil || route.Model.ID != model.ID || route.Group != nil {
		t.Fatalf("manual classifier route=%+v", route)
	}
}
