package converter

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode"

	json "github.com/goccy/go-json"
)

const observedWorkflowNamePattern = `^[^\p{Cc}\p{Zl}\p{Zp}"]*$`

// This fixture records the observed provider-rejected expression without
// retaining any user prompt, tool contents, credentials, or other payload.
var observedWorkflowSchema = json.RawMessage(`{"toolSpecification":{"name":"run_workflow","description":"redacted schema compatibility fixture","inputSchema":{"json":{"type":"object","$defs":{"Workflow":{"type":"object","properties":{"name":{"type":"string","pattern":"^[^\\p{Cc}\\p{Zl}\\p{Zp}\"]*$","description":"workflow name"},"near_match":{"type":"string","pattern":"^[^\\p{Cc}\\p{Zl}\\p{Zp}\"]*x$","default":9007199254740993}}}},"properties":{"workflow":{"$ref":"#/$defs/Workflow"},"note":{"type":"string","default":"^[^\\p{Cc}\\p{Zl}\\p{Zp}\"]*$"}}}}}}`)

func TestOpenAIResponsesSchemaNormalizesOnlyObservedRunWorkflowPattern(t *testing.T) {
	otherTool := json.RawMessage(`{"toolSpecification":{"name":"other_tool","inputSchema":{"json":{"type":"string","pattern":"^[^\\p{Cc}\\p{Zl}\\p{Zp}\"]*$"}}}}`)
	raw := []json.RawMessage{observedWorkflowSchema, otherTool}
	original := toOpenAITools(raw)
	runIndex, otherIndex := -1, -1
	for i, tool := range original {
		switch tool.Function.Name {
		case "run_workflow":
			runIndex = i
		case "other_tool":
			otherIndex = i
		}
	}
	if runIndex < 0 || otherIndex < 0 {
		t.Fatalf("fixture tools not converted: %+v", original)
	}
	originalRunParameters := append(json.RawMessage(nil), original[runIndex].Function.Parameters.(json.RawMessage)...)
	originalOtherParameters := append(json.RawMessage(nil), original[otherIndex].Function.Parameters.(json.RawMessage)...)

	got, changed := normalizeOpenAIResponsesTools(original)
	if !changed || len(got) != 2 {
		t.Fatalf("expected only the observed Responses schema to change: changed=%v tools=%+v", changed, got)
	}
	if string(original[runIndex].Function.Parameters.(json.RawMessage)) != string(originalRunParameters) ||
		string(original[otherIndex].Function.Parameters.(json.RawMessage)) != string(originalOtherParameters) {
		t.Fatal("normalization mutated the cached OpenAI tool input")
	}
	if string(raw[0]) != string(observedWorkflowSchema) || string(raw[1]) != string(otherTool) {
		t.Fatal("normalization mutated the original cached RawMessage")
	}

	first, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	secondTools, changedAgain := normalizeOpenAIResponsesTools(got)
	if changedAgain || !reflect.DeepEqual(secondTools, got) {
		t.Fatalf("normalization is not idempotent: changed=%v", changedAgain)
	}

	if got[runIndex].Function.Name != original[runIndex].Function.Name || got[otherIndex].Function.Name != original[otherIndex].Function.Name {
		t.Fatalf("tool ordering changed: original=%q/%q normalized=%q/%q", original[0].Function.Name, original[1].Function.Name, got[0].Function.Name, got[1].Function.Name)
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(got[runIndex].Function.Parameters.(json.RawMessage), &schema); err != nil {
		t.Fatal(err)
	}
	var defs, properties map[string]json.RawMessage
	if err := json.Unmarshal(schema["$defs"], &defs); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(schema["properties"], &properties); err != nil {
		t.Fatal(err)
	}
	var workflow map[string]json.RawMessage
	if err := json.Unmarshal(defs["Workflow"], &workflow); err != nil {
		t.Fatal(err)
	}
	var workflowProps map[string]json.RawMessage
	if err := json.Unmarshal(workflow["properties"], &workflowProps); err != nil {
		t.Fatal(err)
	}
	var name, near map[string]json.RawMessage
	if err := json.Unmarshal(workflowProps["name"], &name); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(workflowProps["near_match"], &near); err != nil {
		t.Fatal(err)
	}
	var workflowRef, note map[string]json.RawMessage
	if err := json.Unmarshal(properties["workflow"], &workflowRef); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(properties["note"], &note); err != nil {
		t.Fatal(err)
	}
	var gotPattern, noteDefault, nearPattern string
	if err := json.Unmarshal(name["pattern"], &gotPattern); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(note["default"], &noteDefault); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(near["pattern"], &nearPattern); err != nil {
		t.Fatal(err)
	}
	var reference string
	if err := json.Unmarshal(workflowRef["$ref"], &reference); err != nil {
		t.Fatal(err)
	}
	if gotPattern != openAIWorkflowNamePattern {
		t.Fatalf("run_workflow pattern = %q, want explicit Unicode pattern %q", gotPattern, openAIWorkflowNamePattern)
	}
	if noteDefault != observedWorkflowNamePattern || nearPattern == openAIWorkflowNamePattern || reference != "#/$defs/Workflow" {
		t.Fatalf("unexpected unrelated schema edit: note=%q near=%q ref=%q", noteDefault, nearPattern, reference)
	}
	if !strings.Contains(string(first), "9007199254740993") {
		t.Fatalf("numeric schema value lost precision: %s", first)
	}

	otherParameters := got[otherIndex].Function.Parameters.(json.RawMessage)
	if string(otherParameters) != string(originalOtherParameters) {
		t.Fatalf("non-run_workflow schema changed: got=%s want=%s", otherParameters, originalOtherParameters)
	}
	var otherSchema struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(otherParameters, &otherSchema); err != nil {
		t.Fatal(err)
	}
	if otherSchema.Pattern != observedWorkflowNamePattern {
		t.Fatalf("non-run_workflow pattern was modified: %q", otherSchema.Pattern)
	}
}

func TestOpenAIResponsesWorkflowNamePatternMatchesUnicodeContract(t *testing.T) {
	compiled, err := regexp.Compile(`^(?:` + openAIWorkflowNamePattern + `)\z`)
	if err != nil {
		t.Fatalf("normalized pattern does not compile with Go regexp: %v", err)
	}
	for value := rune(0); value <= unicode.MaxRune; value++ {
		if value >= 0xD800 && value <= 0xDFFF {
			continue // Surrogates are not Unicode scalar values.
		}
		want := !unicode.IsControl(value) && value != 0x2028 && value != 0x2029 && value != '"'
		if got := compiled.MatchString(string(value)); got != want {
			t.Fatalf("pattern match for U+%04X = %v, want %v", value, got, want)
		}
	}
}

func TestOpenAIResponsesConversionUsesNormalizedToolsForBodyAndCacheAudit(t *testing.T) {
	raw := []json.RawMessage{observedWorkflowSchema}
	conv := &ParsedConversation{
		ModelID: "gpt-5.6", SystemText: "stable system prompt", CurrentContent: "continue",
		RawTools: raw, ToolsHash: hashTools(raw),
	}
	for _, continuation := range []bool{false, true} {
		name := "ordinary"
		if continuation {
			name = "continuation"
		}
		t.Run(name, func(t *testing.T) {
			body, err := KiroToOpenAIResponsesUpstream(conv, "gpt-5.6", "", continuation)
			if err != nil {
				t.Fatal(err)
			}
			var request struct {
				Tools          []json.RawMessage `json:"tools"`
				PromptCacheKey string            `json:"prompt_cache_key"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatal(err)
			}
			if len(request.Tools) != 1 {
				t.Fatalf("unexpected final tool count: %d", len(request.Tools))
			}
			var outboundTool struct {
				Type        string          `json:"type"`
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			}
			if err := json.Unmarshal(request.Tools[0], &outboundTool); err != nil {
				t.Fatal(err)
			}
			// 保留最终请求中的原始 schema 数字，避免 interface{} 解码为 float64。
			outboundTools := []OpenAITool{{
				Type: outboundTool.Type,
				Function: OpenAIFuncDef{
					Name: outboundTool.Name, Description: outboundTool.Description, Parameters: outboundTool.Parameters,
				},
			}}
			var finalSchema map[string]json.RawMessage
			if err := json.Unmarshal(outboundTool.Parameters, &finalSchema); err != nil {
				t.Fatal(err)
			}
			var defs map[string]json.RawMessage
			if err := json.Unmarshal(finalSchema["$defs"], &defs); err != nil {
				t.Fatal(err)
			}
			var workflow map[string]json.RawMessage
			if err := json.Unmarshal(defs["Workflow"], &workflow); err != nil {
				t.Fatal(err)
			}
			var properties map[string]json.RawMessage
			if err := json.Unmarshal(workflow["properties"], &properties); err != nil {
				t.Fatal(err)
			}
			var nameSchema map[string]json.RawMessage
			if err := json.Unmarshal(properties["name"], &nameSchema); err != nil {
				t.Fatal(err)
			}
			var pattern string
			if err := json.Unmarshal(nameSchema["pattern"], &pattern); err != nil {
				t.Fatal(err)
			}
			if pattern != openAIWorkflowNamePattern {
				t.Fatalf("final outbound pattern = %q", pattern)
			}

			hash, report := CachedOpenAIResponsesToolAudit(conv.ToolsHash, raw)
			bodyToolsHash := hashOpenAIResponsesTools(outboundTools)
			if report.ProviderTools != len(request.Tools) || hash != bodyToolsHash {
				t.Fatalf("cache audit differs from final provider-visible schema: report=%+v audit=%q body=%q", report, hash, bodyToolsHash)
			}
			wantKey := PromptCacheKeyForAuditWithProviderToolsHash(conv, "gpt-5.6", "responses", hash)
			if request.PromptCacheKey != wantKey {
				t.Fatalf("prompt_cache_key = %q, want %q (hash %q)", request.PromptCacheKey, wantKey, hash)
			}
		})
	}

	legacy, err := KiroToResponses(conv, "gpt-5.6", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(legacy), `\\p{Cc}`) {
		t.Fatalf("legacy converter unexpectedly changed its schema behavior: %s", legacy)
	}
}
