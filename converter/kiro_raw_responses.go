package converter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

// ParseKiroRawResponsesRouting extracts only the routing metadata carried by a
// raw Responses request. The caller retains and forwards body unchanged.
func ParseKiroRawResponsesRouting(body []byte) (*ParsedConversation, error) {
	root, err := decodeRawJSONObject(body)
	if err != nil {
		return nil, errors.New("raw Responses request must be one JSON object")
	}

	model, err := requiredRawString(root, "model")
	if err != nil {
		return nil, errors.New("raw Responses model must be a string")
	}
	if _, exists := root["conversation"]; exists {
		return nil, errors.New("raw Responses conversation references are unsupported; send complete input")
	}
	if raw, exists := root["previous_response_id"]; exists {
		previousID, err := decodeRawString(raw)
		if err != nil {
			return nil, errors.New("raw Responses previous_response_id must be a string")
		}
		if strings.TrimSpace(previousID) != "" {
			return nil, errors.New("raw Responses previous_response_id is unsupported; send complete input")
		}
	}

	inputRaw, exists := root["input"]
	if !exists {
		return nil, errors.New("raw Responses input must be an array")
	}
	var input []json.RawMessage
	trimmedInput := bytes.TrimSpace(inputRaw)
	if len(trimmedInput) == 0 || trimmedInput[0] != '[' || json.Unmarshal(trimmedInput, &input) != nil {
		return nil, errors.New("raw Responses input must be an array")
	}

	metadataRaw, exists := root["metadata"]
	if !exists {
		return nil, errors.New("raw Responses metadata must be an object")
	}
	metadata, err := decodeRawJSONObject(metadataRaw)
	if err != nil {
		return nil, errors.New("raw Responses metadata must be an object")
	}
	conversationID, err := requiredRawString(metadata, "kiro:conversation_id")
	if err != nil || strings.TrimSpace(conversationID) == "" {
		return nil, errors.New("raw Responses conversation metadata must be a nonempty string")
	}
	agentMode, err := requiredRawString(metadata, "kiro:agent_mode")
	if err != nil {
		return nil, errors.New("raw Responses agent mode metadata must be a string")
	}
	continuationID, continuationProvenance, err := rawAgentContinuation(metadata)
	if err != nil {
		return nil, errors.New("raw Responses continuation metadata is invalid")
	}
	var currentToolResults int64
	var currentToolContinuation *bool
	if raw, exists := metadata["kiro:proxy_current_tool_results"]; exists {
		currentToolResults, err = decodeNonNegativeInteger(raw)
		if err != nil {
			return nil, errors.New("raw Responses current tool results metadata must be a nonnegative integer")
		}
		functionCallOutputCount := countRawFunctionCallOutputs(input)
		if currentToolResults > int64(functionCallOutputCount) {
			return nil, errors.New("raw Responses current tool results exceed input function_call_output items")
		}
		hasCurrentToolResults := currentToolResults > 0
		currentToolContinuation = &hasCurrentToolResults
	}

	effort, err := rawResponsesReasoningEffort(root)
	if err != nil {
		return nil, errors.New("raw Responses reasoning effort is invalid")
	}
	effort, err = NormalizeReasoningEffort(effort)
	if err != nil {
		return nil, errors.New("raw Responses reasoning effort is unsupported")
	}

	hasImages, err := rawResponsesHasImages(input)
	if err != nil {
		return nil, err
	}

	agentMode = strings.TrimSpace(agentMode)
	continuationID = strings.TrimSpace(continuationID)
	return &ParsedConversation{
		ModelID:                     model,
		ConversationID:              conversationID,
		AgentMode:                   agentMode,
		AgentContinuationID:         continuationID,
		AgentContinuationProvenance: continuationProvenance,
		PromptAffinityKey:           conversationID,
		ContinuationStateKey:        buildContinuationStateKey(conversationID, agentMode, continuationID),
		ReasoningEffort:             effort,
		CurrentToolContinuation:     currentToolContinuation,
		RawResponsesHasImages:       hasImages,
	}, nil
}

