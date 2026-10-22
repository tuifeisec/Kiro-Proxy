package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	goccyjson "github.com/goccy/go-json"
	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"
)

func TestAutoDiagnosticKey(t *testing.T) {
	if got := autoDiagnosticKey(""); got != "" {
		t.Fatalf("empty key = %q, want empty", got)
	}
	wantSum := sha256.Sum256([]byte("diagnostic-session"))
	if got, want := autoDiagnosticKey("diagnostic-session"), hex.EncodeToString(wantSum[:]); got != want {
		t.Fatalf("key = %q, want SHA256 %q", got, want)
	}
	if autoDiagnosticIdentifier("valid_tool_2") != "valid_tool_2" {
		t.Fatal("legal ASCII identifier was not preserved")
	}
	if got, want := autoDiagnosticIdentifier("invalid.tool"), "sha256:"+autoDiagnosticKey("invalid.tool"); got != want {
		t.Fatalf("invalid identifier = %q, want hash %q", got, want)
	}
	long := strings.Repeat("a", autoDiagnosticIdentifierByteLimit+1)
	if got, want := autoDiagnosticIdentifier(long), "sha256:"+autoDiagnosticKey(long); got != want {
		t.Fatalf("long identifier = %q, want hash %q", got, want)
	}
	if got := autoDiagnosticMetadataKey("kiro:conversation_id"); got != "kiro:conversation_id" {
		t.Fatalf("Kiro metadata key = %q, want readable protocol key", got)
	}
	for _, invalid := range []string{"bad/path", "bad key", strings.Repeat("k", autoDiagnosticIdentifierByteLimit+1)} {
		if got, want := autoDiagnosticMetadataKey(invalid), "sha256:"+autoDiagnosticKey(invalid); got != want {
			t.Fatalf("invalid metadata key = %q, want hash %q", got, want)
		}
	}
	if got := autoDiagnosticMetadataKey(""); !strings.HasPrefix(got, "sha256:") || len(got) != len("sha256:")+64 {
		t.Fatalf("empty metadata key is not represented by a hash: %q", got)
	}
	if got := autoDiagnosticBounded(strings.Repeat("x", autoDiagnosticMaxFieldBytes+40)); len(got) > autoDiagnosticMaxFieldBytes {
		t.Fatalf("bounded field has %d bytes", len(got))
	}
}

