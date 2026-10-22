package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	stdjson "encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	goccyjson "github.com/goccy/go-json"
	"kiro-proxy/converter"
	"kiro-proxy/router"
)

const (
	autoDiagnosticMaxFieldBytes       = 1024
	autoDiagnosticToolNameLimit       = 32
	autoDiagnosticMetadataKeyLimit    = 64
	autoDiagnosticTrailingCallLimit   = 12
	autoDiagnosticIdentifierByteLimit = 128
)

// autoDiagnosticKey makes request identities useful for correlation without
// copying their source values into the diagnostic log.
func autoDiagnosticKey(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// logAutoRoutingDiagnostic records only allowlisted routing facts and bounded
// structural counts. It does not participate in routing or request parsing.
func (s *Server) logAutoRoutingDiagnostic(pre *Preflight, route *router.Route) {
	if s == nil || s.logger == nil || pre == nil || pre.Conv == nil || route == nil || route.Model == nil {
		return
	}
	if pre.IsClassifier || pre.Conv.IsClassifier || route.AutoReason == "" {
		return
	}
	if !strings.EqualFold(pre.SourceModel, "auto") {
		return
	}

	conv := pre.Conv
	metadata := map[string]interface{}{
		"request_id":                     autoDiagnosticBounded(autoDiagnosticRequestKey(pre)),
		"protocol":                       autoDiagnosticBounded(pre.Protocol),
		"requested_model":                autoDiagnosticBounded(pre.SourceModel),
		"selection_reason":               autoDiagnosticBounded(route.AutoReason),
		"auto_mode":                      autoDiagnosticBounded(route.AutoMode),
		"selected_model_config_id":       autoDiagnosticBounded(route.Model.ID),
		"selected_model":                 autoDiagnosticBounded(route.Model.ModelID),
		"selected_stage":                 autoDiagnosticBounded(route.AutoStage),
		"selected_duty":                  autoDiagnosticBounded(route.AutoDuty),
		"selection_source":               autoDiagnosticBounded(route.AutoSelectionSource),
		"unknown_reason":                 autoDiagnosticBounded(route.AutoUnknownReason),
		"workflow_stage":                 autoDiagnosticBounded(conv.WorkflowStage),
		"workflow_stage_source":          autoDiagnosticBounded(conv.WorkflowStageSource),
		"workflow_stage_status":          workflowStageStatus(conv),
		"workflow_metadata_version":      conv.WorkflowMetadataVersion,
		"workflow_node_id":               autoDiagnosticBounded(conv.WorkflowNodeID),
		"workflow_node_path":             autoDiagnosticBounded(conv.WorkflowNodePath),
		"workflow_task_type":             autoDiagnosticBounded(conv.WorkflowTaskType),
		"agent_task_type":                autoDiagnosticBounded(conv.AgentTaskType),
		"agent_mode":                     autoDiagnosticBounded(conv.AgentMode),
		"chat_trigger_type":              autoDiagnosticBounded(conv.ChatTriggerType),
		"workflow_key":                   autoDiagnosticKey(conv.WorkflowID),
		"workflow_definition_key":        autoDiagnosticKey(conv.WorkflowDefinitionID),
		"workflow_definition_revision":   autoDiagnosticBounded(conv.WorkflowDefinitionRevision),
		"conversation_key":               autoDiagnosticKey(conv.ConversationID),
		"prompt_affinity_key":            autoDiagnosticKey(conv.PromptAffinityKey),
		"continuation_key":               autoDiagnosticKey(conv.ContinuationStateKey),
		"native_continuation_key":        autoDiagnosticKey(conv.AgentContinuationID),
		"native_continuation_provenance": autoDiagnosticBounded(conv.AgentContinuationProvenance),
		"workflow_execution_key":         autoDiagnosticKey(conv.WorkflowExecutionID),
		"route_execution_key":            autoDiagnosticKey(route.AutoExecutionID),
		"channel_identity_key":           autoDiagnosticKey(route.AutoChannelIdentity),
		"lock_scope":                     autoDiagnosticBounded(route.AutoLockScope),
		"requested_effort":               autoDiagnosticBounded(conv.ReasoningEffort),
		"selected_effort":                autoDiagnosticBounded(route.AutoEffort),
		"effective_effort":               autoDiagnosticBounded(resolveReasoningEffort(pre, route.Model)),
		"effort_fixed":                   route.AutoEffortFixed,
		"effort_conflict":                route.AutoEffortFixed && conv.ReasoningEffort != "" && conv.ReasoningEffort != route.AutoEffort,
		"current_tool_continuation":      currentToolContinuationStatus(conv.CurrentToolContinuation),
		"request_summary_status":         "unavailable",
		"request_summary_kind":           "legacy",
	}
	if route.Group != nil {
		metadata["target_group_id"] = autoDiagnosticBounded(route.Group.ID)
		metadata["target_group_name"] = autoDiagnosticBounded(route.Group.Name)
	}

	if pre.Protocol == "kiro-runtime-responses" {
		metadata["request_summary_kind"] = "raw_responses"
		if summary, err := projectRawResponsesDiagnostic(pre.Body); err == nil {
			for key, value := range summary {
				metadata[key] = value
			}
			metadata["request_summary_status"] = "available"
			metadata["request_summary_kind"] = "raw_responses"
			metadata["raw_responses_has_images"] = conv.RawResponsesHasImages
		}
	} else {
		for key, value := range projectLegacyConversationDiagnostic(conv) {
			metadata[key] = value
		}
		metadata["request_summary_status"] = "available"
	}

	// Diagnostic failures are reported by the shared sink wrapper and must not
	// change the routing result.
	s.recordAutoDiagnostic("route_selected", metadata)
}

func autoDiagnosticBounded(value string) string {
	if len(value) <= autoDiagnosticMaxFieldBytes {
		return value
	}
	value = value[:autoDiagnosticMaxFieldBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func currentToolContinuationStatus(value *bool) string {
	if value == nil {
		return "unknown"
	}
	if *value {
		return "true"
	}
	return "false"
}

func projectLegacyConversationDiagnostic(conv *converter.ParsedConversation) map[string]interface{} {
	historyToolUses := 0
	historyToolResults := 0
	historyImages := 0
	callNames := make([]string, 0, autoDiagnosticTrailingCallLimit)
	callNamesSeen := 0
	for _, entry := range conv.History {
		historyToolUses += len(entry.ToolUses)
		historyToolResults += len(entry.ToolResults)
		historyImages += len(entry.Images)
		for _, use := range entry.ToolUses {
			if name := autoDiagnosticIdentifier(use.Name); name != "" {
				callNamesSeen++
				callNames = appendDiagnosticTail(callNames, name, autoDiagnosticTrailingCallLimit)
			}
		}
	}
	definitionNames, definitionsOmitted, definitionsValid := diagnosticLegacyToolNames(conv.RawTools)
	return map[string]interface{}{
		"history_item_count":               len(conv.History),
		"history_tool_call_count":          historyToolUses,
		"history_tool_result_count":        historyToolResults,
		"current_tool_result_count":        len(conv.CurrentToolResults),
		"history_image_count":              historyImages,
		"current_image_count":              len(conv.CurrentImages),
		"system_text_bytes":                len(conv.SystemText),
		"current_text_bytes":               len(conv.CurrentContent),
		"tool_definition_count":            len(conv.RawTools),
		"tool_definition_names":            definitionNames,
		"tool_definition_names_omitted":    definitionsOmitted,
		"tool_definition_names_available":  definitionsValid,
		"trailing_tool_call_names":         callNames,
		"trailing_tool_call_names_omitted": max(0, callNamesSeen-len(callNames)),
	}
}

func diagnosticLegacyToolNames(rawTools []goccyjson.RawMessage) ([]string, int, bool) {
	names := make([]string, 0, min(len(rawTools), autoDiagnosticToolNameLimit))
	omitted := 0
	valid := true
	for _, raw := range rawTools {
		var tool rawResponsesDiagnosticTool
		if err := stdjson.Unmarshal(raw, &tool); err != nil {
			valid = false
			continue
		}
		if name := tool.diagnosticName(); name != "" {
			if len(names) < autoDiagnosticToolNameLimit {
				names = append(names, name)
			} else {
				omitted++
			}
		}
	}
	return names, omitted, valid
}

func autoDiagnosticIdentifier(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= autoDiagnosticIdentifierByteLimit && isASCIIIdentifier(value) {
		return value
	}
	return "sha256:" + autoDiagnosticKey(value)
}

func autoDiagnosticMetadataKey(value string) string {
	if value == "" {
		return "sha256:" + diagnosticHashBytes(nil)
	}
	if len(value) <= autoDiagnosticIdentifierByteLimit && isASCIIKey(value) {
		return value
	}
	return "sha256:" + autoDiagnosticKey(value)
}

func isASCIIKey(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') ||
			ch == '_' || ch == '-' || ch == ':' || ch == '.' {
			continue
		}
		return false
	}
	return true
}

func isASCIIIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' || (i > 0 && ch >= '0' && ch <= '9') {
			continue
		}
		return false
	}
	return true
}

