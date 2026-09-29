package converter

import (
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

func TestOpenAIChatSchemaRemovesImpossibleAnyOfBranches(t *testing.T) {
	rawSchema := json.RawMessage(`{"type":"object","properties":{"command":{"anyOf":[{"anyOf":[{"not":{}},{"type":"string"}]},{"type":"null"}]},"cwd":{"anyOf":[{"anyOf":[{"not":{}},{"type":"string","description":"inner"}],"description":"outer"},{"type":"null"}]},"untouched":{"type":["string","null"]}},"required":["command"]}`)
	tools := []OpenAITool{{Type: "function", Function: OpenAIFuncDef{Name: "control_pwsh_process", Parameters: rawSchema}}}
	got, changed := normalizeOpenAIChatTools(tools)
	if !changed || len(got) != 1 {
		t.Fatalf("expected one normalized tool: changed=%v tools=%+v", changed, got)
	}
	if string(tools[0].Function.Parameters.(json.RawMessage)) != string(rawSchema) {
		t.Fatal("cached input tool was mutated")
	}
	encoded, err := json.Marshal(got[0].Function.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"not"`) {
		t.Fatalf("impossible branch remains: %s", encoded)
	}
	var schema struct {
		Properties map[string]map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	var command, cwd []map[string]interface{}
	if err := json.Unmarshal(schema.Properties["command"]["anyOf"], &command); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(schema.Properties["cwd"]["anyOf"], &cwd); err != nil {
		t.Fatal(err)
	}
	cwdInner, _ := cwd[0]["anyOf"].([]interface{})
	if len(command) != 2 || command[0]["type"] != "string" || command[1]["type"] != "null" ||
		len(cwd) != 2 || cwd[0]["description"] != "outer" || len(cwdInner) != 1 ||
		cwdInner[0].(map[string]interface{})["description"] != "inner" ||
		cwdInner[0].(map[string]interface{})["type"] != "string" {
		t.Fatalf("nullable branch semantics changed: command=%v cwd=%v", command, cwd)
	}
	if string(schema.Properties["untouched"]["type"]) != `["string","null"]` {
		t.Fatalf("unrelated schema changed: %s", schema.Properties["untouched"]["type"])
	}
}

func TestKiroToOpenAIChatNormalizesOnlyOutboundChatTools(t *testing.T) {
	raw := []json.RawMessage{json.RawMessage(`{"toolSpecification":{"name":"execute_pwsh","description":"Run a command","inputSchema":{"json":{"type":"object","properties":{"cwd":{"anyOf":[{"not":{}},{"type":"string"}]}}}}}}`)}
	conv := &ParsedConversation{ModelID: "deepseek", CurrentContent: "test", RawTools: raw, ToolsHash: hashTools(raw)}
	chatBody, err := KiroToOpenAIChatUpstream(conv, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(chatBody), `"not":{}`) {
		t.Fatalf("Chat still emits impossible branch: %s", chatBody)
	}
	plainBody, err := KiroToOpenAI(conv, "", "")
	if err != nil || !strings.Contains(string(plainBody), `"not":{}`) {
		t.Fatalf("ungated converter changed: err=%v body=%s", err, plainBody)
	}
	original := toOpenAITools(raw)
	originalEncoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(originalEncoded), `"not":{}`) {
		t.Fatal("shared provider tool conversion was changed")
	}
	chatHash, report := CachedOpenAIChatToolAudit(conv.ToolsHash, raw)
	var parsed OpenAIRequest
	if err := json.Unmarshal(chatBody, &parsed); err != nil {
		t.Fatal(err)
	}
	if report.ProviderTools != 1 || chatHash != hashOpenAITools(parsed.Tools) ||
		parsed.PromptCacheKey != promptCacheKeyWithToolsHash(conv, conv.ModelID, "chat", chatHash) {
		t.Fatalf("Chat cache audit differs from outbound body: report=%+v hash=%s key=%s", report, chatHash, parsed.PromptCacheKey)
	}
}

func TestOpenAIChatSchemaKeepsWhollyFalseAnyOf(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"x":{"anyOf":[{"not":{}}]}}}`)
	tools := []OpenAITool{{Type: "function", Function: OpenAIFuncDef{Name: "f", Parameters: raw}}}
	got, changed := normalizeOpenAIChatTools(tools)
	if changed || string(got[0].Function.Parameters.(json.RawMessage)) != string(raw) {
		t.Fatalf("wholly false schema changed: changed=%v parameters=%s", changed, got[0].Function.Parameters)
	}
}

func TestOpenAIChatSchemaPreservesReferencedAnyOfPath(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"tasks":{"anyOf":[{"anyOf":[{"not":{}},{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"}}}}]},{"type":"null"}]},"new_tasks":{"type":"array","items":{"$ref":"#/properties/tasks/anyOf/0/anyOf/1/items"}}}}`)
	tools := []OpenAITool{{Type: "function", Function: OpenAIFuncDef{Name: "todo_list", Parameters: raw}}}
	got, changed := normalizeOpenAIChatTools(tools)
	if changed || string(got[0].Function.Parameters.(json.RawMessage)) != string(raw) {
		t.Fatalf("referenced schema changed: changed=%v parameters=%s", changed, got[0].Function.Parameters)
	}
}

func TestOpenAIChatSchemaRepairsObservedTodoListReference(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"tasks":{"type":["array","null"],"items":{"type":"object","properties":{"task_description":{"type":"string"}}}},"new_tasks":{"type":["array","null"],"items":{"$ref":"#/properties/tasks/anyOf/0/anyOf/1/items"}}}}`)
	tools := []OpenAITool{{Type: "function", Function: OpenAIFuncDef{Name: "todo_list", Parameters: raw}}}
	got, changed := normalizeOpenAIChatTools(tools)
	if !changed {
		t.Fatal("expected observed dangling reference to be repaired")
	}
	if string(tools[0].Function.Parameters.(json.RawMessage)) != string(raw) {
		t.Fatal("cached tool schema was mutated")
	}
	var schema struct {
		Properties struct {
			NewTasks struct {
				Items struct {
					Ref string `json:"$ref"`
				} `json:"items"`
			} `json:"new_tasks"`
		} `json:"properties"`
	}
	encoded, err := json.Marshal(got[0].Function.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties.NewTasks.Items.Ref != "#/properties/tasks/items" {
		t.Fatalf("unexpected repaired reference: %q", schema.Properties.NewTasks.Items.Ref)
	}
	other := []OpenAITool{{Type: "function", Function: OpenAIFuncDef{Name: "other_tool", Parameters: raw}}}
	unchanged, modified := normalizeOpenAIChatTools(other)
	if modified || string(unchanged[0].Function.Parameters.(json.RawMessage)) != string(raw) {
		t.Fatal("unrelated tool schema changed")
	}
}
