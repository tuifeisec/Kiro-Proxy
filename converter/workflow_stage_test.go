package converter

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func workflowStageHeader(stage string) string {
	value := map[string]interface{}{
		"version":             1,
		"stage":               stage,
		"conversationId":      " conv-1 ",
		"agentMode":           " CODE ",
		"agentContinuationId": " continued ",
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func workflowStageConversation() *ParsedConversation {
	return &ParsedConversation{
		ModelID:             "model",
		ConversationID:      "conv-1",
		AgentMode:           "code",
		AgentContinuationID: "continued",
		SystemText:          "stable rules",
		CurrentContent:      "hello",
		MaxTokens:           100,
	}
}

func rewriteWorkflowStageHeader(t *testing.T, header, field string, value interface{}) string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(header), &fields); err != nil {
		t.Fatalf("decode test header: %v", err)
	}
	if value == nil {
		delete(fields, field)
	} else {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode test field: %v", err)
		}
		fields[field] = encoded
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode test header: %v", err)
	}
	return string(encoded)
}

func TestApplyWorkflowStageHeaderAcceptsStagesAndUnknownValues(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "run", want: "run"},
		{input: "plan", want: "plan"},
		{input: "implement", want: "implement"},
		{input: "review", want: "review"},
		{input: "finalize", want: "finalize"},
		{input: "  FUTURE_STAGE  ", want: "future_stage"},
		{input: strings.Repeat("A", 128), want: strings.Repeat("a", 128)},
	} {
		t.Run(test.input, func(t *testing.T) {
			conv := workflowStageConversation()
			if err := ApplyWorkflowStageHeader(conv, workflowStageHeader(test.input)); err != nil {
				t.Fatalf("ApplyWorkflowStageHeader: %v", err)
			}
			if conv.WorkflowStage != test.want || conv.WorkflowStageSource != "request_header" || !conv.WorkflowStageProvided {
				t.Fatalf("workflow metadata = (%q, %q, %t), want (%q, request_header, true)", conv.WorkflowStage, conv.WorkflowStageSource, conv.WorkflowStageProvided, test.want)
			}
			if conv.WorkflowID != "" {
				t.Fatalf("optional workflowId = %q, want empty", conv.WorkflowID)
			}
		})
	}

	conv := workflowStageConversation()
	header := strings.TrimSuffix(workflowStageHeader("plan"), "}") + `,"workflowId":"workflow-7"}`
	if err := ApplyWorkflowStageHeader(conv, header); err != nil {
		t.Fatalf("ApplyWorkflowStageHeader with workflowId: %v", err)
	}
	if conv.WorkflowID != "workflow-7" {
		t.Fatalf("workflowId = %q, want workflow-7", conv.WorkflowID)
	}
}

