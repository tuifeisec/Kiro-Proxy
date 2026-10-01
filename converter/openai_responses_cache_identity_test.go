package converter

import (
	"bytes"
	"fmt"
	"testing"

	json "github.com/goccy/go-json"
)

func parseResponsesIdentityFixture(t *testing.T, description, number string, workflowPattern bool) *ParsedConversation {
	t.Helper()
	name := "identity_probe"
	properties := fmt.Sprintf(`"value":{"type":"number","default":%s}`, number)
	if workflowPattern {
		name = "run_workflow"
		properties += `,"name":{"type":"string","pattern":"^[^\\p{Cc}\\p{Zl}\\p{Zp}\"]*$"}`
	}
	rawTool := fmt.Sprintf(`{"toolSpecification":{"name":%q,"description":%q,"inputSchema":{"json":{"type":"object","properties":{%s}}}}}`, name, description, properties)
	request := fmt.Sprintf(`{"conversationState":{"conversationId":%q,"currentMessage":{"userInputMessage":{"content":"question","modelId":"gpt-5.6","userInputMessageContext":{"tools":[%s]}}}}}`, description, rawTool)
	conv, err := ParseKiroRequest([]byte(request))
	if err != nil {
		t.Fatalf("解析 Responses cache identity fixture 失败：%v", err)
	}
	return conv
}

type responsesIdentityObservation struct {
	providerHash string
	promptKey    string
	defaultValue string
}

