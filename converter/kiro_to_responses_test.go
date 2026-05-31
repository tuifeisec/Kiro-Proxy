package converter

import (
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

// ── 测试辅助 ──────────────────────────────────────────────────────

// parseResponsesReq 反序列化转换结果，方便断言
func parseResponsesReq(t *testing.T, body []byte) ResponsesRequest {
	t.Helper()
	var req ResponsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("反序列化失败: %v\nbody=%s", err, string(body))
	}
	return req
}

// findInputByRole 找到所有指定 role 的 message items
func findInputByRole(input []ReqInput, role string) []ReqInput {
	var result []ReqInput
	for _, item := range input {
		if item.Type == "message" && item.Role == role {
			result = append(result, item)
		}
	}
	return result
}

// findInputByType 找到所有指定 type 的 items
func findInputByType(input []ReqInput, typ string) []ReqInput {
	var result []ReqInput
	for _, item := range input {
		if item.Type == typ {
			result = append(result, item)
		}
	}
	return result
}

// inputContentStr 安全取 content 字符串
func inputContentStr(item ReqInput) string {
	if s, ok := item.Content.(string); ok {
		return s
	}
	return ""
}

// allContentsCombined 合并所有 input items 的文本内容
func allContentsCombined(input []ReqInput) string {
	var b strings.Builder
	for _, item := range input {
		if s := inputContentStr(item); s != "" {
			b.WriteString(s)
			b.WriteString("\n")
		}
		if item.Output != "" {
			b.WriteString(item.Output)
			b.WriteString("\n")
		}
		if item.Arguments != "" {
			b.WriteString(item.Arguments)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// ── 验收测试 1：基本格式正确性 ──────────────────────────────────────

func TestResponses_BasicFormat(t *testing.T) {
	conv := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-001",
		SystemText:     "You are a helpful assistant. Be concise and accurate.",
		CurrentContent: "What is Go?",
		History:        nil,
	}

	body, err := KiroToResponses(conv, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	req := parseResponsesReq(t, body)

	// 验证基础字段
	if req.Model != "gpt-5.5" {
		t.Errorf("model = %q, want gpt-5.5", req.Model)
	}
	if !req.Stream {
		t.Error("stream should be true")
	}
	if !req.Store {
		t.Error("store should be true")
	}
	// instructions 字段应为空（缓存优化）
	if req.Instructions != "" {
		t.Errorf("instructions should be empty for cache optimization, got: %q", req.Instructions)
	}

	// system prompt 应该作为 developer 消息出现在 input 中
	devMsgs := findInputByRole(req.Input, "developer")
	if len(devMsgs) == 0 {
		t.Fatal("缺少 developer 消息（system prompt 应该作为 developer 消息出现）")
	}
	if !strings.Contains(inputContentStr(devMsgs[0]), "You are a helpful assistant") {
		t.Errorf("developer 消息内容不对: %q", inputContentStr(devMsgs[0]))
	}

	// 用户消息应该出现
	userMsgs := findInputByRole(req.Input, "user")
	if len(userMsgs) == 0 {
		t.Fatal("缺少 user 消息")
	}
	found := false
	for _, m := range userMsgs {
		if strings.Contains(inputContentStr(m), "What is Go?") {
			found = true
		}
	}
	if !found {
		t.Error("用户消息 'What is Go?' 丢失")
	}
}

// ── 验收测试 2：内容完整性 - 不能因为缓存优化丢弃内容 ──────────────

func TestResponses_ContentIntegrity_NoLoss(t *testing.T) {
	// 模拟一个完整的多轮对话，包含 system prompt（带动态尾部）、
	// 历史消息、工具调用、工具结果、当前消息（带 EnvironmentContext）
	systemPrompt := `<identity>
You are Kiro, an AI-powered development environment.
</identity>

<capabilities>
- Edit files and code directly
- Run terminal commands
</capabilities>

<rules>
- Follow the safety_guardrails section
</rules>

Here is the last user message:
帮我修一下这个bug`

	conv := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-002",
		SystemText:     systemPrompt,
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: systemPrompt},
			{
				UserContent:      "你好",
				HasAssistant:     true,
				AssistantContent: "你好！有什么可以帮你的？",
			},
			{
				UserContent:      "",
				HasAssistant:     true,
				AssistantContent: "让我看看这个文件",
				ToolUses: []ParsedToolUse{
					{ToolUseID: "call_001", Name: "read_file", Input: json.RawMessage(`{"path":"main.go"}`)},
				},
			},
			{
				ToolResults: []ParsedToolResult{
					{ToolUseID: "call_001", Content: "package main\n\nfunc main() {}\n"},
				},
				HasAssistant:     true,
				AssistantContent: "文件内容已读取",
			},
		},
		CurrentContent: "帮我加个函数\n\n<EnvironmentContext>\n<ACTIVE-EDITOR-FILE>\n<file name=\"main.go\" />\n</ACTIVE-EDITOR-FILE>\n</EnvironmentContext>",
		CurrentToolResults: []ParsedToolResult{
			{ToolUseID: "call_002", Content: "tool result content here"},
		},
		RawTools: []json.RawMessage{
			json.RawMessage(`{"toolSpecification":{"name":"read_file","description":"Read a file","inputSchema":{"json":{"type":"object","properties":{"path":{"type":"string"}}}}}}`),
		},
		ToolsHash: "abc123",
	}

	body, err := KiroToResponses(conv, "gpt-5.5", "medium")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	req := parseResponsesReq(t, body)
	combined := allContentsCombined(req.Input)

	// ── 关键内容完整性检查 ──

	// 1. System prompt 的静态部分不能丢
	if !strings.Contains(combined, "You are Kiro") {
		t.Error("丢失 system prompt 核心内容: 'You are Kiro'")
	}
	if !strings.Contains(combined, "<identity>") {
		t.Error("丢失 system prompt 标记: '<identity>'")
	}
	if !strings.Contains(combined, "<capabilities>") {
		t.Error("丢失 system prompt 内容: '<capabilities>'")
	}
	if !strings.Contains(combined, "<rules>") {
		t.Error("丢失 system prompt 内容: '<rules>'")
	}

	// 2. System prompt 的动态尾部不能丢
	if !strings.Contains(combined, "帮我修一下这个bug") {
		t.Error("丢失 system prompt 动态尾部内容")
	}
	if !strings.Contains(combined, "Here is the last user message") {
		t.Error("丢失 'Here is the last user message' 标记")
	}

	// 3. 历史对话不能丢
	if !strings.Contains(combined, "你好") {
		t.Error("丢失历史用户消息: '你好'")
	}
	if !strings.Contains(combined, "你好！有什么可以帮你的？") {
		t.Error("丢失历史助手消息")
	}
	if !strings.Contains(combined, "让我看看这个文件") {
		t.Error("丢失助手消息（工具调用前的文本）")
	}
	if !strings.Contains(combined, "文件内容已读取") {
		t.Error("丢失助手消息（工具结果后的回复）")
	}

	// 4. 工具调用不能丢
	fcItems := findInputByType(req.Input, "function_call")
	if len(fcItems) == 0 {
		t.Error("丢失 function_call 记录")
	} else {
		found := false
		for _, fc := range fcItems {
			if fc.Name == "read_file" && fc.CallID == "call_001" {
				found = true
				if !strings.Contains(fc.Arguments, "main.go") {
					t.Error("function_call arguments 内容错误")
				}
			}
		}
		if !found {
			t.Error("丢失 read_file 工具调用")
		}
	}

	// 5. 历史工具结果不能丢
	fcoItems := findInputByType(req.Input, "function_call_output")
	if len(fcoItems) < 2 { // 历史的 1 个 + 当前的 1 个
		t.Errorf("function_call_output 数量不足, got %d, want >= 2", len(fcoItems))
	}
	foundHistoryResult := false
	foundCurrentResult := false
	for _, fco := range fcoItems {
		if fco.CallID == "call_001" && strings.Contains(fco.Output, "package main") {
			foundHistoryResult = true
		}
		if fco.CallID == "call_002" && fco.Output == "tool result content here" {
			foundCurrentResult = true
		}
	}
	if !foundHistoryResult {
		t.Error("丢失历史 function_call_output (call_001)")
	}
	if !foundCurrentResult {
		t.Error("丢失当前 function_call_output (call_002)")
	}

	// 6. 当前用户消息不能丢（拆分后两部分都不能丢）
	if !strings.Contains(combined, "帮我加个函数") {
		t.Error("丢失当前用户消息核心内容")
	}
	if !strings.Contains(combined, "EnvironmentContext") {
		t.Error("丢失 EnvironmentContext 内容")
	}
	if !strings.Contains(combined, "main.go") {
		t.Error("丢失 EnvironmentContext 中的文件名")
	}

	// 7. reasoning_effort 应该传递
	if req.ReasoningEffort != "medium" {
		t.Errorf("reasoning_effort = %q, want medium", req.ReasoningEffort)
	}

	// 8. 工具定义不能丢
	if len(req.Tools) == 0 {
		t.Error("丢失 tools 定义")
	} else {
		if req.Tools[0].Name != "read_file" {
			t.Errorf("tool name = %q, want read_file", req.Tools[0].Name)
		}
	}
}

