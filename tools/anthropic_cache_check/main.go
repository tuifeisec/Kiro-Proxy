package main

import (
	"crypto/sha256"
	"fmt"
	"log"
	"strings"

	json "github.com/goccy/go-json"
	"kiro-proxy/converter"
)

type report struct {
	Name           string
	SystemSHA      string
	SystemLen      int
	HasSystemCache bool
	ToolCount      int
	LastToolCache  bool
	MessageCount   int
	BodyLen        int
}

func main() {
	staticSystem := `<identity>
You are Kiro, an AI-powered development environment.
</identity>

<capabilities>
- Edit files and code directly
- Run terminal commands
</capabilities>`

	conv1 := sampleConversation(staticSystem, "第一轮：帮我修缓存", "检查 Anthropic 缓存")
	conv2 := sampleConversation(staticSystem, "第二轮：继续检查缓存", "继续检查 Anthropic 缓存")

	r1 := buildReport("round1", conv1)
	r2 := buildReport("round2", conv2)

	printReport(r1)
	printReport(r2)

	fmt.Println("\n验收结果:")
	ok := true
	if r1.SystemSHA != r2.SystemSHA {
		ok = false
		fmt.Printf("[FAIL] 两轮静态 system hash 不一致: %s vs %s\n", r1.SystemSHA, r2.SystemSHA)
	} else {
		fmt.Printf("[OK] 静态 system hash 稳定: %s\n", r1.SystemSHA)
	}
	if !r1.HasSystemCache || !r2.HasSystemCache {
		ok = false
		fmt.Println("[FAIL] system 缺少 cache_control")
	} else {
		fmt.Println("[OK] system 带 cache_control")
	}
	if !r1.LastToolCache || !r2.LastToolCache {
		ok = false
		fmt.Println("[FAIL] 最后一个 tool 缺少 cache_control")
	} else {
		fmt.Println("[OK] 最后一个 tool 带 cache_control")
	}

	if !ok {
		log.Fatal("Anthropic cache 本地结构验收失败")
	}
	fmt.Println("[PASS] Anthropic cache 本地结构验收通过")
}

func sampleConversation(staticSystem, dynamicTail, current string) *converter.ParsedConversation {
	system := staticSystem + "\n\nHere is the last user message:\n" + dynamicTail
	tools := []json.RawMessage{
		json.RawMessage(`{"toolSpecification":{"name":"read_file","description":"Read a file","inputSchema":{"json":{"type":"object","properties":{"path":{"type":"string"}}}}}}`),
		json.RawMessage(`{"toolSpecification":{"name":"grep_search","description":"Search files","inputSchema":{"json":{"type":"object","properties":{"query":{"type":"string"}}}}}}`),
	}
	return &converter.ParsedConversation{
		ModelID:    "claude-sonnet-4.5",
		SystemText: system,
		History: []converter.ParsedHistoryEntry{
			{IsSystem: true, UserContent: system},
			{UserContent: "你好", HasAssistant: true, AssistantContent: "你好！"},
		},
		CurrentContent: current + "\n\n<EnvironmentContext>\n<ACTIVE-EDITOR-FILE>\n<file name=\"converter/kiro_to_anthropic.go\" />\n</ACTIVE-EDITOR-FILE>\n</EnvironmentContext>",
		RawTools:       tools,
		ToolsHash:      "local-cache-check-tools",
	}
}

func buildReport(name string, conv *converter.ParsedConversation) report {
	body, err := converter.KiroToAnthropic(conv, "claude-sonnet-4.5", "")
	if err != nil {
		log.Fatalf("%s convert failed: %v", name, err)
	}
	var req converter.AnthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		log.Fatalf("%s unmarshal failed: %v", name, err)
	}

	var sys []converter.AnthropicBlock
	raw, _ := json.Marshal(req.System)
	_ = json.Unmarshal(raw, &sys)

	text := ""
	hasSystemCache := false
	if len(sys) > 0 {
		text = sys[0].Text
		hasSystemCache = sys[0].CacheControl != nil && sys[0].CacheControl.Type == "ephemeral"
	}
	lastToolCache := false
	if len(req.Tools) > 0 {
		last := req.Tools[len(req.Tools)-1]
		lastToolCache = last.CacheControl != nil && last.CacheControl.Type == "ephemeral"
	}

	return report{
		Name:           name,
		SystemSHA:      shortSHA(text),
		SystemLen:      len(text),
		HasSystemCache: hasSystemCache,
		ToolCount:      len(req.Tools),
		LastToolCache:  lastToolCache,
		MessageCount:   len(req.Messages),
		BodyLen:        len(body),
	}
}

func printReport(r report) {
	fmt.Printf("\n[%s]\n", r.Name)
	fmt.Printf("system_sha=%s system_len=%d system_cache=%v\n", r.SystemSHA, r.SystemLen, r.HasSystemCache)
	fmt.Printf("tool_count=%d last_tool_cache=%v messages=%d body=%d bytes\n", r.ToolCount, r.LastToolCache, r.MessageCount, r.BodyLen)
}

func shortSHA(s string) string {
	s = strings.TrimSpace(s)
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:6])
}
