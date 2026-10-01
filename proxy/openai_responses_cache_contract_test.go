package proxy

import (
	"bytes"
	"testing"

	"kiro-proxy/converter"
	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

// oldResponsesBody 模拟兼容修复前的转换入口和 store 字段设置方式。
func oldResponsesBody(t *testing.T, conv *converter.ParsedConversation, modelID string, continuation, storeValue bool) []byte {
	t.Helper()
	var body []byte
	var err error
	if continuation {
		body, err = converter.KiroToResponsesForContinuation(conv, modelID, "")
	} else {
		body, err = converter.KiroToResponses(conv, modelID, "")
	}
	if err != nil {
		t.Fatalf("旧 Responses 转换失败：%v", err)
	}

	// HEAD 25ab 的旧逻辑将整个请求解码到 interface{} 并重新编码；测试数据只用小整数。
	var fields map[string]interface{}
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("旧 Responses 请求解码失败：%v", err)
	}
	fields["store"] = storeValue
	body, err = json.Marshal(fields)
	if err != nil {
		t.Fatalf("旧 Responses 请求编码失败：%v", err)
	}
	return body
}

func responsesRawFields(t *testing.T, body []byte, names ...string) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("Responses 请求解码失败：%v；body=%s", err, body)
	}
	selected := make(map[string]json.RawMessage, len(names))
	for _, name := range names {
		value, ok := fields[name]
		if !ok {
			t.Fatalf("Responses 请求缺少 %q：body=%s", name, body)
		}
		selected[name] = value
	}
	return selected
}

func TestResponsesCacheContractPreservesLegacyUnmatchedRequests(t *testing.T) {
	for _, continuation := range []bool{false, true} {
		name := "ordinary"
		if continuation {
			name = "continuation"
		}
		t.Run(name, func(t *testing.T) {
			tools := []json.RawMessage{
				json.RawMessage(`{"toolSpecification":{"name":"read_file","description":"Read a file","inputSchema":{"json":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}}`),
			}
			conv := &converter.ParsedConversation{
				ModelID: "gpt-5.6", ConversationID: "cache-contract-legacy",
				SystemText: "stable system instructions", CurrentContent: "question two",
				History:  []converter.ParsedHistoryEntry{{UserContent: "question one", HasAssistant: true, AssistantContent: "answer one"}},
				RawTools: tools,
			}
			model := &store.Model{
				Provider: "openai", BaseURL: "https://provider.example/v1", Endpoint: "/responses",
				ModelID: "gpt-5.6", EnableContinuation: continuation,
			}
			pre := &Preflight{Protocol: "kiro", Conv: conv}

			got, upstreamURL, err := convertRequestWithProtocol(pre, model, nil, false)
			if err != nil {
				t.Fatalf("当前 Responses 转换失败：%v", err)
			}
			if upstreamURL != "https://provider.example/v1/responses" {
				t.Fatalf("上游 URL = %q", upstreamURL)
			}
			want := oldResponsesBody(t, conv, model.ModelID, continuation, continuation)
			if !bytes.Equal(got, want) {
				t.Errorf("无目标 pattern 的 %s 请求与旧序列化基线不同：\n当前：%s\n旧版：%s", name, got, want)
			}
			gotFields := responsesRawFields(t, got, "input", "tools")
			wantFields := responsesRawFields(t, want, "input", "tools")
			for _, field := range []string{"input", "tools"} {
				if !bytes.Equal(gotFields[field], wantFields[field]) {
					t.Errorf("无目标 pattern 的 %s 请求字段 %s 与旧版不同：\n当前：%s\n旧版：%s", name, field, gotFields[field], wantFields[field])
				}
			}
		})
	}
}

