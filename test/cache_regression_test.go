package test

import (
	"bytes"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/store"
)

func TestContextGathererBootstrapRemainsAppendOnly(t *testing.T) {
	const (
		conversationID = "sess-cache-regression"
		continuationID = "context-branch-1"
		bootstrap      = "# Context Gathering Agent\n\n<session_start_snapshot>tree</session_start_snapshot>\n\ninspect cache flow\n\n<EnvironmentContext>editor</EnvironmentContext>"
	)

	first := parseKiroFixture(t, kiroFixture(
		"context-gatherer",
		conversationID,
		continuationID,
		bootstrap,
		nil,
		nil,
	))
	second := parseKiroFixture(t, kiroFixture(
		"context-gatherer",
		conversationID,
		continuationID,
		"",
		[]any{
			map[string]any{"userInputMessage": map[string]any{"content": bootstrap}},
			map[string]any{"assistantResponseMessage": map[string]any{
				"content": "",
				"toolUses": []any{map[string]any{
					"toolUseId": "tool-1",
					"name":      "read_file",
					"input":     map[string]any{"path": "main.go"},
				}},
			}},
		},
		[]any{map[string]any{
			"toolUseId": "tool-1",
			"status":    "success",
			"content":   []any{map[string]any{"text": "package main"}},
		}},
	))

	if first.SystemText != "" || first.CurrentContent != bootstrap {
		t.Fatalf("首轮 bootstrap 角色错误: system=%q current=%q", first.SystemText, first.CurrentContent)
	}
	if len(second.History) < 1 || second.History[0].IsSystem || second.History[0].UserContent != bootstrap {
		t.Fatalf("续轮 bootstrap 未保持 user: %+v", second.History)
	}
	if first.ContinuationStateKey == conversationID || first.ContinuationStateKey == "" {
		t.Fatalf("context continuation key 未隔离: %q", first.ContinuationStateKey)
	}
	if first.ContinuationStateKey != second.ContinuationStateKey {
		t.Fatalf("同一 context 分支 key 不稳定: %q != %q", first.ContinuationStateKey, second.ContinuationStateKey)
	}
	if first.PromptAffinityKey != conversationID || second.PromptAffinityKey != conversationID {
		t.Fatalf("prompt affinity 不应随 context 分支变化: %q %q", first.PromptAffinityKey, second.PromptAffinityKey)
	}

	firstBody, err := converter.KiroToResponses(first, "gpt-test", "")
	if err != nil {
		t.Fatalf("转换首轮 Responses: %v", err)
	}
	secondBody, err := converter.KiroToResponses(second, "gpt-test", "")
	if err != nil {
		t.Fatalf("转换续轮 Responses: %v", err)
	}
	firstInput := extractRawArray(t, firstBody, "input")
	secondInput := extractRawArray(t, secondBody, "input")
	assertRawPrefix(t, firstInput, secondInput)

	firstState, err := converter.ApplyResponsesContinuation(firstBody, nil)
	if err != nil {
		t.Fatalf("生成首轮 continuation 状态: %v", err)
	}
	continued, err := converter.ApplyResponsesContinuation(secondBody, &converter.ResponsesContinuationState{
		ResponseID: "resp-first",
		InputJSON:  firstState.FullInputJSON,
		ItemCount:  firstState.InputItemCount,
	})
	if err != nil {
		t.Fatalf("应用续传: %v", err)
	}
	if !continued.UsedContinuation {
		t.Fatal("context 工具续轮未命中严格前缀 continuation")
	}
	delta := extractRawArray(t, continued.Body, "input")
	if len(delta) != 1 {
		t.Fatalf("续传应只发送新的工具结果，delta items = %d", len(delta))
	}
	var item struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
	}
	if err := json.Unmarshal(delta[0], &item); err != nil {
		t.Fatalf("解析续传 delta: %v", err)
	}
	if item.Type != "function_call_output" || item.CallID != "tool-1" {
		t.Fatalf("续传重复发送了上次响应输出: %s", delta[0])
	}
}

func TestOpenAICacheWriteUsesInputPriceMultiplier(t *testing.T) {
	breakdown := store.CalculateCreditBreakdown(1400, 0, 200, 1000, &store.Model{
		Provider:       "openai",
		RateMultiplier: 1,
		InputPrice:     2,
	})
	const epsilon = 1e-12
	if math.Abs(breakdown.Input-0.0004) > epsilon ||
		math.Abs(breakdown.CacheRead-0.0001) > epsilon ||
		math.Abs(breakdown.CacheWrite-0.0025) > epsilon {
		t.Fatalf("OpenAI cache pricing mismatch: %+v", breakdown)
	}
}

