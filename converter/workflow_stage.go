package converter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const WorkflowStageHeader = "X-Kiro-Proxy-Workflow"

const maxWorkflowStageHeaderBytes = 4096

// ApplyWorkflowStageHeader validates the proxy's synchronous workflow metadata
// and binds it to the parsed conversation before recording it for routing.
func ApplyWorkflowStageHeader(conv *ParsedConversation, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > maxWorkflowStageHeaderBytes {
		return errors.New("workflow header exceeds 4096 bytes")
	}
	if !utf8.ValidString(value) {
		return errors.New("workflow header must be valid UTF-8 JSON")
	}
	if conv == nil {
		return errors.New("workflow header cannot be bound to a conversation")
	}

	decoder := json.NewDecoder(strings.NewReader(value))
	opening, err := decoder.Token()
	object, isObject := opening.(json.Delim)
	if err != nil || !isObject || object != '{' {
		return errors.New("workflow header must be a JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return errors.New("workflow header must be a JSON object")
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("workflow header must be a JSON object")
		}
		if _, exists := fields[key]; exists {
			return errors.New("workflow header contains a duplicate field")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return errors.New("workflow header must be a JSON object")
		}
		fields[key] = raw
	}
	closing, err := decoder.Token()
	closeObject, isObject := closing.(json.Delim)
	if err != nil || !isObject || closeObject != '}' {
		return errors.New("workflow header must be a JSON object")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("workflow header must contain one JSON value")
	}

	for key := range fields {
		switch key {
		case "version", "stage", "workflowId", "conversationId", "agentMode", "agentContinuationId":
		default:
			return errors.New("workflow header contains an unknown field")
		}
	}

	versionRaw, ok := fields["version"]
	if !ok {
		return errors.New("workflow header version is required")
	}
	var version int
	if err := json.Unmarshal(versionRaw, &version); err != nil || version != 1 {
		return errors.New("workflow header version must be 1")
	}

	stage, err := workflowHeaderString(fields, "stage", true)
	if err != nil {
		return err
	}
	stage = strings.ToLower(strings.TrimSpace(stage))
	if stage == "" || len(stage) > 128 {
		return errors.New("workflow header stage must contain 1 to 128 bytes")
	}
	for i := 0; i < len(stage); i++ {
		ch := stage[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || strings.ContainsRune("._:/-", rune(ch)) {
			continue
		}
		return errors.New("workflow header stage contains an unsafe character")
	}

	conversationID, err := workflowHeaderString(fields, "conversationId", true)
	if err != nil {
		return err
	}
	conversationID = strings.TrimSpace(conversationID)
	boundConversationID := strings.TrimSpace(conv.ConversationID)
	if conversationID == "" || boundConversationID == "" || conversationID != boundConversationID {
		return errors.New("workflow header conversation binding does not match")
	}

	agentMode, err := workflowHeaderString(fields, "agentMode", true)
	if err != nil {
		return err
	}
	if strings.ToLower(strings.TrimSpace(agentMode)) != strings.ToLower(strings.TrimSpace(conv.AgentMode)) {
		return errors.New("workflow header agent mode binding does not match")
	}

	continuationID, err := workflowHeaderString(fields, "agentContinuationId", true)
	if err != nil {
		return err
	}
	if strings.TrimSpace(continuationID) != strings.TrimSpace(conv.AgentContinuationID) {
		return errors.New("workflow header continuation binding does not match")
	}

	workflowID := ""
	if _, ok := fields["workflowId"]; ok {
		workflowID, err = workflowHeaderString(fields, "workflowId", false)
		if err != nil {
			return err
		}
	}

	conv.WorkflowStage = stage
	conv.WorkflowStageSource = "request_header"
	conv.WorkflowStageProvided = true
	conv.WorkflowID = workflowID
	return nil
}

func workflowHeaderString(fields map[string]json.RawMessage, key string, required bool) (string, error) {
	raw, ok := fields[key]
	if !ok {
		if required {
			return "", errors.New("workflow header is missing a required string field")
		}
		return "", nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", errors.New("workflow header contains a field with the wrong type")
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return "", errors.New("workflow header contains an invalid string field")
	}
	return value, nil
}