func TestResponsesCacheContractCanonicalizesToolPrefixDeterministically(t *testing.T) {
	const runWorkflowA = `{"toolSpecification":{"name":"run_workflow","description":"Run workflow","inputSchema":{"json":{"type":"object","properties":{"name":{"type":"string","pattern":"^[^\\p{Cc}\\p{Zl}\\p{Zp}\"]*$"}},"required":["name"]}}}}`
	const runWorkflowB = `{ "toolSpecification" : { "inputSchema" : { "json" : { "required" : ["name"], "properties" : { "name" : { "pattern" : "^[^\\p{Cc}\\p{Zl}\\p{Zp}\"]*$", "type" : "string" } }, "type" : "object" } }, "description" : "Run workflow", "name" : "run_workflow" } }`
	const inspectA = `{"toolSpecification":{"name":"inspect","description":"Inspect values","inputSchema":{"json":{"type":"object","properties":{"alpha":{"type":"integer","default":1},"beta":{"type":"string"}},"required":["alpha","beta"]}}}}`
	const inspectB = `{ "toolSpecification" : { "name" : "inspect", "description" : "Inspect values", "inputSchema" : { "json" : { "required" : ["alpha", "beta"], "properties" : { "beta" : { "type" : "string" }, "alpha" : { "default" : 1, "type" : "integer" } }, "type" : "object" } } } }`

	rawToolsA := []json.RawMessage{json.RawMessage(runWorkflowA), json.RawMessage(inspectA)}
	rawToolsB := []json.RawMessage{json.RawMessage(inspectB), json.RawMessage(runWorkflowB)}
	if bytes.Equal(rawToolsA[0], rawToolsB[1]) || bytes.Equal(rawToolsA[1], rawToolsB[0]) {
		t.Fatal("测试前提错误：两组原始工具 JSON 应有不同字节顺序")
	}
	makePre := func(raw []json.RawMessage) *Preflight {
		return &Preflight{Protocol: "kiro", Conv: &converter.ParsedConversation{
			ModelID: "gpt-5.6", SystemText: "stable system instructions", CurrentContent: "same question",
			RawTools: raw, // ToolsHash 留空，避免缓存键掩盖转换结果。
		}}
	}
	model := &store.Model{
		Provider: "openai", BaseURL: "https://provider.example/v1", Endpoint: "/responses", ModelID: "gpt-5.6",
	}

	var want map[string]json.RawMessage
	variantMismatchReported := make(map[string]bool)
	determinismMismatchReported := make(map[string]bool)
	for iteration := 0; iteration < 20; iteration++ {
		bodies := make([][]byte, 2)
		for index, raw := range [][]json.RawMessage{rawToolsA, rawToolsB} {
			body, _, err := convertRequestWithProtocol(makePre(raw), model, nil, false)
			if err != nil {
				t.Fatalf("第 %d 轮工具组 %d 转换失败：%v", iteration, index, err)
			}
			bodies[index] = body
		}
		fieldsA := responsesRawFields(t, bodies[0], "tools", "prompt_cache_key", "input")
		fieldsB := responsesRawFields(t, bodies[1], "tools", "prompt_cache_key", "input")
		if want == nil {
			want = fieldsA
		}
		for _, field := range []string{"tools", "prompt_cache_key", "input"} {
			if !bytes.Equal(fieldsA[field], fieldsB[field]) && !variantMismatchReported[field] {
				t.Errorf("原始工具顺序/schema 对象顺序导致 %s 前缀字节不同（第 %d 轮）：\n工具组 A：%s\n工具组 B：%s", field, iteration, fieldsA[field], fieldsB[field])
				variantMismatchReported[field] = true
			}
			if iteration > 0 && !bytes.Equal(fieldsA[field], want[field]) && !determinismMismatchReported[field] {
				t.Errorf("第 %d 次转换的 %s 前缀不确定：\n首次：%s\n当前：%s", iteration, field, want[field], fieldsA[field])
				determinismMismatchReported[field] = true
			}
		}
	}
}

