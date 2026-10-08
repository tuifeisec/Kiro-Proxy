package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	json "github.com/goccy/go-json"
)

// contentMetricProtocol identifies the wire protocol carried by an SSE body.
// It is intentionally kept local to the proxy package because it describes
// the upstream response, not the public API exposed by the proxy.
type contentMetricProtocol string

const (
	contentMetricProtocolChat      contentMetricProtocol = "chat"
	contentMetricProtocolResponses contentMetricProtocol = "responses"
	contentMetricProtocolAnthropic contentMetricProtocol = "anthropic"
	contentMetricProtocolGemini    contentMetricProtocol = "gemini"
)

const (
	contentMetricText      = "text"
	contentMetricReasoning = "reasoning"
	contentMetricTool      = "tool"

	// The observer must never become an unbounded second response buffer. A
	// normal delta is much smaller than this; an oversized event is skipped
	// until its blank-line delimiter while the original body keeps flowing.
	contentMetricMaxLineBytes = 64 << 10
	contentMetricMaxDataBytes = 64 << 10
)

type contentMetricSnapshot struct {
	firstEventAt        time.Time
	firstContentAt      time.Time
	lastContentAt       time.Time
	firstContentType    string
	maxContentGap       time.Duration
	contentMeasured     bool
	measurementComplete bool
}

type contentMetricOptions struct {
	protocol       contentMetricProtocol
	start          time.Time
	onFirstContent func(time.Time, string)
}

// contentMetrics incrementally parses only SSE framing and a bounded JSON
// event payload. It never changes or retains bytes that pass through it.
type contentMetrics struct {
	mu       sync.Mutex
	protocol contentMetricProtocol
	start    time.Time

	line         []byte
	lineTooLarge bool
	eventType    string
	data         []byte
	dataTooLarge bool

	firstEventAt     time.Time
	firstContentAt   time.Time
	firstContentType string
	lastContentAt    time.Time
	maxContentGap    time.Duration
	contentMeasured  bool
	finished         bool
	complete         bool
	terminalSeen     bool
	// observationIncomplete is sticky: once a bounded parser limit or invalid
	// data makes part of the stream unobservable, later valid events cannot
	// turn the measurement back into a complete observation.
	observationIncomplete bool
	onFirstContent        func(time.Time, string)
}

func newContentMetrics(options contentMetricOptions) *contentMetrics {
	if options.start.IsZero() {
		options.start = time.Now()
	}
	return &contentMetrics{
		protocol:       options.protocol,
		start:          options.start,
		line:           make([]byte, 0, 256),
		data:           make([]byte, 0, 1024),
		onFirstContent: options.onFirstContent,
	}
}

func (m *contentMetrics) wrap(body io.ReadCloser) io.ReadCloser {
	if m == nil || body == nil {
		return body
	}
	return &contentMetricsReadCloser{ReadCloser: body, metrics: m}
}

type contentMetricsReadCloser struct {
	io.ReadCloser
	metrics   *contentMetrics
	closeOnce sync.Once
}

func (r *contentMetricsReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 && r.metrics != nil {
		r.metrics.consume(p[:n])
	}
	if r.metrics != nil {
		if err == io.EOF {
			r.metrics.finish(true)
		} else if err != nil {
			r.metrics.finish(false)
		}
	}
	return n, err
}

func (r *contentMetricsReadCloser) Close() error {
	var err error
	r.closeOnce.Do(func() {
		err = r.ReadCloser.Close()
		if r.metrics != nil {
			r.metrics.finish(false)
		}
	})
	return err
}

func (m *contentMetrics) consume(p []byte) {
	if m == nil || len(p) == 0 {
		return
	}
	// Non-SSE responses are explicitly marked unmeasured at EOF/Close. This
	// avoids treating an ordinary JSON first byte as model content.
	if m.protocol == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for len(p) > 0 {
		idx := bytes.IndexByte(p, '\n')
		if idx < 0 {
			m.appendLineLocked(p)
			return
		}
		m.appendLineLocked(p[:idx])
		m.finishLineLocked()
		p = p[idx+1:]
	}
}