func observeResponsesIdentity(t *testing.T, conv *ParsedConversation) responsesIdentityObservation {
	t.Helper()
	body, err := KiroToOpenAIResponsesUpstream(conv, "gpt-5.6", "", false)
	if err != nil {
		t.Fatalf("Responses upstream 转换失败：%v", err)
	}
	var request struct {
		Tools          []json.RawMessage `json:"tools"`
		PromptCacheKey string            `json:"prompt_cache_key"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("解码 Responses upstream body 失败：%v；body=%s", err, body)
	}
	if len(request.Tools) != 1 {
		t.Fatalf("Responses upstream tools 数量 = %d；body=%s", len(request.Tools), body)
	}
	var tool map[string]json.RawMessage
	if err := json.Unmarshal(request.Tools[0], &tool); err != nil {
		t.Fatalf("解码出站工具失败：%v", err)
	}
	var parameters map[string]json.RawMessage
	if err := json.Unmarshal(tool["parameters"], &parameters); err != nil {
		t.Fatalf("解码出站 parameters 失败：%v", err)
	}
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(parameters["properties"], &properties); err != nil {
		t.Fatalf("解码出站 properties 失败：%v", err)
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(properties["value"], &value); err != nil {
		t.Fatalf("解码出站 value schema 失败：%v", err)
	}
	providerHash, _ := CachedOpenAIResponsesToolAudit(conv.ToolsHash, conv.RawTools)
	wantPromptKey := PromptCacheKeyForAuditWithProviderToolsHash(conv, "gpt-5.6", "responses", providerHash)
	if request.PromptCacheKey != wantPromptKey {
		t.Errorf("Responses prompt_cache_key 与 provider hash 不一致：got=%q want=%q", request.PromptCacheKey, wantPromptKey)
	}
	return responsesIdentityObservation{
		providerHash: providerHash,
		promptKey:    request.PromptCacheKey,
		defaultValue: string(value["default"]),
	}
}

func TestOpenAIResponsesToolCacheIdentitySeparatesLargeNumericSchemas(t *testing.T) {
	for _, workflowPattern := range []bool{false, true} {
		patternName := "without-original-pattern"
		if workflowPattern {
			patternName = "with-original-pattern"
		}
		for _, reverse := range []bool{false, true} {
			direction := "A-then-B"
			if reverse {
				direction = "B-then-A"
			}
			t.Run(patternName+"/"+direction, func(t *testing.T) {
				description := "large-number-identity-" + patternName + "-" + direction
				a := parseResponsesIdentityFixture(t, description, "9007199254740992", workflowPattern)
				b := parseResponsesIdentityFixture(t, description, "9007199254740993", workflowPattern)
				if a.ToolsHash == "" || a.ToolsHash != b.ToolsHash {
					t.Fatalf("fixture 未复现旧 float-based ToolsHash 碰撞：A=%q B=%q", a.ToolsHash, b.ToolsHash)
				}

				firstConv, secondConv := a, b
				firstWant, secondWant := "9007199254740992", "9007199254740993"
				if reverse {
					firstConv, secondConv = b, a
					firstWant, secondWant = secondWant, firstWant
				}
				first := observeResponsesIdentity(t, firstConv)
				second := observeResponsesIdentity(t, secondConv)
				if first.defaultValue != firstWant {
					t.Errorf("首个 Responses body schema default = %s, want %s", first.defaultValue, firstWant)
				}
				if second.defaultValue != secondWant {
					t.Errorf("第二个 Responses body schema default = %s, want %s", second.defaultValue, secondWant)
				}
				if first.providerHash == second.providerHash {
					t.Errorf("不同精确 schema 得到相同 provider hash：%q", first.providerHash)
				}
				if first.promptKey == second.promptKey {
					t.Errorf("不同精确 schema 得到相同 prompt_cache_key：%q", first.promptKey)
				}
			})
		}
	}
}

func TestOpenAIResponsesToolCacheIdentitySeparatesLegacyCacheNamespace(t *testing.T) {
	for _, warmPath := range []string{"legacy-responses", "legacy-chat"} {
		t.Run(warmPath, func(t *testing.T) {
			description := "legacy-namespace-identity-" + warmPath
			legacy := parseResponsesIdentityFixture(t, description, "9007199254740993", false)
			target := parseResponsesIdentityFixture(t, description, "9007199254740992", false)
			if legacy.ToolsHash == "" || legacy.ToolsHash != target.ToolsHash {
				t.Fatalf("fixture 未复现旧 float-based ToolsHash 碰撞：legacy=%q target=%q", legacy.ToolsHash, target.ToolsHash)
			}
			var warmBody []byte
			var err error
			if warmPath == "legacy-responses" {
				warmBody, err = KiroToResponses(legacy, "gpt-5.6", "")
			} else {
				warmBody, err = KiroToOpenAI(legacy, "gpt-5.6", "")
			}
			if err != nil {
				t.Fatalf("旧路径预热工具转换缓存失败：%v", err)
			}
			if !bytes.Contains(warmBody, []byte("9007199254740993")) {
				t.Fatalf("旧路径未按 993 预热：%s", warmBody)
			}

			got := observeResponsesIdentity(t, target)
			if got.defaultValue != "9007199254740992" {
				t.Errorf("Responses 路径被旧 %s cache entry 污染：schema default=%s, want 9007199254740992", warmPath, got.defaultValue)
			}
		})
	}
}

func TestOpenAIResponsesToolCacheIdentityPreservesNumericLexemesAndLegacySmallIntegerKey(t *testing.T) {
	description := "numeric-lexeme-identity-fixture"
	integer := parseResponsesIdentityFixture(t, description, "1", false)
	decimal := parseResponsesIdentityFixture(t, description, "1.0", false)
	if integer.ToolsHash == "" || integer.ToolsHash != decimal.ToolsHash {
		t.Fatalf("fixture 未复现 1/1.0 旧 ToolsHash 碰撞：integer=%q decimal=%q", integer.ToolsHash, decimal.ToolsHash)
	}
	integerObservation := observeResponsesIdentity(t, integer)
	decimalObservation := observeResponsesIdentity(t, decimal)
	if integerObservation.defaultValue != "1" {
		t.Errorf("integer schema default = %s, want 1", integerObservation.defaultValue)
	}
	if decimalObservation.defaultValue != "1.0" {
		t.Errorf("decimal schema default = %s, want lexical number 1.0", decimalObservation.defaultValue)
	}
	if integerObservation.providerHash == decimalObservation.providerHash {
		t.Errorf("1 与 1.0 得到相同 provider hash：%q", integerObservation.providerHash)
	}
	if integerObservation.promptKey == decimalObservation.promptKey {
		t.Errorf("1 与 1.0 得到相同 prompt_cache_key：%q", integerObservation.promptKey)
	}

	const smallInteger = "1"
	small := parseResponsesIdentityFixture(t, "small-integer-legacy-key-fixture", smallInteger, false)
	legacyBody, err := KiroToResponses(small, "gpt-5.6", "")
	if err != nil {
		t.Fatalf("小整数旧 Responses 转换失败：%v", err)
	}
	var legacyRequest struct {
		PromptCacheKey string `json:"prompt_cache_key"`
	}
	if err := json.Unmarshal(legacyBody, &legacyRequest); err != nil {
		t.Fatalf("小整数旧 Responses body 解码失败：%v", err)
	}
	newObservation := observeResponsesIdentity(t, small)
	if legacyRequest.PromptCacheKey != newObservation.promptKey {
		t.Errorf("未匹配 schema 的常见小整数改变旧 prompt cache key：legacy=%q newResponses=%q", legacyRequest.PromptCacheKey, newObservation.promptKey)
	}
}

func TestOpenAIResponsesToolCacheIdentityLocalCacheHitsWithoutCollisions(t *testing.T) {
	description := "local-cache-large-number-identity"
	a := parseResponsesIdentityFixture(t, description, "9007199254740992", true)
	b := parseResponsesIdentityFixture(t, description, "9007199254740993", true)
	if a.ToolsHash == "" || a.ToolsHash != b.ToolsHash {
		t.Fatalf("fixture 未复现旧 float-based ToolsHash 碰撞：A=%q B=%q", a.ToolsHash, b.ToolsHash)
	}

	var cache ToolCache
	initial := cache.Snapshot()
	toolsA, hashA := cache.GetOpenAIResponsesWithHash(a.RawTools)
	afterFirstA := cache.Snapshot()
	toolsAAgain, hashAAgain := cache.GetOpenAIResponsesWithHash(a.RawTools)
	afterSecondA := cache.Snapshot()
	toolsB, hashB := cache.GetOpenAIResponsesWithHash(b.RawTools)
	afterFirstB := cache.Snapshot()
	toolsBAgain, hashBAgain := cache.GetOpenAIResponsesWithHash(b.RawTools)
	afterSecondB := cache.Snapshot()

	assertCachedNumber := func(label string, tools []OpenAITool, want, avoid string) {
		t.Helper()
		encoded, err := json.Marshal(tools)
		if err != nil {
			t.Fatalf("编码 %s cache tools 失败：%v", label, err)
		}
		if !bytes.Contains(encoded, []byte(want)) || bytes.Contains(encoded, []byte(avoid)) {
			t.Errorf("%s cache schema 数字不精确：got=%s want=%s avoid=%s", label, encoded, want, avoid)
		}
	}
	assertCachedNumber("A", toolsA, "9007199254740992", "9007199254740993")
	assertCachedNumber("A repeat", toolsAAgain, "9007199254740992", "9007199254740993")
	assertCachedNumber("B", toolsB, "9007199254740993", "9007199254740992")
	assertCachedNumber("B repeat", toolsBAgain, "9007199254740993", "9007199254740992")
	if hashA == "" || hashA != hashAAgain || hashB == "" || hashB != hashBAgain {
		t.Fatalf("相同 raw 重复读取的 provider hash 不一致：A=%q/%q B=%q/%q", hashA, hashAAgain, hashB, hashBAgain)
	}
	if hashA == hashB {
		t.Errorf("精确 schema 使用了相同 provider hash：%q", hashA)
	}
	if initial.Entries != 0 || afterFirstA.Misses != initial.Misses+1 || afterFirstA.Entries != 1 {
		t.Errorf("首次 A 转换 cache 统计异常：initial=%+v after=%+v", initial, afterFirstA)
	}
	if afterSecondA.Misses != afterFirstA.Misses || afterSecondA.Hits != afterFirstA.Hits+1 || afterSecondA.Entries != afterFirstA.Entries {
		t.Errorf("重复 A 未命中同一 cache 条目：after-first=%+v after-second=%+v", afterFirstA, afterSecondA)
	}
	if afterFirstB.Misses != afterSecondA.Misses+1 || afterFirstB.Entries != afterSecondA.Entries+1 {
		t.Errorf("不同精确 schema 未建立隔离条目：after-A=%+v after-B=%+v", afterSecondA, afterFirstB)
	}
	if afterSecondB.Misses != afterFirstB.Misses || afterSecondB.Hits != afterFirstB.Hits+1 || afterSecondB.Entries != afterFirstB.Entries {
		t.Errorf("重复 B 未命中独立 cache 条目：after-first=%+v after-second=%+v", afterFirstB, afterSecondB)
	}
}