func TestResponsesCacheContractLegacyFirstInputContinuesWithCurrentRequest(t *testing.T) {
	tools := []json.RawMessage{
		json.RawMessage(`{"toolSpecification":{"name":"run_workflow","description":"Run workflow","inputSchema":{"json":{"type":"object","properties":{"name":{"type":"string","pattern":"^[^\\p{Cc}\\p{Zl}\\p{Zp}\"]*$"}},"required":["name"]}}}}`),
	}
	makeConversation := func(history []converter.ParsedHistoryEntry, current string) *converter.ParsedConversation {
		return &converter.ParsedConversation{
			ModelID: "gpt-5.6", ConversationID: "cache-contract-continuation",
			SystemText: "fixed system instructions", History: history, CurrentContent: current,
			RawTools: tools,
		}
	}
	model := &store.Model{
		Provider: "openai", BaseURL: "https://provider.example/v1", Endpoint: "/responses",
		ModelID: "gpt-5.6", EnableContinuation: true,
	}

	firstConv := makeConversation(nil, "first user message")
	firstBody := oldResponsesBody(t, firstConv, model.ModelID, true, true)
	firstResult, err := converter.ApplyResponsesContinuation(firstBody, nil)
	if err != nil {
		t.Fatalf("保存旧版首轮 input 失败：%v", err)
	}
	if firstResult.InputItemCount != 1 {
		t.Fatalf("旧版首轮 input 条目数 = %d，want 1", firstResult.InputItemCount)
	}

	secondConv := makeConversation([]converter.ParsedHistoryEntry{
		{UserContent: "first user message", HasAssistant: true, AssistantContent: "first answer"},
	}, "second user message")
	secondBody, _, err := convertRequestWithProtocol(&Preflight{Protocol: "kiro", Conv: secondConv}, model, nil, false)
	if err != nil {
		t.Fatalf("当前续接请求转换失败：%v", err)
	}
	continuation, err := converter.ApplyResponsesContinuation(secondBody, &converter.ResponsesContinuationState{
		ResponseID: "resp_previous", InputJSON: firstResult.FullInputJSON, ItemCount: firstResult.InputItemCount,
	})
	if err != nil {
		t.Fatalf("应用 Responses 续接失败：%v", err)
	}
	if !continuation.UsedContinuation {
		t.Errorf("旧版首轮 input 未能作为当前 input 前缀续接；current=%s; previous=%s", secondBody, firstResult.FullInputJSON)
	}

	fullFields := responsesRawFields(t, secondBody, "tools", "prompt_cache_key")
	continuedFields := responsesRawFields(t, continuation.Body, "tools", "prompt_cache_key")
	for _, field := range []string{"tools", "prompt_cache_key"} {
		if !bytes.Equal(fullFields[field], continuedFields[field]) {
			t.Errorf("续接请求未保留当前 %s：full=%s continued=%s", field, fullFields[field], continuedFields[field])
		}
	}
	var continuationFields map[string]json.RawMessage
	if err := json.Unmarshal(continuation.Body, &continuationFields); err != nil {
		t.Fatalf("续接请求解码失败：%v", err)
	}
	if value, ok := continuationFields["previous_response_id"]; !ok {
		t.Error("续接请求缺少 previous_response_id")
	} else if string(value) != `"resp_previous"` {
		t.Errorf("previous_response_id = %s", value)
	}
	if len(continuation.Body) == 0 {
		t.Fatalf("续接请求体为空：%+v", continuation)
	}
}

func TestResponsesCacheContractDynamicSuffixDoesNotChangeStaticPrefix(t *testing.T) {
	tools := []json.RawMessage{
		json.RawMessage(`{"toolSpecification":{"name":"run_workflow","description":"Run workflow","inputSchema":{"json":{"type":"object","properties":{"name":{"type":"string","pattern":"^[^\\p{Cc}\\p{Zl}\\p{Zp}\"]*$"}},"required":["name"]}}}}`),
	}
	makePre := func(question string) *Preflight {
		return &Preflight{Protocol: "kiro", Conv: &converter.ParsedConversation{
			ModelID: "gpt-5.6", ConversationID: "cache-contract-dynamic-tail",
			SystemText:     "fixed system rules\nHere is the last user message:\n" + question,
			History:        []converter.ParsedHistoryEntry{{UserContent: "prior question", HasAssistant: true, AssistantContent: "prior answer"}},
			CurrentContent: question, RawTools: tools,
		}}
	}
	model := &store.Model{
		Provider: "openai", BaseURL: "https://provider.example/v1", Endpoint: "/responses", ModelID: "gpt-5.6",
	}
	bodyA, _, err := convertRequestWithProtocol(makePre("first question"), model, nil, false)
	if err != nil {
		t.Fatalf("第一轮转换失败：%v", err)
	}
	bodyB, _, err := convertRequestWithProtocol(makePre("second question"), model, nil, false)
	if err != nil {
		t.Fatalf("第二轮转换失败：%v", err)
	}
	fieldsA := responsesRawFields(t, bodyA, "tools", "prompt_cache_key", "input")
	fieldsB := responsesRawFields(t, bodyB, "tools", "prompt_cache_key", "input")
	for _, field := range []string{"tools", "prompt_cache_key"} {
		if !bytes.Equal(fieldsA[field], fieldsB[field]) {
			t.Errorf("动态尾部变化影响了 %s：A=%s B=%s", field, fieldsA[field], fieldsB[field])
		}
	}
	var inputA, inputB []json.RawMessage
	if err := json.Unmarshal(fieldsA["input"], &inputA); err != nil {
		t.Fatalf("第一轮 input 解码失败：%v", err)
	}
	if err := json.Unmarshal(fieldsB["input"], &inputB); err != nil {
		t.Fatalf("第二轮 input 解码失败：%v", err)
	}
	if len(inputA) != 5 || len(inputB) != 5 {
		t.Fatalf("input 布局不是静态 developer + history + 动态尾部 + current：A=%s B=%s", fieldsA["input"], fieldsB["input"])
	}
	for index := 0; index < 3; index++ {
		if !bytes.Equal(inputA[index], inputB[index]) {
			t.Errorf("动态尾部变化影响了静态 input[%d]：A=%s B=%s", index, inputA[index], inputB[index])
		}
	}
	if bytes.Equal(inputA[3], inputB[3]) {
		t.Error("动态 developer suffix 应随本轮问题变化")
	}
	if bytes.Equal(inputA[4], inputB[4]) {
		t.Error("当前 user input 应随本轮问题变化")
	}
}

