package converter

import (
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

func parseOpenAIReq(t *testing.T, body []byte) OpenAIRequest {
	t.Helper()
	var req OpenAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("反序列化 OpenAI 请求失败: %v\nbody=%s", err, string(body))
	}
	return req
}

func TestOpenAI_CacheOptimization_SplitsSystemTailAndPreservesCurrentContent(t *testing.T) {
	system := `<identity>
You are Kiro.
</identity>

Here is the last user message:
动态问题`
	conv := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-openai-cache",
		SystemText:     system,
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: system},
			{UserContent: "历史问题", HasAssistant: true, AssistantContent: "历史回答"},
		},
		CurrentContent: "当前问题\n\n<EnvironmentContext>\n<ACTIVE-EDITOR-FILE>\n<file name=\"main.go\" />\n</ACTIVE-EDITOR-FILE>\n</EnvironmentContext>",
	}

	body, err := KiroToOpenAI(conv, "", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	req := parseOpenAIReq(t, body)

	if !strings.HasPrefix(req.PromptCacheKey, "kiro-chat-") {
		t.Fatalf("PromptCacheKey = %q", req.PromptCacheKey)
	}
	if len(req.Messages) < 5 {
		t.Fatalf("messages too short: %+v", req.Messages)
	}
	if req.Messages[0].Role != "system" {
		t.Fatalf("first role = %q", req.Messages[0].Role)
	}
	systemText, _ := req.Messages[0].Content.(string)
	if strings.Contains(systemText, "Here is the last user message") || strings.Contains(systemText, "动态问题") {
		t.Fatalf("system 静态前缀仍包含动态尾部: %q", systemText)
	}

	combined := ""
	for _, m := range req.Messages {
		if s, ok := m.Content.(string); ok {
			combined += s + "\n"
		}
	}
	for _, want := range []string{"动态问题", "历史问题", "历史回答", "EnvironmentContext", "main.go", "当前问题"} {
		if !strings.Contains(combined, want) {
			t.Fatalf("OpenAI 请求丢失内容 %q\ncombined=%s", want, combined)
		}
	}

	last := req.Messages[len(req.Messages)-1]
	lastText, _ := last.Content.(string)
	if last.Role != "user" || lastText != conv.CurrentContent {
		t.Fatalf("最后一条应保持原始 current 内容，got role=%q content=%q", last.Role, lastText)
	}
	prev := req.Messages[len(req.Messages)-2]
	prevText, _ := prev.Content.(string)
	if prev.Role != "system" || !strings.Contains(prevText, "动态问题") {
		t.Fatalf("倒数第二条应为 system 动态尾部，got role=%q content=%q", prev.Role, prevText)
	}
}