func TestResponsesContinuationSkipsPreviousAssistantMessage(t *testing.T) {
	previousInput := []any{
		map[string]any{"type": "message", "role": "developer", "content": "stable"},
		map[string]any{"type": "message", "role": "user", "content": "first"},
	}
	currentInput := append(append([]any{}, previousInput...),
		map[string]any{"type": "message", "role": "assistant", "content": "first answer"},
		map[string]any{"type": "message", "role": "user", "content": "second"},
	)
	previousJSON, err := json.Marshal(previousInput)
	if err != nil {
		t.Fatalf("marshal previous input: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"model":                "gpt-5.6",
		"input":                currentInput,
		"stream":               true,
		"store":                true,
		"prompt_cache_key":     "kiro-responses-test",
		"prompt_cache_options": map[string]any{"mode": "implicit", "ttl": "30m"},
	})
	if err != nil {
		t.Fatalf("marshal current request: %v", err)
	}

	continued, err := converter.ApplyResponsesContinuation(body, &converter.ResponsesContinuationState{
		ResponseID: "resp-first",
		InputJSON:  string(previousJSON),
		ItemCount:  len(previousInput),
	})
	if err != nil {
		t.Fatalf("apply continuation: %v", err)
	}
	if !continued.UsedContinuation {
		t.Fatal("普通 assistant/user 续轮未使用 previous_response_id")
	}

	var request struct {
		PreviousResponseID string `json:"previous_response_id"`
		PromptCacheOptions struct {
			Mode string `json:"mode"`
			TTL  string `json:"ttl"`
		} `json:"prompt_cache_options"`
	}
	if err := json.Unmarshal(continued.Body, &request); err != nil {
		t.Fatalf("解析续传请求: %v", err)
	}
	delta := extractRawArray(t, continued.Body, "input")
	if len(delta) != 1 || !bytes.Contains(delta[0], []byte(`"role":"user"`)) || bytes.Contains(delta[0], []byte("first answer")) {
		t.Fatalf("续传 delta 包含重复 assistant 输出: %s", delta)
	}
	if request.PreviousResponseID != "resp-first" || request.PromptCacheOptions.Mode != "implicit" || request.PromptCacheOptions.TTL != "30m" {
		t.Fatalf("续传请求丢失缓存字段: %+v", request)
	}
}

func TestResponsesSessionsAreIsolatedByContinuationStateKey(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "cache-regression.db"))
	if err != nil {
		t.Fatalf("打开测试数据库: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("关闭测试数据库: %v", err)
		}
	})

	const conversationID = "sess-state-isolation"
	vibe := parseKiroFixture(t, kiroFixture("vibe", conversationID, "vibe-turn", "main", nil, nil))
	context := parseKiroFixture(t, kiroFixture("context-gatherer", conversationID, "context-branch", "inspect", nil, nil))
	if vibe.ContinuationStateKey == context.ContinuationStateKey {
		t.Fatalf("不同分支生成了相同状态键: %q", vibe.ContinuationStateKey)
	}

	now := time.Now()
	for _, session := range []*store.ResponsesSession{
		{StateKey: vibe.ContinuationStateKey, ResponseID: "resp-vibe", InputJSON: `[{"role":"user","content":"main"}]`, ItemCount: 1, UpdatedAt: now},
		{StateKey: context.ContinuationStateKey, ResponseID: "resp-context", InputJSON: `[{"role":"user","content":"inspect"}]`, ItemCount: 1, UpdatedAt: now},
	} {
		if err := db.UpsertResponsesSession(session); err != nil {
			t.Fatalf("保存续传状态 %q: %v", session.StateKey, err)
		}
	}

	gotVibe, err := db.GetResponsesSession(vibe.ContinuationStateKey)
	if err != nil {
		t.Fatalf("读取 vibe 状态: %v", err)
	}
	gotContext, err := db.GetResponsesSession(context.ContinuationStateKey)
	if err != nil {
		t.Fatalf("读取 context 状态: %v", err)
	}
	if gotVibe == nil || gotVibe.ResponseID != "resp-vibe" {
		t.Fatalf("vibe 状态被覆盖: %+v", gotVibe)
	}
	if gotContext == nil || gotContext.ResponseID != "resp-context" {
		t.Fatalf("context 状态被覆盖: %+v", gotContext)
	}

	if err := db.DeleteResponsesSession(context.ContinuationStateKey); err != nil {
		t.Fatalf("删除 context 状态: %v", err)
	}
	gotVibe, err = db.GetResponsesSession(vibe.ContinuationStateKey)
	if err != nil || gotVibe == nil || gotVibe.ResponseID != "resp-vibe" {
		t.Fatalf("删除 context 状态影响了 vibe: state=%+v err=%v", gotVibe, err)
	}
}

