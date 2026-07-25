package converter

import (
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

func parseAnthropicReq(t *testing.T, body []byte) AnthropicRequest {
	t.Helper()
	var req AnthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("反序列化 Anthropic 请求失败: %v\nbody=%s", err, string(body))
	}
	return req
}

func anthropicSystemBlocks(t *testing.T, req AnthropicRequest) []AnthropicBlock {
	t.Helper()
	b, ok := req.System.([]AnthropicBlock)
	if ok {
		return b
	}
	raw, err := json.Marshal(req.System)
	if err != nil {
		t.Fatalf("marshal system failed: %v", err)
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("system is not []AnthropicBlock: %T %v", req.System, req.System)
	}
	return b
}

func anthropicContentBlocks(t *testing.T, content interface{}) []AnthropicBlock {
	t.Helper()
	if b, ok := content.([]AnthropicBlock); ok {
		return b
	}
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal content failed: %v", err)
	}
	var b []AnthropicBlock
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("content is not []AnthropicBlock: %T %v", content, content)
	}
	return b
}

func anthropicMessageText(t *testing.T, msg AnthropicMessage) string {
	t.Helper()
	if s, ok := msg.Content.(string); ok {
		return s
	}
	blocks := anthropicContentBlocks(t, msg.Content)
	var b strings.Builder
	for _, block := range blocks {
		if block.Text != "" {
			b.WriteString(block.Text)
			b.WriteString("\n")
		}
		if s, ok := block.Content.(string); ok {
			b.WriteString(s)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func anthropicAllText(t *testing.T, req AnthropicRequest) string {
	t.Helper()
	var b strings.Builder
	for _, block := range anthropicSystemBlocks(t, req) {
		b.WriteString(block.Text)
		b.WriteString("\n")
	}
	for _, msg := range req.Messages {
		b.WriteString(anthropicMessageText(t, msg))
		b.WriteString("\n")
	}
	return b.String()
}

func TestAnthropic_CacheOptimization_SplitsDynamicSystemTail(t *testing.T) {
	system1 := `<identity>
You are Kiro.
</identity>

<rules>
Be helpful.
</rules>

Here is the last user message:
第一轮问题`
	system2 := `<identity>
You are Kiro.
</identity>

<rules>
Be helpful.
</rules>

Here is the last user message:
第二轮问题`

	conv1 := &ParsedConversation{
		ModelID:        "claude-sonnet-4.5",
		SystemText:     system1,
		CurrentContent: "当前问题 A",
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: system1},
			{UserContent: "你好", HasAssistant: true, AssistantContent: "你好！"},
		},
	}
	conv2 := &ParsedConversation{
		ModelID:        "claude-sonnet-4.5",
		SystemText:     system2,
		CurrentContent: "当前问题 B",
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: system2},
			{UserContent: "你好", HasAssistant: true, AssistantContent: "你好！"},
		},
	}

	body1, err := KiroToAnthropic(conv1, "", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	body2, err := KiroToAnthropic(conv2, "", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	req1 := parseAnthropicReq(t, body1)
	req2 := parseAnthropicReq(t, body2)
	sys1 := anthropicSystemBlocks(t, req1)
	sys2 := anthropicSystemBlocks(t, req2)

	if len(sys1) != 1 || len(sys2) != 1 {
		t.Fatalf("system block count mismatch: %d %d", len(sys1), len(sys2))
	}
	if sys1[0].Text != sys2[0].Text {
		t.Fatalf("静态 system 前缀不稳定:\n1=%q\n2=%q", sys1[0].Text, sys2[0].Text)
	}
	if strings.Contains(sys1[0].Text, "第一轮问题") || strings.Contains(sys1[0].Text, "Here is the last user message") {
		t.Fatalf("静态 system 不应包含动态尾部: %q", sys1[0].Text)
	}
	if sys1[0].CacheControl == nil || sys1[0].CacheControl.Type != "ephemeral" {
		t.Fatal("静态 system 应带 cache_control ephemeral")
	}

	all1 := anthropicAllText(t, req1)
	all2 := anthropicAllText(t, req2)
	if !strings.Contains(all1, "第一轮问题") || !strings.Contains(all1, "Here is the last user message") {
		t.Fatal("第一轮动态 system 尾部丢失")
	}
	if !strings.Contains(all2, "第二轮问题") || !strings.Contains(all2, "Here is the last user message") {
		t.Fatal("第二轮动态 system 尾部丢失")
	}
}

func TestAnthropic_CacheOptimization_ToolsBreakpointAndContentIntegrity(t *testing.T) {
	system := `<identity>
You are Kiro.
</identity>

Here is the last user message:
帮我读取文件`
	conv := &ParsedConversation{
		ModelID:    "claude-sonnet-4.5",
		SystemText: system,
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: system},
			{
				UserContent:      "读取 main.go",
				HasAssistant:     true,
				AssistantContent: "我来读文件",
				ToolUses:         []ParsedToolUse{{ToolUseID: "call_1", Name: "read_file", Input: json.RawMessage(`{"path":"main.go"}`)}},
			},
			{
				ToolResults:      []ParsedToolResult{{ToolUseID: "call_1", Content: "package main\nfunc main() {}"}},
				HasAssistant:     true,
				AssistantContent: "读完了",
			},
		},
		CurrentContent:     "继续分析\n\n<EnvironmentContext>\n<ACTIVE-EDITOR-FILE>\n<file name=\"main.go\" />\n</ACTIVE-EDITOR-FILE>\n</EnvironmentContext>",
		CurrentToolResults: []ParsedToolResult{{ToolUseID: "call_2", Content: "current tool result"}},
		RawTools: []json.RawMessage{
			json.RawMessage(`{"toolSpecification":{"name":"read_file","description":"Read file","inputSchema":{"json":{"type":"object","properties":{"path":{"type":"string"}}}}}}`),
			json.RawMessage(`{"toolSpecification":{"name":"grep_search","description":"Search","inputSchema":{"json":{"type":"object","properties":{"query":{"type":"string"}}}}}}`),
		},
		ToolsHash: "anthropic-test-tools",
	}

	body, err := KiroToAnthropic(conv, "claude-sonnet-4.5", "medium")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	req := parseAnthropicReq(t, body)

	if req.Thinking == nil || req.Thinking.BudgetTokens != 4096 {
		t.Fatalf("thinking medium 未正确传递: %+v", req.Thinking)
	}
	if len(req.Tools) != 2 {
		t.Fatalf("tools count = %d, want 2", len(req.Tools))
	}
	if req.Tools[0].CacheControl != nil {
		t.Fatal("只有最后一个工具应带 cache_control")
	}
	if req.Tools[1].CacheControl == nil || req.Tools[1].CacheControl.Type != "ephemeral" {
		t.Fatal("最后一个工具应带 cache_control ephemeral")
	}

	combined := anthropicAllText(t, req)
	for _, want := range []string{
		"You are Kiro",
		"帮我读取文件",
		"读取 main.go",
		"我来读文件",
		"package main",
		"读完了",
		"current tool result",
		"EnvironmentContext",
		"继续分析",
	} {
		if !strings.Contains(combined, want) {
			t.Fatalf("Anthropic 请求丢失内容 %q\ncombined=%s", want, combined)
		}
	}
}

