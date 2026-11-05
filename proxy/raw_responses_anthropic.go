package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

func (f *Forwarder) prepareRawResponsesAnthropicBodyMeasured(body []byte, pre *Preflight, model *store.Model) ([]byte, time.Duration, error) {
	started := time.Now()
	prepared, err := prepareRawResponsesAnthropicBody(body, pre, model)
	return prepared, time.Since(started), err
}

// prepareRawResponsesAnthropicBody converts the Responses envelope to the
// Anthropic Messages schema. Unsupported input item kinds fail here instead of
// being silently dropped.
func prepareRawResponsesAnthropicBody(body []byte, pre *Preflight, model *store.Model) ([]byte, error) {
	if model == nil {
		return nil, fmt.Errorf("raw Responses Anthropic model is missing")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		return nil, fmt.Errorf("parse raw Responses JSON: %w", err)
	}
	var input json.RawMessage
	if raw := root["input"]; len(raw) > 0 {
		input = raw
	} else {
		return nil, fmt.Errorf("raw Responses input is required for Anthropic")
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(input, &messages); err != nil {
		var text string
		if err := json.Unmarshal(input, &text); err != nil {
			return nil, fmt.Errorf("raw Responses input must be an array or string: %w", err)
		}
		messages = []map[string]json.RawMessage{{"role": json.RawMessage(`"user"`), "content": json.RawMessage(mustJSON(text))}}
	}
	anthropicMessages := make([]map[string]interface{}, 0, len(messages))
	var system string
	if raw := root["instructions"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &system); err != nil {
			return nil, fmt.Errorf("raw Responses instructions must be a string: %w", err)
		}
	}
	for _, message := range messages {
		itemType := stringField(message, "type")
		if itemType == "input_text" || itemType == "input_image" {
			convertedContent := anthropicContent([]interface{}{map[string]interface{}{
				"type":      itemType,
				"text":      stringField(message, "text"),
				"image_url": stringField(message, "image_url"),
			}})
			if convertedContent == nil {
				return nil, fmt.Errorf("raw Responses %s cannot be converted to Anthropic", itemType)
			}
			anthropicMessages = append(anthropicMessages, map[string]interface{}{"role": "user", "content": convertedContent})
			continue
		}
		if itemType == "function_call" {
			id, err := rawStringField(message, "call_id")
			if err != nil {
				return nil, fmt.Errorf("raw Responses function call: %w", err)
			}
			name, err := rawStringField(message, "name")
			if err != nil {
				return nil, fmt.Errorf("raw Responses function call: %w", err)
			}
			arguments := decodeRawValue(message["arguments"])
			if text, ok := arguments.(string); ok {
				var decoded interface{}
				if json.Unmarshal([]byte(text), &decoded) == nil {
					arguments = decoded
				}
			}
			anthropicMessages = append(anthropicMessages, map[string]interface{}{"role": "assistant", "content": []interface{}{map[string]interface{}{"type": "tool_use", "id": id, "name": name, "input": arguments}}})
			continue
		}
		if itemType == "function_call_output" {
			callID, err := rawStringField(message, "call_id")
			if err != nil {
				return nil, fmt.Errorf("raw Responses function call output: %w", err)
			}
			anthropicMessages = append(anthropicMessages, map[string]interface{}{"role": "user", "content": []interface{}{map[string]interface{}{"type": "tool_result", "tool_use_id": callID, "content": decodeRawValue(message["output"])}}})
			continue
		}
		role, err := rawStringField(message, "role")
		if err != nil {
			role = "user"
		}
		content := decodeRawValue(message["content"])
		switch role {
		case "system", "developer":
			text, ok := content.(string)
			if !ok {
				return nil, fmt.Errorf("raw Responses %s content must be text for Anthropic", role)
			}
			if system != "" {
				system += "\n"
			}
			system += text
		case "assistant":
			converted, err := anthropicAssistantContent(message, content)
			if err != nil {
				return nil, err
			}
			anthropicMessages = append(anthropicMessages, map[string]interface{}{"role": "assistant", "content": converted})
		case "function_call_output":
			callID, err := rawStringField(message, "call_id")
			if err != nil {
				return nil, err
			}
			anthropicMessages = append(anthropicMessages, map[string]interface{}{"role": "user", "content": []interface{}{map[string]interface{}{"type": "tool_result", "tool_use_id": callID, "content": content}}})
		default:
			convertedContent := anthropicContent(content)
			if convertedContent == nil {
				return nil, fmt.Errorf("raw Responses message content contains blocks unsupported by Anthropic")
			}
			anthropicMessages = append(anthropicMessages, map[string]interface{}{"role": "user", "content": convertedContent})
		}
	}
	if len(anthropicMessages) == 0 {
		return nil, fmt.Errorf("raw Responses request contains no Anthropic messages")
	}
	request := map[string]interface{}{"model": model.ModelID, "messages": anthropicMessages, "max_tokens": 16384, "stream": rawBoolField(root, "stream", false)}
	if system != "" {
		request["system"] = system
	}
	if raw := root["max_output_tokens"]; len(raw) > 0 {
		var value int
		if json.Unmarshal(raw, &value) != nil || value <= 0 {
			return nil, fmt.Errorf("raw Responses max_output_tokens must be a positive integer")
		}
		request["max_tokens"] = value
	}
	if raw := root["tools"]; len(raw) > 0 {
		tools, err := responsesToolsToAnthropic(raw)
		if err != nil {
			return nil, err
		}
		request["tools"] = tools
	}
	if raw := root["tool_choice"]; len(raw) > 0 {
		choice, err := responsesToolChoiceToAnthropic(raw)
		if err != nil {
			return nil, err
		}
		if choice != nil {
			request["tool_choice"] = choice
		}
	}
	effort, err := rawAnthropicWireEffort(root, pre, model)
	if err != nil {
		return nil, err
	}
	if err := applyRawResponsesAnthropicThinking(request, model, effort); err != nil {
		return nil, err
	}
	return json.Marshal(request)
}
func mustJSON(value string) []byte { encoded, _ := json.Marshal(value); return encoded }
func rawBoolField(root map[string]json.RawMessage, name string, fallback bool) bool {
	var value bool
	if json.Unmarshal(root[name], &value) != nil {
		return fallback
	}
	return value
}
func rawStringField(fields map[string]json.RawMessage, name string) (string, error) {
	var value string
	if json.Unmarshal(fields[name], &value) != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must be a nonempty string", name)
	}
	return value, nil
}
func anthropicContent(content interface{}) interface{} {
	parts, ok := content.([]interface{})
	if !ok {
		return content
	}
	converted := make([]interface{}, 0, len(parts))
	for _, part := range parts {
		object, ok := part.(map[string]interface{})
		if !ok {
			return nil
		}
		switch object["type"] {
		case "text", "input_text":
			text, ok := object["text"].(string)
			if !ok {
				return nil
			}
			converted = append(converted, map[string]interface{}{"type": "text", "text": text})
		case "image_url", "input_image":
			var url string
			if object["type"] == "input_image" {
				value, ok := object["image_url"].(string)
				if !ok || strings.TrimSpace(value) == "" {
					return nil
				}
				url = value
			} else {
				image, ok := object["image_url"].(map[string]interface{})
				if !ok {
					return nil
				}
				value, ok := image["url"].(string)
				if !ok {
					return nil
				}
				url = value
			}
			url = strings.TrimSpace(url)
			if !strings.HasPrefix(url, "data:") {
				return nil
			}
			media, data, ok := splitDataURL(url)
			if !ok {
				return nil
			}
			converted = append(converted, map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "base64", "media_type": media, "data": data}})
		default:
			return nil
		}
	}
	return converted
}
func splitDataURL(value string) (string, string, bool) {
	if !strings.HasPrefix(value, "data:") {
		return "", "", false
	}
	parts := strings.SplitN(value[5:], ",", 2)
	if len(parts) != 2 || !strings.HasSuffix(parts[0], ";base64") {
		return "", "", false
	}
	return strings.TrimSuffix(parts[0], ";base64"), parts[1], true
}
func encodeRawArguments(value interface{}) (string, error) {
	if value == nil {
		return "{}", nil
	}
	if text, ok := value.(string); ok {
		var decoded interface{}
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			return "", fmt.Errorf("Anthropic tool input must be valid JSON: %w", err)
		}
		value = decoded
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode Anthropic tool input: %w", err)
	}
	return string(encoded), nil
}
func anthropicAssistantContent(message map[string]json.RawMessage, content interface{}) (interface{}, error) {
	blocks := make([]interface{}, 0, 2)
	if text, ok := content.(string); ok && text != "" {
		blocks = append(blocks, map[string]interface{}{"type": "text", "text": text})
	}
	var calls []map[string]json.RawMessage
	if raw := message["tool_calls"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &calls); err != nil {
			return nil, fmt.Errorf("raw Responses assistant tool calls: %w", err)
		}
	}
	for _, call := range calls {
		id, err := rawStringField(call, "id")
		if err != nil {
			return nil, err
		}
		var function map[string]json.RawMessage
		if err := json.Unmarshal(call["function"], &function); err != nil {
			return nil, fmt.Errorf("assistant tool call function: %w", err)
		}
		name, err := rawStringField(function, "name")
		if err != nil {
			return nil, err
		}
		input := decodeRawValue(function["arguments"])
		if argument, ok := input.(string); ok {
			var decoded interface{}
			if json.Unmarshal([]byte(argument), &decoded) == nil {
				input = decoded
			}
		}
		blocks = append(blocks, map[string]interface{}{"type": "tool_use", "id": id, "name": name, "input": input})
	}
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]interface{}{"type": "text", "text": " "})
	}
	return blocks, nil
}
func responsesToolsToAnthropic(raw json.RawMessage) ([]map[string]interface{}, error) {
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, fmt.Errorf("raw Responses tools must be an array: %w", err)
	}
	result := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		var typ string
		_ = json.Unmarshal(tool["type"], &typ)
		if typ != "function" {
			return nil, fmt.Errorf("raw Responses tool type %q is unsupported by Anthropic", typ)
		}
		definition := tool
		if nested := tool["function"]; len(nested) > 0 {
			if err := json.Unmarshal(nested, &definition); err != nil {
				return nil, err
			}
		}
		name, err := rawStringField(definition, "name")
		if err != nil {
			return nil, err
		}
		inputSchema := decodeRawValue(definition["parameters"])
		if inputSchema == nil {
			inputSchema = map[string]interface{}{"type": "object"}
		}
		description := stringField(definition, "description")
		result = append(result, map[string]interface{}{"name": name, "description": description, "input_schema": inputSchema})
	}
	return result, nil
}
func stringField(fields map[string]json.RawMessage, name string) string {
	var value string
	_ = json.Unmarshal(fields[name], &value)
	return value
}
func responsesToolChoiceToAnthropic(raw json.RawMessage) (interface{}, error) {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		switch value {
		case "auto":
			return map[string]string{"type": "auto"}, nil
		case "required":
			return map[string]string{"type": "any"}, nil
		case "none":
			return nil, nil
		default:
			return nil, fmt.Errorf("raw Responses tool_choice %q is unsupported by Anthropic", value)
		}
	}
	var choice map[string]json.RawMessage
	if err := json.Unmarshal(raw, &choice); err != nil {
		return nil, err
	}
	name, err := rawStringField(choice, "name")
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"type": "tool", "name": name}, nil
}
func adaptRawAnthropicResponse(ctx context.Context, resp *http.Response) (*http.Response, error) {
	if resp == nil || resp.StatusCode >= http.StatusBadRequest || resp.Body == nil {
		return resp, nil
	}
	if isSSEResponse(resp) {
		if ctx == nil {
			ctx = context.Background()
		}
		resp.Body = newRawChatResponsesLazyBody(ctx, resp.Body, func(_ context.Context, source io.ReadCloser, writer *io.PipeWriter) error {
			return copyAnthropicStreamAsResponses(writer, source)
		})
		resp.Header = adaptedRawChatResponseHeaders(resp.Header, true)
	} else {
		if ctx == nil {
			ctx = context.Background()
		}
		resp.Body = newRawChatResponsesLazyBody(ctx, resp.Body, func(_ context.Context, source io.ReadCloser, writer *io.PipeWriter) error {
			body, err := convertAnthropicJSONResponse(source)
			if err != nil {
				return err
			}
			_, err = writer.Write(body)
			return err
		})
		resp.Header = adaptedRawChatResponseHeaders(resp.Header, false)
	}
	resp.ContentLength = -1
	resp.TransferEncoding = nil
	resp.Trailer = nil
	return resp, nil
}
func anthropicResponsesUsage(usage localCompatUsage) map[string]interface{} {
	return map[string]interface{}{
		"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens,
		"total_tokens": usage.InputTokens + usage.OutputTokens,
		"input_tokens_details": map[string]int{
			"cached_tokens":      usage.CacheReadTokens,
			"cache_write_tokens": usage.CacheWriteTokens,
		},
		"cache_creation_input_tokens": usage.CacheWriteTokens,
	}
}

