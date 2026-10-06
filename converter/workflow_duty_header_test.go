package converter

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func workflowDutyHeaderForTest(duty string) string {
	value := map[string]interface{}{
		"version": 2, "conversationId": "conv-1", "agentMode": "code", "agentContinuationId": "continued",
		"workflowId": "workflow-1", "nodeId": "investigate", "definitionId": "bundled://investigate",
		"definitionRevision": "revision-1", "nodePath": "steps/investigate", "taskType": duty, "executionId": "step-session-1",
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestWorkflowDutyHeaderKeepsNodeAndDeclaredDutySeparate(t *testing.T) {
	for _, duty := range []string{"investigation", "design", "future_duty", ""} {
		conv := workflowStageConversation()
		if err := ApplyWorkflowStageHeader(conv, workflowDutyHeaderForTest(duty)); err != nil {
			t.Fatal(err)
		}
		if conv.WorkflowMetadataVersion != 2 || conv.WorkflowStage != "" || !conv.WorkflowStageProvided ||
			conv.WorkflowNodeID != "investigate" || conv.WorkflowTaskType != duty || conv.WorkflowExecutionID != "step-session-1" ||
			conv.WorkflowDefinitionID != "bundled://investigate" || conv.WorkflowDefinitionRevision != "revision-1" ||
			conv.WorkflowNodePath != "steps/investigate" {
			t.Fatalf("node identity or declaration was conflated: %+v", conv)
		}
	}
}

func TestWorkflowDutyHeaderAllowsExplicitUnknownWithoutInventingRun(t *testing.T) {
	header := workflowDutyHeaderForTest("")
	for _, key := range []string{"nodeId", "definitionId", "definitionRevision", "nodePath", "executionId"} {
		header = rewriteWorkflowStageHeader(t, header, key, "")
	}
	conv := workflowStageConversation()
	if err := ApplyWorkflowStageHeader(conv, header); err != nil {
		t.Fatal(err)
	}
	if conv.WorkflowNodeID != "" || conv.WorkflowTaskType != "" || conv.WorkflowExecutionID != "" || conv.WorkflowStage != "" {
		t.Fatalf("missing identity was invented: %+v", conv)
	}
}

func TestWorkflowDutyHeaderRejectsMalformedMetadataAtomically(t *testing.T) {
	valid := workflowDutyHeaderForTest("investigation")
	for _, header := range []string{
		rewriteWorkflowStageHeader(t, valid, "executionId", nil),
		rewriteWorkflowStageHeader(t, valid, "taskType", 7),
		rewriteWorkflowStageHeader(t, valid, "version", 3),
		rewriteWorkflowStageHeader(t, valid, "stage", "run"),
		rewriteWorkflowStageHeader(t, valid, "nodePath", "steps\nrun"),
		rewriteWorkflowStageHeader(t, valid, "nodePath", " steps/investigate"),
		rewriteWorkflowStageHeader(t, valid, "definitionId", "bundled://investigate "),
		rewriteWorkflowStageHeader(t, valid, "definitionRevision", " revision-1"),
		rewriteWorkflowStageHeader(t, valid, "executionId", " step-session-1"),
		rewriteWorkflowStageHeader(t, valid, "definitionId", strings.Repeat("x", 1025)),
		rewriteWorkflowStageHeader(t, valid, "conversationId", "other-conversation"),
		strings.TrimSuffix(valid, "}") + `,"nodeId":"duplicate"}`,
	} {
		conv := workflowStageConversation()
		before := *conv
		if err := ApplyWorkflowStageHeader(conv, header); err == nil {
			t.Fatal("invalid metadata accepted")
		}
		if !reflect.DeepEqual(*conv, before) {
			t.Fatal("metadata error partially changed the conversation")
		}
	}
}

func TestWorkflowDutyMetadataDoesNotChangeProviderPromptOrCacheKey(t *testing.T) {
	first, second := workflowStageConversation(), workflowStageConversation()
	if err := ApplyWorkflowStageHeader(first, workflowDutyHeaderForTest("investigation")); err != nil {
		t.Fatal(err)
	}
	header := rewriteWorkflowStageHeader(t, workflowDutyHeaderForTest("design"), "executionId", "other-session")
	header = rewriteWorkflowStageHeader(t, header, "nodeId", "design")
	if err := ApplyWorkflowStageHeader(second, header); err != nil {
		t.Fatal(err)
	}
	firstBody, err := KiroToResponses(first, "gpt-test", "high")
	if err != nil {
		t.Fatal(err)
	}
	secondBody, err := KiroToResponses(second, "gpt-test", "high")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBody, secondBody) {
		t.Fatal("private routing metadata altered provider-visible input or cache key")
	}
}