// ── 验收测试 3：缓存优化结构验证 ──────────────────────────────────

func TestResponses_CacheOptimization_Structure(t *testing.T) {
	systemPrompt := `<identity>
You are Kiro, an AI-powered development environment.
You write the code so developers can focus on what matters.
</identity>

<capabilities>
- Knowledge about the user's system context
- Edit files and code directly
- Run terminal commands
</capabilities>

Here is the last user message:
请帮我写一个排序函数`

	conv := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-003",
		SystemText:     systemPrompt,
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: systemPrompt},
			{
				UserContent:      "你好",
				HasAssistant:     true,
				AssistantContent: "你好！",
			},
		},
		CurrentContent: "写个冒泡排序\n\n<EnvironmentContext>\n<ACTIVE-EDITOR-FILE>\n<file name=\"sort.go\" />\n</ACTIVE-EDITOR-FILE>\n</EnvironmentContext>",
	}

	body, err := KiroToResponses(conv, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	req := parseResponsesReq(t, body)

	// ── 结构验证 ──

	// 1. instructions 必须为空
	if req.Instructions != "" {
		t.Errorf("instructions 应为空: %q", req.Instructions)
	}

	// 2. 第一条消息必须是 developer（静态 system prompt）
	if len(req.Input) == 0 {
		t.Fatal("input 为空")
	}
	first := req.Input[0]
	if first.Role != "developer" {
		t.Errorf("第一条消息 role = %q, want developer", first.Role)
	}
	// 静态部分不应包含动态尾部
	firstContent := inputContentStr(first)
	if strings.Contains(firstContent, "请帮我写一个排序函数") {
		t.Error("静态 developer 消息不应包含动态尾部的用户输入")
	}
	if strings.Contains(firstContent, "Here is the last user message") {
		t.Error("静态 developer 消息不应包含 'Here is the last user message' 标记")
	}
	// 但应包含核心 system prompt 内容
	if !strings.Contains(firstContent, "<identity>") {
		t.Error("developer 消息缺少 <identity>")
	}

	// 3. 历史消息应该在 developer 之后、动态内容之前
	historyUserIdx := -1
	historyAsstIdx := -1
	for i, item := range req.Input {
		if item.Type == "message" && item.Role == "user" && inputContentStr(item) == "你好" {
			historyUserIdx = i
		}
		if item.Type == "message" && item.Role == "assistant" && inputContentStr(item) == "你好！" {
			historyAsstIdx = i
		}
	}
	if historyUserIdx < 0 {
		t.Error("找不到历史 user 消息")
	}
	if historyAsstIdx < 0 {
		t.Error("找不到历史 assistant 消息")
	}
	if historyUserIdx > 0 && historyUserIdx < historyAsstIdx {
		// 历史消息顺序正确
	} else if historyUserIdx >= 0 && historyAsstIdx >= 0 {
		t.Errorf("历史消息顺序错误: user@%d, assistant@%d", historyUserIdx, historyAsstIdx)
	}

	// 4. 动态内容应该在最后
	lastIdx := len(req.Input) - 1
	lastItem := req.Input[lastIdx]
	// 最后的消息应该是用户问题或 EnvironmentContext
	if lastItem.Role != "user" {
		t.Errorf("最后一条消息 role = %q, want user", lastItem.Role)
	}

	// 5. 动态 developer 尾部应该在 input 中（但在历史之后）
	dynamicDevIdx := -1
	for i, item := range req.Input {
		if item.Type == "message" && item.Role == "developer" && i > 0 {
			if strings.Contains(inputContentStr(item), "Here is the last user message") {
				dynamicDevIdx = i
			}
		}
	}
	if dynamicDevIdx < 0 {
		t.Error("找不到动态 developer 尾部消息")
	}
	if dynamicDevIdx >= 0 && historyAsstIdx >= 0 && dynamicDevIdx <= historyAsstIdx {
		t.Errorf("动态 developer 消息(@%d)应该在历史消息(@%d)之后", dynamicDevIdx, historyAsstIdx)
	}
}