func TestRawResponsesDiagnosticProjectionAllowlist(t *testing.T) {
	body := []byte(`{
		"model":"auto",
		"instructions":"PRIVATE_INSTRUCTIONS_4fe0",
		"input":[
			{"type":"function_call","call_id":"PRIVATE_CALL_ID_4fe0","name":"run_tool","arguments":{"secret":"PRIVATE_ARGUMENT_4fe0"}},
			{"type":"function_call_output","call_id":"PRIVATE_CALL_ID_4fe0","output":{"secret":"PRIVATE_RESULT_4fe0"}},
			{"role":"user","content":[{"type":"input_text","text":"PRIVATE_CONTENT_4fe0"}]},
			{"type":"future_kind","payload":"PRIVATE_UNKNOWN_4fe0"},
			{"type":"message","role":"future_role","content":"PRIVATE_ROLE_CONTENT_4fe0"}
		],
		"tools":[
			{"type":"function","name":"lookup","parameters":{"description":"PRIVATE_SCHEMA_4fe0"}},
			{"type":"function","function":{"name":"wrapped_tool","parameters":{"secret":"PRIVATE_WRAPPED_SCHEMA_4fe0"}}},
			{"toolSpecification":{"name":"kiro_tool","inputSchema":{"json":{"secret":"PRIVATE_KIRO_SCHEMA_4fe0"}}}},
			{"toolSpecification":{"function":{"name":"wrapped_kiro","inputSchema":{"secret":"PRIVATE_KIRO_FUNCTION_SCHEMA_4fe0"}}}},
			{"name":"bad.tool"}
		],
		"metadata":{"kiro:conversation_id":"PRIVATE_METADATA_VALUE_4fe0","custom_key":{"secret":"PRIVATE_NESTED_METADATA_4fe0"},"bad/path":"PRIVATE_BAD_KEY_VALUE_4fe0"}
	}`)

	before := append([]byte(nil), body...)
	summary, err := projectRawResponsesDiagnostic(body)
	if err != nil {
		t.Fatalf("projectRawResponsesDiagnostic: %v", err)
	}
	if got := summary["input_scope"]; got != "complete_input_history" {
		t.Fatalf("input_scope = %v", got)
	}
	if got := summary["input_item_count"]; got != 5 {
		t.Fatalf("input_item_count = %v, want 5", got)
	}
	types := summary["input_type_counts"].(map[string]int)
	if types["function_call"] != 1 || types["function_call_output"] != 1 || types["message"] != 2 || types["other"] != 1 {
		t.Fatalf("input_type_counts = %#v", types)
	}
	roles := summary["input_role_counts"].(map[string]int)
	if roles["user"] != 1 || roles["other"] != 1 {
		t.Fatalf("input_role_counts = %#v", roles)
	}
	if summary["tool_call_count"] != 1 || summary["tool_result_count"] != 1 {
		t.Fatalf("tool counts = %v/%v", summary["tool_call_count"], summary["tool_result_count"])
	}
	if names := summary["trailing_tool_call_names"].([]string); !reflect.DeepEqual(names, []string{"run_tool"}) {
		t.Fatalf("trailing tool call names = %#v", names)
	}
	if summary["tool_definition_count"] != 5 {
		t.Fatalf("tool_definition_count = %v", summary["tool_definition_count"])
	}
	toolNames := summary["tool_definition_names"].([]string)
	wantNames := []string{
		"lookup", "wrapped_tool", "kiro_tool", "wrapped_kiro",
		"sha256:" + autoDiagnosticKey("bad.tool"),
	}
	if !reflect.DeepEqual(toolNames, wantNames) {
		t.Fatalf("tool definition names = %#v, want %#v", toolNames, wantNames)
	}
	metadataNames := summary["metadata_key_names"].([]string)
	wantMetadataNames := []string{"kiro:conversation_id", "custom_key", "sha256:" + autoDiagnosticKey("bad/path")}
	if !reflect.DeepEqual(metadataNames, wantMetadataNames) {
		t.Fatalf("metadata key names = %#v, want %#v", metadataNames, wantMetadataNames)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"PRIVATE_INSTRUCTIONS_4fe0", "PRIVATE_CALL_ID_4fe0", "PRIVATE_ARGUMENT_4fe0",
		"PRIVATE_RESULT_4fe0", "PRIVATE_CONTENT_4fe0", "PRIVATE_UNKNOWN_4fe0",
		"PRIVATE_ROLE_CONTENT_4fe0", "PRIVATE_SCHEMA_4fe0", "PRIVATE_WRAPPED_SCHEMA_4fe0",
		"PRIVATE_KIRO_SCHEMA_4fe0", "PRIVATE_KIRO_FUNCTION_SCHEMA_4fe0", "PRIVATE_METADATA_VALUE_4fe0",
		"PRIVATE_NESTED_METADATA_4fe0", "PRIVATE_BAD_KEY_VALUE_4fe0",
	} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("projection contains private value %q: %s", secret, encoded)
		}
	}
	if !bytes.Equal(body, before) {
		t.Fatal("projection modified its source body")
	}
}

