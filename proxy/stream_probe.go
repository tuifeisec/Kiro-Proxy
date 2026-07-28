package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

const maxSSEPrecommitBuffer = 1 << 20

var (
	errSSEPrecommitTimeout        = errors.New("SSE precommit timeout")
	errSSEPrecommitBufferExceeded = errors.New("SSE precommit buffer limit exceeded")
	errSSEPrecommitMissingEvent   = errors.New("SSE stream ended before first valid event")
	errSSEPrecommitUpstream       = errors.New("SSE upstream failure before commit")
)

type sseProbeProtocol uint8

const (
	sseProbeChat sseProbeProtocol = iota
	sseProbeResponses
	sseProbeAnthropic
	sseProbeGemini
)

type sseProbeResult struct {
	prefix []byte
	err    error
}

// probeSSEPrecommit waits for the first valid protocol event before the
// response is committed downstream. On success, every byte consumed while
// probing is restored to resp.Body.
func probeSSEPrecommit(
	ctx context.Context,
	resp *http.Response,
	provider string,
	endpoint string,
	timeout time.Duration,
	maxBufferedBytes int,
) error {
	if resp == nil || resp.Body == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		_ = resp.Body.Close()
		return err
	}

	limit := maxBufferedBytes
	if limit <= 0 || limit > maxSSEPrecommitBuffer {
		limit = maxSSEPrecommitBuffer
	}

	body := resp.Body
	resultCh := make(chan sseProbeResult, 1)
	go func() {
		prefix, err := readUntilValidSSEEvent(body, sseProtocol(provider, endpoint), limit)
		resultCh <- sseProbeResult{prefix: prefix, err: err}
	}()

	var timer *time.Timer
	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		timeoutCh = timer.C
		defer timer.Stop()
	}

	select {
	case result := <-resultCh:
		if ctxErr := ctx.Err(); ctxErr != nil {
			_ = body.Close()
			return ctxErr
		}
		if result.err != nil {
			_ = body.Close()
			return result.err
		}
		resp.Body = &prefixedReadCloser{
			reader: io.MultiReader(bytes.NewReader(result.prefix), body),
			closer: body,
		}
		return nil
	case <-ctx.Done():
		_ = body.Close()
		return ctx.Err()
	case <-timeoutCh:
		_ = body.Close()
		return errSSEPrecommitTimeout
	}
}

func readUntilValidSSEEvent(body io.Reader, protocol sseProbeProtocol, limit int) ([]byte, error) {
	prefix := make([]byte, 0, min(limit, 32*1024))
	pending := make([]byte, 0, 4096)
	readBuffer := make([]byte, 4096)

	for {
		remaining := limit - len(prefix)
		if remaining <= 0 {
			return prefix, fmt.Errorf("%w: %d bytes", errSSEPrecommitBufferExceeded, limit)
		}
		n, readErr := body.Read(readBuffer[:min(len(readBuffer), remaining)])
		if n > 0 {
			prefix = append(prefix, readBuffer[:n]...)
			pending = append(pending, readBuffer[:n]...)

			for {
				event, rest, complete := nextSSEEvent(pending)
				if !complete {
					break
				}
				pending = rest
				accepted, eventErr := classifySSEEvent(protocol, event)
				if eventErr != nil {
					return prefix, eventErr
				}
				if accepted {
					return prefix, nil
				}
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return prefix, errSSEPrecommitMissingEvent
			}
			return prefix, fmt.Errorf("SSE precommit read: %w", readErr)
		}
		if n == 0 {
			return prefix, io.ErrNoProgress
		}
	}
}

func nextSSEEvent(data []byte) (event, rest []byte, complete bool) {
	for i := 0; i < len(data); i++ {
		if data[i] != '\n' {
			continue
		}
		lineStart := i
		for lineStart > 0 && data[lineStart-1] != '\n' {
			lineStart--
		}
		line := data[lineStart:i]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) == 0 {
			return data[:lineStart], data[i+1:], true
		}
	}
	return nil, data, false
}

func classifySSEEvent(protocol sseProbeProtocol, event []byte) (bool, error) {
	var eventType string
	dataLines := make([]string, 0, 1)
	for len(event) > 0 {
		line := event
		if newline := bytes.IndexByte(event, '\n'); newline >= 0 {
			line, event = event[:newline], event[newline+1:]
		} else {
			event = nil
		}
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		field, value, found := bytes.Cut(line, []byte{':'})
		if !found {
			value = nil
		}
		value = bytes.TrimPrefix(value, []byte{' '})
		switch string(field) {
		case "event":
			eventType = string(value)
		case "data":
			dataLines = append(dataLines, string(value))
		}
	}

	data := strings.TrimSpace(strings.Join(dataLines, "\n"))
	if data == "" {
		return false, nil
	}
	if data == "[DONE]" {
		return protocol == sseProbeChat, nil
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &payload); err != nil || payload == nil {
		return false, nil
	}
	if rawType, ok := payload["type"]; ok {
		var payloadType string
		if json.Unmarshal(rawType, &payloadType) == nil && payloadType != "" {
			if eventType == "" || isSSEFailureEventType(payloadType) {
				eventType = payloadType
			}
		}
	}

	switch protocol {
	case sseProbeResponses:
		return classifyResponsesProbeEvent(eventType, payload, data)
	case sseProbeAnthropic:
		return classifyAnthropicProbeEvent(eventType, data)
	case sseProbeGemini:
		return classifyGeminiProbeEvent(eventType, payload, data)
	default:
		return classifyChatProbeEvent(eventType, payload, data)
	}
}

