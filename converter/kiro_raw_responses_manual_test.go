package converter

import (
	"bytes"
	"testing"
)

func TestParseKiroRawResponsesManualRoutingOptionalMetadataAndScope(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		conversation string
		mode         string
		continuation string
		provenance   string
		wantStateKey string
	}{
		{
			name:       "without metadata",
			body:       `{"model":"local-id","input":[]}`,
			provenance: AgentContinuationProvenanceNativeRaw,
		},
		{
			name:         "partial metadata",
			body:         `{"model":"local-id","input":[],"metadata":{"kiro:conversation_id":" conv-1 "}}`,
			conversation: "conv-1",
			provenance:   AgentContinuationProvenanceNativeRaw,
			wantStateKey: "conv-1",
		},
		{
			name:         "complete metadata",
			body:         `{"model":"local-id","input":[],"metadata":{"kiro:conversation_id":"conv-2","kiro:agent_mode":" plan ","kiro:proxy_continuation_id":" branch-2 "}}`,
			conversation: "conv-2",
			mode:         "plan",
			continuation: "branch-2",
			provenance:   AgentContinuationProvenanceProxyBridge,
			wantStateKey: buildContinuationStateKey("conv-2", "plan", "branch-2"),
		},
		{
			name:         "native continuation metadata",
			body:         `{"model":"local-id","input":[],"metadata":{"kiro:conversation_id":"conv-3","kiro:agent_mode":"plan","kiro:agent_continuation_id":"native-3"}}`,
			conversation: "conv-3",
			mode:         "plan",
			continuation: "native-3",
			provenance:   AgentContinuationProvenanceNativeRaw,
			wantStateKey: buildContinuationStateKey("conv-3", "plan", "native-3"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(test.body)
			before := append([]byte(nil), body...)
			conv, err := ParseKiroRawResponsesManualRouting(body)
			if err != nil {
				t.Fatalf("ParseKiroRawResponsesManualRouting: %v", err)
			}
			if conv.ModelID != "local-id" || conv.ConversationID != test.conversation || conv.AgentMode != test.mode || conv.AgentContinuationID != test.continuation || conv.AgentContinuationProvenance != test.provenance {
				t.Fatalf("parsed routing values = %+v", conv)
			}
			if conv.PromptAffinityKey != test.conversation || conv.ContinuationStateKey != test.wantStateKey {
				t.Fatalf("scope keys = %q/%q, want %q/%q", conv.PromptAffinityKey, conv.ContinuationStateKey, test.conversation, test.wantStateKey)
			}
			if conv.CurrentToolContinuation != nil || !bytes.Equal(body, before) {
				t.Fatalf("manual routing inferred continuation or changed body: continuation=%v body=%s", conv.CurrentToolContinuation, body)
			}
		})
	}
}

func TestParseKiroRawResponsesManualRoutingRejectsInvalidInputAndReferences(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty model", body: `{"model":"  ","input":[]}`},
		{name: "auto model", body: `{"model":"auto","input":[]}`},
		{name: "case insensitive auto", body: `{"model":" AuTo ","input":[]}`},
		{name: "model type", body: `{"model":1,"input":[]}`},
		{name: "missing model", body: `{"input":[]}`},
		{name: "input missing", body: `{"model":"local-id"}`},
		{name: "input not array", body: `{"model":"local-id","input":"hello"}`},
		{name: "nonempty previous response", body: `{"model":"local-id","input":[],"previous_response_id":"resp-1"}`},
		{name: "wrong previous response type", body: `{"model":"local-id","input":[],"previous_response_id":null}`},
		{name: "conversation reference", body: `{"model":"local-id","input":[],"conversation":null}`},
		{name: "metadata not object", body: `{"model":"local-id","input":[],"metadata":[]}`},
		{name: "bad conversation metadata", body: `{"model":"local-id","input":[],"metadata":{"kiro:conversation_id":1}}`},
		{name: "bad agent mode metadata", body: `{"model":"local-id","input":[],"metadata":{"kiro:agent_mode":false}}`},
		{name: "bad continuation metadata", body: `{"model":"local-id","input":[],"metadata":{"kiro:proxy_continuation_id":null}}`},
		{name: "bad native continuation metadata", body: `{"model":"local-id","input":[],"metadata":{"kiro:agent_continuation_id":false}}`},
		{name: "conflicting continuation metadata", body: `{"model":"local-id","input":[],"metadata":{"kiro:agent_continuation_id":"native","kiro:proxy_continuation_id":"proxy"}}`},
		{name: "bad native continuation metadata", body: `{"model":"local-id","input":[],"metadata":{"kiro:agent_continuation_id":false}}`},
		{name: "conflicting continuation metadata", body: `{"model":"local-id","input":[],"metadata":{"kiro:agent_continuation_id":"native","kiro:proxy_continuation_id":"proxy"}}`},
		{name: "duplicate key", body: `{"model":"local-id","model":"local-id","input":[]}`},
		{name: "trailing object", body: `{"model":"local-id","input":[]} {}`},
		{name: "invalid effort", body: `{"model":"local-id","input":[],"reasoning":{"effort":"ultra"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if conv, err := ParseKiroRawResponsesManualRouting([]byte(test.body)); err == nil || conv != nil {
				t.Fatalf("expected error, got conversation=%+v err=%v", conv, err)
			}
		})
	}
}

func TestParseKiroRawResponsesManualRoutingEffortAndImages(t *testing.T) {
	body := []byte(`{"model":"local-id","input":[{"type":"message","content":[{"type":"input_image","image_url":" data:image/png;base64,abc "}]}],"reasoning":{"effort":" HIGH "}}`)
	before := append([]byte(nil), body...)
	conv, err := ParseKiroRawResponsesManualRouting(body)
	if err != nil {
		t.Fatalf("ParseKiroRawResponsesManualRouting: %v", err)
	}
	if conv.ReasoningEffort != "high" || !conv.RawResponsesHasImages {
		t.Fatalf("effort/image = %q/%t", conv.ReasoningEffort, conv.RawResponsesHasImages)
	}
	if !bytes.Equal(body, before) {
		t.Fatal("parser modified the raw request body")
	}
	if _, err := ParseKiroRawResponsesManualRouting([]byte(`{"model":"local-id","input":[],"previous_response_id":" "}`)); err != nil {
		t.Fatalf("empty previous_response_id should be allowed: %v", err)
	}
}
