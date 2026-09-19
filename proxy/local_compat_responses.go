package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"io"
	"kiro-proxy/store"
	"net/http"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

// responsesNeedsChatFallback reports whether the configured OpenAI upstream
// only exposes Chat Completions. The local Responses endpoint still accepts
// the modern SDK shape, then adapts it at the boundary.
func responsesNeedsChatFallback(m *store.Model) bool {
	// OpenAI exposes both native endpoints. The configured endpoint is used as
	// the base route and localCompatUpstreamURL derives the sibling path, so
	// native OpenAI Responses requests should stay in the Responses protocol.
	return m != nil && m.Provider != "openai" && strings.Contains(strings.ToLower(m.Endpoint), "/chat/completions")
}

type fallbackResponseState struct {
	Model         string
	Provider      string
	ModelConfigID string
	SessionIDHash string
	Messages      []map[string]interface{}
	ExpiresAt     time.Time
}

func (s *Server) fallbackMessages(previousID, sessionID, modelConfigID, provider string) ([]map[string]interface{}, error) {
	previousID = strings.TrimSpace(previousID)
	if previousID == "" {
		return nil, nil
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("previous_response_id is unavailable without an explicit local session; resend the full input")
	}
	s.responsesFallbackMu.Lock()
	defer s.responsesFallbackMu.Unlock()
	state, ok := s.responsesFallback[previousID]
	if !ok || time.Now().After(state.ExpiresAt) {
		delete(s.responsesFallback, previousID)
		return nil, fmt.Errorf("previous_response_id %q is not available on the local chat fallback; resend the full input", opaqueIDForLog(previousID))
	}
	if state.SessionIDHash != sessionIDHash(sessionID) || state.ModelConfigID != strings.TrimSpace(modelConfigID) || state.Provider != strings.TrimSpace(provider) {
		return nil, fmt.Errorf("previous_response_id %q is not available for this local session; resend the full input", opaqueIDForLog(previousID))
	}
	return cloneFallbackMessages(state.Messages), nil
}

func (s *Server) saveFallbackResponse(responseID string, state *fallbackResponseState, assistant map[string]interface{}) {
	if state == nil || strings.TrimSpace(responseID) == "" {
		return
	}
	messages := cloneFallbackMessages(state.Messages)
	if assistant != nil {
		messages = append(messages, assistant)
	}
	s.responsesFallbackMu.Lock()
	defer s.responsesFallbackMu.Unlock()
	now := time.Now()
	for id, item := range s.responsesFallback {
		if now.After(item.ExpiresAt) {
			delete(s.responsesFallback, id)
		}
	}
	if len(s.responsesFallback) >= 256 {
		for id := range s.responsesFallback {
			delete(s.responsesFallback, id)
			break
		}
	}
	state.Messages = messages
	state.ExpiresAt = now.Add(24 * time.Hour)
	s.responsesFallback[responseID] = *state
}

func sessionIDHash(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:16])
}
func cloneFallbackMessages(messages []map[string]interface{}) []map[string]interface{} {
	cloned := make([]map[string]interface{}, 0, len(messages))
	for _, message := range messages {
		copyMessage := make(map[string]interface{}, len(message))
		for key, value := range message {
			copyMessage[key] = value
		}
		cloned = append(cloned, copyMessage)
	}
	return cloned
}

func ensureResponsesPromptCacheKey(body []byte, model string) ([]byte, string, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, "", fmt.Errorf("invalid Responses request: %w", err)
	}
	if raw := root["prompt_cache_key"]; len(raw) > 0 {
		var key string
		if json.Unmarshal(raw, &key) == nil && strings.TrimSpace(key) != "" {
			return body, key, nil
		}
	}

	// Only stable request parts participate in the generated namespace. The
	// current user input must stay outside this key so later turns can reuse
	// the same cached prefix.
	stable := map[string]interface{}{"model": strings.ToLower(strings.TrimSpace(model))}
	for _, name := range []string{"instructions", "tools", "prompt_cache_options"} {
		if raw := root[name]; len(raw) > 0 {
			// UseNumber 保留大整数与小数的原始精度；对象排序不改变数组顺序。
			decoder := stdjson.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			var value interface{}
			if err := decoder.Decode(&value); err != nil {
				return nil, "", fmt.Errorf("invalid Responses %s: %w", name, err)
			}
			stable[name] = value
		}
	}
	canonical, err := stdjson.Marshal(stable)
	if err != nil {
		return nil, "", fmt.Errorf("encode Responses cache namespace: %w", err)
	}
	sum := sha256.Sum256(canonical)
	key := "kiro-local-v2-" + hex.EncodeToString(sum[:16])
	encoded, err := json.Marshal(key)
	if err != nil {
		return nil, "", err
	}
	root["prompt_cache_key"] = encoded
	result, err := json.Marshal(root)
	if err != nil {
		return nil, "", err
	}
	return result, key, nil
}