// ── 验收测试 4：无 system prompt 场景不应崩溃 ──────────────────────

func TestResponses_NoSystemPrompt(t *testing.T) {
	conv := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-004",
		SystemText:     "", // 无 system prompt
		CurrentContent: "hello",
		History: []ParsedHistoryEntry{
			{
				UserContent:      "hi",
				HasAssistant:     true,
				AssistantContent: "hello!",
			},
		},
	}

	body, err := KiroToResponses(conv, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	req := parseResponsesReq(t, body)

	// 无 system prompt 时不应有 developer 消息
	devMsgs := findInputByRole(req.Input, "developer")
	if len(devMsgs) != 0 {
		t.Errorf("无 system prompt 时不应有 developer 消息, got %d", len(devMsgs))
	}

	// 但历史和当前消息不能丢
	combined := allContentsCombined(req.Input)
	if !strings.Contains(combined, "hi") {
		t.Error("丢失历史消息")
	}
	if !strings.Contains(combined, "hello!") {
		t.Error("丢失历史助手回复")
	}
	if !strings.Contains(combined, "hello") {
		t.Error("丢失当前用户消息")
	}
}

// ── 验收测试 5：无动态尾部的 system prompt 不应被错误拆分 ──────────

func TestResponses_StaticOnlySystemPrompt(t *testing.T) {
	systemPrompt := `<identity>
You are Kiro, an AI assistant.
</identity>

<rules>
- Be helpful
- Be concise
</rules>`

	conv := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-005",
		SystemText:     systemPrompt,
		CurrentContent: "hello",
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: systemPrompt},
		},
	}

	body, err := KiroToResponses(conv, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	req := parseResponsesReq(t, body)

	// 只有一条 developer 消息（无动态尾部不拆分）
	devMsgs := findInputByRole(req.Input, "developer")
	if len(devMsgs) != 1 {
		t.Errorf("纯静态 system prompt 应只有 1 条 developer 消息, got %d", len(devMsgs))
	}

	// 完整内容保留
	if !strings.Contains(inputContentStr(devMsgs[0]), "You are Kiro") {
		t.Error("developer 消息内容不完整")
	}
	if !strings.Contains(inputContentStr(devMsgs[0]), "Be helpful") {
		t.Error("developer 消息内容不完整")
	}
}

