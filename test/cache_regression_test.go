package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/store"
)

func TestPrefixAuditAttributesAppendAndRewrite(t *testing.T) {
	base := prefixAuditShape(t, `[{"role":"system","content":"stable"},{"role":"user","content":"one"}]`)
	appended := prefixAuditShape(t, `[{"role":"system","content":"stable"},{"role":"user","content":"one"},{"role":"assistant","content":"done"}]`)
	diagnostics := converter.ComparePrefixAuditShapes(&base, appended)
	if diagnostics.PrefixChanged || diagnostics.InputRelation != "append" || diagnostics.StableItems != 2 || diagnostics.AddedItems != 1 {
		t.Fatalf("正常追加被错误归因: %+v", diagnostics)
	}

	rewritten := prefixAuditShape(t, `[{"role":"system","content":"stable"},{"role":"user","content":"changed"},{"role":"assistant","content":"done"}]`)
	diagnostics = converter.ComparePrefixAuditShapes(&base, rewritten)
	if !diagnostics.PrefixChanged || diagnostics.InputRelation != "rewrite" || diagnostics.FirstChangedItem != 1 || !hasAuditReason(diagnostics.ChangeReasons, "history_rewrite") {
		t.Fatalf("历史重写未被识别: %+v", diagnostics)
	}

	routeChanged := appended
	routeChanged.ModelConfigID = "model-config-b"
	routeChanged.ToolsHash = "tools-b"
	diagnostics = converter.ComparePrefixAuditShapes(&appended, routeChanged)
	if !diagnostics.PrefixChanged || diagnostics.InputRelation != "same" || !hasAuditReason(diagnostics.ChangeReasons, "model_config") || !hasAuditReason(diagnostics.ChangeReasons, "tools") {
		t.Fatalf("路由或工具漂移未被识别: %+v", diagnostics)
	}

	cacheControlA := `[{"role":"user","content":[{"type":"text","text":"stable","cache_control":{"type":"ephemeral"}}]}]`
	cacheControlB := `[{"role":"user","content":[{"type":"text","text":"stable"}]}]`
	hashesA, err := converter.PrefixAuditItemHashesIgnoringCacheControl(cacheControlA)
	if err != nil {
		t.Fatalf("归一化第一个 Anthropic 输入: %v", err)
	}
	hashesB, err := converter.PrefixAuditItemHashesIgnoringCacheControl(cacheControlB)
	if err != nil {
		t.Fatalf("归一化第二个 Anthropic 输入: %v", err)
	}
	if len(hashesA) != 1 || len(hashesB) != 1 || hashesA[0] != hashesB[0] {
		t.Fatalf("Anthropic 滚动 cache_control 被误判为内容变化: %v != %v", hashesA, hashesB)
	}
}

