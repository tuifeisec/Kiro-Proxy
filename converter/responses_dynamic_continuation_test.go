package converter

import (
	json "github.com/goccy/go-json"
	"strings"
	"testing"
)

func TestResponsesDynamicInstructionsAcrossTurns(t *testing.T) {
	first := &ParsedConversation{SystemText: "stable rules\n\nHere is the last user message:\nfirst", CurrentContent: "first"}
	body, err := KiroToResponsesForContinuation(first, "model", "")
	if err != nil {
		t.Fatal(err)
	}
	initial, _ := ApplyResponsesContinuation(body, nil)
	second := &ParsedConversation{SystemText: "stable rules\n\nHere is the last user message:\nsecond", CurrentContent: "second", History: []ParsedHistoryEntry{{UserContent: "first", HasAssistant: true, AssistantContent: "answer"}}}
	body, err = KiroToResponsesForContinuation(second, "model", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyResponsesContinuation(body, &ResponsesContinuationState{ResponseID: "r1", InputJSON: initial.FullInputJSON, ItemCount: initial.InputItemCount})
	if err != nil || !result.UsedContinuation {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var request ResponsesRequest
	if err := json.Unmarshal(result.Body, &request); err != nil {
		t.Fatal(err)
	}
	if request.Instructions != second.SystemText || len(request.Input) != 1 || request.Input[0].Role != "user" || request.Input[0].Content != "second" {
		t.Fatalf("request=%+v", request)
	}
	if strings.Contains(string(result.Body), "first") {
		t.Fatalf("old dynamic tail retained: %s", result.Body)
	}
	if first.SystemText == "" || second.SystemText == "" {
		t.Fatal("conversion mutated conversation")
	}
}

func TestResponsesContinuationPreservesUnknownFields(t *testing.T) {
	body := []byte(`{"model":"m","instructions":"current","input":[{"type":"message","role":"user","content":"a"},{"type":"message","role":"assistant","content":"b"},{"type":"message","role":"user","content":"c"}],"store":true,"stream":true,"vendor_options":{"x":[1,true]}}`)
	result, _ := ApplyResponsesContinuation(body, &ResponsesContinuationState{ResponseID: "r", InputJSON: `[{"type":"message","role":"user","content":"a"}]`})
	if !result.UsedContinuation || !strings.Contains(string(result.Body), `"vendor_options":{"x":[1,true]}`) {
		t.Fatalf("extension lost: %s", result.Body)
	}
	rebuilt, err := RebuildResponsesWithoutContinuation(result.Body, result.FullInputJSON)
	if err != nil || !strings.Contains(string(rebuilt), `"vendor_options":{"x":[1,true]}`) || strings.Contains(string(rebuilt), "previous_response_id") {
		t.Fatalf("rebuild=%s err=%v", rebuilt, err)
	}
}

func TestResponsesDynamicInstructionsToolRoundAndCompaction(t *testing.T) {
	first := &ParsedConversation{SystemText: "rules\nHere is the last user message:\nread", CurrentContent: "read"}
	body, _ := KiroToResponsesForContinuation(first, "m", "")
	initial, _ := ApplyResponsesContinuation(body, nil)
	toolTurn := ParsedHistoryEntry{UserContent: "read", HasAssistant: true, ToolUses: []ParsedToolUse{{ToolUseID: "call-1", Name: "read_file", Input: json.RawMessage(`{}`)}}}
	toolResult := ParsedToolResult{ToolUseID: "call-1", Content: "file contents"}
	second := &ParsedConversation{SystemText: "rules\nHere is the last user message:\nresult", History: []ParsedHistoryEntry{toolTurn}, CurrentToolResults: []ParsedToolResult{toolResult}}
	body, _ = KiroToResponsesForContinuation(second, "m", "")
	next, _ := ApplyResponsesContinuation(body, &ResponsesContinuationState{ResponseID: "r1", InputJSON: initial.FullInputJSON})
	var request ResponsesRequest
	if err := json.Unmarshal(next.Body, &request); err != nil {
		t.Fatal(err)
	}
	if !next.UsedContinuation || len(request.Input) != 1 || request.Input[0].Type != "function_call_output" || request.Input[0].CallID != "call-1" || request.Input[0].Output == nil || *request.Input[0].Output != "file contents" {
		t.Fatalf("tool delta changed: %s", next.Body)
	}
	third := &ParsedConversation{SystemText: "rules\nHere is the last user message:\nnext", History: []ParsedHistoryEntry{toolTurn, {ToolResults: []ParsedToolResult{toolResult}, HasAssistant: true, AssistantContent: "done"}}, CurrentContent: "next"}
	body, _ = KiroToResponsesForContinuation(third, "m", "")
	last, _ := ApplyResponsesContinuation(body, &ResponsesContinuationState{ResponseID: "r2", InputJSON: next.FullInputJSON})
	if !last.UsedContinuation {
		t.Fatalf("third turn failed: %s", last.Body)
	}
	third.History = []ParsedHistoryEntry{{UserContent: "compacted summary"}}
	body, _ = KiroToResponsesForContinuation(third, "m", "")
	compacted, _ := ApplyResponsesContinuation(body, &ResponsesContinuationState{ResponseID: "r2", InputJSON: next.FullInputJSON})
	if compacted.UsedContinuation || string(compacted.Body) != string(body) {
		t.Fatal("compacted history must use complete original input")
	}
}