// ── 验收测试 6：Sub-agent system prompt 识别 ──────────────────────

func TestResponses_SubAgentSystemPrompt(t *testing.T) {
	// 模拟 context-gatherer sub-agent 的 system prompt
	subAgentSystem := `# Efficient Context Gathering Agent

You are a context gathering specialist that identifies and retrieves the minimal, sufficient information needed to address user queries.

## Your Mission

Intelligently gather ONLY essential code sections using the cheapest effective approach.

## Available Context

You have been provided with the following context.

## Tool Usage Guidelines

**ALWAYS BATCH - Never sequential reads**

## Critical Rules

1. Batch all file reads
2. Parallel independent operations

## Output Format

You MUST call the subagentResponse tool as your final action.

<progress_reporting>
- You MUST use the reportProgress tool
</progress_reporting>

<response_requirement>
- You MUST use the subagentResponse tool
</response_requirement>`

	conv := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-006",
		SystemText:     subAgentSystem,
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: subAgentSystem},
			{
				UserContent:      "请查找相关代码",
				HasAssistant:     true,
				AssistantContent: "I will follow these instructions.",
			},
		},
		CurrentContent: "具体的任务描述",
	}

	body, err := KiroToResponses(conv, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	req := parseResponsesReq(t, body)

	// Sub-agent system prompt 应被作为 developer 消息
	devMsgs := findInputByRole(req.Input, "developer")
	if len(devMsgs) == 0 {
		t.Fatal("sub-agent system prompt 应被识别为 developer 消息")
	}
	if !strings.Contains(inputContentStr(devMsgs[0]), "Efficient Context Gathering Agent") {
		t.Error("developer 消息内容不对")
	}

	// 完整性检查
	combined := allContentsCombined(req.Input)
	if !strings.Contains(combined, "请查找相关代码") {
		t.Error("丢失历史用户消息")
	}
	if !strings.Contains(combined, "I will follow these instructions") {
		t.Error("丢失历史助手消息")
	}
	if !strings.Contains(combined, "具体的任务描述") {
		t.Error("丢失当前用户消息")
	}
}

// ── 验收测试 7：EnvironmentContext 拆分后两部分都保留 ──────────────