func TestRawResponsesDiagnosticProjectionBoundsAndInvalidInput(t *testing.T) {
	var body strings.Builder
	body.WriteString(`{"input":[`)
	for i := 0; i < 15; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"type":"function_call","name":"call_%02d","arguments":{"secret":"ignored"}}`, i)
	}
	body.WriteString(`],"tools":[`)
	for i := 0; i < 40; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"type":"function","name":"tool_%02d","parameters":{"secret":"ignored"}}`, i)
	}
	body.WriteString(`],"metadata":{`)
	for i := 0; i < 70; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `"key_%02d":"PRIVATE_META_%02d"`, i, i)
	}
	body.WriteString(`}}`)

	summary, err := projectRawResponsesDiagnostic([]byte(body.String()))
	if err != nil {
		t.Fatalf("bounded projection: %v", err)
	}
	if summary["input_item_count"] != 15 || summary["tool_call_count"] != 15 {
		t.Fatalf("input/tool call counts = %v/%v", summary["input_item_count"], summary["tool_call_count"])
	}
	calls := summary["trailing_tool_call_names"].([]string)
	if len(calls) != autoDiagnosticTrailingCallLimit || calls[0] != "call_03" || calls[len(calls)-1] != "call_14" || summary["trailing_tool_call_names_omitted"] != 3 {
		t.Fatalf("call name tail/omission = %#v/%v", calls, summary["trailing_tool_call_names_omitted"])
	}
	toolNames := summary["tool_definition_names"].([]string)
	if summary["tool_definition_count"] != 40 || len(toolNames) != autoDiagnosticToolNameLimit || summary["tool_definition_names_omitted"] != 8 {
		t.Fatalf("tool definition bounds/count = %d/%v/%v", len(toolNames), summary["tool_definition_count"], summary["tool_definition_names_omitted"])
	}
	metadataNames := summary["metadata_key_names"].([]string)
	if len(metadataNames) != autoDiagnosticMetadataKeyLimit || summary["metadata_key_names_omitted"] != 6 {
		t.Fatalf("metadata key bounds/count = %d/%v", len(metadataNames), summary["metadata_key_names_omitted"])
	}

	for _, body := range [][]byte{
		[]byte(`{`),
		[]byte(`{"model":"auto"}`),
		[]byte(`{"input":null}`),
		[]byte(`{"input":[{"type":3}]}`),
		[]byte(`{"input":[],"metadata":[]}`),
	} {
		if _, err := projectRawResponsesDiagnostic(body); err == nil {
			t.Errorf("projectRawResponsesDiagnostic(%s) unexpectedly succeeded", body)
		}
	}
}

