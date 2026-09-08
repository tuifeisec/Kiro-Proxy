package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

func TestReasoningResponsesCompatibilityUsesChatWireFormat(t *testing.T) {
	pre := &Preflight{Protocol: "kiro", Conv: &converter.ParsedConversation{
		ConversationID: "conv-1",
		History: []converter.ParsedHistoryEntry{{
			HasAssistant: true,
			ToolUses:     []converter.ParsedToolUse{{ToolUseID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}},
		}},
	}}
	model := &store.Model{Provider: "openai", BaseURL: "https://example.test/v1", Endpoint: "/responses", ModelID: "deepseek"}
	history := map[string]converter.ReasoningTrace{
		converter.ReasoningTurnKey([]string{"call_1"}): {
			Text: "inspect", Protocol: "chat", WireField: "reasoning_content",
		},
	}

	body, upstreamURL, err := convertRequestWithProtocol(pre, model, history, true)
	if err != nil {
		t.Fatal(err)
	}
	if upstreamURL != "https://example.test/v1/chat/completions" {
		t.Fatalf("upstream URL = %q", upstreamURL)
	}
	if !strings.Contains(string(body), `"reasoning_content":"inspect"`) || strings.Contains(string(body), `"type":"reasoning"`) {
		t.Fatalf("chat compatibility body = %s", body)
	}
}

func TestResponsesToChatURLOnlyRewritesPath(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"https://responses.example.test/v1/responses?route=responses", "https://responses.example.test/v1/chat/completions?route=responses"},
		{"https://responses.example.test/v1/responses/?route=responses", "https://responses.example.test/v1/chat/completions?route=responses"},
	} {
		if got := responsesToChatURL(tc.input); got != tc.want {
			t.Fatalf("chat URL = %q, want %q", got, tc.want)
		}
	}
}

func TestMissingReasoningReplayErrorIsNarrow(t *testing.T) {
	matching := `{"error":{"type":"invalid_request_error","message":"The reasoning_text in the thinking mode must be passed back to the API"}}`
	if !isMissingReasoningReplayError(400, matching) {
		t.Fatal("expected missing reasoning error to match")
	}
	for _, tc := range []struct {
		status int
		body   string
	}{
		{500, matching},
		{400, `{"error":{"type":"invalid_request_error","message":"bad model"}}`},
		{400, `{"error":{"message":"reasoning_text is optional"}}`},
	} {
		if isMissingReasoningReplayError(tc.status, tc.body) {
			t.Fatalf("unexpected match: status=%d body=%s", tc.status, tc.body)
		}
	}
}

func TestReasoningWireFieldForError(t *testing.T) {
	newAPIHeader := make(http.Header)
	newAPIHeader.Set("X-New-Api-Version", "test")
	if got := reasoningWireFieldForError(newAPIHeader, "reasoning_text"); got != "reasoning_content" {
		t.Fatalf("new-api wire field = %q", got)
	}
	if got := reasoningWireFieldForError(http.Header{}, "reasoning_content must be passed back"); got != "reasoning_content" {
		t.Fatalf("reasoning_content wire field = %q", got)
	}
	if got := reasoningWireFieldForError(http.Header{}, "reasoning_text must be passed back"); got != "reasoning_text" {
		t.Fatalf("reasoning_text wire field = %q", got)
	}
}