func TestResponses_EnvironmentContextSplit(t *testing.T) {
	conv := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-007",
		SystemText:     "<identity>\nYou are Kiro.\n</identity>",
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: "<identity>\nYou are Kiro.\n</identity>"},
		},
		CurrentContent: "帮我写个测试\n\n<EnvironmentContext>\nThis information is provided as context.\n\n<OPEN-EDITOR-FILES>\n<file name=\"main.go\" />\n<file name=\"main_test.go\" />\n</OPEN-EDITOR-FILES>\n\n<ACTIVE-EDITOR-FILE>\n<file name=\"main_test.go\" />\n</ACTIVE-EDITOR-FILE>\n</EnvironmentContext>",
	}

	body, err := KiroToResponses(conv, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	req := parseResponsesReq(t, body)
	combined := allContentsCombined(req.Input)

	// 用户问题不能丢
	if !strings.Contains(combined, "帮我写个测试") {
		t.Error("丢失用户问题")
	}

	// EnvironmentContext 不能丢
	if !strings.Contains(combined, "EnvironmentContext") {
		t.Error("丢失 EnvironmentContext 标签")
	}
	if !strings.Contains(combined, "main.go") {
		t.Error("丢失 EnvironmentContext 中的文件引用")
	}
	if !strings.Contains(combined, "main_test.go") {
		t.Error("丢失 EnvironmentContext 中的活跃文件")
	}

	// EnvironmentContext 应该和用户问题分开（作为不同的 user 消息）
	userMsgs := findInputByRole(req.Input, "user")
	if len(userMsgs) < 2 {
		t.Errorf("EnvironmentContext 应该被拆分为独立消息, user msgs count = %d", len(userMsgs))
	}
}

// ── 验收测试 8：前缀稳定性 - 两轮对话前缀应一致 ──────────────────

func TestResponses_PrefixStability(t *testing.T) {
	systemPrompt := `<identity>
You are Kiro, an AI-powered development environment.
</identity>

<capabilities>
- Edit files and code directly
</capabilities>

Here is the last user message:
第一轮用户输入`

	// 第一轮对话
	conv1 := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-008",
		SystemText:     systemPrompt,
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: systemPrompt},
			{UserContent: "你好", HasAssistant: true, AssistantContent: "你好！"},
		},
		CurrentContent: "第一个问题",
	}

	// 第二轮对话：system prompt 动态尾部变了，但静态部分不变
	systemPrompt2 := `<identity>
You are Kiro, an AI-powered development environment.
</identity>

<capabilities>
- Edit files and code directly
</capabilities>

Here is the last user message:
第二轮用户输入`

	conv2 := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-008",
		SystemText:     systemPrompt2,
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: systemPrompt2},
			{UserContent: "你好", HasAssistant: true, AssistantContent: "你好！"},
			{UserContent: "第一个问题", HasAssistant: true, AssistantContent: "第一个回答"},
		},
		CurrentContent: "第二个问题",
	}

	body1, _ := KiroToResponses(conv1, "gpt-5.5", "")
	body2, _ := KiroToResponses(conv2, "gpt-5.5", "")

	req1 := parseResponsesReq(t, body1)
	req2 := parseResponsesReq(t, body2)

	// 两轮请求的第一条消息（静态 developer）应该完全一致
	if len(req1.Input) == 0 || len(req2.Input) == 0 {
		t.Fatal("input 为空")
	}

	dev1 := inputContentStr(req1.Input[0])
	dev2 := inputContentStr(req2.Input[0])

	if dev1 != dev2 {
		t.Errorf("前缀不稳定！\n第一轮 developer: %q\n第二轮 developer: %q", dev1, dev2)
	}

	// 静态前缀不应包含动态内容
	if strings.Contains(dev1, "第一轮用户输入") || strings.Contains(dev1, "第二轮用户输入") {
		t.Error("静态前缀不应包含动态用户输入")
	}

	// 历史消息部分：第一轮的历史应该是第二轮前缀的子集
	// （第二轮多了一轮对话，但前面的消息顺序和内容不变）
	// 这里只检查共同的历史部分一致
	if len(req1.Input) < 2 || len(req2.Input) < 2 {
		t.Fatal("input 太短")
	}
	// 第二条消息应该都是 "你好"
	if inputContentStr(req1.Input[1]) != inputContentStr(req2.Input[1]) {
		t.Errorf("历史前缀不一致:\n round1[1]=%q\n round2[1]=%q",
			inputContentStr(req1.Input[1]), inputContentStr(req2.Input[1]))
	}
}