func TestApplyWorkflowStageHeaderRejectsConversationBindings(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
	}{
		{name: "conversation id is case sensitive", field: "conversationId", value: `"CONV-1"`},
		{name: "empty conversation id", field: "conversationId", value: `" "`},
		{name: "agent mode differs", field: "agentMode", value: `"plan"`},
		{name: "continuation differs", field: "agentContinuationId", value: `"other"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var replacement string
			if err := json.Unmarshal([]byte(test.value), &replacement); err != nil {
				t.Fatalf("decode replacement: %v", err)
			}
			header := rewriteWorkflowStageHeader(t, workflowStageHeader("run"), test.field, replacement)

			conv := workflowStageConversation()
			before := *conv
			if err := ApplyWorkflowStageHeader(conv, header); err == nil {
				t.Fatal("expected binding mismatch")
			}
			if !reflect.DeepEqual(*conv, before) {
				t.Fatal("conversation was partially modified after a binding error")
			}
		})
	}
}

func TestApplyWorkflowStageHeaderAllowsEmptyAgentMetadataWhenBound(t *testing.T) {
	conv := workflowStageConversation()
	conv.AgentMode = ""
	conv.AgentContinuationID = ""
	header := rewriteWorkflowStageHeader(t, workflowStageHeader("plan"), "agentMode", "")
	header = rewriteWorkflowStageHeader(t, header, "agentContinuationId", "")
	if err := ApplyWorkflowStageHeader(conv, header); err != nil {
		t.Fatalf("ApplyWorkflowStageHeader with matching empty agent metadata: %v", err)
	}
	if conv.WorkflowStage != "plan" || !conv.WorkflowStageProvided {
		t.Fatalf("workflow metadata was not recorded: %+v", conv)
	}
}

func TestApplyWorkflowStageHeaderRejectsUnboundConversationID(t *testing.T) {
	conv := workflowStageConversation()
	conv.ConversationID = " \t"
	header := rewriteWorkflowStageHeader(t, workflowStageHeader("plan"), "conversationId", " \t")
	before := *conv
	if err := ApplyWorkflowStageHeader(conv, header); err == nil {
		t.Fatal("expected an error for an empty bound conversation ID")
	}
	if !reflect.DeepEqual(*conv, before) {
		t.Fatal("conversation was modified after an empty binding error")
	}
}

func TestApplyWorkflowStageHeaderRejectsInvalidValuesWithoutLeakingHeader(t *testing.T) {
	valid := workflowStageHeader("run")
	unknownKey := rewriteWorkflowStageHeader(t, valid, "secret-sentinel", "x")
	wrongType := rewriteWorkflowStageHeader(t, valid, "stage", 7)
	duplicateKey := strings.TrimSuffix(valid, "}") + `,"stage":"plan"}`
	tests := []struct {
		name  string
		value string
	}{
		{name: "malformed json", value: `{"version":`},
		{name: "invalid utf8", value: string([]byte{'"', 0xff, '"'})},
		{name: "non object", value: `[]`},
		{name: "unknown key", value: unknownKey},
		{name: "duplicate key", value: duplicateKey},
		{name: "multiple json values", value: valid + ` {}`},
		{name: "wrong field type", value: wrongType},
		{name: "wrong version", value: rewriteWorkflowStageHeader(t, valid, "version", 2)},
		{name: "missing version", value: rewriteWorkflowStageHeader(t, valid, "version", nil)},
		{name: "empty stage", value: workflowStageHeader("  ")},
		{name: "unsafe stage control character", value: workflowStageHeader("run\x01review")},
		{name: "stage too long", value: workflowStageHeader(strings.Repeat("a", 129))},
		{name: "oversized", value: strings.Repeat("x", maxWorkflowStageHeaderBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conv := workflowStageConversation()
			conv.WorkflowStage = "existing"
			conv.WorkflowStageSource = "existing-source"
			conv.WorkflowStageProvided = true
			conv.WorkflowID = "existing-id"
			before := *conv
			err := ApplyWorkflowStageHeader(conv, test.value)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !reflect.DeepEqual(*conv, before) {
				t.Fatal("conversation was partially modified after a validation error")
			}
			if strings.Contains(err.Error(), "secret-sentinel") || strings.Contains(err.Error(), test.value) {
				t.Fatal("error leaked header contents")
			}
		})
	}
}

func TestApplyWorkflowStageHeaderEmptyValueHasNoSideEffect(t *testing.T) {
	if err := ApplyWorkflowStageHeader(nil, ""); err != nil {
		t.Fatalf("empty header with nil conversation: %v", err)
	}
	conv := workflowStageConversation()
	before := *conv
	if err := ApplyWorkflowStageHeader(conv, ""); err != nil {
		t.Fatalf("empty header: %v", err)
	}
	if !reflect.DeepEqual(*conv, before) {
		t.Fatal("empty header modified the conversation")
	}
}

func TestWorkflowMetadataDoesNotAffectProviderBodiesOrCacheKey(t *testing.T) {
	plain := workflowStageConversation()
	withMetadata := workflowStageConversation()
	header := workflowStageHeader("review")
	if err := ApplyWorkflowStageHeader(withMetadata, header); err != nil {
		t.Fatalf("ApplyWorkflowStageHeader: %v", err)
	}

	plainResponses, err := KiroToResponses(plain, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("convert plain Responses request: %v", err)
	}
	metadataResponses, err := KiroToResponses(withMetadata, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("convert metadata Responses request: %v", err)
	}
	if !reflect.DeepEqual(metadataResponses, plainResponses) {
		t.Fatal("workflow metadata changed the Responses provider body")
	}

	plainOpenAI, err := KiroToOpenAI(plain, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("convert plain OpenAI request: %v", err)
	}
	metadataOpenAI, err := KiroToOpenAI(withMetadata, "gpt-5.5", "")
	if err != nil {
		t.Fatalf("convert metadata OpenAI request: %v", err)
	}
	if !reflect.DeepEqual(metadataOpenAI, plainOpenAI) {
		t.Fatal("workflow metadata changed the OpenAI provider body")
	}

	for _, api := range []string{"responses", "openai"} {
		plainKey := PromptCacheKeyForAudit(plain, "gpt-5.5", api)
		metadataKey := PromptCacheKeyForAudit(withMetadata, "gpt-5.5", api)
		if metadataKey != plainKey {
			t.Fatalf("workflow metadata changed %s cache key: %q != %q", api, metadataKey, plainKey)
		}
	}
}