func TestOpenAI_CacheOptimization_ToolRoundAppendsDynamicTail(t *testing.T) {
	conv := &ParsedConversation{
		SystemText: `<identity>stable</identity>

Here is the last user message:
tool round context`,
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: "<identity>stable</identity>\n\nHere is the last user message:\ntool round context"},
			{UserContent: "previous question", HasAssistant: true, AssistantContent: "calling tool"},
		},
		CurrentToolResults: []ParsedToolResult{{ToolUseID: "call-1", Content: "tool output"}},
	}
	body, err := KiroToOpenAI(conv, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	req := parseOpenAIReq(t, body)
	if len(req.Messages) < 4 {
		t.Fatalf("消息数量过少: %d", len(req.Messages))
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "system" || last.Content != "Here is the last user message:\ntool round context" {
		t.Fatalf("工具轮次动态尾部应追加到末尾, got role=%q content=%v", last.Role, last.Content)
	}
	if req.Messages[len(req.Messages)-2].Role != "tool" {
		t.Fatalf("工具结果应位于动态尾部之前, got role=%q", req.Messages[len(req.Messages)-2].Role)
	}
}

func TestHashToolsCanonicalStable(t *testing.T) {
	toolsA := []json.RawMessage{
		json.RawMessage(`{"toolSpecification":{"name":"b","description":"B","inputSchema":{"json":{"type":"object","properties":{"q":{"type":"string"}}}}}}`),
		json.RawMessage(`{"toolSpecification":{"name":"a","description":"A","inputSchema":{"json":{"properties":{"p":{"type":"string"}},"type":"object"}}}}`),
	}
	toolsB := []json.RawMessage{
		json.RawMessage(`{
			"toolSpecification": {
				"inputSchema": {"json": {"type": "object", "properties": {"p": {"type": "string"}}}},
				"description": "A",
				"name": "a"
			}
		}`),
		json.RawMessage(`{"toolSpecification":{"inputSchema":{"json":{"properties":{"q":{"type":"string"}},"type":"object"}},"description":"B","name":"b"}}`),
	}

	if gotA, gotB := hashTools(toolsA), hashTools(toolsB); gotA != gotB {
		t.Fatalf("语义相同的工具 hash 不稳定: %s != %s", gotA, gotB)
	}
}

func TestOpenAIToolsHashUsesProviderVisibleShape(t *testing.T) {
	base := json.RawMessage(`{"toolSpecification":{"name":"read_file","description":"Read a file","inputSchema":{"json":{"type":"object","properties":{"path":{"type":"string"}}}}}}`)
	withRelayOnlyFields := json.RawMessage(`{"toolSpecification":{"name":"read_file","description":"Read a file","inputSchema":{"json":{"properties":{"path":{"type":"string"}},"type":"object"}},"relayMetadata":{"updatedAt":"now"}}}`)
	withWebSearch := json.RawMessage(`{"webSearchTool":{"description":"provider-specific"}}`)

	baseHash := hashOpenAITools(toOpenAITools([]json.RawMessage{base}))
	metadataHash := hashOpenAITools(toOpenAITools([]json.RawMessage{withRelayOnlyFields}))
	if baseHash == "" || metadataHash == "" || baseHash != metadataHash {
		t.Fatalf("provider-visible tool hash changed for relay-only/raw-order differences: %q != %q", baseHash, metadataHash)
	}
	withWebSearchHash := hashOpenAITools(toOpenAITools([]json.RawMessage{base, withWebSearch}))
	if withWebSearchHash == baseHash {
		t.Fatalf("normalized web search tool did not change OpenAI tool hash: %q", withWebSearchHash)
	}
	webTools := toOpenAITools([]json.RawMessage{withWebSearch})
	if len(webTools) != 1 || webTools[0].Function.Name != "web_search" {
		t.Fatalf("web search tool was not normalized to an OpenAI function: %+v", webTools)
	}
	if webTools[0].Function.Description != "provider-specific" {
		t.Fatalf("web search description was not retained: %q", webTools[0].Function.Description)
	}
}

func TestToolConversionReportCapturesLossyRules(t *testing.T) {
	longDescription := strings.Repeat("x", maxDescLen+1)
	raw := []json.RawMessage{
		json.RawMessage(`{"webSearchTool":{}}`),
		json.RawMessage(`{"toolSpecification":{"name":"broken","description":"` + longDescription + `","inputSchema":{"json":"not-json"}}}`),
		json.RawMessage(`{"invalid":true}`),
	}

	report := ToolConversionReportForAudit(raw, false)
	if report.RawTools != 3 || report.ProviderTools != 1 {
		t.Fatalf("unexpected tool report: %+v", report)
	}
	if report.FilteredWebSearchTools != 1 || report.InvalidSchemas != 1 ||
		report.InvalidTools != 1 || report.DescriptionTruncations != 1 {
		t.Fatalf("lossy conversion report incomplete: %+v", report)
	}
}

func TestPromptCacheKeyUsesProviderToolsForOpenAI(t *testing.T) {
	base := json.RawMessage(`{"toolSpecification":{"name":"read_file","description":"Read a file","inputSchema":{"json":{"type":"object"}}}}`)
	metadata := json.RawMessage(`{"toolSpecification":{"name":"read_file","description":"Read a file","inputSchema":{"json":{"type":"object"}},"relayMetadata":{"version":2}}}`)
	first := &ParsedConversation{
		ModelID:        "gpt-5.6",
		ConversationID: "conv-a",
		SystemText:     "stable system",
		RawTools:       []json.RawMessage{base},
		ToolsHash:      hashTools([]json.RawMessage{base}),
	}
	second := &ParsedConversation{
		ModelID:        "gpt-5.6",
		ConversationID: "conv-b",
		SystemText:     "stable system",
		RawTools:       []json.RawMessage{metadata},
		ToolsHash:      hashTools([]json.RawMessage{metadata}),
	}
	if firstKey, secondKey := promptCacheKey(first, "gpt-5.6", "responses"), promptCacheKey(second, "gpt-5.6", "responses"); firstKey != secondKey {
		t.Fatalf("provider-equivalent tools split Responses key: %q != %q", firstKey, secondKey)
	}
}

func TestToolConversionStableOrder(t *testing.T) {
	tools := []json.RawMessage{
		json.RawMessage(`{"toolSpecification":{"name":"z_tool","description":"Z","inputSchema":{"json":{"type":"object"}}}}`),
		json.RawMessage(`{"toolSpecification":{"name":"a_tool","description":"A","inputSchema":{"json":{"type":"object"}}}}`),
	}
	openaiTools := toOpenAITools(tools)
	if len(openaiTools) != 2 {
		t.Fatalf("openai tool count = %d", len(openaiTools))
	}
	if openaiTools[0].Function.Name != "a_tool" || openaiTools[1].Function.Name != "z_tool" {
		t.Fatalf("OpenAI tools 未稳定排序: %+v", openaiTools)
	}

	anthropicTools := toAnthropicTools(tools)
	if len(anthropicTools) != 2 {
		t.Fatalf("anthropic tool count = %d", len(anthropicTools))
	}
	if anthropicTools[0].Name != "a_tool" || anthropicTools[1].Name != "z_tool" {
		t.Fatalf("Anthropic tools 未稳定排序: %+v", anthropicTools)
	}
}

func TestCacheAuditHelpersMatchOptimization(t *testing.T) {
	system := "static rules\n\nHere is the last user message:\nhello"
	staticPart, dynamicPart := OptimizeSystemForCacheForAudit(system)
	if staticPart != "static rules" {
		t.Fatalf("static part = %q", staticPart)
	}
	if !strings.Contains(dynamicPart, "hello") {
		t.Fatalf("dynamic part = %q", dynamicPart)
	}

	question, env := OptimizeCurrentContentForCacheForAudit("do it\n\n<EnvironmentContext>ctx</EnvironmentContext>")
	if question != "do it" || !strings.Contains(env, "ctx") {
		t.Fatalf("split current content failed: question=%q env=%q", question, env)
	}
}

func TestOpenAI_GPT56AddsCacheControls(t *testing.T) {
	conv := &ParsedConversation{
		ModelID:        "gpt-5.6",
		ConversationID: "conv-gpt56-chat",
		SystemText:     "stable system prompt",
		History:        []ParsedHistoryEntry{{IsSystem: true, UserContent: "stable system prompt"}},
		CurrentContent: "current question",
	}
	body, err := KiroToOpenAI(conv, "gpt-5.6", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	var req struct {
		PromptCacheOpts *PromptCacheOptions `json:"prompt_cache_options"`
		Messages        []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("解析请求失败: %v", err)
	}
	if req.PromptCacheOpts == nil || req.PromptCacheOpts.Mode != "implicit" || req.PromptCacheOpts.TTL != "30m" {
		t.Fatalf("prompt_cache_options = %+v", req.PromptCacheOpts)
	}
	var parts []OpenAIContentPart
	if err := json.Unmarshal(req.Messages[0].Content, &parts); err != nil {
		t.Fatalf("解析 system content blocks: %v", err)
	}
	if len(parts) != 1 || parts[0].PromptCacheBreakpoint == nil || parts[0].PromptCacheBreakpoint.Mode != "explicit" {
		t.Fatalf("system cache breakpoint = %+v", parts)
	}
}

func TestOpenAI_OlderModelOmitsGPT56CacheControls(t *testing.T) {
	conv := &ParsedConversation{ModelID: "gpt-5.5", SystemText: "stable", CurrentContent: "question"}
	body, err := KiroToOpenAI(conv, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var req OpenAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("解析请求失败: %v", err)
	}
	if req.PromptCacheOpts != nil {
		t.Fatalf("旧模型不应携带 GPT-5.6 cache options: %+v", req.PromptCacheOpts)
	}
}

func TestPromptCacheBreakpointModelCapability(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"gpt-5.6", true},
		{"gpt-5.6-pro", true},
		{"openai/gpt-5.6", true},
		{"gpt-6", true},
		{"gpt-6.1-preview", true},
		{"gpt-5.5", false},
		{"gpt-4o", false},
		{"gpt-5.6foo", false},
		{"custom-gpt-5.6", false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			if got := supportsPromptCacheBreakpoints(tc.model); got != tc.want {
				t.Fatalf("supportsPromptCacheBreakpoints(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestPromptCacheKeySharesStablePrefixAcrossConversations(t *testing.T) {
	base := ParsedConversation{
		ModelID:    "gpt-5.6",
		SystemText: "stable rules\n\nHere is the last user message:\nfirst",
		ToolsHash:  "tools-v1",
	}
	first := base
	first.ConversationID = "conversation-a"
	second := base
	second.ConversationID = "conversation-b"
	second.SystemText = "stable rules\n\nHere is the last user message:\nsecond"

	firstKey := promptCacheKey(&first, "gpt-5.6", "chat")
	secondKey := promptCacheKey(&second, "gpt-5.6", "chat")
	if firstKey != secondKey {
		t.Fatalf("相同静态前缀未共享 key: %q != %q", firstKey, secondKey)
	}
	second.ToolsHash = "tools-v2"
	if changed := promptCacheKey(&second, "gpt-5.6", "chat"); changed == firstKey {
		t.Fatalf("工具定义变化后 key 未变化: %q", changed)
	}
	if responsesKey := promptCacheKey(&first, "gpt-5.6", "responses"); responsesKey == firstKey {
		t.Fatalf("不同 API 复用了同一 key: %q", responsesKey)
	}
}