func TestResponsesCacheContractConcurrentSharedToolsRemainImmutable(t *testing.T) {
	const sharedToolsHash = "responses-cache-contract-shared-tools-unique"
	rawTools := []json.RawMessage{
		json.RawMessage(`{"toolSpecification":{"name":"run_workflow","description":"Run workflow","inputSchema":{"json":{"type":"object","properties":{"name":{"type":"string","pattern":"^[^\\p{Cc}\\p{Zl}\\p{Zp}\"]*$"}},"required":["name"]}}}}`),
	}
	originalRawTools := make([]json.RawMessage, len(rawTools))
	for index := range rawTools {
		originalRawTools[index] = append(json.RawMessage(nil), rawTools[index]...)
	}
	pre := &Preflight{Protocol: "kiro", Conv: &converter.ParsedConversation{
		ModelID: "gpt-5.6", ConversationID: "cache-contract-concurrent",
		SystemText: "fixed system instructions", CurrentContent: "same question",
		RawTools: rawTools, ToolsHash: sharedToolsHash,
	}}
	model := &store.Model{
		Provider: "openai", BaseURL: "https://provider.example/v1", Endpoint: "/responses", ModelID: "gpt-5.6",
	}

	const concurrency = 16
	type result struct {
		body []byte
		err  string
	}
	results := make(chan result, concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			body, _, err := convertRequestWithProtocol(pre, model, nil, false)
			if err != nil {
				results <- result{err: err.Error()}
				return
			}
			results <- result{body: body}
		}()
	}

	var firstBody []byte
	for i := 0; i < concurrency; i++ {
		result := <-results
		if result.err != "" {
			t.Errorf("并发转换失败：%s", result.err)
			continue
		}
		if firstBody == nil {
			firstBody = result.body
		} else if !bytes.Equal(firstBody, result.body) {
			t.Errorf("共享 ToolsHash 并发转换 body 不一致：首次=%s 当前=%s", firstBody, result.body)
		}
	}
	if len(rawTools) != len(originalRawTools) {
		t.Fatalf("共享缓存改变了 RawTools 数量：got=%d want=%d", len(rawTools), len(originalRawTools))
	}
	for index := range rawTools {
		if !bytes.Equal(rawTools[index], originalRawTools[index]) {
			t.Errorf("共享缓存改变了 RawTools[%d]：got=%s want=%s", index, rawTools[index], originalRawTools[index])
		}
	}
	legacyBody, err := converter.KiroToResponses(pre.Conv, model.ModelID, "")
	if err != nil {
		t.Fatalf("缓存后旧版转换失败：%v", err)
	}
	if patterns := workflowPatterns(t, legacyBody); len(patterns) != 1 || patterns[0] != proxyObservedWorkflowPattern {
		t.Errorf("共享缓存污染旧版转换的原始 pattern：%q", patterns)
	}
}