// ── 验收测试 9：大量工具结果的完整性 ──────────────────────────────

func TestResponses_ManyToolResults(t *testing.T) {
	conv := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-009",
		SystemText:     "<identity>\nYou are Kiro.\n</identity>",
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: "<identity>\nYou are Kiro.\n</identity>"},
		},
		CurrentContent: "继续",
		CurrentToolResults: []ParsedToolResult{
			{ToolUseID: "call_a", Content: "result_a content is important"},
			{ToolUseID: "call_b", Content: "result_b content is critical"},
			{ToolUseID: "call_c", Content: "result_c has the file content:\npackage main\nfunc main() {}"},
		},
	}

	body, err := KiroToResponses(conv, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	req := parseResponsesReq(t, body)
	combined := allContentsCombined(req.Input)

	// 所有工具结果都不能丢
	if !strings.Contains(combined, "result_a content is important") {
		t.Error("丢失 call_a 工具结果")
	}
	if !strings.Contains(combined, "result_b content is critical") {
		t.Error("丢失 call_b 工具结果")
	}
	if !strings.Contains(combined, "result_c has the file content") {
		t.Error("丢失 call_c 工具结果")
	}
	if !strings.Contains(combined, "package main") {
		t.Error("丢失 call_c 工具结果中的代码内容")
	}

	// call_id 必须正确对应
	fcoItems := findInputByType(req.Input, "function_call_output")
	if len(fcoItems) != 3 {
		t.Fatalf("function_call_output 数量 = %d, want 3", len(fcoItems))
	}
	ids := map[string]bool{}
	for _, fco := range fcoItems {
		ids[fco.CallID] = true
	}
	for _, id := range []string{"call_a", "call_b", "call_c"} {
		if !ids[id] {
			t.Errorf("缺少 call_id = %s 的 function_call_output", id)
		}
	}
}

// ── 验收测试 10：looksLikeSystemPrompt 识别验证 ──────────────────

func TestLooksLikeSystemPrompt(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		expect bool
	}{
		{"主 agent - identity 标记", "<identity>\nYou are Kiro\n</identity>", true},
		{"主 agent - key_kiro_features", "<key_kiro_features>\nfeatures here\n</key_kiro_features>", true},
		{"主 agent - You are Kiro", "You are Kiro, an AI-powered development environment.", true},
		{"classifier", "You are an intent classifier. IMPORTANT: Respond ONLY with a raw JSON object", true},
		{"sub-agent - context gatherer", "# Efficient Context Gathering Agent\n\nYou are a context gathering specialist\n\n## Your Mission\n\n## Available Context\n\n## Tool Usage Guidelines\n\n## Critical Rules\n\n## Output Format\n\nYou MUST call the subagentResponse tool\n\n<progress_reporting>\n</progress_reporting>\n\n<response_requirement>\n</response_requirement>", true},
		{"sub-agent - progress_reporting 标记", strings.Repeat("x", 2100) + "<progress_reporting>\n- report progress\n</progress_reporting>", true},
		{"sub-agent - 通用启发式（含多个模式）", "# Custom Agent\n\nYou do things.\n\n## Available Context\n\nSome context here.\n\n## Tool Usage Guidelines\n\nUse tools wisely.\n\n## Critical Rules\n\n1. Be good.\n\n" + strings.Repeat("detailed instructions and context for the agent to follow carefully ", 40), true},
		{"普通用户消息 - 短", "你好，帮我修个bug", false},
		{"普通用户消息 - 中等长度", "我想让你帮我看一下这段代码有什么问题，这是一个 Go 项目，主要负责代理转发。", false},
		{"普通用户消息 - 含代码但非系统提示", "请帮我优化这个函数：\n\nfunc main() {\n  fmt.Println(\"hello\")\n}\n\n要求提高性能", false},
		{"空字符串", "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := looksLikeSystemPrompt(c.input)
			if got != c.expect {
				t.Errorf("looksLikeSystemPrompt() = %v, want %v\ninput[:100] = %q",
					got, c.expect, truncate(c.input, 100))
			}
		})
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ── 验收测试 11：对比新旧实现 - 所有内容必须存在 ──────────────────