func (m *contentMetrics) appendLineLocked(part []byte) {
	if m.lineTooLarge || len(part) == 0 {
		return
	}
	if len(m.line)+len(part) > contentMetricMaxLineBytes {
		m.line = nil
		m.lineTooLarge = true
		m.observationIncomplete = true
		return
	}
	m.line = append(m.line, part...)
}

func (m *contentMetrics) finishLineLocked() {
	line := m.line
	m.line = m.line[:0]
	if m.lineTooLarge {
		m.lineTooLarge = false
		m.dataTooLarge = true
		m.observationIncomplete = true
		return
	}
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) == 0 {
		m.finishEventLocked()
		return
	}
	if line[0] == ':' {
		return
	}
	field, value, found := bytes.Cut(line, []byte{':'})
	if found {
		value = bytes.TrimPrefix(value, []byte{' '})
	}
	switch string(field) {
	case "event":
		if len(value) <= 256 {
			m.eventType = string(value)
		}
	case "data":
		if m.dataTooLarge {
			return
		}
		if len(m.data)+len(value)+1 > contentMetricMaxDataBytes {
			m.data = nil
			m.dataTooLarge = true
			m.observationIncomplete = true
			return
		}
		if len(m.data) > 0 {
			m.data = append(m.data, '\n')
		}
		m.data = append(m.data, value...)
	}
}