func TestReasoningCompatibilityHistoryAddsExplicitEmptyField(t *testing.T) {
	conv := &converter.ParsedConversation{History: []converter.ParsedHistoryEntry{{
		HasAssistant: true,
		ToolUses:     []converter.ParsedToolUse{{ToolUseID: "call_old", Name: "read_file", Input: json.RawMessage(`{}`)}},
	}}}
	history := reasoningCompatibilityHistory(conv, nil, "reasoning_content")
	compatible := converter.WithReasoningHistory(conv, history)
	body, err := converter.KiroToOpenAI(compatible, "deepseek", "high")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"reasoning_content":""`) {
		t.Fatalf("explicit empty reasoning field missing: %s", body)
	}
}

func TestLearnedChatCompatibilityFillsMissingToolTurn(t *testing.T) {
	conv := &converter.ParsedConversation{History: []converter.ParsedHistoryEntry{
		{HasAssistant: true, ToolUses: []converter.ParsedToolUse{{ToolUseID: "call_known", Name: "read_file", Input: json.RawMessage(`{}`)}}},
		{HasAssistant: true, ToolUses: []converter.ParsedToolUse{{ToolUseID: "call_missing", Name: "read_file", Input: json.RawMessage(`{}`)}}},
	}}
	history := map[string]converter.ReasoningTrace{
		converter.ReasoningTurnKey([]string{"call_known"}): {
			Present: true, Text: "inspect", Protocol: "chat", WireField: "reasoning_content",
		},
	}
	completed := reasoningHistoryFillMissing(conv, history, reasoningChatWireField(history))
	body, err := converter.KiroToOpenAI(converter.WithReasoningHistory(conv, completed), "deepseek", "high")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"reasoning_content":"inspect"`) || !strings.Contains(string(body), `"reasoning_content":""`) {
		t.Fatalf("learned chat history was not completed: %s", body)
	}
	preserved := reasoningHistoryFillMissing(conv, map[string]converter.ReasoningTrace{
		converter.ReasoningTurnKey([]string{"call_known"}): {
			Present: true, Text: "inspect", Protocol: "chat", WireField: "reasoning_text",
		},
	}, "reasoning_content")
	if got := preserved[converter.ReasoningTurnKey([]string{"call_known"})].WireField; got != "reasoning_text" {
		t.Fatalf("existing wire field was rewritten to %q", got)
	}
}

func TestReasoningStateScopeIncludesModelEndpointAndEffort(t *testing.T) {
	pre := &Preflight{Protocol: "kiro", Conv: &converter.ParsedConversation{ConversationID: "conv-1"}}
	base := &store.Model{ID: "config-1", ModelID: "deepseek", Provider: "openai", BaseURL: "https://one.test/v1", Endpoint: "/responses", ReasoningEffort: "high"}
	first := reasoningStateKeyFor(pre, base)
	if first == "" {
		t.Fatal("empty reasoning state key")
	}
	variants := []*store.Model{
		{ID: "config-2", ModelID: base.ModelID, Provider: base.Provider, BaseURL: base.BaseURL, Endpoint: base.Endpoint, ReasoningEffort: base.ReasoningEffort},
		{ID: base.ID, ModelID: base.ModelID, Provider: base.Provider, BaseURL: "https://two.test/v1", Endpoint: base.Endpoint, ReasoningEffort: base.ReasoningEffort},
		{ID: base.ID, ModelID: base.ModelID, Provider: base.Provider, BaseURL: base.BaseURL, Endpoint: "/chat/completions", ReasoningEffort: base.ReasoningEffort},
		{ID: base.ID, ModelID: base.ModelID, Provider: base.Provider, BaseURL: base.BaseURL, Endpoint: base.Endpoint, ReasoningEffort: "low"},
	}
	for _, variant := range variants {
		if got := reasoningStateKeyFor(pre, variant); got == first {
			t.Fatalf("state scope collision for %+v", variant)
		}
	}
}