func TestOpenAICachePrefixCurveWithMockProvider(t *testing.T) {
	mock := &prefixCacheMock{t: t}
	server := httptest.NewServer(mock)
	defer server.Close()

	systemPrompt := strings.Repeat("stable project policy and coding conventions. ", 160)
	tools := []json.RawMessage{json.RawMessage(`{"toolSpecification":{"name":"read_file","description":"Read a file","inputSchema":{"json":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}}`)}
	history := []converter.ParsedHistoryEntry{{IsSystem: true, UserContent: systemPrompt}}
	rates := make([]int, 0, 8)

	for turn := 0; turn < 8; turn++ {
		question := fmt.Sprintf("turn-%d: inspect the cache prefix", turn)
		conversation := &converter.ParsedConversation{
			ModelID:        "cache-test-model",
			ConversationID: "cache-curve",
			SystemText:     systemPrompt,
			History:        append([]converter.ParsedHistoryEntry(nil), history...),
			CurrentContent: question,
			RawTools:       tools,
			ToolsHash:      "stable-tools",
		}
		body, err := converter.KiroToOpenAI(conversation, "cache-test-model", "")
		if err != nil {
			t.Fatalf("第 %d 轮生成请求: %v", turn, err)
		}
		response, err := http.Post(server.URL, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("第 %d 轮发送 mock 请求: %v", turn, err)
		}
		var result struct {
			Usage struct {
				PromptTokens          int `json:"prompt_tokens"`
				PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
				PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
			} `json:"usage"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&result)
		response.Body.Close()
		if decodeErr != nil {
			t.Fatalf("第 %d 轮解析 mock usage: %v", turn, decodeErr)
		}
		denominator := result.Usage.PromptCacheHitTokens + result.Usage.PromptCacheMissTokens
		if denominator == 0 {
			t.Fatalf("第 %d 轮 mock usage 缺少输入 token", turn)
		}
		rates = append(rates, result.Usage.PromptCacheHitTokens*100/denominator)
		history = append(history, converter.ParsedHistoryEntry{
			UserContent:      question,
			HasAssistant:     true,
			AssistantContent: fmt.Sprintf("turn-%d complete", turn),
		})
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	for i := 1; i < len(mock.totalBytes); i++ {
		if mock.hitBytes[i] != mock.totalBytes[i-1] {
			t.Fatalf("第 %d 轮没有完整复用上一轮前缀: hit=%d previous=%d", i, mock.hitBytes[i], mock.totalBytes[i-1])
		}
		if mock.cacheKeys[i] != mock.cacheKeys[0] {
			t.Fatalf("第 %d 轮 prompt_cache_key 漂移: %q != %q", i, mock.cacheKeys[i], mock.cacheKeys[0])
		}
	}
	if rates[len(rates)-1] < rates[1] || rates[len(rates)-1] < 90 {
		t.Fatalf("缓存命中曲线未保持高位: %v", rates)
	}
}

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

func prefixAuditShape(t *testing.T, inputJSON string) converter.PrefixAuditShape {
	t.Helper()
	hashes, err := converter.PrefixAuditItemHashes(inputJSON)
	if err != nil {
		t.Fatalf("生成 prefix audit hashes: %v", err)
	}
	return converter.PrefixAuditShape{
		SystemHash:    "system-a",
		ToolsHash:     "tools-a",
		CacheKey:      "cache-a",
		ModelConfigID: "model-config-a",
		ModelID:       "model-a",
		Provider:      "openai",
		Endpoint:      "/chat/completions",
		API:           "chat",
		ItemHashes:    hashes,
	}
}

func hasAuditReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

type prefixCacheMock struct {
	t          *testing.T
	mu         sync.Mutex
	previous   []json.RawMessage
	totalBytes []int
	hitBytes   []int
	cacheKeys  []string
}

func (m *prefixCacheMock) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		m.t.Errorf("读取 mock 请求: %v", err)
		http.Error(w, "read request", http.StatusBadRequest)
		return
	}
	var payload struct {
		Messages       []json.RawMessage `json:"messages"`
		Tools          json.RawMessage   `json:"tools"`
		PromptCacheKey string            `json:"prompt_cache_key"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		m.t.Errorf("解析 mock 请求: %v", err)
		http.Error(w, "decode request", http.StatusBadRequest)
		return
	}

	prefix := make([]json.RawMessage, 0, len(payload.Messages)+1)
	if len(payload.Tools) > 0 {
		prefix = append(prefix, append(json.RawMessage(nil), payload.Tools...))
	}
	for _, message := range payload.Messages {
		prefix = append(prefix, append(json.RawMessage(nil), message...))
	}

	m.mu.Lock()
	common := commonRawPrefix(m.previous, prefix)
	hitBytes := rawMessageBytes(prefix[:common])
	totalBytes := rawMessageBytes(prefix)
	m.previous = prefix
	m.totalBytes = append(m.totalBytes, totalBytes)
	m.hitBytes = append(m.hitBytes, hitBytes)
	m.cacheKeys = append(m.cacheKeys, payload.PromptCacheKey)
	m.mu.Unlock()

	promptTokens := maxInt(1, (totalBytes+3)/4)
	hitTokens := hitBytes / 4
	if hitTokens > promptTokens {
		hitTokens = promptTokens
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"usage": map[string]int{
			"prompt_tokens":            promptTokens,
			"prompt_cache_hit_tokens":  hitTokens,
			"prompt_cache_miss_tokens": promptTokens - hitTokens,
		},
	})
}

func commonRawPrefix(previous, current []json.RawMessage) int {
	limit := len(previous)
	if len(current) < limit {
		limit = len(current)
	}
	for i := 0; i < limit; i++ {
		if !bytes.Equal(previous[i], current[i]) {
			return i
		}
	}
	return limit
}

func rawMessageBytes(messages []json.RawMessage) int {
	total := 0
	for _, message := range messages {
		total += len(message)
	}
	return total
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