func (m *contentMetrics) finishEventLocked() {
	defer func() {
		m.eventType = ""
		m.data = m.data[:0]
		m.dataTooLarge = false
	}()
	data := bytes.TrimSpace(m.data)
	if len(data) == 0 {
		if m.dataTooLarge && m.firstEventAt.IsZero() {
			m.firstEventAt = time.Now()
		}
		return
	}
	now := time.Now()
	// The event boundary itself is observable even when its JSON is unknown or
	// exceeds the parser bound. Content classification remains unmeasured in
	// those cases, while first_event_ms still reflects the stream's first event.
	if m.firstEventAt.IsZero() {
		m.firstEventAt = now
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		m.terminalSeen = true
		return
	}
	if m.dataTooLarge {
		return
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil || payload == nil {
		m.observationIncomplete = true
		return
	}

	eventType := m.eventType
	if rawType := payload["type"]; len(rawType) > 0 {
		var payloadType string
		if json.Unmarshal(rawType, &payloadType) == nil && payloadType != "" {
			if eventType == "" || isContentMetricFailureType(eventType) {
				eventType = payloadType
			}
		}
	}
	if contentMetricTerminalEvent(m.protocol, eventType, payload) {
		m.terminalSeen = true
	}
	contentType := classifyContentMetricEvent(m.protocol, eventType, payload)
	if contentType == "" {
		return
	}
	if m.firstContentAt.IsZero() {
		m.firstContentAt = now
		m.firstContentType = contentType
		m.contentMeasured = true
		if m.onFirstContent != nil && !m.observationIncomplete {
			m.onFirstContent(now, contentType)
		}
	}
	if !m.lastContentAt.IsZero() {
		gap := now.Sub(m.lastContentAt)
		if gap > m.maxContentGap {
			m.maxContentGap = gap
		}
	}
	m.lastContentAt = now
}

func (m *contentMetrics) finish(eof bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.finished {
		m.mu.Unlock()
		return
	}
	m.finished = true
	// A body ending without a blank line is not a complete SSE event. Likewise,
	// a close/read error cannot prove that the full stream was observed.
	m.complete = (eof || m.terminalSeen) && !m.observationIncomplete && len(m.line) == 0 && !m.lineTooLarge && len(m.data) == 0 &&
		!m.dataTooLarge && m.eventType == ""
	m.mu.Unlock()
}

func (m *contentMetrics) snapshot() contentMetricSnapshot {
	if m == nil {
		return contentMetricSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return contentMetricSnapshot{
		firstEventAt:        m.firstEventAt,
		firstContentAt:      m.firstContentAt,
		lastContentAt:       m.lastContentAt,
		firstContentType:    m.firstContentType,
		maxContentGap:       m.maxContentGap,
		contentMeasured:     m.contentMeasured,
		measurementComplete: m.complete,
	}
}

func isContentMetricFailureType(eventType string) bool {
	switch eventType {
	case "error", "response.error", "response.failed", "response.incomplete":
		return true
	default:
		return false
	}
}

func contentMetricString(payload map[string]json.RawMessage, field string) string {
	var value string
	if raw := payload[field]; len(raw) > 0 && json.Unmarshal(raw, &value) == nil {
		return value
	}
	return ""
}

func contentMetricNonEmptyString(payload map[string]json.RawMessage, field string) bool {
	return contentMetricString(payload, field) != ""
}

func contentMetricObject(payload map[string]json.RawMessage, field string) map[string]json.RawMessage {
	var object map[string]json.RawMessage
	if raw := payload[field]; len(raw) > 0 && json.Unmarshal(raw, &object) == nil {
		return object
	}
	return nil
}

func classifyContentMetricEvent(protocol contentMetricProtocol, eventType string, payload map[string]json.RawMessage) string {
	switch protocol {
	case contentMetricProtocolResponses:
		switch eventType {
		case "response.output_text.delta", "response.output_text.done":
			if contentMetricNonEmptyString(payload, "delta") || contentMetricNonEmptyString(payload, "text") {
				return contentMetricText
			}
		case "response.reasoning_text.delta", "response.reasoning_text.done", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
			if contentMetricNonEmptyString(payload, "delta") || contentMetricNonEmptyString(payload, "text") {
				return contentMetricReasoning
			}
		case "response.function_call_arguments.delta":
			if contentMetricNonEmptyString(payload, "delta") {
				return contentMetricTool
			}
		case "response.output_item.added", "response.output_item.done":
			item := contentMetricObject(payload, "item")
			switch itemType := contentMetricString(item, "type"); itemType {
			case "function_call", "tool_use", "custom_tool_call", "computer_call", "code_interpreter_call", "file_search_call", "web_search_call":
				return contentMetricTool
			case "reasoning":
				if contentMetricHasText(item, "summary") || contentMetricNonEmptyString(item, "text") {
					return contentMetricReasoning
				}
			default:
				if strings.Contains(itemType, "tool") || strings.HasSuffix(itemType, "_call") {
					return contentMetricTool
				}
			}
		case "response.content_part.added", "response.content_part.done":
			part := contentMetricObject(payload, "part")
			switch contentMetricString(part, "type") {
			case "output_text":
				if contentMetricNonEmptyString(part, "text") {
					return contentMetricText
				}
			case "reasoning_text", "reasoning_summary_text":
				if contentMetricNonEmptyString(part, "text") {
					return contentMetricReasoning
				}
			}
		}
	case contentMetricProtocolChat:
		choices := contentMetricArray(payload, "choices")
		for _, choice := range choices {
			delta := contentMetricObject(choice, "delta")
			if contentMetricNonEmptyString(delta, "content") {
				return contentMetricText
			}
			for _, field := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
				if contentMetricNonEmptyString(delta, field) {
					return contentMetricReasoning
				}
			}
			if len(contentMetricArray(delta, "tool_calls")) > 0 || len(contentMetricObject(delta, "function_call")) > 0 {
				return contentMetricTool
			}
			if message := contentMetricObject(choice, "message"); contentMetricNonEmptyString(message, "content") {
				return contentMetricText
			}
		}
	case contentMetricProtocolAnthropic:
		switch eventType {
		case "content_block_start":
			block := contentMetricObject(payload, "content_block")
			switch contentMetricString(block, "type") {
			case "text":
				if contentMetricNonEmptyString(block, "text") {
					return contentMetricText
				}
			case "thinking", "redacted_thinking":
				if contentMetricNonEmptyString(block, "thinking") || contentMetricNonEmptyString(block, "text") || contentMetricString(block, "type") == "redacted_thinking" {
					return contentMetricReasoning
				}
			case "tool_use":
				return contentMetricTool
			}
		case "content_block_delta":
			delta := contentMetricObject(payload, "delta")
			switch contentMetricString(delta, "type") {
			case "text_delta":
				if contentMetricNonEmptyString(delta, "text") {
					return contentMetricText
				}
			case "thinking_delta", "signature_delta":
				if contentMetricNonEmptyString(delta, "thinking") || contentMetricNonEmptyString(delta, "signature") {
					return contentMetricReasoning
				}
			case "input_json_delta":
				if contentMetricNonEmptyString(delta, "partial_json") {
					return contentMetricTool
				}
			}
		}
	case contentMetricProtocolGemini:
		candidates := contentMetricArray(payload, "candidates")
		for _, candidate := range candidates {
			content := contentMetricObject(candidate, "content")
			parts := contentMetricArray(content, "parts")
			for _, part := range parts {
				if contentMetricNonEmptyString(part, "text") {
					var thought bool
					if raw := part["thought"]; len(raw) > 0 {
						_ = json.Unmarshal(raw, &thought)
					}
					if thought {
						return contentMetricReasoning
					}
					return contentMetricText
				}
				if len(contentMetricObject(part, "functionCall")) > 0 || len(contentMetricObject(part, "functionResponse")) > 0 {
					return contentMetricTool
				}
			}
		}
	}
	return ""
}

func contentMetricArray(payload map[string]json.RawMessage, field string) []map[string]json.RawMessage {
	var values []map[string]json.RawMessage
	if raw := payload[field]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &values)
	}
	return values
}

