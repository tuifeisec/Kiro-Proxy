package proxy

import (
	"bytes"
	"context"
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

type upstreamContentProtocolKey struct{}

func withUpstreamContentProtocol(ctx context.Context, protocol contentMetricProtocol) context.Context {
	return context.WithValue(ctx, upstreamContentProtocolKey{}, protocol)
}

func upstreamContentProtocolFromResponse(resp *http.Response) contentMetricProtocol {
	if resp == nil || resp.Request == nil {
		return ""
	}
	if protocol := normalizeContentMetricProtocol(strings.ToLower(strings.TrimSpace(upstreamMetadataFromResponse(resp).Protocol))); protocol != "" {
		return protocol
	}
	protocol, _ := resp.Request.Context().Value(upstreamContentProtocolKey{}).(contentMetricProtocol)
	return protocol
}

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
	speedOutputTokens   int
	speedDuration       time.Duration
	speedBasis          string
	contentMeasured     bool
	measurementComplete bool
}

type contentMetricOptions struct {
	protocol       contentMetricProtocol
	start          time.Time
	onFirstContent func(time.Time, string)
	now            func() time.Time
}

// contentMetrics incrementally parses only SSE framing and a bounded JSON
// event payload. It never changes or retains bytes that pass through it.
type contentMetrics struct {
	mu       sync.Mutex
	protocol contentMetricProtocol
	start    time.Time
	now      func() time.Time

	line         []byte
	lineTooLarge bool
	eventType    string
	data         []byte
	dataTooLarge bool

	firstEventAt          time.Time
	firstContentAt        time.Time
	firstContentType      string
	lastContentAt         time.Time
	maxContentGap         time.Duration
	firstSpeedContentAt   time.Time
	lastSpeedContentAt    time.Time
	speedBasis            string
	speedOutputTokens     int
	speedTotalTokens      int64
	speedTotalKnown       bool
	speedReasoning        int64
	speedReasoningKnown   bool
	speedRejected         int64
	speedRejectedKnown    bool
	speedAudio            int64
	speedAudioKnown       bool
	speedUsageUnsupported bool
	gptReasoningSeen      bool
	contentMeasured       bool
	finished              bool
	complete              bool
	terminalSeen          bool
	// observationIncomplete is sticky: once a bounded parser limit or invalid
	// data makes a relevant part of the stream unobservable, later valid events
	// cannot turn the measurement back into a complete observation. Known
	// Responses metadata snapshots are explicitly ignored before setting this
	// flag because they do not carry content or usage needed by the metric.
	observationIncomplete  bool
	firstContentHook       func(time.Time, string)
	firstContentHookCalled bool
}

func newContentMetrics(options contentMetricOptions) *contentMetrics {
	if options.now == nil {
		options.now = time.Now
	}
	if options.start.IsZero() {
		options.start = options.now()
	}
	return &contentMetrics{
		protocol:          options.protocol,
		start:             options.start,
		now:               options.now,
		line:              make([]byte, 0, 256),
		data:              make([]byte, 0, 1024),
		speedBasis:        contentMetricSpeedBasis(options.protocol),
		speedOutputTokens: -1,
		firstContentHook:  options.onFirstContent,
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
		r.metrics.consumeAt(p[:n], r.metrics.now())
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
	if m == nil {
		return
	}
	m.consumeAt(p, m.now())
}

func (m *contentMetrics) consumeAt(p []byte, now time.Time) {
	if m == nil || len(p) == 0 {
		return
	}
	// Non-SSE responses are explicitly marked unmeasured at EOF/Close. This
	// avoids treating an ordinary JSON first byte as model content.
	if m.protocol == "" {
		return
	}

	m.mu.Lock()
	for len(p) > 0 {
		idx := bytes.IndexByte(p, '\n')
		if idx < 0 {
			m.appendLineLocked(p)
			break
		}
		m.appendLineLocked(p[:idx])
		m.finishLineLocked(now)
		p = p[idx+1:]
	}
	hook := m.takeFirstContentHookLocked()
	firstAt, firstType := m.firstContentAt, m.firstContentType
	m.mu.Unlock()
	if hook != nil {
		hook(firstAt, firstType)
	}
}

func (m *contentMetrics) appendLineLocked(part []byte) {
	if m.lineTooLarge || len(part) == 0 {
		return
	}
	if len(m.line)+len(part) > contentMetricMaxLineBytes {
		m.line = nil
		m.lineTooLarge = true
		return
	}
	m.line = append(m.line, part...)
}

func (m *contentMetrics) finishLineLocked(now time.Time) {
	line := m.line
	m.line = m.line[:0]
	if m.lineTooLarge {
		m.lineTooLarge = false
		m.dataTooLarge = true
		return
	}
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) == 0 {
		m.finishEventLocked(now)
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
			return
		}
		if len(m.data) > 0 {
			m.data = append(m.data, '\n')
		}
		m.data = append(m.data, value...)
	}
}

