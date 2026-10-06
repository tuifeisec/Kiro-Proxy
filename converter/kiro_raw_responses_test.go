package converter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func rawResponsesRoutingBody(model, currentToolResults, input string, extras string) []byte {
	metadata := `{"kiro:conversation_id":"conv-raw","kiro:agent_mode":" vibe ","kiro:proxy_continuation_id":" branch-1 ","kiro:proxy_current_tool_results":` + currentToolResults + `}`
	return rawResponsesRoutingBodyWithMetadata(model, input, metadata, extras)
}

func rawResponsesRoutingBodyWithMetadata(model, input, metadata, extras string) []byte {
	modelJSON, _ := json.Marshal(model)
	return []byte(`{"model":` + string(modelJSON) + `,"input":` + input + `,"metadata":` + metadata + extras + `}`)
}

func TestParseKiroRawResponsesRoutingExtractsBoundMetadata(t *testing.T) {
	body := rawResponsesRoutingBody("auto", "1", `[{"type":"function_call_output","call_id":"call-1","output":"ok"}]`, `,"reasoning":{"effort":" HIGH "}`)
	before := append([]byte(nil), body...)
	conv, err := ParseKiroRawResponsesRouting(body)
	if err != nil {
		t.Fatalf("ParseKiroRawResponsesRouting: %v", err)
	}
	if conv.ModelID != "auto" || conv.ConversationID != "conv-raw" || conv.AgentMode != "vibe" || conv.AgentContinuationID != "branch-1" {
		t.Fatalf("routing metadata = %+v", conv)
	}
	if conv.ContinuationStateKey != buildContinuationStateKey("conv-raw", "vibe", "branch-1") || conv.PromptAffinityKey != "conv-raw" {
		t.Fatalf("conversation identity was not derived using existing rules: %+v", conv)
	}
	if conv.CurrentToolContinuation == nil || !*conv.CurrentToolContinuation || len(conv.CurrentToolResults) != 0 {
		t.Fatalf("raw current-tool marker/results = %v/%+v", conv.CurrentToolContinuation, conv.CurrentToolResults)
	}
	if conv.ReasoningEffort != "high" || conv.RawResponsesHasImages {
		t.Fatalf("effort/image metadata = %q/%t", conv.ReasoningEffort, conv.RawResponsesHasImages)
	}
	if !bytes.Equal(body, before) {
		t.Fatal("routing parser modified the raw request body")
	}
}

func TestParseKiroRawResponsesRoutingRequiresCompleteMetadataAndSingleObject(t *testing.T) {
	valid := rawResponsesRoutingBody("auto", "0", `[]`, "")
	tests := []struct {
		name string
		body []byte
	}{
		{name: "missing model", body: []byte(`{"input":[],"metadata":{}}`)},
		{name: "wrong model type", body: []byte(`{"model":1,"input":[],"metadata":{}}`)},
		{name: "metadata missing", body: []byte(`{"model":"auto","input":[]}`)},
		{name: "metadata non object", body: []byte(`{"model":"auto","input":[],"metadata":[]}`)},
		{name: "missing conversation", body: rawResponsesRoutingBodyWithMetadata("auto", `[]`, `{"kiro:agent_mode":"vibe","kiro:proxy_continuation_id":"","kiro:proxy_current_tool_results":0}`, "")},
		{name: "empty conversation", body: rawResponsesRoutingBodyWithMetadata("auto", `[]`, `{"kiro:conversation_id":"","kiro:agent_mode":"","kiro:proxy_continuation_id":"","kiro:proxy_current_tool_results":0}`, "")},
		{name: "whitespace conversation", body: rawResponsesRoutingBodyWithMetadata("auto", `[]`, `{"kiro:conversation_id":" \t ","kiro:agent_mode":"","kiro:proxy_continuation_id":"","kiro:proxy_current_tool_results":0}`, "")},
		{name: "wrong conversation type", body: rawResponsesRoutingBodyWithMetadata("auto", `[]`, `{"kiro:conversation_id":7,"kiro:agent_mode":"","kiro:proxy_continuation_id":"","kiro:proxy_current_tool_results":0}`, "")},
		{name: "missing agent mode", body: rawResponsesRoutingBodyWithMetadata("auto", `[]`, `{"kiro:conversation_id":"conv-raw","kiro:proxy_continuation_id":"","kiro:proxy_current_tool_results":0}`, "")},
		{name: "wrong agent mode type", body: rawResponsesRoutingBodyWithMetadata("auto", `[]`, `{"kiro:conversation_id":"conv-raw","kiro:agent_mode":false,"kiro:proxy_continuation_id":"","kiro:proxy_current_tool_results":0}`, "")},
		{name: "missing continuation", body: rawResponsesRoutingBodyWithMetadata("auto", `[]`, `{"kiro:conversation_id":"conv-raw","kiro:agent_mode":"vibe","kiro:proxy_current_tool_results":0}`, "")},
		{name: "wrong continuation type", body: rawResponsesRoutingBodyWithMetadata("auto", `[]`, `{"kiro:conversation_id":"conv-raw","kiro:agent_mode":"vibe","kiro:proxy_continuation_id":7,"kiro:proxy_current_tool_results":0}`, "")},
		{name: "missing current tool count", body: rawResponsesRoutingBodyWithMetadata("auto", `[]`, `{"kiro:conversation_id":"conv-raw","kiro:agent_mode":"vibe","kiro:proxy_continuation_id":""}`, "")},
		{name: "wrong count type", body: rawResponsesRoutingBody("auto", `1.0`, `[]`, "")},
		{name: "negative count", body: rawResponsesRoutingBody("auto", `-1`, `[]`, "")},
		{name: "input not array", body: rawResponsesRoutingBody("auto", "0", `"text"`, "")},
		{name: "duplicate top-level key", body: append([]byte(nil), `{"model":"auto","model":"auto","input":[],"metadata":{}}`...)},
		{name: "trailing JSON", body: append(append([]byte(nil), valid...), []byte(` {}`)...)},
		{name: "malformed JSON", body: []byte(`{"model":"auto","input":[],"metadata":`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if conv, err := ParseKiroRawResponsesRouting(test.body); err == nil || conv != nil {
				t.Fatalf("expected parse failure, got conversation=%+v err=%v", conv, err)
			}
		})
	}
}

