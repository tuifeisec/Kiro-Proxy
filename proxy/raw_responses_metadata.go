package proxy

import (
	"bytes"
	"fmt"
	"strings"

	json "github.com/goccy/go-json"
)

// stripRawUpstreamKiroContext runs only after local routing has consumed the
// Kiro context. Capture and official passthrough retain the native fields.
func stripRawUpstreamKiroContext(body []byte) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("parse raw Responses JSON: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("raw Responses body must be a JSON object")
	}
	changed := false
	for _, key := range []string{"origin", "profileArn"} {
		if _, exists := root[key]; exists {
			delete(root, key)
			changed = true
		}
	}
	if raw, exists := root["metadata"]; exists {
		var metadata map[string]json.RawMessage
		if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
				return nil, fmt.Errorf("raw Responses metadata must be an object or null")
			}
		}
		metadataChanged := len(metadata) == 0
		for key := range metadata {
			if strings.HasPrefix(key, "kiro:") {
				delete(metadata, key)
				metadataChanged = true
			}
		}
		if metadataChanged {
			if len(metadata) == 0 {
				delete(root, "metadata")
			} else {
				encoded, err := json.Marshal(metadata)
				if err != nil {
					return nil, fmt.Errorf("encode raw Responses metadata: %w", err)
				}
				root["metadata"] = encoded
			}
			changed = true
		}
	}
	if raw, exists := root["reasoning"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var reasoning map[string]json.RawMessage
		if err := json.Unmarshal(raw, &reasoning); err != nil || reasoning == nil {
			return nil, fmt.Errorf("raw Responses reasoning must be an object or null")
		}
		if _, exists := reasoning["type"]; exists {
			delete(reasoning, "type")
			if len(reasoning) == 0 {
				delete(root, "reasoning")
			} else {
				encoded, err := json.Marshal(reasoning)
				if err != nil {
					return nil, fmt.Errorf("encode raw Responses reasoning: %w", err)
				}
				root["reasoning"] = encoded
			}
			changed = true
		}
	}
	if !changed {
		return body, nil
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode raw Responses body: %w", err)
	}
	return encoded, nil
}