func (m *contentMetrics) finishEventLocked(now time.Time) {
	defer func() {
		m.eventType = ""
		m.data = m.data[:0]
		m.dataTooLarge = false
	}()
	data := bytes.TrimSpace(m.data)
	if len(data) == 0 {
		if m.dataTooLarge && m.firstEventAt.IsZero() {
			m.firstEventAt = now
		}
		if m.dataTooLarge && !contentMetricCanIgnoreOversizedEvent(m.protocol, m.eventType) {
			m.observationIncomplete = true
		}
		return
	}
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
		if !contentMetricCanIgnoreOversizedEvent(m.protocol, m.eventType) {
			m.observationIncomplete = true
		}
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
	if isContentMetricFailureType(eventType) || contentMetricRawHasValue(payload["error"]) {
		m.observationIncomplete = true
	}
	m.observeUsageLocked(eventType, payload)
	if contentMetricHasAudioOutput(m.protocol, eventType, payload) {
		m.speedUsageUnsupported = true
		m.refreshSpeedOutputTokensLocked()
	}
	contentType := classifyContentMetricEvent(m.protocol, eventType, payload)
	if (m.protocol == contentMetricProtocolChat || m.protocol == contentMetricProtocolResponses) && contentMetricHasGPTReasoning(m.protocol, eventType, payload) {
		m.gptReasoningSeen = true
		m.refreshSpeedOutputTokensLocked()
	}
	if contentType != "" && m.firstContentAt.IsZero() {
		m.firstContentAt = now
		m.firstContentType = contentType
		m.contentMeasured = true
	}
	if contentType != "" && !m.lastContentAt.IsZero() {
		gap := now.Sub(m.lastContentAt)
		if gap > m.maxContentGap {
			m.maxContentGap = gap
		}
	}
	if contentType != "" {
		m.lastContentAt = now
	}
	if contentMetricEventHasSpeedContent(m.protocol, eventType, payload, contentType) {
		if m.firstSpeedContentAt.IsZero() {
			m.firstSpeedContentAt = now
		}
		m.lastSpeedContentAt = now
	}
}

func (m *contentMetrics) takeFirstContentHookLocked() func(time.Time, string) {
	if m.firstContentHook == nil || m.firstContentHookCalled || m.observationIncomplete || m.firstContentAt.IsZero() {
		return nil
	}
	m.firstContentHookCalled = true
	return m.firstContentHook
}

func (m *contentMetrics) setFirstContentHook(hook func(time.Time, string)) {
	if m == nil || hook == nil {
		return
	}
	m.mu.Lock()
	if !m.firstContentHookCalled {
		m.firstContentHook = hook
	}
	callback := m.takeFirstContentHookLocked()
	firstAt, firstType := m.firstContentAt, m.firstContentType
	m.mu.Unlock()
	if callback != nil {
		callback(firstAt, firstType)
	}
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
		speedOutputTokens:   m.speedOutputTokens,
		speedDuration:       m.speedDurationLocked(),
		speedBasis:          m.speedBasis,
		contentMeasured:     m.contentMeasured,
		measurementComplete: m.complete,
	}
}