func appendDiagnosticTail(values []string, value string, limit int) []string {
	if value == "" || limit <= 0 {
		return values
	}
	if len(values) < limit {
		return append(values, value)
	}
	copy(values, values[1:])
	values[len(values)-1] = value
	return values
}

var diagnosticInputTypeNames = [...]string{
	"message", "function_call", "function_call_output", "computer_call", "computer_call_output",
	"custom_tool_call", "custom_tool_call_output", "mcp_call", "mcp_call_output",
	"code_interpreter_call", "web_search_call", "file_search_call", "image_generation_call",
}

var diagnosticInputRoleNames = [...]string{"system", "developer", "user", "assistant", "other"}

type rawResponsesDiagnosticProjection struct {
	Input        rawResponsesDiagnosticInput    `json:"input"`
	Tools        rawResponsesDiagnosticTools    `json:"tools"`
	Metadata     rawResponsesDiagnosticMetadata `json:"metadata"`
	Content      diagnosticDiscardJSON          `json:"content"`
	Instructions diagnosticDiscardJSON          `json:"instructions"`
	Arguments    diagnosticDiscardJSON          `json:"arguments"`
	Output       diagnosticDiscardJSON          `json:"output"`
}

func projectRawResponsesDiagnostic(body []byte) (map[string]interface{}, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("raw Responses diagnostic body must be an object")
	}
	projection := rawResponsesDiagnosticProjection{
		Tools:    rawResponsesDiagnosticTools{names: []string{}},
		Metadata: rawResponsesDiagnosticMetadata{keys: []string{}},
	}
	if err := stdjson.Unmarshal(trimmed, &projection); err != nil {
		return nil, err
	}
	if !projection.Input.present {
		return nil, errors.New("raw Responses input is missing")
	}
	return projection.metadata(), nil
}