func TestForwardCandidateRetriesMissingReasoningThroughChatOnce(t *testing.T) {
	var responsesCalls, chatCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			responsesCalls++
			w.Header().Set("X-New-Api-Version", "test")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"The reasoning_text in the thinking mode must be passed back to the API"}}`)
		case "/v1/chat/completions":
			chatCalls++
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"reasoning_content":"inspect"`) {
				t.Errorf("chat retry lost reasoning content: %s", body)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if r.Header.Get("x-session-id") == "conv-truncated" {
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"partial\"}}]}\n\n")
				return
			}
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "proxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	forwarder := NewForwarder(store.NewStats(db), db)
	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}

	pre := &Preflight{Protocol: "kiro", StartTime: time.Now(), Conv: &converter.ParsedConversation{
		ConversationID: "conv-retry",
		History: []converter.ParsedHistoryEntry{{
			HasAssistant: true,
			ToolUses:     []converter.ParsedToolUse{{ToolUseID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}},
		}},
	}}
	pre.usage = newRequestUsageRecorder(db, pre.StartTime, nil, "/", "kiro")
	model := &store.Model{ID: "config-1", Provider: "openai", BaseURL: server.URL + "/v1", Endpoint: "/responses", ModelID: "deepseek", APIKey: "test"}
	if err := forwarder.saveReasoningTrace(pre, model, converter.ReasoningTrace{
		Text: "inspect", Protocol: "responses", WireField: "reasoning_summary_text", ToolCallIDs: []string{"call_1"},
	}); err != nil {
		t.Fatal(err)
	}

	resp, _, err := forwarder.forwardCandidate(context.Background(), pre, model, logger, 0)
	if err != nil {
		t.Fatal(err)
	}
	if protocol := upstreamMetadataFromResponse(resp).Protocol; resp.StatusCode != http.StatusOK || protocol != "chat" {
		t.Fatalf("retry response status=%d protocol=%q", resp.StatusCode, protocol)
	}
	if endpoint := sseProbeEndpointForResponse(resp, model.Endpoint); endpoint != "/v1/chat/completions" {
		t.Fatalf("retry probe endpoint=%q", endpoint)
	}
	if err := probeSSEPrecommit(context.Background(), resp, model.Provider, sseProbeEndpointForResponse(resp, model.Endpoint), time.Second, 0); err != nil {
		t.Fatalf("chat compatibility stream rejected by precommit probe: %v", err)
	}
	if responsesCalls != 1 || chatCalls != 1 {
		t.Fatalf("calls: responses=%d chat=%d", responsesCalls, chatCalls)
	}
	history, err := forwarder.loadReasoningHistory(pre, model)
	if err != nil {
		t.Fatal(err)
	}
	if reasoningHistoryPrefersChat(history) {
		t.Fatalf("compatibility mode persisted before stream completion: %+v", history)
	}
	outcomeStarted := make(chan struct{})
	allowCompletion := make(chan struct{})
	wrapped, err := forwarder.finalizeForwardResponse(context.Background(), resp, pre, model, logger, nil, func(converter.StreamOutcome) {
		close(outcomeStarted)
		<-allowCompletion
	})
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, readErr := io.ReadAll(wrapped.Body)
		readDone <- readErr
	}()
	<-outcomeStarted
	select {
	case readErr := <-readDone:
		t.Fatalf("stream EOF returned before completion state was persisted: %v", readErr)
	case <-time.After(20 * time.Millisecond):
	}
	close(allowCompletion)
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	_ = wrapped.Body.Close()
	history, err = forwarder.loadReasoningHistory(pre, model)
	if err != nil {
		t.Fatal(err)
	}
	if !reasoningHistoryPrefersChat(history) {
		t.Fatalf("successful chat compatibility mode was not persisted before EOF: %+v", history)
	}

	second, _, err := forwarder.forwardCandidate(context.Background(), pre, model, logger, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if protocol := upstreamMetadataFromResponse(second).Protocol; second.StatusCode != http.StatusOK || protocol != "chat" {
		t.Fatalf("learned response status=%d protocol=%q", second.StatusCode, protocol)
	}
	if responsesCalls != 1 || chatCalls != 2 {
		t.Fatalf("learned routing calls: responses=%d chat=%d", responsesCalls, chatCalls)
	}

	failedPre := &Preflight{Protocol: "kiro", StartTime: time.Now(), Conv: &converter.ParsedConversation{
		ConversationID: "conv-truncated",
		History: []converter.ParsedHistoryEntry{{
			HasAssistant: true,
			ToolUses:     []converter.ParsedToolUse{{ToolUseID: "call_2", Name: "read_file", Input: json.RawMessage(`{}`)}},
		}},
	}}
	failedPre.usage = newRequestUsageRecorder(db, failedPre.StartTime, nil, "/", "kiro")
	if err := forwarder.saveReasoningTrace(failedPre, model, converter.ReasoningTrace{
		Text: "inspect", Protocol: "responses", WireField: "reasoning_summary_text", ToolCallIDs: []string{"call_2"},
	}); err != nil {
		t.Fatal(err)
	}
	failedResp, failedCont, err := forwarder.forwardCandidate(context.Background(), failedPre, model, logger, 0)
	if err != nil {
		t.Fatal(err)
	}
	outcomeCh := make(chan converter.StreamOutcome, 1)
	failedWrapped, err := forwarder.finalizeForwardResponse(context.Background(), failedResp, failedPre, model, logger, failedCont, func(outcome converter.StreamOutcome) {
		outcomeCh <- outcome
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(failedWrapped.Body)
	_ = failedWrapped.Body.Close()
	if outcome := <-outcomeCh; outcome == converter.StreamOutcomeCompleted {
		t.Fatalf("truncated stream outcome = %s", outcome)
	}
	failedHistory, err := forwarder.loadReasoningHistory(failedPre, model)
	if err != nil {
		t.Fatal(err)
	}
	if reasoningHistoryPrefersChat(failedHistory) {
		t.Fatalf("truncated stream persisted compatibility mode: %+v", failedHistory)
	}
}

func TestForwardCandidatePreservesReasoningErrorWhenRetryBudgetIsConsumed(t *testing.T) {
	const errorBody = `{"error":{"type":"invalid_request_error","message":"The reasoning_text in the thinking mode must be passed back to the API"}}`
	var chatCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			chatCalls++
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-New-Api-Version", "test")
		w.WriteHeader(http.StatusBadRequest)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, errorBody)
	}))
	defer server.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "proxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	forwarder := NewForwarder(store.NewStats(db), db)
	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}
	pre := &Preflight{Protocol: "kiro", StartTime: time.Now(), Conv: &converter.ParsedConversation{
		ConversationID: "conv-budget",
		History: []converter.ParsedHistoryEntry{{
			HasAssistant: true,
			ToolUses:     []converter.ParsedToolUse{{ToolUseID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}},
		}},
	}}
	pre.usage = newRequestUsageRecorder(db, pre.StartTime, nil, "/", "kiro")
	model := &store.Model{ID: "config-1", Provider: "openai", BaseURL: server.URL + "/v1", Endpoint: "/responses", ModelID: "deepseek", APIKey: "test"}

	resp, _, err := forwarder.forwardCandidate(context.Background(), pre, model, logger, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest || string(body) != errorBody {
		t.Fatalf("response status=%d body=%s", resp.StatusCode, body)
	}
	if resp.ContentLength != int64(len(errorBody)) || chatCalls != 0 {
		t.Fatalf("content_length=%d chat_calls=%d", resp.ContentLength, chatCalls)
	}
}