func (m *contentMetrics) speedDurationLocked() time.Duration {
	if m.firstSpeedContentAt.IsZero() || m.lastSpeedContentAt.IsZero() {
		return 0
	}
	return m.lastSpeedContentAt.Sub(m.firstSpeedContentAt)
}

func contentMetricSpeedBasis(protocol contentMetricProtocol) string {
	switch protocol {
	case contentMetricProtocolChat, contentMetricProtocolResponses:
		return "reported_output"
	case contentMetricProtocolGemini:
		return "visible_output"
	case contentMetricProtocolAnthropic:
		return "output_with_reasoning"
	default:
		return ""
	}
}

func contentMetricIsSpeedContent(protocol contentMetricProtocol, contentType string) bool {
	if contentType == contentMetricReasoning {
		return protocol == contentMetricProtocolAnthropic
	}
	return contentType == contentMetricText || contentType == contentMetricTool
}

func contentMetricHasGPTReasoning(protocol contentMetricProtocol, eventType string, payload map[string]json.RawMessage) bool {
	if protocol == contentMetricProtocolResponses {
		return classifyContentMetricEvent(protocol, eventType, payload) == contentMetricReasoning
	}
	if protocol != contentMetricProtocolChat {
		return false
	}
	for _, choice := range contentMetricArray(payload, "choices") {
		for _, content := range []map[string]json.RawMessage{contentMetricObject(choice, "delta"), contentMetricObject(choice, "message")} {
			for _, field := range []string{"reasoning_content", "reasoning", "reasoning_text", "reasoning_summary", "reasoning_summary_text"} {
				if contentMetricTextValue(content, field) {
					return true
				}
			}
		}
	}
	return false
}

func contentMetricHasAudioOutput(protocol contentMetricProtocol, eventType string, payload map[string]json.RawMessage) bool {
	switch protocol {
	case contentMetricProtocolResponses:
		switch eventType {
		case "response.audio.delta", "response.audio.done", "response.audio.transcript.delta", "response.audio.transcript.done":
			return true
		}
	case contentMetricProtocolChat:
		for _, choice := range contentMetricArray(payload, "choices") {
			for _, content := range []map[string]json.RawMessage{contentMetricObject(choice, "delta"), contentMetricObject(choice, "message")} {
				if contentMetricRawHasValue(content["audio"]) {
					return true
				}
			}
		}
	case contentMetricProtocolGemini:
		for _, candidate := range contentMetricArray(payload, "candidates") {
			for _, part := range contentMetricArray(contentMetricObject(candidate, "content"), "parts") {
				inline := contentMetricObject(part, "inlineData")
				if strings.HasPrefix(strings.ToLower(contentMetricString(inline, "mimeType")), "audio/") && contentMetricNonEmptyString(inline, "data") {
					return true
				}
			}
		}
	}
	return false
}

func contentMetricEventHasSpeedContent(protocol contentMetricProtocol, eventType string, payload map[string]json.RawMessage, contentType string) bool {
	switch protocol {
	case contentMetricProtocolChat:
		for _, choice := range contentMetricArray(payload, "choices") {
			for _, content := range []map[string]json.RawMessage{contentMetricObject(choice, "delta"), contentMetricObject(choice, "message")} {
				if contentMetricTextValue(content, "content") || contentMetricNonEmptyString(content, "refusal") {
					return true
				}
				for _, tool := range contentMetricArray(content, "tool_calls") {
					if contentMetricFunctionToolHasOutput(tool) {
						return true
					}
				}
				if function := contentMetricObject(content, "function_call"); contentMetricNonEmptyString(function, "name") || contentMetricRawHasValue(function["arguments"]) {
					return true
				}
			}
		}
		return false
	case contentMetricProtocolGemini:
		for _, candidate := range contentMetricArray(payload, "candidates") {
			content := contentMetricObject(candidate, "content")
			for _, part := range contentMetricArray(content, "parts") {
				if contentMetricNonEmptyString(part, "text") {
					var thought bool
					_ = json.Unmarshal(part["thought"], &thought)
					if !thought {
						return true
					}
				}
				if functionCall := contentMetricObject(part, "functionCall"); contentMetricNonEmptyString(functionCall, "name") || contentMetricRawHasValue(functionCall["args"]) {
					return true
				}
			}
		}
		return false
	default:
		return contentMetricIsSpeedContent(protocol, contentType)
	}
}