func (projection rawResponsesDiagnosticProjection) metadata() map[string]interface{} {
	return map[string]interface{}{
		"input_scope":                      "complete_input_history",
		"input_item_count":                 projection.Input.itemCount,
		"input_type_counts":                projection.Input.typeCounts,
		"input_role_counts":                projection.Input.roleCounts,
		"tool_call_count":                  projection.Input.toolCallCount,
		"tool_result_count":                projection.Input.toolResultCount,
		"trailing_tool_call_names":         projection.Input.trailingCallNames,
		"trailing_tool_call_names_omitted": max(0, projection.Input.callNamesSeen-len(projection.Input.trailingCallNames)),
		"tool_definition_count":            projection.Tools.count,
		"tool_definition_names":            projection.Tools.names,
		"tool_definition_names_omitted":    projection.Tools.omitted,
		"metadata_key_names":               projection.Metadata.keys,
		"metadata_key_names_omitted":       projection.Metadata.omitted,
	}
}

type rawResponsesDiagnosticInput struct {
	present           bool
	itemCount         int
	typeCounts        map[string]int
	roleCounts        map[string]int
	toolCallCount     int
	toolResultCount   int
	callNamesSeen     int
	trailingCallNames []string
}

func (input *rawResponsesDiagnosticInput) UnmarshalJSON(data []byte) error {
	*input = rawResponsesDiagnosticInput{
		present:           true,
		typeCounts:        makeDiagnosticCounts(diagnosticInputTypeNames[:]),
		roleCounts:        makeDiagnosticCounts(diagnosticInputRoleNames[:]),
		trailingCallNames: make([]string, 0, autoDiagnosticTrailingCallLimit),
	}
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		return errors.New("raw Responses input is null")
	}
	if len(trimmed) > 0 && trimmed[0] == '"' {
		if !stdjson.Valid(trimmed) {
			return errors.New("raw Responses input string is invalid")
		}
		input.itemCount = 1
		input.typeCounts["other"]++
		return nil
	}
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return errors.New("raw Responses input must be an array or string")
	}
	decoder := stdjson.NewDecoder(bytes.NewReader(trimmed))
	start, err := decoder.Token()
	if err != nil || start != stdjson.Delim('[') {
		return errors.New("raw Responses input array is invalid")
	}
	for decoder.More() {
		var item rawResponsesDiagnosticInputItem
		if err := decoder.Decode(&item); err != nil {
			return err
		}
		input.observe(item)
	}
	end, err := decoder.Token()
	if err != nil || end != stdjson.Delim(']') {
		return errors.New("raw Responses input array is incomplete")
	}
	return diagnosticDecoderAtEOF(decoder)
}