// rawAgentContinuation reads Kiro's native continuation identity while
// retaining compatibility with the older private bridge metadata key.
func rawAgentContinuation(metadata map[string]json.RawMessage) (string, string, error) {
	var nativeID, proxyID string
	_, proxyContinuationPresent := metadata["kiro:proxy_continuation_id"]
	_, proxyToolResultsPresent := metadata["kiro:proxy_current_tool_results"]
	if raw, exists := metadata["kiro:agent_continuation_id"]; exists {
		value, err := decodeRawString(raw)
		if err != nil {
			return "", "", errors.New("native continuation is not a string")
		}
		nativeID = strings.TrimSpace(value)
	}
	if raw, exists := metadata["kiro:proxy_continuation_id"]; exists {
		value, err := decodeRawString(raw)
		if err != nil {
			return "", "", errors.New("proxy continuation is not a string")
		}
		proxyID = strings.TrimSpace(value)
	}
	if nativeID != "" && proxyID != "" && nativeID != proxyID {
		return "", "", errors.New("native and proxy continuation metadata conflict")
	}
	if nativeID != "" {
		return nativeID, AgentContinuationProvenanceNativeRaw, nil
	}
	if proxyContinuationPresent || proxyToolResultsPresent {
		return proxyID, AgentContinuationProvenanceProxyBridge, nil
	}
	// Raw Kiro Responses without either private bridge field follow the native
	// activity-driven contract, including when Kiro supplies no continuation.
	return "", AgentContinuationProvenanceNativeRaw, nil
}

func decodeRawJSONObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := opening.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("not an object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("invalid object key")
		}
		if _, exists := fields[key]; exists {
			return nil, errors.New("duplicate object key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	closeDelim, ok := closing.(json.Delim)
	if !ok || closeDelim != '}' {
		return nil, errors.New("invalid object ending")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("trailing JSON value")
	}
	return fields, nil
}

func requiredRawString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", errors.New("missing string")
	}
	return decodeRawString(raw)
}

func decodeRawString(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", errors.New("not a string")
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return "", err
	}
	return value, nil
}

func decodeNonNegativeInteger(raw json.RawMessage) (int64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0, errors.New("missing integer")
	}
	value, err := strconv.ParseInt(string(trimmed), 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("invalid nonnegative integer")
	}
	return value, nil
}

func countRawFunctionCallOutputs(input []json.RawMessage) int {
	count := 0
	for _, item := range input {
		fields, err := decodeRawJSONObject(item)
		if err != nil {
			continue
		}
		itemType, err := optionalRawString(fields, "type")
		if err == nil && itemType == "function_call_output" {
			count++
		}
	}
	return count
}

func rawResponsesReasoningEffort(root map[string]json.RawMessage) (string, error) {
	reasoningRaw, exists := root["reasoning"]
	if !exists || bytes.Equal(bytes.TrimSpace(reasoningRaw), []byte("null")) {
		return "", nil
	}
	reasoning, err := decodeRawJSONObject(reasoningRaw)
	if err != nil {
		return "", err
	}
	effortRaw, exists := reasoning["effort"]
	if !exists {
		return "", nil
	}
	return decodeRawString(effortRaw)
}

func rawResponsesHasImages(input []json.RawMessage) (bool, error) {
	for _, item := range input {
		fields, err := decodeRawJSONObject(item)
		if err != nil {
			continue
		}
		itemType, err := optionalRawString(fields, "type")
		if err != nil {
			continue
		}
		if itemType == "input_image" {
			hasImage, err := rawInputImageHasURL(fields)
			if err != nil || hasImage {
				return hasImage, err
			}
			continue
		}
		isMessage := itemType == "message"
		if _, hasType := fields["type"]; !hasType {
			role, err := optionalRawString(fields, "role")
			if err == nil {
				switch role {
				case "user", "assistant", "system", "developer":
					isMessage = true
				}
			}
		}
		if !isMessage {
			continue
		}
		contentRaw, exists := fields["content"]
		if !exists {
			continue
		}
		content := bytes.TrimSpace(contentRaw)
		if len(content) > 0 && content[0] == '"' {
			continue
		}
		if len(content) == 0 || content[0] != '[' {
			return false, errors.New("raw Responses message content must be a string or block array")
		}
		var blocks []json.RawMessage
		if err := json.Unmarshal(content, &blocks); err != nil {
			return false, errors.New("raw Responses message content must be a string or block array")
		}
		for _, block := range blocks {
			blockFields, err := decodeRawJSONObject(block)
			if err != nil {
				continue
			}
			blockType, err := optionalRawString(blockFields, "type")
			if err != nil || blockType != "input_image" {
				continue
			}
			hasImage, err := rawInputImageHasURL(blockFields)
			if err != nil || hasImage {
				return hasImage, err
			}
		}
	}
	return false, nil
}

func rawInputImageHasURL(fields map[string]json.RawMessage) (bool, error) {
	raw, exists := fields["image_url"]
	if !exists {
		fileID, err := optionalRawString(fields, "file_id")
		if err != nil {
			return false, errors.New("raw Responses input_image file_id must be a string")
		}
		return strings.TrimSpace(fileID) != "", nil
	}
	url, err := decodeRawString(raw)
	if err != nil {
		return false, errors.New("raw Responses input_image image_url must be a string")
	}
	return strings.TrimSpace(url) != "", nil
}

func optionalRawString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, exists := fields[key]
	if !exists {
		return "", nil
	}
	return decodeRawString(raw)
}