func (m *contentMetrics) observeUsageLocked(eventType string, payload map[string]json.RawMessage) {
	switch m.protocol {
	case contentMetricProtocolChat:
		if usage := contentMetricObject(payload, "usage"); usage != nil {
			m.observeGPTUsageLocked(usage)
		}
	case contentMetricProtocolResponses:
		response := contentMetricObject(payload, "response")
		usage := contentMetricObject(response, "usage")
		if usage == nil {
			usage = contentMetricObject(payload, "usage")
		}
		if usage != nil {
			m.observeGPTUsageLocked(usage)
		}
	case contentMetricProtocolAnthropic:
		var usage map[string]json.RawMessage
		switch eventType {
		case "message_start":
			usage = contentMetricObject(contentMetricObject(payload, "message"), "usage")
		case "message_delta":
			usage = contentMetricObject(payload, "usage")
		}
		if usage != nil {
			m.observeSingleUsageLocked(usage, "output_tokens")
		}
	case contentMetricProtocolGemini:
		if usage := contentMetricObject(payload, "usageMetadata"); usage != nil {
			m.observeSingleUsageLocked(usage, "candidatesTokenCount")
		}
	}
}

func (m *contentMetrics) observeSingleUsageLocked(usage map[string]json.RawMessage, field string) {
	value, present, valid := contentMetricTokenField(usage, field)
	if present {
		if !valid {
			m.speedUsageUnsupported = true
		} else {
			m.speedTotalTokens = value
			m.speedTotalKnown = true
		}
	}
	m.refreshSpeedOutputTokensLocked()
}

func (m *contentMetrics) observeGPTUsageLocked(usage map[string]json.RawMessage) {
	total, totalPresent, totalValid := contentMetricTokenFields(usage, "completion_tokens", "output_tokens")
	if totalPresent {
		if !totalValid {
			m.speedUsageUnsupported = true
		} else {
			m.speedTotalTokens = total
			m.speedTotalKnown = true
		}
	}

	detailSources := make([]map[string]json.RawMessage, 0, 2)
	if details := contentMetricObject(usage, "completion_tokens_details"); details != nil {
		detailSources = append(detailSources, details)
	}
	if details := contentMetricObject(usage, "output_tokens_details"); details != nil {
		detailSources = append(detailSources, details)
	}
	if len(detailSources) > 0 {
		if value, present, valid := contentMetricTokenFieldsFrom(detailSources, "reasoning_tokens"); present {
			if !valid {
				m.speedUsageUnsupported = true
			} else {
				m.speedReasoning = value
				m.speedReasoningKnown = true
			}
		}
		if value, present, valid := contentMetricTokenFieldsFrom(detailSources, "rejected_prediction_tokens"); present {
			if !valid {
				m.speedUsageUnsupported = true
			} else {
				m.speedRejected = value
				m.speedRejectedKnown = true
			}
		}
		if value, present, valid := contentMetricTokenFieldsFrom(detailSources, "audio_tokens"); present {
			if !valid {
				m.speedUsageUnsupported = true
			} else {
				m.speedAudio = value
				m.speedAudioKnown = true
			}
		}
	}
	m.refreshSpeedOutputTokensLocked()
}