func TestResponses_OldVsNew_ContentParity(t *testing.T) {
	// 用旧的 buildResponsesInput 和新的 buildResponsesInputCacheOptimized 对比
	// 新实现的所有文本内容必须是旧实现的超集（允许顺序不同，但内容不能少）

	systemPrompt := `<identity>
You are Kiro.
</identity>

<rules>
Do things right.
</rules>

Here is the last user message:
测试消息`

	conv := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-010",
		SystemText:     systemPrompt,
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: systemPrompt},
			{
				UserContent:      "问题1",
				HasAssistant:     true,
				AssistantContent: "回答1",
				ToolUses: []ParsedToolUse{
					{ToolUseID: "tc1", Name: "grep_search", Input: json.RawMessage(`{"query":"test"}`)},
				},
			},
			{
				ToolResults: []ParsedToolResult{
					{ToolUseID: "tc1", Content: "搜索结果：找到3处匹配"},
				},
				UserContent:      "问题2",
				HasAssistant:     true,
				AssistantContent: "回答2",
			},
		},
		CurrentContent: "当前问题\n\n<EnvironmentContext>\n<ACTIVE-EDITOR-FILE>\n<file name=\"test.go\" />\n</ACTIVE-EDITOR-FILE>\n</EnvironmentContext>",
		CurrentToolResults: []ParsedToolResult{
			{ToolUseID: "tc2", Content: "当前工具结果"},
		},
	}

	oldInput := buildResponsesInput(conv)
	newInput := buildResponsesInputCacheOptimized(conv)

	oldCombined := allContentsCombined(oldInput)
	newCombined := allContentsCombined(newInput)

	// 旧实现的每一段关键内容在新实现中都必须存在
	keyContents := []string{
		"问题1",
		"回答1",
		"搜索结果：找到3处匹配",
		"问题2",
		"回答2",
		"当前问题",
		"EnvironmentContext",
		"test.go",
		"当前工具结果",
	}

	for _, key := range keyContents {
		if !strings.Contains(oldCombined, key) {
			t.Errorf("旧实现竟然也缺少内容 %q（测试数据有误）", key)
		}
		if !strings.Contains(newCombined, key) {
			t.Errorf("新实现丢失了内容: %q", key)
		}
	}

	// 新实现应该额外包含 system prompt 的内容（旧实现不包含，因为旧实现靠 instructions 字段）
	if !strings.Contains(newCombined, "You are Kiro") {
		t.Error("新实现缺少 system prompt 内容（应在 developer 消息中）")
	}
	if !strings.Contains(newCombined, "测试消息") {
		t.Error("新实现缺少 system prompt 动态尾部内容")
	}

	// 工具调用参数也不能丢
	oldHasTC := false
	newHasTC := false
	for _, item := range oldInput {
		if item.Type == "function_call" && item.CallID == "tc1" {
			oldHasTC = true
		}
	}
	for _, item := range newInput {
		if item.Type == "function_call" && item.CallID == "tc1" {
			newHasTC = true
		}
	}
	if oldHasTC && !newHasTC {
		t.Error("新实现丢失了 function_call")
	}
}

// ── 验收测试 12：只有 EnvironmentContext 没有用户文本 ──────────────

func TestResponses_OnlyEnvironmentContext(t *testing.T) {
	// 有时用户消息可能只有 EnvironmentContext（比如自动触发的请求）
	conv := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-011",
		SystemText:     "<identity>\nYou are Kiro.\n</identity>",
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: "<identity>\nYou are Kiro.\n</identity>"},
		},
		CurrentContent: "<EnvironmentContext>\n<ACTIVE-EDITOR-FILE>\n<file name=\"app.ts\" />\n</ACTIVE-EDITOR-FILE>\n</EnvironmentContext>",
	}

	body, err := KiroToResponses(conv, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	req := parseResponsesReq(t, body)
	combined := allContentsCombined(req.Input)

	// EnvironmentContext 不能丢
	if !strings.Contains(combined, "EnvironmentContext") {
		t.Error("丢失 EnvironmentContext")
	}
	if !strings.Contains(combined, "app.ts") {
		t.Error("丢失文件名")
	}
}

// ── 验收测试 13：空当前消息 + 有工具结果（纯 tool response 轮） ────