func responsesToChatFallback(body []byte, model string) ([]byte, *fallbackResponseState, error) {
	return responsesToChatFallbackWithPrevious(body, model, nil)
}

func responsesToChatFallbackWithPrevious(body []byte, model string, previous []map[string]interface{}) ([]byte, *fallbackResponseState, error) {
	var cacheErr error
	body, _, cacheErr = ensureResponsesPromptCacheKey(body, model)
	if cacheErr != nil {
		return nil, nil, cacheErr
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, nil, fmt.Errorf("invalid Responses request: %w", err)
	}

	chat := make(map[string]json.RawMessage, len(root)+2)
	for _, name := range []string{
		"model", "stream", "temperature", "top_p", "user", "frequency_penalty",
		"presence_penalty", "seed", "stop", "logprobs", "top_logprobs", "tool_choice",
		"parallel_tool_calls", "metadata", "service_tier", "response_format",
		"reasoning_effort", "prompt_cache_key",
	} {
		if raw := root[name]; len(raw) > 0 {
			chat[name] = raw
		}
	}
	if _, ok := chat["model"]; !ok {
		encoded, _ := json.Marshal(model)
		chat["model"] = encoded
	}

	messages := make([]map[string]interface{}, 0, 8)
	if raw := root["instructions"]; len(raw) > 0 {
		var instructions string
		if err := json.Unmarshal(raw, &instructions); err != nil {
			return nil, nil, fmt.Errorf("responses instructions must be a string: %w", err)
		}
		if instructions != "" {
			messages = append(messages, map[string]interface{}{"role": "system", "content": instructions})
		}
	}
	if raw := root["input"]; len(raw) > 0 {
		converted, err := responsesInputToChatMessages(raw)
		if err != nil {
			return nil, nil, err
		}
		messages = append(messages, converted...)
	}
	if len(messages) == 0 {
		return nil, nil, fmt.Errorf("Responses request requires input or instructions")
	}
	if len(previous) > 0 && !fallbackMessagesPrefix(messages, previous) {
		messages = append(cloneFallbackMessages(previous), messages...)
	}
	messageBytes, err := json.Marshal(messages)
	if err != nil {
		return nil, nil, err
	}
	chat["messages"] = messageBytes

	if raw := root["max_output_tokens"]; len(raw) > 0 {
		chat["max_completion_tokens"] = raw
	}
	if raw := root["text"]; len(raw) > 0 {
		if responseFormat := responsesTextToChatFormat(raw); len(responseFormat) > 0 {
			chat["response_format"] = responseFormat
		}
	}
	if raw := root["tools"]; len(raw) > 0 {
		convertedTools, err := responsesToolsToChat(raw)
		if err != nil {
			return nil, nil, err
		}
		chat["tools"] = convertedTools
	}
	if stream, ok := boolField(root, "stream"); ok && stream {
		if _, exists := chat["stream_options"]; !exists {
			chat["stream_options"], _ = json.Marshal(map[string]bool{"include_usage": true})
		}
	}

	result, err := json.Marshal(chat)
	if err != nil {
		return nil, nil, err
	}
	return result, &fallbackResponseState{Model: model, Messages: messages}, nil
}

func fallbackMessagesPrefix(messages, prefix []map[string]interface{}) bool {
	if len(messages) < len(prefix) {
		return false
	}
	for i, expected := range prefix {
		actualJSON, _ := json.Marshal(messages[i])
		expectedJSON, _ := json.Marshal(expected)
		if !bytes.Equal(actualJSON, expectedJSON) {
			return false
		}
	}
	return true
}

func responsesPreviousResponseID(body []byte) string {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return ""
	}
	var id string
	_ = json.Unmarshal(root["previous_response_id"], &id)
	return strings.TrimSpace(id)
}