func (m *contentMetrics) refreshSpeedOutputTokensLocked() {
	if m.protocol == contentMetricProtocolChat || m.protocol == contentMetricProtocolResponses {
		if m.speedReasoningKnown {
			m.speedBasis = "visible_output"
		} else {
			m.speedBasis = "reported_output"
		}
	}
	if m.speedUsageUnsupported || !m.speedTotalKnown {
		m.speedOutputTokens = -1
		return
	}
	output := m.speedTotalTokens
	if m.protocol == contentMetricProtocolChat || m.protocol == contentMetricProtocolResponses {
		if m.gptReasoningSeen && !m.speedReasoningKnown {
			m.speedOutputTokens = -1
			return
		}
		if m.speedAudioKnown && m.speedAudio > 0 {
			m.speedUsageUnsupported = true
			m.speedOutputTokens = -1
			return
		}
		reasoning, rejected := int64(0), int64(0)
		if m.speedReasoningKnown {
			reasoning = m.speedReasoning
		}
		if m.speedRejectedKnown {
			rejected = m.speedRejected
		}
		if reasoning > output || rejected > output-reasoning {
			m.speedUsageUnsupported = true
			m.speedOutputTokens = -1
			return
		}
		output -= reasoning + rejected
	}
	if output < 0 || int64(int(output)) != output {
		m.speedUsageUnsupported = true
		m.speedOutputTokens = -1
		return
	}
	m.speedOutputTokens = int(output)
}

func contentMetricTokenFields(source map[string]json.RawMessage, fields ...string) (int64, bool, bool) {
	sources := []map[string]json.RawMessage{source}
	return contentMetricTokenFieldsFrom(sources, fields...)
}

func contentMetricTokenFieldsFrom(sources []map[string]json.RawMessage, fields ...string) (int64, bool, bool) {
	var value int64
	present := false
	for _, source := range sources {
		for _, field := range fields {
			current, currentPresent, valid := contentMetricTokenField(source, field)
			if !currentPresent {
				continue
			}
			if !valid || (present && current != value) {
				return 0, true, false
			}
			value = current
			present = true
		}
	}
	return value, present, true
}

