package converter

import (
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

func TestGeminiMixedToolResultUsesStructuredEnvelope(t *testing.T) {
	conv := &ParsedConversation{
		ModelID: "gemini-2.5-pro",
		CurrentToolResults: []ParsedToolResult{{
			ToolUseID: "call_mixed",
			Content:   "hello",
			Blocks: []ParsedToolResultBlock{
				{Kind: "text", Text: "hello"},
				{Kind: "json", Raw: json.RawMessage(`{"type":"json","value":{"answer":42}}`)},
			},
		}},
	}

	body, err := KiroToGemini(conv, "gemini-2.5-pro", "")
	if err != nil {
		t.Fatalf("convert Gemini request: %v", err)
	}
	var req GeminiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode Gemini request: %v", err)
	}
	if len(req.Contents) != 1 || len(req.Contents[0].Parts) != 1 || req.Contents[0].Parts[0].FunctionResponse == nil {
		t.Fatalf("unexpected Gemini function response: %+v", req.Contents)
	}
	response, ok := req.Contents[0].Parts[0].FunctionResponse.Response.(map[string]interface{})
	if !ok {
		t.Fatalf("Gemini response has unexpected type: %T", req.Contents[0].Parts[0].FunctionResponse.Response)
	}
	result, ok := response["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("mixed result was encoded as a string: %#v", response["result"])
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal mixed result: %v", err)
	}
	if !strings.Contains(string(encodedResult), `"answer":42`) {
		t.Fatalf("mixed result lost nested JSON: %#v", result)
	}
}