func TestAnthropic_RollingCacheBreakpointOnLastToolResult(t *testing.T) {
	conv := &ParsedConversation{
		ModelID: "claude-sonnet-4.5",
		History: []ParsedHistoryEntry{
			{UserContent: "first", HasAssistant: true, AssistantContent: "done"},
		},
		CurrentToolResults: []ParsedToolResult{{
			ToolUseID: "call_1",
			Content:   "tool output",
		}},
	}

	body, err := KiroToAnthropic(conv, "", "")
	if err != nil {
		t.Fatalf("convert request: %v", err)
	}
	req := parseAnthropicReq(t, body)
	if len(req.Messages) != 3 {
		t.Fatalf("messages count = %d, want 3", len(req.Messages))
	}
	historyBlocks := anthropicContentBlocks(t, req.Messages[0].Content)
	if len(historyBlocks) != 1 || historyBlocks[0].CacheControl != nil {
		t.Fatalf("historical user should not retain rolling breakpoint: %+v", historyBlocks)
	}
	blocks := anthropicContentBlocks(t, req.Messages[2].Content)
	if len(blocks) != 1 || blocks[0].Type != "tool_result" {
		t.Fatalf("current tool_result blocks = %+v", blocks)
	}
	if blocks[0].CacheControl == nil || blocks[0].CacheControl.Type != "ephemeral" {
		t.Fatal("last tool_result should be the rolling cache breakpoint")
	}
}

func TestAnthropic_EnvironmentContextPreservesCurrentMessage(t *testing.T) {
	conv := &ParsedConversation{
		ModelID:        "claude-sonnet-4.5",
		SystemText:     "<identity>\nYou are Kiro.\n</identity>",
		CurrentContent: "帮我写测试\n\n<EnvironmentContext>\n<file name=\"app_test.go\" />\n</EnvironmentContext>",
	}

	body, err := KiroToAnthropic(conv, "", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	req := parseAnthropicReq(t, body)
	if len(req.Messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(req.Messages))
	}
	blocks := anthropicContentBlocks(t, req.Messages[0].Content)
	content, ok := "", len(blocks) == 1
	if ok {
		content = blocks[0].Text
	}
	if ok && (blocks[0].CacheControl == nil || blocks[0].CacheControl.Type != "ephemeral") {
		t.Fatal("current user should be the rolling cache breakpoint")
	}
	if !ok {
		t.Fatalf("current user should contain one text block, got %T", req.Messages[0].Content)
	}
	if content != conv.CurrentContent {
		t.Fatalf("current 内容发生变化: %q", content)
	}
}