func TestContextCompactionResetsThenRebuildsContinuation(t *testing.T) {
	const (
		conversationID = "sess-context-compaction"
		continuationID = "vibe-task-after-compaction"
		summaryPrompt  = "[SYSTEM NOTE: This is an automated summarization request due to context limit]\n\nCreate a structured summary."
		resumePrompt   = "[SYSTEM NOTE: context compacted - resume from the summary and recent messages above.]"
	)

	summary := parseKiroFixture(t, kiroFixture(
		"vibe",
		conversationID,
		"",
		summaryPrompt,
		[]any{
			map[string]any{"userInputMessage": map[string]any{"content": "old context"}},
			map[string]any{"assistantResponseMessage": map[string]any{"content": "old response"}},
		},
		nil,
	))
	compacted := parseKiroFixture(t, kiroFixture(
		"vibe",
		conversationID,
		continuationID,
		resumePrompt,
		[]any{
			map[string]any{"userInputMessage": map[string]any{"content": "compressed conversation summary"}},
			map[string]any{"assistantResponseMessage": map[string]any{"content": "summary acknowledged"}},
		},
		nil,
	))
	postCompaction := parseKiroFixture(t, kiroFixture(
		"vibe",
		conversationID,
		continuationID,
		"",
		[]any{
			map[string]any{"userInputMessage": map[string]any{"content": "compressed conversation summary"}},
			map[string]any{"assistantResponseMessage": map[string]any{"content": "summary acknowledged"}},
			map[string]any{"userInputMessage": map[string]any{"content": resumePrompt}},
			map[string]any{"assistantResponseMessage": map[string]any{
				"content": "",
				"toolUses": []any{map[string]any{
					"toolUseId": "tool-after-compaction",
					"name":      "read_file",
					"input":     map[string]any{"path": "main.go"},
				}},
			}},
		},
		[]any{map[string]any{
			"toolUseId": "tool-after-compaction",
			"status":    "success",
			"content":   []any{map[string]any{"text": "package main"}},
		}},
	))

	for name, conv := range map[string]*converter.ParsedConversation{
		"summary":         summary,
		"compacted":       compacted,
		"post-compaction": postCompaction,
	} {
		if conv.ContinuationStateKey != conversationID {
			t.Fatalf("%s 的 vibe 状态键不稳定: %q", name, conv.ContinuationStateKey)
		}
	}

	summaryBody, err := converter.KiroToResponses(summary, "gpt-test", "")
	if err != nil {
		t.Fatalf("转换摘要请求: %v", err)
	}
	summaryState, err := converter.ApplyResponsesContinuation(summaryBody, nil)
	if err != nil {
		t.Fatalf("生成摘要状态: %v", err)
	}

	compactedBody, err := converter.KiroToResponses(compacted, "gpt-test", "")
	if err != nil {
		t.Fatalf("转换压缩后请求: %v", err)
	}
	reset, err := converter.ApplyResponsesContinuation(compactedBody, &converter.ResponsesContinuationState{
		ResponseID: "resp-summary",
		InputJSON:  summaryState.FullInputJSON,
		ItemCount:  summaryState.InputItemCount,
	})
	if err != nil {
		t.Fatalf("检查压缩后重置: %v", err)
	}
	if reset.UsedContinuation {
		t.Fatal("历史已被压缩重写，不应沿用摘要请求的 previous_response_id")
	}

	compactedState, err := converter.ApplyResponsesContinuation(compactedBody, nil)
	if err != nil {
		t.Fatalf("建立压缩后基线: %v", err)
	}
	postBody, err := converter.KiroToResponses(postCompaction, "gpt-test", "")
	if err != nil {
		t.Fatalf("转换压缩后工具续轮: %v", err)
	}
	continued, err := converter.ApplyResponsesContinuation(postBody, &converter.ResponsesContinuationState{
		ResponseID: "resp-compacted",
		InputJSON:  compactedState.FullInputJSON,
		ItemCount:  compactedState.InputItemCount,
	})
	if err != nil {
		t.Fatalf("应用压缩后续传: %v", err)
	}
	if !continued.UsedContinuation {
		t.Fatal("压缩后的新基线未在下一工具轮命中 continuation")
	}
}