func anthropicStreamError(raw json.RawMessage) map[string]string {
	var detail struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &detail)
	message := strings.TrimSpace(detail.Message)
	if len(message) > 800 {
		message = message[:800]
	}
	if message == "" {
		message = "Anthropic upstream returned an error"
	}
	result := map[string]string{"message": message}
	if detail.Type != "" {
		result["type"] = detail.Type
	}
	if detail.Code != "" {
		result["code"] = detail.Code
	}
	return result
}

func writeAnthropicStreamFailure(write func(string, interface{}) error, responseID, model string, raw json.RawMessage) error {
	return write("response.failed", map[string]interface{}{
		"type": "response.failed",
		"response": map[string]interface{}{
			"id": responseID, "object": "response", "status": "failed", "model": model,
			"error": anthropicStreamError(raw),
		},
	})
}

func copyAnthropicStreamAsResponses(writer io.Writer, source io.Reader) error {
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64*1024), localCompatBodyLimit)
	responseID, model := "resp_anthropic", ""
	created, terminal := false, false
	toolIDs := make(map[int]string)
	toolNames := make(map[int]string)
	toolArguments := make(map[int]*strings.Builder)
	var usage localCompatUsage
	stopReason := "end_turn"
	write := func(event string, payload interface{}) error {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event, encoded)
		return err
	}
	ensure := func() error {
		if created {
			return nil
		}
		created = true
		return write("response.created", map[string]interface{}{"type": "response.created", "response": map[string]interface{}{"id": responseID, "object": "response", "status": "in_progress", "model": model}})
	}
	fail := func(raw json.RawMessage, cause error) error {
		if err := ensure(); err != nil {
			return err
		}
		if err := writeAnthropicStreamFailure(write, responseID, model, raw); err != nil {
			return err
		}
		return cause
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			return fail(nil, fmt.Errorf("Anthropic stream contains unsupported SSE line"))
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fail(nil, fmt.Errorf("parse Anthropic SSE event: %w", err))
		}
		var typ string
		if err := json.Unmarshal(event["type"], &typ); err != nil || typ == "" {
			return fail(nil, fmt.Errorf("Anthropic SSE event type is missing"))
		}
		if typ == "error" {
			return fail(event["error"], fmt.Errorf("Anthropic upstream returned an error: %s", anthropicStreamError(event["error"])["message"]))
		}
		switch typ {
		case "message_start":
			var msg struct {
				ID    string          `json:"id"`
				Model string          `json:"model"`
				Usage json.RawMessage `json:"usage"`
			}
			if err := json.Unmarshal(event["message"], &msg); err != nil {
				return fail(nil, fmt.Errorf("parse Anthropic message_start: %w", err))
			}
			if msg.ID != "" {
				responseID = "resp_" + msg.ID
			}
			model = msg.Model
			mergeAnthropicUsage(&usage, msg.Usage)
			if err := ensure(); err != nil {
				return err
			}
		case "ping":
			if err := ensure(); err != nil {
				return err
			}
		case "content_block_start":
			var block struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(event["content_block"], &block); err != nil || block.Type == "" {
				return fail(nil, fmt.Errorf("malformed Anthropic content_block_start"))
			}
			if err := ensure(); err != nil {
				return err
			}
			idx := rawIndex(event)
			switch block.Type {
			case "text", "thinking":
			case "tool_use":
				if block.ID == "" || block.Name == "" {
					return fail(nil, fmt.Errorf("Anthropic tool_use is missing id or name"))
				}
				toolIDs[idx], toolNames[idx], toolArguments[idx] = block.ID, block.Name, &strings.Builder{}
				if err := write("response.output_item.added", map[string]interface{}{"type": "response.output_item.added", "item": map[string]interface{}{"type": "function_call", "id": block.ID, "call_id": block.ID, "name": block.Name, "arguments": "", "status": "in_progress"}}); err != nil {
					return err
				}
			default:
				return fail(nil, fmt.Errorf("Anthropic content block type %q is unsupported", block.Type))
			}
		case "content_block_delta":
			var delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
			}
			if err := json.Unmarshal(event["delta"], &delta); err != nil || delta.Type == "" {
				return fail(nil, fmt.Errorf("malformed Anthropic content_block_delta"))
			}
			if err := ensure(); err != nil {
				return err
			}
			idx := rawIndex(event)
			switch delta.Type {
			case "text_delta":
				if err := write("response.output_text.delta", map[string]interface{}{"type": "response.output_text.delta", "delta": delta.Text}); err != nil {
					return err
				}
			case "thinking_delta":
				if err := write("response.reasoning_summary_text.delta", map[string]interface{}{"type": "response.reasoning_summary_text.delta", "delta": delta.Thinking}); err != nil {
					return err
				}
			case "input_json_delta":
				if toolArguments[idx] == nil || toolIDs[idx] == "" {
					return fail(nil, fmt.Errorf("Anthropic tool arguments reference an unknown block"))
				}
				toolArguments[idx].WriteString(delta.PartialJSON)
				if err := write("response.function_call_arguments.delta", map[string]interface{}{"type": "response.function_call_arguments.delta", "item_id": toolIDs[idx], "call_id": toolIDs[idx], "delta": delta.PartialJSON}); err != nil {
					return err
				}
			case "signature_delta":
				_ = delta.Signature
			default:
				return fail(nil, fmt.Errorf("Anthropic content delta type %q is unsupported", delta.Type))
			}
		case "content_block_stop":
			if err := ensure(); err != nil {
				return err
			}
		case "message_delta":
			mergeAnthropicUsage(&usage, event["usage"])
			var delta struct {
				StopReason string `json:"stop_reason"`
			}
			if err := json.Unmarshal(event["delta"], &delta); err != nil {
				return fail(nil, fmt.Errorf("malformed Anthropic message_delta: %w", err))
			}
			if delta.StopReason != "" {
				stopReason = delta.StopReason
			}
			if err := ensure(); err != nil {
				return err
			}
		case "message_stop":
			if err := ensure(); err != nil {
				return err
			}
			indices := make([]int, 0, len(toolIDs))
			for idx := range toolIDs {
				indices = append(indices, idx)
			}
			sort.Ints(indices)
			for _, idx := range indices {
				arguments := ""
				if builder := toolArguments[idx]; builder != nil {
					arguments = builder.String()
				}
				if err := write("response.output_item.done", map[string]interface{}{"type": "response.output_item.done", "item": map[string]interface{}{"type": "function_call", "id": toolIDs[idx], "call_id": toolIDs[idx], "name": toolNames[idx], "arguments": arguments, "status": "completed"}}); err != nil {
					return err
				}
			}
			terminal = true
			status := "completed"
			if stopReason == "max_tokens" {
				status = "incomplete"
			}
			if err := write("response.completed", map[string]interface{}{"type": "response.completed", "response": map[string]interface{}{"id": responseID, "object": "response", "status": status, "model": model, "usage": anthropicResponsesUsage(usage)}}); err != nil {
				return err
			}
		default:
			return fail(nil, fmt.Errorf("Anthropic SSE event type %q is unsupported", typ))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !terminal {
		return fmt.Errorf("Anthropic stream ended without message_stop")
	}
	return nil
}
func rawIndex(event map[string]json.RawMessage) int {
	var index int
	_ = json.Unmarshal(event["index"], &index)
	return index
}
func mergeAnthropicUsage(dst *localCompatUsage, raw json.RawMessage) {
	var u struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		CacheRead    int `json:"cache_read_input_tokens"`
		CacheWrite   int `json:"cache_creation_input_tokens"`
	}
	if json.Unmarshal(raw, &u) == nil {
		dst.InputTokens = maxInt(dst.InputTokens, u.InputTokens)
		dst.OutputTokens = maxInt(dst.OutputTokens, u.OutputTokens)
		dst.CacheReadTokens = maxInt(dst.CacheReadTokens, u.CacheRead)
		dst.CacheWriteTokens = maxInt(dst.CacheWriteTokens, u.CacheWrite)
	}
}
func convertAnthropicJSONResponse(source io.Reader) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(source, localCompatBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > localCompatBodyLimit {
		return nil, fmt.Errorf("Anthropic response body too large")
	}
	var message struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Content []struct {
			Type  string      `json:"type"`
			Text  string      `json:"text"`
			ID    string      `json:"id"`
			Name  string      `json:"name"`
			Input interface{} `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			CacheRead    int `json:"cache_read_input_tokens"`
			CacheWrite   int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &message); err != nil {
		return nil, fmt.Errorf("parse Anthropic response: %w", err)
	}
	output := make([]interface{}, 0)
	var text strings.Builder
	for _, block := range message.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
			output = append(output, map[string]interface{}{"type": "message", "id": message.ID + "_msg", "role": "assistant", "status": "completed", "content": []interface{}{map[string]interface{}{"type": "output_text", "text": block.Text, "annotations": []interface{}{}}}})
		case "tool_use":
			arguments, err := encodeRawArguments(block.Input)
			if err != nil {
				return nil, err
			}
			output = append(output, map[string]interface{}{"type": "function_call", "id": block.ID, "call_id": block.ID, "name": block.Name, "arguments": arguments, "status": "completed"})
		}
	}
	status := "completed"
	if message.StopReason == "max_tokens" {
		status = "incomplete"
	}
	result := map[string]interface{}{"id": "resp_" + message.ID, "object": "response", "status": status, "model": message.Model, "output": output, "output_text": text.String(), "usage": anthropicResponsesUsage(localCompatUsage{InputTokens: message.Usage.InputTokens, OutputTokens: message.Usage.OutputTokens, CacheReadTokens: message.Usage.CacheRead, CacheWriteTokens: message.Usage.CacheWrite})}
	return json.Marshal(result)
}
