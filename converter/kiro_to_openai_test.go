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

func TestOpenAI_CacheOptimization_SplitsSystemTailAndEnvironmentContext(t *testing.T) {
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

	if req.PromptCacheKey != "conv-openai-cache" {
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
	if last.Role != "user" || lastText != "当前问题" {
		t.Fatalf("最后一条应为真实用户问题，got role=%q content=%q", last.Role, lastText)
	}
	prev := req.Messages[len(req.Messages)-2]
	prevText, _ := prev.Content.(string)
	if prev.Role != "user" || !strings.Contains(prevText, "EnvironmentContext") {
		t.Fatalf("倒数第二条应为 EnvironmentContext，got role=%q content=%q", prev.Role, prevText)
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
