package converter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

	gojson "github.com/goccy/go-json"
)

const (
	kiroWorkflowNamePattern   = `^[^\p{Cc}\p{Zl}\p{Zp}"]*$`
	openAIWorkflowNamePattern = "^[^\u0000-\u001f\u007f-\u009f\u2028\u2029\"]*$"
)

// canonicalOpenAIResponsesToolJSON 保留数字词法并排序对象键；数组顺序不变。
// 仅用于 Responses，避免改变其他协议已有的缓存身份。
func canonicalOpenAIResponsesToolJSON(raw gojson.RawMessage) []byte {
	var value interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return bytes.TrimSpace(raw)
	}
	var trailing interface{}
	if decoder.Decode(&trailing) != io.EOF {
		return bytes.TrimSpace(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return bytes.TrimSpace(raw)
	}
	return encoded
}

// hashOpenAIResponsesRawTools 保留源数组顺序，避免截断或同名工具排序前误取缓存。
func hashOpenAIResponsesRawTools(raw []gojson.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	hash := sha256.New()
	for _, tool := range raw {
		hash.Write(canonicalOpenAIResponsesToolJSON(tool))
		hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}

// hashOpenAIResponsesTools 保持既有 provider hash 的字段、顺序和摘要格式，
// 仅在 JSON 规范化时保留精确数字。
func hashOpenAIResponsesTools(tools []OpenAITool) string {
	if len(tools) == 0 {
		return ""
	}
	hash := sha256.New()
	for _, tool := range tools {
		encoded, err := gojson.Marshal(tool)
		if err != nil {
			return ""
		}
		canonical := canonicalOpenAIResponsesToolJSON(encoded)
		if len(canonical) == 0 {
			return ""
		}
		hash.Write(canonical)
		hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}

// normalizeOpenAIResponsesTools 将已确认的 run_workflow 正则写为显式字符范围，
// 保留原字符限制，并避免依赖上游对 Unicode 属性转义的支持。
func normalizeOpenAIResponsesTools(tools []OpenAITool) ([]OpenAITool, bool) {
	var normalized []OpenAITool
	changed := false
	for i, tool := range tools {
		if tool.Function.Name != "run_workflow" {
			continue
		}
		raw, ok := tool.Function.Parameters.(gojson.RawMessage)
		if !ok {
			if other, yes := tool.Function.Parameters.([]byte); yes {
				raw = other
			} else {
				continue
			}
		}
		var schema interface{}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if decoder.Decode(&schema) != nil || !rewriteOpenAIResponsesSchemaPatterns(schema) {
			continue
		}
		encoded, err := json.Marshal(schema)
		if err != nil {
			continue
		}
		if !changed {
			normalized = append([]OpenAITool(nil), tools...)
			changed = true
		}
		normalized[i].Function.Parameters = gojson.RawMessage(encoded)
	}
	if !changed {
		return tools, false
	}
	return normalized, true
}

func rewriteOpenAIResponsesSchemaPatterns(value interface{}) bool {
	if schemas, ok := value.([]interface{}); ok {
		changed := false
		for _, schema := range schemas {
			if rewriteOpenAIResponsesSchemaPatterns(schema) {
				changed = true
			}
		}
		return changed
	}
	object, ok := value.(map[string]interface{})
	if !ok {
		return false
	}
	changed := false
	if pattern, ok := object["pattern"].(string); ok && pattern == kiroWorkflowNamePattern {
		object["pattern"] = openAIWorkflowNamePattern
		changed = true
	}
	for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
		if children, ok := object[key].(map[string]interface{}); ok {
			for _, child := range children {
				if rewriteOpenAIResponsesSchemaPatterns(child) {
					changed = true
				}
			}
		}
	}
	for _, key := range []string{"items", "additionalProperties", "unevaluatedProperties", "unevaluatedItems", "propertyNames", "contains", "contentSchema", "if", "then", "else", "not"} {
		if rewriteOpenAIResponsesSchemaPatterns(object[key]) {
			changed = true
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		if branches, ok := object[key].([]interface{}); ok {
			for _, branch := range branches {
				if rewriteOpenAIResponsesSchemaPatterns(branch) {
					changed = true
				}
			}
		}
	}
	return changed
}