func contentMetricTokenField(source map[string]json.RawMessage, field string) (int64, bool, bool) {
	raw := bytes.TrimSpace(source[field])
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, false, true
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil || value < 0 {
		return 0, true, false
	}
	return value, true, true
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

func contentMetricRawHasValue(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || bytes.Equal(raw, []byte("{}")) || bytes.Equal(raw, []byte("[]")) {
		return false
	}
	if raw[0] == '{' {
		var object map[string]json.RawMessage
		return json.Unmarshal(raw, &object) == nil && len(object) > 0
	}
	if raw[0] == '[' {
		var array []json.RawMessage
		return json.Unmarshal(raw, &array) == nil && len(array) > 0
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value != ""
	}
	return true
}

func contentMetricTextValue(payload map[string]json.RawMessage, field string) bool {
	raw := bytes.TrimSpace(payload[field])
	if len(raw) == 0 {
		return false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text != ""
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return false
	}
	for _, part := range parts {
		if contentMetricNonEmptyString(part, "text") {
			return true
		}
	}
	return false
}

func contentMetricFunctionToolHasOutput(tool map[string]json.RawMessage) bool {
	function := contentMetricObject(tool, "function")
	if contentMetricNonEmptyString(function, "name") || contentMetricRawHasValue(function["arguments"]) {
		return true
	}
	custom := contentMetricObject(tool, "custom")
	return contentMetricNonEmptyString(custom, "name") || contentMetricRawHasValue(custom["input"])
}

func contentMetricToolItemHasOutput(item map[string]json.RawMessage) bool {
	switch contentMetricString(item, "type") {
	case "function_call", "tool_use", "mcp_call":
		return contentMetricNonEmptyString(item, "name") || contentMetricRawHasValue(item["arguments"])
	case "custom_tool_call":
		return contentMetricNonEmptyString(item, "name") || contentMetricRawHasValue(item["input"])
	default:
		return false
	}
}

func classifyContentMetricEvent(protocol contentMetricProtocol, eventType string, payload map[string]json.RawMessage) string {
	switch protocol {
	case contentMetricProtocolResponses:
		switch eventType {
		case "response.output_text.delta", "response.refusal.delta":
			if contentMetricTextValue(payload, "delta") {
				return contentMetricText
			}
		case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			if contentMetricTextValue(payload, "delta") {
				return contentMetricReasoning
			}
		case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta", "response.mcp_call_arguments.delta", "response.code_interpreter_call_code.delta":
			if contentMetricTextValue(payload, "delta") {
				return contentMetricTool
			}
		case "response.output_item.added":
			item := contentMetricObject(payload, "item")
			if contentMetricToolItemHasOutput(item) {
				return contentMetricTool
			}
			if contentMetricString(item, "type") == "reasoning" {
				if contentMetricHasText(item, "summary") || contentMetricNonEmptyString(item, "text") {
					return contentMetricReasoning
				}
			}
		case "response.content_part.added":
			part := contentMetricObject(payload, "part")
			switch contentMetricString(part, "type") {
			case "output_text":
				if contentMetricTextValue(part, "text") {
					return contentMetricText
				}
			case "reasoning_text", "reasoning_summary_text":
				if contentMetricTextValue(part, "text") {
					return contentMetricReasoning
				}
			}
		}
	case contentMetricProtocolChat:
		choices := contentMetricArray(payload, "choices")
		for _, choice := range choices {
			delta := contentMetricObject(choice, "delta")
			if contentMetricTextValue(delta, "content") || contentMetricNonEmptyString(delta, "refusal") {
				return contentMetricText
			}
			for _, field := range []string{"reasoning_content", "reasoning", "reasoning_text", "reasoning_summary", "reasoning_summary_text"} {
				if contentMetricTextValue(delta, field) {
					return contentMetricReasoning
				}
			}
			for _, tool := range contentMetricArray(delta, "tool_calls") {
				if contentMetricFunctionToolHasOutput(tool) {
					return contentMetricTool
				}
			}
			if function := contentMetricObject(delta, "function_call"); contentMetricNonEmptyString(function, "name") || contentMetricRawHasValue(function["arguments"]) {
				return contentMetricTool
			}
			if message := contentMetricObject(choice, "message"); contentMetricTextValue(message, "content") || contentMetricNonEmptyString(message, "refusal") {
				return contentMetricText
			}
			if message := contentMetricObject(choice, "message"); contentMetricTextValue(message, "reasoning_content") || contentMetricTextValue(message, "reasoning") || contentMetricTextValue(message, "reasoning_text") || contentMetricTextValue(message, "reasoning_summary") || contentMetricTextValue(message, "reasoning_summary_text") {
				return contentMetricReasoning
			}
			if message := contentMetricObject(choice, "message"); message != nil {
				for _, tool := range contentMetricArray(message, "tool_calls") {
					if contentMetricFunctionToolHasOutput(tool) {
						return contentMetricTool
					}
				}
				if function := contentMetricObject(message, "function_call"); contentMetricNonEmptyString(function, "name") || contentMetricRawHasValue(function["arguments"]) {
					return contentMetricTool
				}
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
				if contentMetricNonEmptyString(block, "name") || contentMetricRawHasValue(block["input"]) {
					return contentMetricTool
				}
			}
		case "content_block_delta":
			delta := contentMetricObject(payload, "delta")
			switch contentMetricString(delta, "type") {
			case "text_delta":
				if contentMetricNonEmptyString(delta, "text") {
					return contentMetricText
				}
			case "thinking_delta":
				if contentMetricNonEmptyString(delta, "thinking") {
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
				if functionCall := contentMetricObject(part, "functionCall"); contentMetricNonEmptyString(functionCall, "name") || contentMetricRawHasValue(functionCall["args"]) {
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

// Responses providers may repeat the full tool schema in metadata events. A
// single data line can exceed the bounded observer buffer even though the
// event contains no output, usage, or terminal state. It is safe to discard
// only these known metadata snapshots; oversized content or terminal events
// must keep the conservative incomplete result.
func contentMetricCanIgnoreOversizedEvent(protocol contentMetricProtocol, eventType string) bool {
	if protocol != contentMetricProtocolResponses {
		return false
	}
	switch eventType {
	case "codex.response.metadata", "response.created", "response.in_progress":
		return true
	default:
		return false
	}
}

func contentMetricProtocolForResponse(resp *http.Response, provider, endpoint string) contentMetricProtocol {
	if !isSSEResponse(resp) {
		return ""
	}
	if protocol := upstreamContentProtocolFromResponse(resp); protocol != "" {
		return protocol
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