func (input *rawResponsesDiagnosticInput) observe(item rawResponsesDiagnosticInputItem) {
	input.itemCount++
	kind := item.Type.value
	if item.Type.oversized {
		kind = ""
	} else if !item.Type.present && isDiagnosticInputRole(item.Role.value) {
		// Responses accepts the concise {role, content} message shape.
		kind = "message"
	}
	if _, ok := input.typeCounts[kind]; ok {
		input.typeCounts[kind]++
	} else {
		input.typeCounts["other"]++
	}
	if kind == "message" {
		role := item.Role.value
		if item.Role.oversized {
			role = ""
		}
		if _, ok := input.roleCounts[role]; ok && role != "other" {
			input.roleCounts[role]++
		} else {
			input.roleCounts["other"]++
		}
	}
	if isDiagnosticToolCallType(kind) {
		input.toolCallCount++
		if name := item.Name.diagnosticIdentifier(); name != "" {
			input.callNamesSeen++
			input.trailingCallNames = appendDiagnosticTail(input.trailingCallNames, name, autoDiagnosticTrailingCallLimit)
		}
	}
	if isDiagnosticToolResultType(kind) {
		input.toolResultCount++
	}
}

func isDiagnosticInputRole(role string) bool {
	switch role {
	case "system", "developer", "user", "assistant":
		return true
	default:
		return false
	}
}

func makeDiagnosticCounts(names []string) map[string]int {
	counts := make(map[string]int, len(names)+1)
	for _, name := range names {
		counts[name] = 0
	}
	counts["other"] = 0
	return counts
}

func isDiagnosticToolCallType(kind string) bool {
	switch kind {
	case "function_call", "computer_call", "custom_tool_call", "mcp_call", "code_interpreter_call", "web_search_call", "file_search_call", "image_generation_call":
		return true
	default:
		return false
	}
}

func isDiagnosticToolResultType(kind string) bool {
	switch kind {
	case "function_call_output", "computer_call_output", "custom_tool_call_output", "mcp_call_output":
		return true
	default:
		return false
	}
}

type rawResponsesDiagnosticInputItem struct {
	Type         diagnosticJSONText
	Role         diagnosticJSONText
	Name         diagnosticJSONText
	Content      diagnosticDiscardJSON
	Instructions diagnosticDiscardJSON
	Arguments    diagnosticDiscardJSON
	Output       diagnosticDiscardJSON
}

func (item *rawResponsesDiagnosticInputItem) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		if !stdjson.Valid(trimmed) {
			return errors.New("raw Responses input item is invalid")
		}
		*item = rawResponsesDiagnosticInputItem{Type: diagnosticJSONText{value: "other", present: true}}
		return nil
	}
	var fields struct {
		Type         diagnosticJSONText    `json:"type"`
		Role         diagnosticJSONText    `json:"role"`
		Name         diagnosticJSONText    `json:"name"`
		Content      diagnosticDiscardJSON `json:"content"`
		Instructions diagnosticDiscardJSON `json:"instructions"`
		Arguments    diagnosticDiscardJSON `json:"arguments"`
		Output       diagnosticDiscardJSON `json:"output"`
	}
	if err := stdjson.Unmarshal(trimmed, &fields); err != nil {
		return err
	}
	*item = rawResponsesDiagnosticInputItem{
		Type:         fields.Type,
		Role:         fields.Role,
		Name:         fields.Name,
		Content:      fields.Content,
		Instructions: fields.Instructions,
		Arguments:    fields.Arguments,
		Output:       fields.Output,
	}
	return nil
}

type rawResponsesDiagnosticTools struct {
	count   int
	names   []string
	omitted int
}

func (tools *rawResponsesDiagnosticTools) UnmarshalJSON(data []byte) error {
	*tools = rawResponsesDiagnosticTools{names: make([]string, 0, autoDiagnosticToolNameLimit)}
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return errors.New("raw Responses tools must be an array or null")
	}
	decoder := stdjson.NewDecoder(bytes.NewReader(trimmed))
	start, err := decoder.Token()
	if err != nil || start != stdjson.Delim('[') {
		return errors.New("raw Responses tools array is invalid")
	}
	for decoder.More() {
		var tool rawResponsesDiagnosticTool
		if err := decoder.Decode(&tool); err != nil {
			return err
		}
		tools.count++
		if name := tool.diagnosticName(); name != "" {
			if len(tools.names) < autoDiagnosticToolNameLimit {
				tools.names = append(tools.names, name)
			} else {
				tools.omitted++
			}
		}
	}
	end, err := decoder.Token()
	if err != nil || end != stdjson.Delim(']') {
		return errors.New("raw Responses tools array is incomplete")
	}
	return diagnosticDecoderAtEOF(decoder)
}