func TestResponses_EmptyCurrentContent_WithToolResults(t *testing.T) {
	conv := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-012",
		SystemText:     "<identity>\nYou are Kiro.\n</identity>",
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: "<identity>\nYou are Kiro.\n</identity>"},
			{UserContent: "读取文件", HasAssistant: true, AssistantContent: "好的"},
		},
		CurrentContent: "", // 空，这是纯工具结果返回
		CurrentToolResults: []ParsedToolResult{
			{ToolUseID: "call_read", Content: "file content here: package main\nimport \"fmt\""},
		},
	}

	body, err := KiroToResponses(conv, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	req := parseResponsesReq(t, body)
	combined := allContentsCombined(req.Input)

	// 工具结果不能丢
	if !strings.Contains(combined, "file content here") {
		t.Error("丢失工具结果内容")
	}
	if !strings.Contains(combined, "package main") {
		t.Error("丢失工具结果中的代码")
	}

	// 历史不能丢
	if !strings.Contains(combined, "读取文件") {
		t.Error("丢失历史消息")
	}

	// function_call_output 应该存在
	fcoItems := findInputByType(req.Input, "function_call_output")
	if len(fcoItems) != 1 {
		t.Fatalf("function_call_output 数量 = %d, want 1", len(fcoItems))
	}
	if fcoItems[0].CallID != "call_read" {
		t.Errorf("call_id = %q, want call_read", fcoItems[0].CallID)
	}
}

// ── 验收测试 14：Responses API 保留当前与历史图片 ────────────────

func TestResponses_ImagesIncluded(t *testing.T) {
	conv := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-013",
		SystemText:     "<identity>\nYou are Kiro.\n</identity>",
		History: []ParsedHistoryEntry{
			{IsSystem: true, UserContent: "<identity>\nYou are Kiro.\n</identity>"},
			{
				UserContent: "历史图片是什么？",
				Images: []KiroImage{{
					Format: "jpeg",
					Source: KiroImageSource{Bytes: "aGlzdG9yeS1pbWFnZQ=="},
				}},
				HasAssistant:     true,
				AssistantContent: "历史图片回答",
			},
		},
		CurrentContent: "当前图片是什么？\n\n<EnvironmentContext>\n<file name=\"main.go\" />\n</EnvironmentContext>",
		CurrentImages: []KiroImage{{
			Format: "png",
			Source: KiroImageSource{Bytes: "Y3VycmVudC1pbWFnZQ=="},
		}},
	}

	body, err := KiroToResponses(conv, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	req := parseResponsesReq(t, body)
	userMsgs := findInputByRole(req.Input, "user")

	var foundHistoryImage bool
	var foundCurrentImage bool
	var foundCurrentText bool
	for _, msg := range userMsgs {
		parts, ok := msg.Content.([]interface{})
		if !ok {
			continue
		}
		for _, raw := range parts {
			part, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if part["type"] == "input_image" {
				s, _ := part["image_url"].(string)
				if s == "data:image/jpeg;base64,aGlzdG9yeS1pbWFnZQ==" {
					foundHistoryImage = true
				}
				if s == "data:image/png;base64,Y3VycmVudC1pbWFnZQ==" {
					foundCurrentImage = true
				}
			}
			if part["type"] == "input_text" && strings.Contains(part["text"].(string), "当前图片是什么") {
				foundCurrentText = true
			}
		}
	}

	if !foundHistoryImage {
		t.Error("Responses 请求中丢失历史图片")
	}
	if !foundCurrentImage {
		t.Error("Responses 请求中丢失当前图片")
	}
	if !foundCurrentText {
		t.Error("Responses 图片消息中丢失当前用户文本")
	}
}

func TestResponses_ImageOnlyCurrentMessage(t *testing.T) {
	conv := &ParsedConversation{
		ModelID:        "gpt-5.5",
		ConversationID: "conv-014",
		CurrentImages: []KiroImage{{
			Format: "webp",
			Source: KiroImageSource{Bytes: "b25seS1pbWFnZQ=="},
		}},
	}

	body, err := KiroToResponses(conv, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	req := parseResponsesReq(t, body)
	userMsgs := findInputByRole(req.Input, "user")
	if len(userMsgs) != 1 {
		t.Fatalf("只有图片时应生成 1 条 user 消息, got %d", len(userMsgs))
	}
	parts, ok := userMsgs[0].Content.([]interface{})
	if !ok || len(parts) != 1 {
		t.Fatalf("只有图片时 content 应为 1 个 part 的数组, got %#v", userMsgs[0].Content)
	}
	part, ok := parts[0].(map[string]interface{})
	if !ok {
		t.Fatalf("图片 part 类型错误: %#v", parts[0])
	}
	if part["type"] != "input_image" {
		t.Errorf("part type = %v, want input_image", part["type"])
	}
	if part["image_url"] != "data:image/webp;base64,b25seS1pbWFnZQ==" {
		t.Errorf("image_url = %v", part["image_url"])
	}
}