func TestEnvironmentContextHasSameCurrentAndHistoryShape(t *testing.T) {
	const (
		conversationID = "sess-environment-regression"
		content        = "fix cache\n\n<EnvironmentContext><ACTIVE-EDITOR-FILE>main.go</ACTIVE-EDITOR-FILE></EnvironmentContext>"
	)

	first := parseKiroFixture(t, kiroFixture("vibe", conversationID, "vibe-turn-1", content, nil, nil))
	second := parseKiroFixture(t, kiroFixture(
		"vibe",
		conversationID,
		"vibe-turn-2",
		"next question",
		[]any{
			map[string]any{"userInputMessage": map[string]any{"content": content}},
			map[string]any{"assistantResponseMessage": map[string]any{"content": "done"}},
		},
		nil,
	))

	if first.ContinuationStateKey != conversationID || second.ContinuationStateKey != conversationID {
		t.Fatalf("vibe continuation key 不应随任务 ID 变化: %q %q", first.ContinuationStateKey, second.ContinuationStateKey)
	}

	converters := []struct {
		name  string
		field string
		build func(*converter.ParsedConversation) ([]byte, error)
	}{
		{name: "openai", field: "messages", build: func(conv *converter.ParsedConversation) ([]byte, error) {
			return converter.KiroToOpenAI(conv, "gpt-test", "")
		}},
		{name: "responses", field: "input", build: func(conv *converter.ParsedConversation) ([]byte, error) {
			return converter.KiroToResponses(conv, "gpt-test", "")
		}},
		{name: "anthropic", field: "messages", build: func(conv *converter.ParsedConversation) ([]byte, error) {
			return converter.KiroToAnthropic(conv, "claude-test", "")
		}},
		{name: "gemini", field: "contents", build: func(conv *converter.ParsedConversation) ([]byte, error) {
			return converter.KiroToGemini(conv, "gemini-test", "")
		}},
	}

	for _, tc := range converters {
		t.Run(tc.name, func(t *testing.T) {
			firstBody, err := tc.build(first)
			if err != nil {
				t.Fatalf("转换首轮: %v", err)
			}
			secondBody, err := tc.build(second)
			if err != nil {
				t.Fatalf("转换续轮: %v", err)
			}
			previous := extractRawArray(t, firstBody, tc.field)
			current := extractRawArray(t, secondBody, tc.field)
			if tc.name == "anthropic" {
				previous = withoutCacheControl(t, previous)
				current = withoutCacheControl(t, current)
			}
			assertRawPrefix(t, previous, current)
		})
	}
}

func kiroFixture(agentMode, conversationID, continuationID, current string, history, toolResults []any) []byte {
	context := map[string]any{}
	if len(toolResults) > 0 {
		context["toolResults"] = toolResults
	}
	user := map[string]any{
		"content": current,
		"modelId": "claude-haiku-4.5",
	}
	if len(context) > 0 {
		user["userInputMessageContext"] = context
	}
	body := map[string]any{
		"agentMode": agentMode,
		"conversationState": map[string]any{
			"conversationId":      conversationID,
			"agentContinuationId": continuationID,
			"currentMessage": map[string]any{
				"userInputMessage": user,
			},
			"history": history,
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return encoded
}

func parseKiroFixture(t *testing.T, body []byte) *converter.ParsedConversation {
	t.Helper()
	conv, err := converter.ParseKiroRequest(body)
	if err != nil {
		t.Fatalf("解析 Kiro fixture: %v", err)
	}
	return conv
}

func extractRawArray(t *testing.T, body []byte, field string) []json.RawMessage {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("解析 provider body: %v", err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(envelope[field], &items); err != nil {
		t.Fatalf("解析 %s: %v", field, err)
	}
	return items
}

func assertRawPrefix(t *testing.T, previous, current []json.RawMessage) {
	t.Helper()
	if len(previous) == 0 || len(current) <= len(previous) {
		t.Fatalf("不是增长序列: previous=%d current=%d", len(previous), len(current))
	}
	for i := range previous {
		if !bytes.Equal(previous[i], current[i]) {
			t.Fatalf("第 %d 项前缀变化:\nprevious=%s\ncurrent=%s", i, previous[i], current[i])
		}
	}
}

func withoutCacheControl(t *testing.T, items []json.RawMessage) []json.RawMessage {
	t.Helper()
	normalized := make([]json.RawMessage, len(items))
	for i, item := range items {
		var value any
		if err := json.Unmarshal(item, &value); err != nil {
			t.Fatalf("decode item %d: %v", i, err)
		}
		removeCacheControl(value)
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode item %d: %v", i, err)
		}
		normalized[i] = encoded
	}
	return normalized
}

func removeCacheControl(value any) {
	switch value := value.(type) {
	case map[string]any:
		delete(value, "cache_control")
		for _, child := range value {
			removeCacheControl(child)
		}
	case []any:
		for _, child := range value {
			removeCacheControl(child)
		}
	}
}