func contentMetricHasText(payload map[string]json.RawMessage, field string) bool {
	if len(payload) == 0 {
		return false
	}
	if contentMetricNonEmptyString(payload, field) {
		return true
	}
	var values []map[string]json.RawMessage
	if raw := payload[field]; len(raw) > 0 && json.Unmarshal(raw, &values) == nil {
		for _, value := range values {
			if contentMetricNonEmptyString(value, "text") || contentMetricNonEmptyString(value, "thinking") {
				return true
			}
		}
	}
	return false
}

func contentMetricTerminalEvent(protocol contentMetricProtocol, eventType string, payload map[string]json.RawMessage) bool {
	switch protocol {
	case contentMetricProtocolResponses:
		switch eventType {
		case "response.completed", "response.failed", "response.incomplete", "response.error":
			return true
		}
	case contentMetricProtocolAnthropic:
		return eventType == "message_stop"
	case contentMetricProtocolChat:
		for _, choice := range contentMetricArray(payload, "choices") {
			if contentMetricNonEmptyString(choice, "finish_reason") {
				return true
			}
		}
	case contentMetricProtocolGemini:
		for _, candidate := range contentMetricArray(payload, "candidates") {
			if contentMetricNonEmptyString(candidate, "finishReason") {
				return true
			}
		}
	}
	return false
}

func contentMetricProtocolForResponse(resp *http.Response, provider, endpoint string) contentMetricProtocol {
	if !isSSEResponse(resp) {
		return ""
	}
	if metadata := upstreamMetadataFromResponse(resp); metadata.Protocol != "" {
		if protocol := normalizeContentMetricProtocol(strings.ToLower(strings.TrimSpace(metadata.Protocol))); protocol != "" {
			return protocol
		}
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	switch {
	case provider == "anthropic":
		return contentMetricProtocolAnthropic
	case provider == "gemini" || strings.Contains(endpoint, "streamgeneratecontent"):
		return contentMetricProtocolGemini
	case provider == "codex" || strings.Contains(endpoint, "/responses"):
		return contentMetricProtocolResponses
	default:
		return contentMetricProtocolChat
	}
}

func normalizeContentMetricProtocol(value string) contentMetricProtocol {
	switch contentMetricProtocol(value) {
	case contentMetricProtocolChat, contentMetricProtocolResponses, contentMetricProtocolAnthropic, contentMetricProtocolGemini:
		return contentMetricProtocol(value)
	default:
		return ""
	}
}