func TestForwardCandidateRetriesDirectChatMissingReasoningOnce(t *testing.T) {
	var calls int
	var lastBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		lastBody = string(body)
		if calls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"reasoning_content must be passed back"}}`)
			return
		}
		if !strings.Contains(string(body), `"reasoning_content":""`) {
			t.Errorf("direct chat retry did not add empty reasoning field: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "proxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	forwarder := NewForwarder(store.NewStats(db), db)
	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}
	pre := &Preflight{Protocol: "kiro", StartTime: time.Now(), Conv: &converter.ParsedConversation{
		ConversationID: "conv-direct-chat",
		History: []converter.ParsedHistoryEntry{{
			HasAssistant: true,
			ToolUses:     []converter.ParsedToolUse{{ToolUseID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}},
		}},
	}}
	pre.usage = newRequestUsageRecorder(db, pre.StartTime, nil, "/", "kiro")
	model := &store.Model{ID: "config-chat", Provider: "openai", BaseURL: server.URL + "/v1", Endpoint: "/chat/completions", ModelID: "deepseek", APIKey: "test"}

	resp, contResult, err := forwarder.forwardCandidate(context.Background(), pre, model, logger, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || calls != 2 {
		t.Fatalf("status=%d calls=%d", resp.StatusCode, calls)
	}
	wrapped, err := forwarder.finalizeForwardResponse(context.Background(), resp, pre, model, logger, contResult, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(wrapped.Body)
	_ = wrapped.Body.Close()
	history, err := forwarder.loadReasoningHistory(pre, model)
	if err != nil {
		t.Fatal(err)
	}
	if !reasoningHistoryPrefersChat(history) {
		t.Fatalf("direct chat compatibility state was not persisted: %+v", history)
	}
	pre.Conv.History = append(pre.Conv.History, converter.ParsedHistoryEntry{
		HasAssistant: true,
		ToolUses:     []converter.ParsedToolUse{{ToolUseID: "call_2", Name: "read_file", Input: json.RawMessage(`{}`)}},
	})
	second, _, err := forwarder.forwardCandidate(context.Background(), pre, model, logger, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusOK || calls != 3 || strings.Count(lastBody, `"reasoning_content":`) != 2 {
		t.Fatalf("learned direct chat status=%d calls=%d body=%s", second.StatusCode, calls, lastBody)
	}
}
