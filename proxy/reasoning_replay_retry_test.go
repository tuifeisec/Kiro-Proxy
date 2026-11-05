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

// api.deepseek.com/anthropic 的原文（magpie thinking_back_test.go deepseekThinkingBackLive，2026-11-02 实测）
const deepSeekAnthropicThinkingBack = `{"error":{"message":"The ` + "`content[].thinking`" + ` in the thinking mode must be passed back to the API. (request_id: cae816b6-d668-4a55-935d-99d1b8ad3686)","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`

const anthropicDoneStream = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"data: {\"type\":\"message_stop\"}\n\n"

func newReasoningTestForwarder(t *testing.T) (*Forwarder, *store.DB, *TrafficLogger) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "proxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger, err := NewTrafficLogger("", nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewForwarder(store.NewStats(db), db), db, logger
}

func anthropicToolTurnPre(db *store.DB, conversationID string) *Preflight {
	pre := &Preflight{Protocol: "kiro", StartTime: time.Now(), Conv: &converter.ParsedConversation{
		ConversationID: conversationID,
		History: []converter.ParsedHistoryEntry{{
			UserContent:  "fix it",
			HasAssistant: true,
			ToolUses:     []converter.ParsedToolUse{{ToolUseID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}},
		}},
		CurrentToolResults: []converter.ParsedToolResult{{ToolUseID: "call_1", Content: "ok"}},
	}}
	pre.usage = newRequestUsageRecorder(db, pre.StartTime, nil, "/", "kiro")
	return pre
}

func TestForwardCandidateRetriesAnthropicMissingThinkingOnce(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, deepSeekAnthropicThinkingBack)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicDoneStream)
	}))
	defer server.Close()

	forwarder, db, logger := newReasoningTestForwarder(t)
	pre := anthropicToolTurnPre(db, "conv-anthropic-retry")
	// 中转 host 与模型名都看不出 DeepSeek：首发不补，靠 400 识别后补齐重放
	model := &store.Model{ID: "config-relay", Provider: "anthropic", BaseURL: server.URL, Endpoint: "/v1/messages", ModelID: "relay-model", APIKey: "test"}

	resp, _, err := forwarder.forwardCandidate(context.Background(), pre, model, logger, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(bodies) != 2 {
		t.Fatalf("status=%d calls=%d", resp.StatusCode, len(bodies))
	}
	if strings.Contains(bodies[0], `"type":"thinking"`) {
		t.Fatalf("first send already had thinking: %s", bodies[0])
	}
	if !strings.Contains(bodies[1], `"type":"thinking","thinking":" "`) || strings.Contains(bodies[1], `"signature"`) {
		t.Fatalf("retry did not add unsigned placeholder thinking: %s", bodies[1])
	}
}

func TestForwardCandidateKeepsAnthropicErrorWhenReplayCannotChange(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, deepSeekAnthropicThinkingBack)
	}))
	defer server.Close()

	forwarder, db, logger := newReasoningTestForwarder(t)
	pre := anthropicToolTurnPre(db, "conv-anthropic-deepseek")
	// DeepSeek host 首发已补齐，重建结果不变：不重复发送，原样返回上游 400
	model := &store.Model{ID: "config-deepseek", Provider: "anthropic", BaseURL: server.URL, Endpoint: "/v1/messages", ModelID: "deepseek-v4", APIKey: "test"}

	resp, _, err := forwarder.forwardCandidate(context.Background(), pre, model, logger, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest || calls != 1 || string(body) != deepSeekAnthropicThinkingBack {
		t.Fatalf("status=%d calls=%d body=%s", resp.StatusCode, calls, body)
	}
}

func TestSignatureOnlyAnthropicTraceSurvivesEffortChange(t *testing.T) {
	forwarder, db, _ := newReasoningTestForwarder(t)
	model := &store.Model{ID: "config-claude", Provider: "anthropic", BaseURL: "https://api.anthropic.com", Endpoint: "/v1/messages", ModelID: "claude-sonnet-4.5", ReasoningEffort: "high"}
	saved := anthropicToolTurnPre(db, "conv-signature-only")
	trace := converter.ReasoningTrace{Present: true, Protocol: "anthropic", ToolCallIDs: []string{"call_1"}, Blocks: []converter.ReasoningBlock{
		{Type: converter.ReasoningBlockThinking, Signature: "adaptive-sig"},
		{Type: converter.ReasoningBlockRedactedThinking, Data: "sealed"},
	}}
	if err := forwarder.saveReasoningTrace(saved, model, trace); err != nil {
		t.Fatal(err)
	}

	// 下一轮换了请求档位（Auto 职责或分组变化），历史仍需找得到
	next := anthropicToolTurnPre(db, "conv-signature-only")
	next.Conv.ReasoningEffort = "low"
	history, err := forwarder.loadReasoningHistory(next, model)
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := convertRequestWithProtocol(next, model, history, false)
	if err != nil {
		t.Fatal(err)
	}
	thinkingAt := strings.Index(string(body), `"type":"thinking","thinking":"","signature":"adaptive-sig"`)
	redactedAt := strings.Index(string(body), `"type":"redacted_thinking","data":"sealed"`)
	toolAt := strings.Index(string(body), `"type":"tool_use"`)
	if thinkingAt < 0 || redactedAt < thinkingAt || toolAt < redactedAt {
		t.Fatalf("signature-only replay order wrong: %s", body)
	}
}

func TestLegacyReasoningTraceRowReplaysAsSingleThinkingBlock(t *testing.T) {
	forwarder, db, _ := newReasoningTestForwarder(t)
	model := &store.Model{ID: "config-claude", Provider: "anthropic", BaseURL: "https://api.anthropic.com", Endpoint: "/v1/messages", ModelID: "claude-sonnet-4.5"}
	pre := anthropicToolTurnPre(db, "conv-legacy")
	// 旧版本只写 text + signature，blocks_json 为空
	if err := db.UpsertReasoningTrace(&store.ReasoningTrace{
		StateKey: reasoningStateKeyFor(pre, model), TurnKey: converter.ReasoningTurnKey([]string{"call_1"}),
		Protocol: "anthropic", Text: "legacy thought", Signature: "legacy-sig", UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	history, err := forwarder.loadReasoningHistory(pre, model)
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := convertRequestWithProtocol(pre, model, history, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"type":"thinking","thinking":"legacy thought","signature":"legacy-sig"`) {
		t.Fatalf("legacy trace not replayed: %s", body)
	}
}