func TestLegacyAutoRoutingDiagnosticUsesParsedConversationCounts(t *testing.T) {
	falseContinuation := false
	conv := &converter.ParsedConversation{
		ConversationID:              "legacy-session-private",
		ContinuationStateKey:        "legacy-continuation-private",
		AgentContinuationID:         "legacy-native-continuation-private",
		AgentContinuationProvenance: converter.AgentContinuationProvenanceNativeRaw,
		WorkflowExecutionID:         "legacy-execution-private",
		SystemText:                  "PRIVATE_SYSTEM_TEXT_31ab",
		CurrentContent:              "PRIVATE_CURRENT_TEXT_31ab",
		CurrentToolContinuation:     &falseContinuation,
		CurrentToolResults:          []converter.ParsedToolResult{{ToolUseID: "current-result-id", Content: "PRIVATE_CURRENT_RESULT_31ab"}},
		CurrentImages:               []converter.KiroImage{{Source: converter.KiroImageSource{Bytes: "PRIVATE_IMAGE_31ab"}}},
		History: []converter.ParsedHistoryEntry{
			{
				UserContent: "PRIVATE_OLD_USER_31ab", AssistantContent: "PRIVATE_OLD_ASSISTANT_31ab",
				ToolUses:    []converter.ParsedToolUse{{ToolUseID: "old-call-id", Name: "history_tool", Input: goccyjson.RawMessage(`{"secret":"PRIVATE_ARGUMENT_31ab"}`)}},
				ToolResults: []converter.ParsedToolResult{{ToolUseID: "old-result-id", Content: "PRIVATE_HISTORY_RESULT_31ab"}},
				Images:      []converter.KiroImage{{Source: converter.KiroImageSource{Bytes: "PRIVATE_HISTORY_IMAGE_31ab"}}},
			},
			{
				ToolUses:    []converter.ParsedToolUse{{ToolUseID: "invalid-call-id", Name: "history.tool"}},
				ToolResults: []converter.ParsedToolResult{{ToolUseID: "old-result-2", Content: "PRIVATE_HISTORY_RESULT_2_31ab"}},
			},
		},
		RawTools: []goccyjson.RawMessage{
			goccyjson.RawMessage(`{"type":"function","name":"legacy_tool","parameters":{"secret":"PRIVATE_TOOL_SCHEMA_31ab"}}`),
			goccyjson.RawMessage(`{"toolSpecification":{"function":{"name":"legacy_kiro_tool","inputSchema":{"secret":"PRIVATE_KIRO_SCHEMA_31ab"}}}}`),
		},
	}

	summary := projectLegacyConversationDiagnostic(conv)
	for key, want := range map[string]int{
		"history_item_count":        2,
		"history_tool_call_count":   2,
		"history_tool_result_count": 2,
		"current_tool_result_count": 1,
		"history_image_count":       1,
		"current_image_count":       1,
		"system_text_bytes":         len(conv.SystemText),
		"current_text_bytes":        len(conv.CurrentContent),
		"tool_definition_count":     2,
	} {
		if got := summary[key]; got != want {
			t.Errorf("%s = %v, want %d", key, got, want)
		}
	}
	if got, want := summary["trailing_tool_call_names"].([]string), []string{"history_tool", "sha256:" + autoDiagnosticKey("history.tool")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("trailing tool call names = %#v, want %#v", got, want)
	}
	if got, want := summary["tool_definition_names"].([]string), []string{"legacy_tool", "legacy_kiro_tool"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool definition names = %#v, want %#v", got, want)
	}
	if got := currentToolContinuationStatus(conv.CurrentToolContinuation); got != "false" {
		t.Fatalf("explicit current continuation status = %q", got)
	}
	if got := currentToolContinuationStatus(nil); got != "unknown" {
		t.Fatalf("nil current continuation status = %q, want unknown", got)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"PRIVATE_SYSTEM_TEXT_31ab", "PRIVATE_CURRENT_TEXT_31ab", "PRIVATE_OLD_USER_31ab",
		"PRIVATE_OLD_ASSISTANT_31ab", "PRIVATE_ARGUMENT_31ab", "PRIVATE_HISTORY_RESULT_31ab",
		"PRIVATE_CURRENT_RESULT_31ab", "PRIVATE_IMAGE_31ab", "PRIVATE_HISTORY_IMAGE_31ab",
		"PRIVATE_TOOL_SCHEMA_31ab", "PRIVATE_KIRO_SCHEMA_31ab",
	} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("legacy projection contains private value %q: %s", secret, encoded)
		}
	}
}