func responsesInputToChatMessages(raw json.RawMessage) ([]map[string]interface{}, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []map[string]interface{}{{"role": "user", "content": text}}, nil
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("Responses input must be a string or array: %w", err)
	}
	messages := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		var typ, role string
		_ = json.Unmarshal(item["type"], &typ)
		_ = json.Unmarshal(item["role"], &role)
		switch typ {
		case "", "message":
			if role == "" {
				role = "user"
			}
			content := interface{}(nil)
			if rawContent := item["content"]; len(rawContent) > 0 {
				content = decodeResponsesContent(rawContent)
			}
			messages = append(messages, map[string]interface{}{"role": role, "content": content})
		case "function_call":
			var callID, name, arguments string
			_ = json.Unmarshal(item["call_id"], &callID)
			_ = json.Unmarshal(item["name"], &name)
			_ = json.Unmarshal(item["arguments"], &arguments)
			messages = append(messages, map[string]interface{}{
				"role": "assistant",
				"tool_calls": []interface{}{map[string]interface{}{
					"id": callID, "type": "function", "function": map[string]string{"name": name, "arguments": arguments},
				}},
			})
		case "function_call_output":
			var callID string
			_ = json.Unmarshal(item["call_id"], &callID)
			content := decodeRawValue(item["output"])
			messages = append(messages, map[string]interface{}{"role": "tool", "tool_call_id": callID, "content": content})
		case "reasoning":
			// Reasoning items are not accepted by Chat Completions. The visible
			// transcript remains valid without replaying encrypted reasoning.
			continue
		default:
			return nil, fmt.Errorf("Responses input item type %q is not supported by Chat Completions fallback", typ)
		}
	}
	return messages, nil
}

func decodeResponsesContent(raw json.RawMessage) interface{} {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return decodeRawValue(raw)
	}
	converted := make([]map[string]interface{}, 0, len(parts))
	for _, part := range parts {
		var typ string
		_ = json.Unmarshal(part["type"], &typ)
		switch typ {
		case "input_text", "output_text", "text":
			var value string
			_ = json.Unmarshal(part["text"], &value)
			converted = append(converted, map[string]interface{}{"type": "text", "text": value})
		case "input_image":
			var url string
			_ = json.Unmarshal(part["image_url"], &url)
			converted = append(converted, map[string]interface{}{"type": "image_url", "image_url": map[string]string{"url": url}})
		default:
			converted = append(converted, map[string]interface{}{"type": typ, "text": decodeRawValue(part["text"])})
		}
	}
	return converted
}

func responsesToolsToChat(raw json.RawMessage) (json.RawMessage, error) {
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, fmt.Errorf("invalid Responses tools: %w", err)
	}
	converted := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		var typ string
		_ = json.Unmarshal(tool["type"], &typ)
		if typ != "function" {
			return nil, fmt.Errorf("Responses tool type %q requires a native Responses upstream", typ)
		}
		var fn map[string]interface{}
		if rawFn := tool["function"]; len(rawFn) > 0 {
			if err := json.Unmarshal(rawFn, &fn); err != nil {
				return nil, err
			}
		} else {
			fn = map[string]interface{}{
				"name":        decodeRawValue(tool["name"]),
				"description": decodeRawValue(tool["description"]),
				"parameters":  decodeRawValue(tool["parameters"]),
			}
		}
		converted = append(converted, map[string]interface{}{"type": "function", "function": fn})
	}
	return json.Marshal(converted)
}

func responsesTextToChatFormat(raw json.RawMessage) json.RawMessage {
	var text map[string]json.RawMessage
	if json.Unmarshal(raw, &text) != nil {
		return nil
	}
	format := text["format"]
	if len(format) == 0 {
		return nil
	}
	var parsed map[string]interface{}
	if json.Unmarshal(format, &parsed) != nil {
		return nil
	}
	if typ, ok := parsed["type"].(string); ok && typ == "json_schema" {
		if name, ok := parsed["name"]; ok {
			parsed["json_schema"] = map[string]interface{}{"name": name, "strict": parsed["strict"], "schema": parsed["schema"]}
			delete(parsed, "name")
			delete(parsed, "strict")
			delete(parsed, "schema")
		}
	}
	result, _ := json.Marshal(parsed)
	return result
}

func decodeRawValue(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return ""
	}
	var value interface{}
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	return value
}

func boolField(root map[string]json.RawMessage, name string) (bool, bool) {
	raw := root[name]
	if len(raw) == 0 {
		return false, false
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return false, false
	}
	return value, true
}