func TestResponsesCacheContractNonTargetPrecisionPreservesLegacyBody(t *testing.T) {
	const requestBody = `{"conversationState":{"conversationId":"cache-contract-non-target-precision","currentMessage":{"userInputMessage":{"content":"question","modelId":"gpt-5.6","userInputMessageContext":{"tools":[{"toolSpecification":{"name":"precision_probe","description":"non-target precision fixture","inputSchema":{"json":{"type":"object","properties":{"large":{"type":"integer","default":9007199254740993},"decimal":{"type":"number","default":1.0}}}}}}]}}}}}`
	conv, err := converter.ParseKiroRequest([]byte(requestBody))
	if err != nil {
		t.Fatalf("解析精度 fixture 失败：%v", err)
	}
	model := &store.Model{
		Provider: "openai", BaseURL: "https://provider.example/custom", Endpoint: "/responses",
		ModelID: "gpt-5.6", DisableAutoSuffix: true,
	}
	got, upstreamURL, err := convertRequestWithProtocol(&Preflight{Protocol: "kiro", Conv: conv}, model, nil, false)
	if err != nil {
		t.Fatalf("非目标 URL 转换失败：%v", err)
	}
	if upstreamURL != model.BaseURL {
		t.Fatalf("DisableAutoSuffix 上游 URL = %q, want %q", upstreamURL, model.BaseURL)
	}
	want := oldResponsesBody(t, conv, model.ModelID, false, false)
	if !bytes.Equal(got, want) {
		t.Fatalf("非目标实际 URL 的 Responses 请求与旧序列化 body 不同：\n当前：%s\n旧版：%s", got, want)
	}
}

func TestSetResponsesStorePreservesNumbersAndRejectsTrailingDocuments(t *testing.T) {
	const validBody = `{"model":"gpt-5.6","input":[{"type":"message","content":{"large":9007199254740993,"decimal":1.0}}],"store":true}`
	updated := setResponsesStore([]byte(validBody), false, true)
	if bytes.Contains(updated, []byte(`"store":true`)) || !bytes.Contains(updated, []byte(`"store":false`)) {
		t.Fatalf("合法单文档未更新 store：%s", updated)
	}
	if !bytes.Contains(updated, []byte(`9007199254740993`)) || !bytes.Contains(updated, []byte(`1.0`)) {
		t.Fatalf("精确保真模式改变了数字词法：%s", updated)
	}

	withTrailingDocument := append([]byte(validBody), []byte(` {"ignored":true}`)...)
	got := setResponsesStore(withTrailingDocument, false, true)
	if !bytes.Equal(got, withTrailingDocument) {
		t.Fatalf("setter 接受了尾随第二 JSON 文档：got=%s want unchanged=%s", got, withTrailingDocument)
	}
}

