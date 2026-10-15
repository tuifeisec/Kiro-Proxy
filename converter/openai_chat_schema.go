package converter

import (
	"bytes"
	"encoding/json"
	"io"

	gojson "github.com/goccy/go-json"
)

// NormalizeOpenAIChatToolParameters applies the established Chat schema
// compatibility rules without changing the input bytes when no change is needed.
func NormalizeOpenAIChatToolParameters(name string, raw gojson.RawMessage) (gojson.RawMessage, bool) {
	var schema interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&schema) != nil {
		return raw, false
	}
	var trailing interface{}
	if decoder.Decode(&trailing) != io.EOF {
		return raw, false
	}
	referenceRepaired := repairKiroTodoListReference(name, schema)
	// Simplifying anyOf can change array indexes or remove an intermediate
	// node named by a JSON Pointer. Keep referenced schemas intact until
	// their pointers can be rewritten together with the target nodes.
	if hasOpenAIChatSchemaReference(schema) && !referenceRepaired {
		return raw, false
	}
	result, modified := schema, referenceRepaired
	if !hasOpenAIChatSchemaReference(schema) {
		result, modified = simplifyOpenAIChatSchema(schema)
	}
	if !modified {
		return raw, false
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return raw, false
	}
	return gojson.RawMessage(encoded), true
}

// normalizeOpenAIChatTools preserves cached provider tools unless their
// parameters change, then copies the slice before replacing those parameters.
func normalizeOpenAIChatTools(tools []OpenAITool) ([]OpenAITool, bool) {
	var normalized []OpenAITool
	for i, tool := range tools {
		raw, ok := tool.Function.Parameters.(gojson.RawMessage)
		if !ok {
			// The converter stores RawMessage today; preserve other forms.
			if rawOther, yes := tool.Function.Parameters.([]byte); yes {
				raw = rawOther
			} else {
				continue
			}
		}
		parameters, changed := NormalizeOpenAIChatToolParameters(tool.Function.Name, raw)
		if !changed {
			continue
		}
		if normalized == nil {
			normalized = append([]OpenAITool(nil), tools...)
		}
		normalized[i].Function.Parameters = parameters
	}
	if normalized == nil {
		return tools, false
	}
	return normalized, true
}

// Kiro currently emits this stale todo_list pointer after flattening tasks
// from a nested anyOf into a nullable array. Repair only the observed shape.
func repairKiroTodoListReference(toolName string, value interface{}) bool {
	if toolName != "todo_list" {
		return false
	}
	root, ok := value.(map[string]interface{})
	if !ok {
		return false
	}
	properties, ok := root["properties"].(map[string]interface{})
	if !ok {
		return false
	}
	tasks, ok := properties["tasks"].(map[string]interface{})
	if !ok {
		return false
	}
	if _, hasAnyOf := tasks["anyOf"]; hasAnyOf {
		return false
	}
	if _, hasItems := tasks["items"].(map[string]interface{}); !hasItems {
		return false
	}
	newTasks, ok := properties["new_tasks"].(map[string]interface{})
	if !ok {
		return false
	}
	newItems, ok := newTasks["items"].(map[string]interface{})
	if !ok || newItems["$ref"] != "#/properties/tasks/anyOf/0/anyOf/1/items" {
		return false
	}
	newItems["$ref"] = "#/properties/tasks/items"
	return true
}

func hasOpenAIChatSchemaReference(value interface{}) bool {
	switch node := value.(type) {
	case map[string]interface{}:
		if _, exists := node["$ref"]; exists {
			return true
		}
		for _, child := range node {
			if hasOpenAIChatSchemaReference(child) {
				return true
			}
		}
	case []interface{}:
		for _, child := range node {
			if hasOpenAIChatSchemaReference(child) {
				return true
			}
		}
	}
	return false
}

func simplifyOpenAIChatSchema(value interface{}) (interface{}, bool) {
	object, ok := value.(map[string]interface{})
	if !ok {
		return value, false
	}
	changed := false
	for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
		children, ok := object[key].(map[string]interface{})
		if !ok {
			continue
		}
		for name, child := range children {
			updated, childChanged := simplifyOpenAIChatSchema(child)
			if childChanged {
				children[name] = updated
				changed = true
			}
		}
	}
	for _, key := range []string{"items", "additionalProperties", "unevaluatedProperties", "unevaluatedItems", "propertyNames", "contains", "contentSchema", "if", "then", "else", "not"} {
		if child, exists := object[key]; exists {
			updated, childChanged := simplifyOpenAIChatSchema(child)
			if childChanged {
				object[key] = updated
				changed = true
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		branches, ok := object[key].([]interface{})
		if !ok {
			continue
		}
		updated := make([]interface{}, 0, len(branches))
		removedFalse := false
		for _, branch := range branches {
			simplified, branchChanged := simplifyOpenAIChatSchema(branch)
			if branchChanged {
				changed = true
			}
			if key == "anyOf" && isFalseJSONSchema(simplified) {
				removedFalse = true
				continue
			}
			updated = append(updated, simplified)
		}
		if len(updated) == 0 {
			// Keep a wholly false schema instead of inventing a replacement.
			continue
		}
		if removedFalse {
			changed = true
		}
		object[key] = updated
		if key == "anyOf" && len(updated) == 1 && len(object) == 1 {
			return updated[0], true
		}
	}
	return object, changed
}

func isFalseJSONSchema(value interface{}) bool {
	object, ok := value.(map[string]interface{})
	if !ok || len(object) != 1 {
		return false
	}
	not, ok := object["not"].(map[string]interface{})
	return ok && len(not) == 0
}