func TestLogAutoRoutingDiagnosticPrivacyAndGuards(t *testing.T) {
	logger, diagnosticPath := newAutoRoutingDiagnosticTestLogger(t)
	body := []byte(`{"instructions":"PRIVATE_INSTRUCTIONS_62cf","input":[{"type":"function_call","call_id":"PRIVATE_CALL_ID_62cf","name":"safe_tool","arguments":"PRIVATE_ARGUMENT_62cf"},{"type":"function_call_output","call_id":"PRIVATE_CALL_ID_62cf","output":"PRIVATE_RESULT_62cf"},{"role":"user","content":"PRIVATE_CONTENT_62cf"}],"tools":[{"type":"function","name":"safe_tool","parameters":{"description":"PRIVATE_SCHEMA_62cf"}}],"metadata":{"session_id":"PRIVATE_METADATA_VALUE_62cf"}}`)
	conv := &converter.ParsedConversation{
		ModelID: "auto", ConversationID: "PRIVATE_SESSION_ID_62cf",
		WorkflowID: "PRIVATE_WORKFLOW_ID_62cf", WorkflowExecutionID: "PRIVATE_EXECUTION_ID_62cf",
		WorkflowDefinitionID: "PRIVATE_WORKFLOW_DEFINITION_ID_62cf",
		AgentContinuationID:  "PRIVATE_NATIVE_CONTINUATION_ID_62cf",
		ContinuationStateKey: "PRIVATE_CONTINUATION_KEY_62cf",
		WorkflowNodePath:     strings.Repeat("node/", 300),
		CurrentContent:       "PRIVATE_CONV_CURRENT_TEXT_62cf",
	}
	pre := &Preflight{Protocol: "kiro-runtime-responses", SourceModel: "AuTo", Body: body, Conv: conv}
	pre.autoDiagnosticRequestID = "generated-request-correlation-62cf"
	route := &router.Route{
		Model:         &store.Model{ID: "model-config-62cf", ModelID: "selected-model-62cf"},
		Group:         &store.ModelGroup{ID: "group-62cf", Name: "diagnostic-group-62cf"},
		RequestedName: "auto",
		AutoReason:    "duty_fixed", AutoMode: "stage_fixed", AutoStage: "implement",
		AutoDuty: "coding", AutoSelectionSource: "workflow_header", AutoUnknownReason: "",
		AutoExecutionID: "PRIVATE_ROUTE_EXECUTION_ID_62cf", AutoLockScope: "execution",
		AutoChannelIdentity: "PRIVATE_CHANNEL_ID_62cf", AutoEffort: "high", AutoEffortFixed: true,
	}
	bodyBefore := append([]byte(nil), pre.Body...)
	convBefore := *conv
	preBodyBefore := append([]byte(nil), pre.Body...)
	(&Server{logger: logger}).logAutoRoutingDiagnostic(pre, route)
	if !bytes.Equal(pre.Body, preBodyBefore) || !bytes.Equal(pre.Body, bodyBefore) {
		t.Fatal("route diagnostic changed the request body")
	}
	if conv.ConversationID != convBefore.ConversationID || conv.CurrentContent != convBefore.CurrentContent || conv.WorkflowExecutionID != convBefore.WorkflowExecutionID {
		t.Fatal("route diagnostic changed parsed conversation fields")
	}
	logger.Close()
	data, err := os.ReadFile(diagnosticPath)
	if err != nil {
		t.Fatal(err)
	}
	var entry struct {
		Message  string                 `json:"message"`
		Metadata map[string]interface{} `json:"metadata"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatalf("decode diagnostic entry: %v; data=%s", err, data)
	}
	if entry.Message != "route_selected" {
		t.Fatalf("diagnostic event = %q", entry.Message)
	}
	for field, source := range map[string]string{
		"conversation_key":        conv.ConversationID,
		"workflow_key":            conv.WorkflowID,
		"continuation_key":        conv.ContinuationStateKey,
		"native_continuation_key": conv.AgentContinuationID,
		"workflow_definition_key": conv.WorkflowDefinitionID,
		"workflow_execution_key":  conv.WorkflowExecutionID,
		"route_execution_key":     route.AutoExecutionID,
		"channel_identity_key":    route.AutoChannelIdentity,
	} {
		want := autoDiagnosticKey(source)
		if got := entry.Metadata[field]; got != want {
			t.Errorf("%s = %v, want hash %q", field, got, want)
		}
	}
	if entry.Metadata["request_summary_status"] != "available" || entry.Metadata["request_summary_kind"] != "raw_responses" {
		t.Fatalf("raw summary status/kind = %v/%v", entry.Metadata["request_summary_status"], entry.Metadata["request_summary_kind"])
	}
	if entry.Metadata["current_tool_continuation"] != "unknown" {
		t.Fatalf("current tool continuation = %v, want unknown", entry.Metadata["current_tool_continuation"])
	}
	if entry.Metadata["tool_result_count"] != float64(1) || entry.Metadata["input_item_count"] != float64(3) {
		t.Fatalf("raw summary counts = tool_results:%v input:%v", entry.Metadata["tool_result_count"], entry.Metadata["input_item_count"])
	}
	if entry.Metadata["target_group_name"] != "diagnostic-group-62cf" || entry.Metadata["selection_source"] != "workflow_header" {
		t.Fatalf("allowlisted route fields missing: %#v", entry.Metadata)
	}
	for _, secret := range []string{
		"PRIVATE_INSTRUCTIONS_62cf", "PRIVATE_CALL_ID_62cf", "PRIVATE_ARGUMENT_62cf",
		"PRIVATE_RESULT_62cf", "PRIVATE_CONTENT_62cf", "PRIVATE_SCHEMA_62cf",
		"PRIVATE_METADATA_VALUE_62cf", "PRIVATE_SESSION_ID_62cf", "PRIVATE_WORKFLOW_ID_62cf",
		"PRIVATE_WORKFLOW_DEFINITION_ID_62cf", "PRIVATE_EXECUTION_ID_62cf", "PRIVATE_NATIVE_CONTINUATION_ID_62cf",
		"PRIVATE_CONTINUATION_KEY_62cf", "PRIVATE_ROUTE_EXECUTION_ID_62cf", "PRIVATE_CHANNEL_ID_62cf",
		"PRIVATE_CONV_CURRENT_TEXT_62cf",
	} {
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("diagnostic log contains private value %q: %s", secret, data)
		}
	}
	assertDiagnosticStringFieldsBounded(t, entry.Metadata)

	guardLogger, guardPath := newAutoRoutingDiagnosticTestLogger(t)
	server := &Server{logger: guardLogger}
	server.logAutoRoutingDiagnostic(nil, route)
	server.logAutoRoutingDiagnostic(pre, nil)
	server.logAutoRoutingDiagnostic(pre, &router.Route{Model: route.Model})
	server.logAutoRoutingDiagnostic(&Preflight{SourceModel: "auto"}, route)
	manual := *pre
	manual.SourceModel = "manual-model"
	server.logAutoRoutingDiagnostic(&manual, route)
	classifier := *pre
	classifier.IsClassifier = true
	server.logAutoRoutingDiagnostic(&classifier, route)
	classifier = *pre
	classifier.Conv = &converter.ParsedConversation{ModelID: "auto", IsClassifier: true}
	server.logAutoRoutingDiagnostic(&classifier, route)
	var nilServer *Server
	nilServer.logAutoRoutingDiagnostic(pre, route)
	(&Server{}).logAutoRoutingDiagnostic(pre, route)
	guardLogger.Close()
	if data, err := os.ReadFile(guardPath); err != nil {
		t.Fatal(err)
	} else if len(data) != 0 {
		t.Fatalf("guarded requests unexpectedly emitted diagnostic data: %s", data)
	}

	invalidLogger, invalidPath := newAutoRoutingDiagnosticTestLogger(t)
	invalidPre := *pre
	invalidPre.Body = []byte(`{"input":[{"type":3}]}`)
	(&Server{logger: invalidLogger}).logAutoRoutingDiagnostic(&invalidPre, route)
	invalidLogger.Close()
	invalidData, err := os.ReadFile(invalidPath)
	if err != nil {
		t.Fatal(err)
	}
	var invalidEntry struct {
		Message  string                 `json:"message"`
		Metadata map[string]interface{} `json:"metadata"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(invalidData), &invalidEntry); err != nil {
		t.Fatalf("decode invalid-body diagnostic: %v; data=%s", err, invalidData)
	}
	if invalidEntry.Message != "route_selected" || invalidEntry.Metadata["request_summary_status"] != "unavailable" || invalidEntry.Metadata["request_summary_kind"] != "raw_responses" {
		t.Fatalf("invalid summary changed route diagnostic: %#v", invalidEntry)
	}
}

func newAutoRoutingDiagnosticTestLogger(t *testing.T) (*TrafficLogger, string) {
	t.Helper()
	trafficPath := filepath.Join(t.TempDir(), "traffic.log")
	logger, err := NewTrafficLogger(trafficPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	return logger, trafficPath + ".auto-diagnostic.log"
}

func assertDiagnosticStringFieldsBounded(t *testing.T, value interface{}) {
	t.Helper()
	switch value := value.(type) {
	case string:
		if len(value) > autoDiagnosticMaxFieldBytes {
			t.Errorf("diagnostic string has %d bytes", len(value))
		}
	case []interface{}:
		for _, item := range value {
			assertDiagnosticStringFieldsBounded(t, item)
		}
	case map[string]interface{}:
		for key, item := range value {
			if len(key) > autoDiagnosticMaxFieldBytes {
				t.Errorf("diagnostic key has %d bytes", len(key))
			}
			assertDiagnosticStringFieldsBounded(t, item)
		}
	}
}