func TestResponsesCacheContractPreservesPreciseToolsAcrossAppliedContinuation(t *testing.T) {
	const firstRequestBody = `{"conversationState":{"conversationId":"cache-contract-precise-continuation","currentMessage":{"userInputMessage":{"content":"first user message","modelId":"gpt-5.6","userInputMessageContext":{"tools":[{"toolSpecification":{"name":"precision_probe","description":"continuation precision fixture","inputSchema":{"json":{"type":"object","properties":{"revision":{"type":"integer","default":9007199254740992}}}}}}]}}}}}`
	const secondRequestBody = `{"conversationState":{"conversationId":"cache-contract-precise-continuation","currentMessage":{"userInputMessage":{"content":"first user message","modelId":"gpt-5.6","userInputMessageContext":{"tools":[{"toolSpecification":{"name":"precision_probe","description":"continuation precision fixture","inputSchema":{"json":{"type":"object","properties":{"revision":{"type":"integer","default":9007199254740993}}}}}}]}}}}}`
	firstConv, err := converter.ParseKiroRequest([]byte(firstRequestBody))
	if err != nil {
		t.Fatalf("解析首轮续接精度 fixture 失败：%v", err)
	}
	secondConv, err := converter.ParseKiroRequest([]byte(secondRequestBody))
	if err != nil {
		t.Fatalf("解析后续续接精度 fixture 失败：%v", err)
	}
	if firstConv.ToolsHash == "" || firstConv.ToolsHash != secondConv.ToolsHash {
		t.Fatalf("fixture 未复现旧非空 ToolsHash 碰撞：first=%q second=%q", firstConv.ToolsHash, secondConv.ToolsHash)
	}
	firstConv.SystemText = "fixed continuation instructions"
	secondConv.SystemText = firstConv.SystemText
	model := &store.Model{
		Provider: "openai", BaseURL: "https://provider.example/v1", Endpoint: "/responses",
		ModelID: "gpt-5.6", EnableContinuation: true,
	}

	firstBody, firstURL, err := convertRequestWithProtocol(&Preflight{Protocol: "kiro", Conv: firstConv}, model, nil, false)
	if err != nil {
		t.Fatalf("首轮 actual Responses 转换失败：%v", err)
	}
	if firstURL != "https://provider.example/v1/responses" {
		t.Fatalf("首轮上游 URL = %q", firstURL)
	}
	firstResult, err := converter.ApplyResponsesContinuation(firstBody, nil)
	if err != nil {
		t.Fatalf("保存首轮 Responses continuation 状态失败：%v", err)
	}
	if firstResult.InputItemCount != 1 {
		t.Fatalf("首轮 full input 条目数 = %d，want 1", firstResult.InputItemCount)
	}

	secondConv.History = []converter.ParsedHistoryEntry{{
		UserContent: "first user message", HasAssistant: true, AssistantContent: "first answer",
	}}
	secondConv.CurrentContent = "second user message"
	secondBody, secondURL, err := convertRequestWithProtocol(&Preflight{Protocol: "kiro", Conv: secondConv}, model, nil, false)
	if err != nil {
		t.Fatalf("后续 actual Responses 转换失败：%v", err)
	}
	if secondURL != firstURL {
		t.Fatalf("续接请求上游 URL 改变：first=%q second=%q", firstURL, secondURL)
	}
	firstFields := responsesRawFields(t, firstBody, "tools", "prompt_cache_key")
	secondFields := responsesRawFields(t, secondBody, "tools", "prompt_cache_key")
	if !bytes.Contains(firstFields["tools"], []byte("9007199254740992")) || bytes.Contains(firstFields["tools"], []byte("9007199254740993")) {
		t.Fatalf("首轮 tools 未保留 A=9007199254740992：%s", firstFields["tools"])
	}
	if !bytes.Contains(secondFields["tools"], []byte("9007199254740993")) || bytes.Contains(secondFields["tools"], []byte("9007199254740992")) {
		t.Fatalf("后续 tools 未保留 B=9007199254740993：%s", secondFields["tools"])
	}
	firstProviderHash, _ := converter.CachedOpenAIResponsesToolAudit(firstConv.ToolsHash, firstConv.RawTools)
	secondProviderHash, _ := converter.CachedOpenAIResponsesToolAudit(secondConv.ToolsHash, secondConv.RawTools)
	if firstProviderHash == secondProviderHash {
		t.Fatalf("不同精确 schema 得到相同 Responses provider hash：%q", firstProviderHash)
	}
	if firstFields["prompt_cache_key"] == nil || bytes.Equal(firstFields["prompt_cache_key"], secondFields["prompt_cache_key"]) {
		t.Fatalf("不同精确 schema 未得到不同 prompt_cache_key：first=%s second=%s", firstFields["prompt_cache_key"], secondFields["prompt_cache_key"])
	}
	wantSecondKey := converter.PromptCacheKeyForAuditWithProviderToolsHash(secondConv, model.ModelID, "responses", secondProviderHash)
	if string(secondFields["prompt_cache_key"]) != `"`+wantSecondKey+`"` {
		t.Fatalf("后续 prompt_cache_key 与精确 provider hash 不一致：got=%s want=%q", secondFields["prompt_cache_key"], wantSecondKey)
	}
	continuation, err := converter.ApplyResponsesContinuation(secondBody, &converter.ResponsesContinuationState{
		ResponseID: "resp_precise_previous", InputJSON: firstResult.FullInputJSON, ItemCount: firstResult.InputItemCount,
	})
	if err != nil {
		t.Fatalf("应用后续 Responses continuation 失败：%v", err)
	}
	if !continuation.UsedContinuation {
		t.Fatalf("实际 Responses 请求未应用 previous_response_id：body=%s previous=%s", secondBody, firstResult.FullInputJSON)
	}

	fullFields := secondFields
	continuedFields := responsesRawFields(t, continuation.Body, "tools", "prompt_cache_key")
	if !bytes.Contains(fullFields["tools"], []byte("9007199254740993")) {
		t.Fatalf("full body 的工具 schema 丢失精确整数：%s", fullFields["tools"])
	}
	for _, field := range []string{"tools", "prompt_cache_key"} {
		if !bytes.Equal(fullFields[field], continuedFields[field]) {
			t.Errorf("续接 body 未保留 full body 的 %s：full=%s continued=%s", field, fullFields[field], continuedFields[field])
		}
	}
	if !bytes.Contains(continuedFields["tools"], []byte("9007199254740993")) {
		t.Errorf("续接 body 的工具 schema 丢失精确整数：%s", continuedFields["tools"])
	}
}