func convertChatCompletionToResponses(body []byte) ([]byte, localCompatUsage, error) {
	var chat struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Created int64  `json:"created"`
		Choices []struct {
			Message struct {
				Role      string      `json:"role"`
				Content   interface{} `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(body, &chat); err != nil {
		return nil, localCompatUsage{}, fmt.Errorf("invalid Chat Completions response: %w", err)
	}
	usage := localCompatUsage{}
	mergeUsageObject(&usage, chat.Usage)
	responseID := chat.ID
	if responseID == "" {
		responseID = "chat-fallback"
	}
	if !strings.HasPrefix(responseID, "resp_") {
		responseID = "resp_" + responseID
	}
	output := make([]interface{}, 0, 1)
	if len(chat.Choices) > 0 {
		message := chat.Choices[0].Message
		if message.Content != nil && message.Content != "" {
			output = append(output, map[string]interface{}{
				"type": "message", "id": responseID + "_msg", "role": "assistant",
				"status": "completed", "content": []interface{}{map[string]interface{}{
					"type": "output_text", "text": message.Content, "annotations": []interface{}{},
				}},
			})
		}
		for _, call := range message.ToolCalls {
			output = append(output, map[string]interface{}{
				"type": "function_call", "id": call.ID, "call_id": call.ID,
				"name": call.Function.Name, "arguments": call.Function.Arguments,
				"status": "completed",
			})
		}
	}
	response := map[string]interface{}{
		"id": responseID, "object": "response", "created_at": chat.Created,
		"status": "completed", "model": chat.Model, "output": output,
		"output_text": responseOutputText(output), "usage": responsesUsage(usage),
	}
	result, err := json.Marshal(response)
	return result, usage, err
}

func readAndConvertChatResponse(body io.Reader) ([]byte, localCompatUsage, error) {
	payload, err := io.ReadAll(io.LimitReader(body, localCompatBodyLimit+1))
	if err != nil {
		return nil, localCompatUsage{}, err
	}
	if len(payload) > localCompatBodyLimit {
		return nil, localCompatUsage{}, fmt.Errorf("upstream response body too large")
	}
	return convertChatCompletionToResponses(payload)
}

func responseOutputText(output []interface{}) string {
	for _, item := range output {
		obj, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		content, ok := obj["content"].([]interface{})
		if !ok {
			continue
		}
		for _, part := range content {
			p, ok := part.(map[string]interface{})
			if ok {
				if text, ok := p["text"].(string); ok {
					return text
				}
			}
		}
	}
	return ""
}

func responsesUsage(usage localCompatUsage) map[string]interface{} {
	return map[string]interface{}{
		"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens,
		"total_tokens":         usage.InputTokens + usage.OutputTokens,
		"input_tokens_details": map[string]int{"cached_tokens": usage.CacheReadTokens},
	}
}

func copyChatStreamAsResponses(w http.ResponseWriter, body io.Reader) (localCompatUsage, error) {
	usage, _, _, err := copyChatStreamAsResponsesWithState(context.Background(), w, body)
	return usage, err
}

func copyChatStreamAsResponsesWithState(ctx context.Context, w http.ResponseWriter, body io.Reader) (localCompatUsage, string, map[string]interface{}, error) {
	flusher, _ := w.(http.Flusher)
	writer := bufio.NewWriter(w)
	responseID := "resp_chat_fallback"
	model := ""
	var text strings.Builder
	usage := localCompatUsage{}
	createdSent := false
	terminalSeen := false
	type streamTool struct {
		ID        string
		Name      string
		Arguments strings.Builder
	}
	tools := make(map[int]*streamTool)
	writeEvent := func(event string, value interface{}) error {
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event, payload); err != nil {
			return wrapLocalCompatDownstreamWriteError(err)
		}
		if err := writer.Flush(); err != nil {
			return wrapLocalCompatDownstreamWriteError(err)
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	ensureCreated := func() error {
		if createdSent {
			return nil
		}
		createdSent = true
		return writeEvent("response.created", map[string]interface{}{"type": "response.created", "response": map[string]interface{}{"id": responseID, "object": "response", "status": "in_progress", "model": model}})
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), localCompatBodyLimit)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			terminalSeen = true
			continue
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Error json.RawMessage `json:"error"`
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		mergeLocalCompatUsage(&usage, localCompatChat, []byte(data))
		if chunk.ID != "" {
			responseID = "resp_" + strings.TrimPrefix(chunk.ID, "resp_")
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if err := ensureCreated(); err != nil {
			return usage, responseID, nil, err
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			err := fmt.Errorf("%w: chat stream reported failure", errLocalCompatStreamProtocol)
			if writeErr := writeEvent("response.failed", responsesStreamFailure(responseID, model, err)); writeErr != nil {
				return usage, responseID, nil, writeErr
			}
			return usage, responseID, nil, err
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			delta := chunk.Choices[0].Delta.Content
			text.WriteString(delta)
			if err := writeEvent("response.output_text.delta", map[string]interface{}{"type": "response.output_text.delta", "delta": delta}); err != nil {
				return usage, responseID, nil, err
			}
		}
		if len(chunk.Choices) > 0 {
			if chunk.Choices[0].FinishReason != "" {
				terminalSeen = true
			}
			for _, call := range chunk.Choices[0].Delta.ToolCalls {
				tool := tools[call.Index]
				if tool == nil {
					id := call.ID
					if id == "" {
						id = fmt.Sprintf("call_%d", call.Index)
					}
					tool = &streamTool{ID: id, Name: call.Function.Name}
					tools[call.Index] = tool
					if err := writeEvent("response.output_item.added", map[string]interface{}{
						"type": "response.output_item.added", "item": map[string]interface{}{
							"type": "function_call", "id": id, "call_id": id,
							"name": tool.Name, "arguments": "", "status": "in_progress",
						},
					}); err != nil {
						return usage, responseID, nil, err
					}
				}
				if tool.Name == "" && call.Function.Name != "" {
					tool.Name = call.Function.Name
				}
				if call.Function.Arguments != "" {
					tool.Arguments.WriteString(call.Function.Arguments)
					if err := writeEvent("response.function_call_arguments.delta", map[string]interface{}{
						"type": "response.function_call_arguments.delta", "item_id": tool.ID,
						"call_id": tool.ID, "delta": call.Function.Arguments,
					}); err != nil {
						return usage, responseID, nil, err
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx == nil || ctx.Err() == nil {
			_ = ensureCreated()
			_ = writeEvent("response.failed", responsesStreamFailure(responseID, model, err))
		}
		return usage, responseID, nil, err
	}
	if err := ensureCreated(); err != nil {
		return usage, responseID, nil, err
	}
	if !terminalSeen {
		err := errLocalCompatStreamMissingTerminal
		if ctx == nil || ctx.Err() == nil {
			if writeErr := writeEvent("response.failed", responsesStreamFailure(responseID, model, err)); writeErr != nil {
				return usage, responseID, nil, writeErr
			}
		}
		return usage, responseID, nil, err
	}
	for _, tool := range tools {
		if err := writeEvent("response.output_item.done", map[string]interface{}{
			"type": "response.output_item.done", "item": map[string]interface{}{
				"type": "function_call", "id": tool.ID, "call_id": tool.ID,
				"name": tool.Name, "arguments": tool.Arguments.String(), "status": "completed",
			},
		}); err != nil {
			return usage, responseID, nil, err
		}
	}
	assistant := map[string]interface{}{"role": "assistant", "content": text.String()}
	toolCalls := make([]interface{}, 0, len(tools))
	for _, tool := range tools {
		toolCalls = append(toolCalls, map[string]interface{}{
			"id": tool.ID, "type": "function", "function": map[string]string{
				"name": tool.Name, "arguments": tool.Arguments.String(),
			},
		})
	}
	if len(toolCalls) > 0 {
		assistant["tool_calls"] = toolCalls
	}
	if err := writeEvent("response.completed", map[string]interface{}{
		"type": "response.completed", "response": map[string]interface{}{
			"id": responseID, "object": "response", "status": "completed", "model": model,
			"output_text": text.String(), "usage": responsesUsage(usage),
		},
	}); err != nil {
		return usage, responseID, nil, err
	}
	return usage, responseID, assistant, nil
}

func responsesStreamFailure(responseID, model string, cause error) map[string]interface{} {
	return map[string]interface{}{
		"type": "response.failed",
		"response": map[string]interface{}{
			"id": responseID, "object": "response", "status": "failed", "model": model,
			"error": map[string]string{"code": "upstream_stream_interrupted", "message": cause.Error()},
		},
	}
}

func fallbackAssistantFromResponses(body []byte) (string, map[string]interface{}) {
	var response struct {
		ID         string `json:"id"`
		OutputText string `json:"output_text"`
		Output     []struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"output"`
	}
	if json.Unmarshal(body, &response) != nil {
		return "", nil
	}
	if response.ID == "" {
		return "", nil
	}
	assistant := map[string]interface{}{"role": "assistant", "content": response.OutputText}
	toolCalls := make([]interface{}, 0)
	for _, item := range response.Output {
		if item.Type != "function_call" {
			continue
		}
		toolCalls = append(toolCalls, map[string]interface{}{
			"id": item.CallID, "type": "function", "function": map[string]string{
				"name": item.Name, "arguments": item.Arguments,
			},
		})
	}
	if len(toolCalls) > 0 {
		assistant["tool_calls"] = toolCalls
	}
	return response.ID, assistant
}
