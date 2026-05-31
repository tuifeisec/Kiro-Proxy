// tool_convert.go
// 工具定义格式转换：Kiro toolSpecification → OpenAI function / Anthropic tool
// 只做纯转换，不涉及缓存逻辑（缓存在 tool_cache.go）

package converter

import (
	"sort"

	json "github.com/goccy/go-json"
)

const (
	maxTools   = 128
	maxDescLen = 10000
)

// toOpenAITools 将 Kiro 原始工具定义转换为 OpenAI function 格式
func toOpenAITools(rawTools []json.RawMessage) []OpenAITool {
	specs := sortedToolSpecs(rawTools, false)
	result := make([]OpenAITool, 0, len(specs))
	for _, spec := range specs {
		result = append(result, OpenAITool{
			Type: "function",
			Function: OpenAIFuncDef{
				Name:        spec.name,
				Description: truncateDesc(spec.description),
				Parameters:  spec.schema,
			},
		})
	}
	return result
}

// toAnthropicTools 将 Kiro 原始工具定义转换为 Anthropic tool 格式
func toAnthropicTools(rawTools []json.RawMessage) []AnthropicTool {
	specs := sortedToolSpecs(rawTools, true)
	result := make([]AnthropicTool, 0, len(specs))
	for _, spec := range specs {
		if spec.isWebSearch {
			result = append(result, AnthropicTool{
				Name:        "web_search",
				Description: "Search the web for current information",
				InputSchema: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			})
			continue
		}
		result = append(result, AnthropicTool{
			Name:        spec.name,
			Description: truncateDesc(spec.description),
			InputSchema: spec.schema,
		})
	}
	return result
}

func sortedToolSpecs(rawTools []json.RawMessage, includeWebSearch bool) []toolSpec {
	specs := make([]toolSpec, 0, len(rawTools))
	for i, raw := range rawTools {
		if i >= maxTools {
			break
		}
		spec, ok := extractToolSpec(raw)
		if !ok {
			continue
		}
		if spec.isWebSearch && !includeWebSearch {
			continue
		}
		specs = append(specs, spec)
	}
	sort.SliceStable(specs, func(i, j int) bool {
		return specs[i].stableName() < specs[j].stableName()
	})
	return specs
}

// toolSpec 是从 Kiro 工具定义中提取的中间结构
type toolSpec struct {
	name        string
	description string
	schema      json.RawMessage // 保留原始 JSON 字节，避免 interface{} 深层分配
	isWebSearch bool
}

func (s toolSpec) stableName() string {
	if s.isWebSearch {
		return "web_search"
	}
	return s.name
}

// extractToolSpec 从单条 Kiro 工具 JSON 中提取规范字段
func extractToolSpec(raw json.RawMessage) (toolSpec, bool) {
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return toolSpec{}, false
	}

	// webSearchTool 特殊处理
	if _, ok := wrapper["webSearchTool"]; ok {
		return toolSpec{isWebSearch: true}, true
	}

	specRaw, ok := wrapper["toolSpecification"]
	if !ok {
		return toolSpec{}, false
	}

	var spec struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema struct {
			JSON json.RawMessage `json:"json"`
		} `json:"inputSchema"`
	}
	if err := json.Unmarshal(specRaw, &spec); err != nil {
		return toolSpec{}, false
	}
	if spec.Name == "" {
		return toolSpec{}, false
	}

	var schema json.RawMessage
	if len(spec.InputSchema.JSON) > 0 {
		// 验证 JSON 有效性，但保留原始字节
		var js json.RawMessage
		if json.Unmarshal(spec.InputSchema.JSON, &js) == nil {
			schema = spec.InputSchema.JSON
		}
	}
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object","properties":{}}`)
	}

	return toolSpec{
		name:        spec.Name,
		description: spec.Description,
		schema:      schema,
	}, true
}

func truncateDesc(s string) string {
	if len(s) <= maxDescLen {
		return s
	}
	return s[:maxDescLen] + "..."
}