func TestParseKiroRequestLeavesRawResponsesMarkersUnset(t *testing.T) {
	body, err := json.Marshal(KiroRequest{
		AgentMode: "vibe",
		ConversationState: KiroConversationState{
			ConversationID: "kiro-conv",
			CurrentMessage: KiroCurrentMsg{UserInputMessage: KiroUserInputMsg{ModelID: "auto", Content: "hello"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := ParseKiroRequest(body)
	if err != nil {
		t.Fatalf("ParseKiroRequest: %v", err)
	}
	if conv.CurrentToolContinuation != nil || conv.RawResponsesHasImages {
		t.Fatalf("Kiro protocol populated raw-only fields: %+v", conv)
	}
}

func TestParseKiroRawResponsesRoutingCountsOnlyCurrentToolResults(t *testing.T) {
	input := `[{"type":"function_call_output","call_id":"old","output":"history"},{"type":"function_call_output","call_id":"new","output":"current"}]`
	conv, err := ParseKiroRawResponsesRouting(rawResponsesRoutingBody("auto", "0", input, ""))
	if err != nil {
		t.Fatalf("ParseKiroRawResponsesRouting historical outputs: %v", err)
	}
	if conv.CurrentToolContinuation == nil || *conv.CurrentToolContinuation || len(conv.CurrentToolResults) != 0 {
		t.Fatalf("historical outputs incorrectly created a current-tool continuation: %v/%+v", conv.CurrentToolContinuation, conv.CurrentToolResults)
	}
	if _, err := ParseKiroRawResponsesRouting(rawResponsesRoutingBody("auto", "3", input, "")); err == nil {
		t.Fatal("count greater than actual input function_call_output items was accepted")
	}
	if _, err := ParseKiroRawResponsesRouting(rawResponsesRoutingBody("auto", "9007199254740993", input, "")); err == nil {
		t.Fatal("large exact current-tool count was rounded or accepted despite exceeding input count")
	}
}

func TestParseKiroRawResponsesRoutingDetectsStructuredInputImages(t *testing.T) {
	input := `[
		{"type":"input_image","image_url":"https://example.invalid/image.png"},
		{"type":"message","content":[{"type":"input_text","text":"plain"},{"type":"input_image","image_url":" data:image/png;base64,abc "}]},
		{"type":"function_call","arguments":"{\"type\":\"input_image\",\"image_url\":\"not-a-structured-image\"}"},
		{"type":"message","content":"the words input_image and image_url in this text are not structure"}
	]`
	conv, err := ParseKiroRawResponsesRouting(rawResponsesRoutingBody("auto", "0", input, ""))
	if err != nil {
		t.Fatalf("ParseKiroRawResponsesRouting images: %v", err)
	}
	if !conv.RawResponsesHasImages {
		t.Fatal("structured input_image was not detected")
	}
	textOnly := `[{"type":"message","content":"input_image image_url"},{"type":"function_call","arguments":"input_image"}]`
	conv, err = ParseKiroRawResponsesRouting(rawResponsesRoutingBody("auto", "0", textOnly, ""))
	if err != nil || conv.RawResponsesHasImages {
		t.Fatalf("plain text/tool arguments were treated as structured images: conv=%+v err=%v", conv, err)
	}
	badImageURL := `[{"type":"input_image","image_url":{}}]`
	if _, err := ParseKiroRawResponsesRouting(rawResponsesRoutingBody("auto", "0", badImageURL, "")); err == nil {
		t.Fatal("malformed structured image URL was silently ignored")
	}
}

func TestParseKiroRawResponsesRoutingDetectsRoleOnlyMessagesAndFileImages(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "role-only user message with input_image",
			input:    `[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,abc"}]}]`,
			expected: true,
		},
		{
			name:     "role-only assistant message with file image",
			input:    `[{"role":"assistant","content":[{"type":"input_image","file_id":"file-1"}]}]`,
			expected: true,
		},
		{
			name:     "explicit empty image_url suppresses file_id fallback",
			input:    `[{"role":"user","content":[{"type":"input_image","image_url":"","file_id":"file-1"}]}]`,
			expected: false,
		},
		{
			name:     "other role is not a message",
			input:    `[{"role":"tool","content":[{"type":"input_image","file_id":"file-1"}]}]`,
			expected: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conv, err := ParseKiroRawResponsesRouting(rawResponsesRoutingBody("auto", "0", test.input, ""))
			if err != nil {
				t.Fatalf("ParseKiroRawResponsesRouting: %v", err)
			}
			if conv.RawResponsesHasImages != test.expected {
				t.Fatalf("RawResponsesHasImages = %t, want %t", conv.RawResponsesHasImages, test.expected)
			}
		})
	}
}

func TestParseKiroRawResponsesRoutingNormalizesEffortAndPreservesRawNumbers(t *testing.T) {
	body := []byte(`{"model":"auto","input":[],"metadata":{"kiro:conversation_id":"conv-raw","kiro:agent_mode":"","kiro:proxy_continuation_id":"","kiro:proxy_current_tool_results":0,"extension":{"precise":900719925474099312345678901234567890}},"reasoning":{"effort":"xhigh","extension":0.123456789012345678901234567890},"extension":{"integer":900719925474099312345678901234567890}}`)
	before := append([]byte(nil), body...)
	conv, err := ParseKiroRawResponsesRouting(body)
	if err != nil {
		t.Fatalf("ParseKiroRawResponsesRouting: %v", err)
	}
	if conv.ReasoningEffort != "xhigh" || conv.AgentMode != "" || conv.AgentContinuationID != "" {
		t.Fatalf("normalized metadata = %+v", conv)
	}
	if !bytes.Equal(body, before) {
		t.Fatal("raw number precision or unknown extensions were changed")
	}
	if _, err := ParseKiroRawResponsesRouting(rawResponsesRoutingBody("auto", "0", `[]`, `,"reasoning":{"effort":"ultra"}`)); err == nil {
		t.Fatal("unsupported effort was silently downgraded")
	}
}

func TestParseKiroRawResponsesRoutingRejectsServerSideReferences(t *testing.T) {
	base := rawResponsesRoutingBody("auto", "0", `[]`, "")
	previousResponse := strings.TrimSuffix(string(base), "}") + `,"previous_response_id":"resp-1"}`
	if _, err := ParseKiroRawResponsesRouting([]byte(previousResponse)); err == nil {
		t.Fatal("nonempty previous_response_id was accepted")
	}
	conversation := strings.TrimSuffix(string(base), "}") + `,"conversation":null}`
	if _, err := ParseKiroRawResponsesRouting([]byte(conversation)); err == nil {
		t.Fatal("explicit top-level conversation reference was accepted")
	}
	emptyPreviousResponse := strings.TrimSuffix(string(base), "}") + `,"previous_response_id":" "}`
	if _, err := ParseKiroRawResponsesRouting([]byte(emptyPreviousResponse)); err != nil {
		t.Fatalf("empty previous_response_id should be allowed: %v", err)
	}
}