type rawResponsesDiagnosticTool struct {
	Type              diagnosticJSONText                      `json:"type"`
	Name              diagnosticJSONText                      `json:"name"`
	Function          rawResponsesDiagnosticNamed             `json:"function"`
	ToolSpecification rawResponsesDiagnosticToolSpecification `json:"toolSpecification"`
	Parameters        diagnosticDiscardJSON                   `json:"parameters"`
	InputSchema       diagnosticDiscardJSON                   `json:"inputSchema"`
}

type rawResponsesDiagnosticNamed struct {
	Name       diagnosticJSONText    `json:"name"`
	Parameters diagnosticDiscardJSON `json:"parameters"`
}

type rawResponsesDiagnosticToolSpecification struct {
	Name        diagnosticJSONText          `json:"name"`
	Function    rawResponsesDiagnosticNamed `json:"function"`
	Description diagnosticDiscardJSON       `json:"description"`
	InputSchema diagnosticDiscardJSON       `json:"inputSchema"`
}

func (tool rawResponsesDiagnosticTool) diagnosticName() string {
	for _, name := range []diagnosticJSONText{
		tool.Name, tool.Function.Name, tool.ToolSpecification.Name, tool.ToolSpecification.Function.Name,
	} {
		if value := name.diagnosticIdentifier(); value != "" {
			return value
		}
	}
	return ""
}

type rawResponsesDiagnosticMetadata struct {
	keys    []string
	omitted int
}

func (metadata *rawResponsesDiagnosticMetadata) UnmarshalJSON(data []byte) error {
	*metadata = rawResponsesDiagnosticMetadata{keys: make([]string, 0, autoDiagnosticMetadataKeyLimit)}
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("raw Responses metadata must be an object or null")
	}
	decoder := stdjson.NewDecoder(bytes.NewReader(trimmed))
	start, err := decoder.Token()
	if err != nil || start != stdjson.Delim('{') {
		return errors.New("raw Responses metadata object is invalid")
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("raw Responses metadata key is not a string")
		}
		if name := autoDiagnosticMetadataKey(key); name != "" {
			if len(metadata.keys) < autoDiagnosticMetadataKeyLimit {
				metadata.keys = append(metadata.keys, name)
			} else {
				metadata.omitted++
			}
		}
		var value diagnosticDiscardJSON
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil || end != stdjson.Delim('}') {
		return errors.New("raw Responses metadata object is incomplete")
	}
	return diagnosticDecoderAtEOF(decoder)
}

type diagnosticDiscardJSON struct{}

func (*diagnosticDiscardJSON) UnmarshalJSON([]byte) error { return nil }

type diagnosticJSONText struct {
	value     string
	hash      string
	present   bool
	oversized bool
}

func (value *diagnosticJSONText) UnmarshalJSON(data []byte) error {
	*value = diagnosticJSONText{present: true}
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return errors.New("diagnostic string field is not a string")
	}
	// A JSON string uses at most six bytes per decoded code point. Avoid making
	// a second large allocation for untrusted names and metadata keys.
	if len(trimmed) > autoDiagnosticMaxFieldBytes*6+2 {
		value.oversized = true
		value.hash = diagnosticHashBytes(trimmed)
		return nil
	}
	if err := stdjson.Unmarshal(trimmed, &value.value); err != nil {
		return err
	}
	if len(value.value) > autoDiagnosticMaxFieldBytes {
		value.oversized = true
		value.hash = autoDiagnosticKey(value.value)
		value.value = ""
	}
	return nil
}

func (value diagnosticJSONText) diagnosticIdentifier() string {
	if !value.present {
		return ""
	}
	if value.oversized {
		return "sha256:" + value.hash
	}
	return autoDiagnosticIdentifier(value.value)
}

func diagnosticDecoderAtEOF(decoder *stdjson.Decoder) error {
	if _, err := decoder.Token(); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("unexpected data after diagnostic projection")
}

func diagnosticHashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
