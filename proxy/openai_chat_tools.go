package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	json "github.com/goccy/go-json"
)

// openAIChatToolSummary contains only non-sensitive diagnostics about the tools
// in a serialized OpenAI Chat Completions request.
type openAIChatToolSummary struct {
	ToolCount   int                    `json:"tool_count"`
	ToolsSHA256 string                 `json:"tools_sha256"`
	Tools       []openAIChatToolDetail `json:"tools"`
}

// openAIChatToolDetail identifies one tool without retaining its definition.
type openAIChatToolDetail struct {
	Index                    int            `json:"index"`
	FunctionName             string         `json:"function_name"`
	DefinitionSHA256         string         `json:"definition_sha256"`
	ParametersSHA256         string         `json:"parameters_sha256"`
	ParametersBytes          int            `json:"parameters_bytes"`
	ParametersTopLevelObject bool           `json:"parameters_top_level_object"`
	SchemaFeatures           map[string]int `json:"schema_features,omitempty"`
}

// summarizeOpenAIChatTools extracts diagnostics from the final serialized
// OpenAI Chat Completions request body. It never retains messages, descriptions,
// schema contents, or credentials in its result.
func summarizeOpenAIChatTools(body []byte) (openAIChatToolSummary, error) {
	var summary openAIChatToolSummary

	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return summary, fmt.Errorf("decode OpenAI Chat request JSON: %w", err)
	}
	if request == nil {
		return summary, errors.New("OpenAI Chat request must be a JSON object")
	}

	rawTools, exists := request["tools"]
	if !exists {
		return summary, nil
	}
	trimmedTools := bytes.TrimSpace(rawTools)
	if len(trimmedTools) == 0 || trimmedTools[0] != '[' {
		return summary, errors.New("OpenAI Chat request tools must be a JSON array")
	}

	var tools []json.RawMessage
	if err := json.Unmarshal(rawTools, &tools); err != nil {
		return summary, fmt.Errorf("decode OpenAI Chat request tools: %w", err)
	}
	if tools == nil {
		return summary, errors.New("OpenAI Chat request tools must be a JSON array")
	}

	summary.ToolCount = len(tools)
	summary.ToolsSHA256 = sha256Hex(rawTools)
	summary.Tools = make([]openAIChatToolDetail, 0, len(tools))
	for i, rawTool := range tools {
		detail := openAIChatToolDetail{
			Index:            i,
			DefinitionSHA256: sha256Hex(rawTool),
			ParametersSHA256: sha256Hex(nil),
		}

		var tool map[string]json.RawMessage
		if err := json.Unmarshal(rawTool, &tool); err == nil && tool != nil {
			var toolType string
			if json.Unmarshal(tool["type"], &toolType) == nil && toolType == "function" {
				var function map[string]json.RawMessage
				if json.Unmarshal(tool["function"], &function) == nil && function != nil {
					_ = json.Unmarshal(function["name"], &detail.FunctionName)
					if rawParameters, present := function["parameters"]; present {
						detail.ParametersSHA256 = sha256Hex(rawParameters)
						detail.ParametersBytes = len(rawParameters)
						var parameters map[string]json.RawMessage
						detail.ParametersTopLevelObject = json.Unmarshal(rawParameters, &parameters) == nil && parameters != nil
						if detail.ParametersTopLevelObject {
							detail.SchemaFeatures = openAIChatSchemaFeatures(rawParameters)
						}
					}
				}
			}
		}
		summary.Tools = append(summary.Tools, detail)
	}
	return summary, nil
}

// openAIChatSchemaFeatures records structure only; it never includes property
// names, descriptions, enum values, defaults, or other schema contents.
func openAIChatSchemaFeatures(raw json.RawMessage) map[string]int {
	var schema interface{}
	if json.Unmarshal(raw, &schema) != nil {
		return nil
	}
	features := make(map[string]int)
	var visit func(interface{})
	visit = func(value interface{}) {
		object, ok := value.(map[string]interface{})
		if !ok {
			return
		}
		for _, key := range []string{"anyOf", "oneOf", "allOf", "not", "$ref", "patternProperties", "additionalProperties", "format", "default"} {
			if _, exists := object[key]; exists {
				features[key]++
			}
		}
		if types, ok := object["type"].([]interface{}); ok && len(types) > 1 {
			features["type_array"]++
		}
		if not, ok := object["not"].(map[string]interface{}); ok && len(not) == 0 && len(object) == 1 {
			features["always_false_schema"]++
		}
		for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
			if children, ok := object[key].(map[string]interface{}); ok {
				for _, child := range children {
					visit(child)
				}
			}
		}
		for _, key := range []string{"items", "additionalProperties", "unevaluatedProperties", "unevaluatedItems", "propertyNames", "contains", "if", "then", "else", "not"} {
			visit(object[key])
		}
		for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
			if branches, ok := object[key].([]interface{}); ok {
				for _, branch := range branches {
					visit(branch)
				}
			}
		}
	}
	visit(schema)
	if len(features) == 0 {
		return nil
	}
	return features
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// captureOpenAIChatToolParameters saves only names and parameter schemas from
// a rejected outbound request. The caller limits this to gateway error 11129.
func captureOpenAIChatToolParameters(body []byte, traceID string, attempt int, directory string) (string, error) {
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return "", fmt.Errorf("decode OpenAI Chat request: %w", err)
	}
	var rawTools []json.RawMessage
	if err := json.Unmarshal(request["tools"], &rawTools); err != nil {
		return "", fmt.Errorf("decode OpenAI Chat tools: %w", err)
	}
	type capturedTool struct {
		Index      int             `json:"index"`
		Name       string          `json:"name"`
		Parameters json.RawMessage `json:"parameters"`
	}
	captured := struct {
		TraceID string         `json:"trace_id"`
		Attempt int            `json:"attempt"`
		Tools   []capturedTool `json:"tools"`
	}{TraceID: traceID, Attempt: attempt, Tools: make([]capturedTool, 0, len(rawTools))}
	for index, rawTool := range rawTools {
		var tool struct {
			Function struct {
				Name       string          `json:"name"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if err := json.Unmarshal(rawTool, &tool); err != nil {
			return "", fmt.Errorf("decode tool %d: %w", index, err)
		}
		captured.Tools = append(captured.Tools, capturedTool{Index: index, Name: tool.Function.Name, Parameters: tool.Function.Parameters})
	}
	encoded, err := json.Marshal(captured)
	if err != nil {
		return "", fmt.Errorf("encode tool capture: %w", err)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", fmt.Errorf("create tool capture directory: %w", err)
	}
	traceName := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, traceID)
	if traceName == "" {
		traceName = "unknown"
	}
	absoluteDirectory, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve tool capture directory: %w", err)
	}
	file, err := os.CreateTemp(absoluteDirectory, fmt.Sprintf("openai-chat-11129-%s-attempt-%d-*.json", traceName, attempt))
	if err != nil {
		return "", fmt.Errorf("create tool capture: %w", err)
	}
	path := file.Name()
	_, writeErr := file.Write(encoded)
	closeErr := file.Close()
	if writeErr != nil {
		return path, fmt.Errorf("write tool capture: %w", writeErr)
	}
	if closeErr != nil {
		return path, fmt.Errorf("close tool capture: %w", closeErr)
	}
	return path, nil
}
