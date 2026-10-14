package converter

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// ParseKiroRawResponsesManualRouting extracts routing metadata from a raw
// Responses request that selects a locally configured model. The caller keeps
// and forwards body unchanged.
func ParseKiroRawResponsesManualRouting(body []byte) (*ParsedConversation, error) {
	root, err := decodeRawJSONObject(body)
	if err != nil {
		return nil, errors.New("raw Responses request must be one JSON object")
	}

	model, err := requiredRawString(root, "model")
	if err != nil || strings.TrimSpace(model) == "" || strings.EqualFold(strings.TrimSpace(model), "auto") {
		return nil, errors.New("raw Responses manual model must be a nonempty non-auto string")
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

	var conversationID, agentMode, continuationID string
	if metadataRaw, exists := root["metadata"]; exists {
		metadata, err := decodeRawJSONObject(metadataRaw)
		if err != nil {
			return nil, errors.New("raw Responses metadata must be an object")
		}
		conversationID, err = optionalRawString(metadata, "kiro:conversation_id")
		if err != nil {
			return nil, errors.New("raw Responses conversation metadata must be a string")
		}
		agentMode, err = optionalRawString(metadata, "kiro:agent_mode")
		if err != nil {
			return nil, errors.New("raw Responses agent mode metadata must be a string")
		}
		continuationID, err = optionalRawString(metadata, "kiro:proxy_continuation_id")
		if err != nil {
			return nil, errors.New("raw Responses continuation metadata must be a string")
		}
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

	conversationID = strings.TrimSpace(conversationID)
	agentMode = strings.TrimSpace(agentMode)
	continuationID = strings.TrimSpace(continuationID)
	return &ParsedConversation{
		ModelID:               model,
		ConversationID:        conversationID,
		AgentMode:             agentMode,
		AgentContinuationID:   continuationID,
		PromptAffinityKey:     conversationID,
		ContinuationStateKey:  buildContinuationStateKey(conversationID, agentMode, continuationID),
		ReasoningEffort:       effort,
		RawResponsesHasImages: hasImages,
	}, nil
}
