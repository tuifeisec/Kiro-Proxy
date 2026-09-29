package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

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
	Index                    int    `json:"index"`
	FunctionName             string `json:"function_name"`
	DefinitionSHA256         string `json:"definition_sha256"`
	ParametersSHA256         string `json:"parameters_sha256"`
	ParametersBytes          int    `json:"parameters_bytes"`
	ParametersTopLevelObject bool   `json:"parameters_top_level_object"`
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
					}
				}
			}
		}
		summary.Tools = append(summary.Tools, detail)
	}
	return summary, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