func isSSEFailureEventType(eventType string) bool {
	switch eventType {
	case "error", "response.error", "response.failed", "response.incomplete":
		return true
	default:
		return false
	}
}

func classifyResponsesProbeEvent(eventType string, payload map[string]json.RawMessage, data string) (bool, error) {
	if hasNonNullJSONField(payload, "error") {
		return false, fmt.Errorf("%w: %s", errSSEPrecommitUpstream, compactProbeDetail(data))
	}
	switch eventType {
	case "response.failed", "response.incomplete", "error", "response.error":
		return false, fmt.Errorf("%w: %s", errSSEPrecommitUpstream, compactProbeDetail(data))
	case "response.completed":
		return true, nil
	case "response.output_text.delta", "response.function_call_arguments.delta":
		return hasNonEmptyJSONString(payload, "delta"), nil
	case "response.output_item.added", "response.output_item.done":
		var item struct {
			Type string `json:"type"`
		}
		if rawItem := payload["item"]; len(rawItem) > 0 && json.Unmarshal(rawItem, &item) == nil {
			return item.Type == "function_call", nil
		}
		return false, nil
	case "response.created", "response.in_progress", "response.queued":
		return false, nil
	}
	return false, nil
}

func classifyAnthropicProbeEvent(eventType, data string) (bool, error) {
	switch eventType {
	case "error":
		return false, fmt.Errorf("%w: %s", errSSEPrecommitUpstream, compactProbeDetail(data))
	case "message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop":
		return true, nil
	default:
		return false, nil
	}
}

func classifyChatProbeEvent(eventType string, payload map[string]json.RawMessage, data string) (bool, error) {
	if eventType == "error" || hasNonNullJSONField(payload, "error") {
		return false, fmt.Errorf("%w: %s", errSSEPrecommitUpstream, compactProbeDetail(data))
	}
	if rawChoices, ok := payload["choices"]; ok {
		var choices []json.RawMessage
		if json.Unmarshal(rawChoices, &choices) == nil {
			return true, nil
		}
	}
	if rawObject, ok := payload["object"]; ok {
		var object string
		if json.Unmarshal(rawObject, &object) == nil && object == "chat.completion.chunk" {
			return true, nil
		}
	}
	return false, nil
}

func classifyGeminiProbeEvent(eventType string, payload map[string]json.RawMessage, data string) (bool, error) {
	if eventType == "error" || hasNonNullJSONField(payload, "error") {
		return false, fmt.Errorf("%w: %s", errSSEPrecommitUpstream, compactProbeDetail(data))
	}
	if rawCandidates, ok := payload["candidates"]; ok {
		var candidates []json.RawMessage
		if json.Unmarshal(rawCandidates, &candidates) == nil && len(candidates) > 0 {
			return true, nil
		}
	}
	if rawUsage, ok := payload["usageMetadata"]; ok && string(rawUsage) != "null" {
		var usage map[string]json.RawMessage
		if json.Unmarshal(rawUsage, &usage) == nil {
			return true, nil
		}
	}
	return false, nil
}

func hasNonNullJSONField(payload map[string]json.RawMessage, field string) bool {
	raw, ok := payload[field]
	return ok && len(raw) > 0 && string(raw) != "null"
}

func hasNonEmptyJSONString(payload map[string]json.RawMessage, field string) bool {
	var value string
	raw, ok := payload[field]
	return ok && json.Unmarshal(raw, &value) == nil && value != ""
}

func compactProbeDetail(data string) string {
	const maxDetailBytes = 512
	if len(data) <= maxDetailBytes {
		return data
	}
	return data[:maxDetailBytes] + "..."
}

func sseProtocol(provider, endpoint string) sseProbeProtocol {
	provider = strings.ToLower(strings.TrimSpace(provider))
	endpoint = strings.ToLower(endpoint)
	switch {
	case provider == "anthropic":
		return sseProbeAnthropic
	case provider == "gemini":
		return sseProbeGemini
	case provider == "codex" || strings.Contains(endpoint, "/responses"):
		return sseProbeResponses
	default:
		return sseProbeChat
	}
}
