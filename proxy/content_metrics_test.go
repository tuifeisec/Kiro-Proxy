package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

func readContentMetrics(t *testing.T, protocol contentMetricProtocol, body string, chunkSize int) ([]byte, contentMetricSnapshot) {
	t.Helper()
	metrics := newContentMetrics(contentMetricOptions{protocol: protocol, start: time.Now().Add(-time.Second)})
	wrapped := metrics.wrap(io.NopCloser(bytes.NewReader([]byte(body))))
	var got bytes.Buffer
	buf := make([]byte, chunkSize)
	for {
		n, err := wrapped.Read(buf)
		if n > 0 {
			_, _ = got.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}
	if err := wrapped.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return got.Bytes(), metrics.snapshot()
}

func TestContentMetricsSeparatesMetadataAndContentAcrossFragments(t *testing.T) {
	body := "event: response.created\r\ndata: {\"type\":\"response.created\"}\r\n\r\n" +
		"event: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\r\n\r\n" +
		"event: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\" world\"}\r\n\r\n" +
		"data: [DONE]\r\n\r\n"
	got, snapshot := readContentMetrics(t, contentMetricProtocolResponses, body, 3)
	if string(got) != body {
		t.Fatalf("body changed: got %q want %q", got, body)
	}
	if snapshot.firstEventAt.IsZero() || snapshot.firstContentAt.IsZero() {
		t.Fatalf("missing event/content timestamps: %+v", snapshot)
	}
	if !snapshot.contentMeasured || snapshot.firstContentType != contentMetricText {
		t.Fatalf("content classification: %+v", snapshot)
	}
	if !snapshot.measurementComplete {
		t.Fatalf("complete stream marked partial: %+v", snapshot)
	}
	if snapshot.firstContentAt.Before(snapshot.firstEventAt) {
		t.Fatalf("content preceded event: %+v", snapshot)
	}
}

func TestContentMetricsDoneOnlyRecordsEventWithoutContent(t *testing.T) {
	body := "data: [DONE]\n\n"
	got, snapshot := readContentMetrics(t, contentMetricProtocolChat, body, 1)
	if string(got) != body || snapshot.firstEventAt.IsZero() || !snapshot.firstContentAt.IsZero() || snapshot.contentMeasured || !snapshot.measurementComplete {
		t.Fatalf("DONE-only event incorrectly measured: %q %+v", got, snapshot)
	}
}

func TestContentMetricsRecognizesToolAndIgnoresMetadata(t *testing.T) {
	body := "event: message\ndata: {\"id\":\"m1\",\"choices\":[]}\n\n" +
		"event: message\ndata: {\"id\":\"m1\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\"}]}}]}\n\n" +
		"event: message\ndata: {\"id\":\"m1\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{}\"}}]}}]}\n\n" +
		"event: message\ndata: {\"id\":\"m1\",\"choices\":[],\"usage\":{\"total_tokens\":1}}\n\n"
	_, snapshot := readContentMetrics(t, contentMetricProtocolChat, body, 5)
	if !snapshot.contentMeasured || snapshot.firstContentType != contentMetricTool {
		t.Fatalf("tool event not recognized: %+v", snapshot)
	}
	if snapshot.firstEventAt.IsZero() || snapshot.firstContentAt.Before(snapshot.firstEventAt) {
		t.Fatalf("metadata/content order not recorded: %+v", snapshot)
	}
}

func TestContentMetricsPartialAndOversizedEventsRemainBounded(t *testing.T) {
	overlong := "event: message\ndata: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("x", contentMetricMaxDataBytes) + "\"}}]}\n\n"
	valid := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: [DONE]\n\n"
	got, snapshot := readContentMetrics(t, contentMetricProtocolChat, overlong+valid, 97)
	if string(got) != overlong+valid {
		t.Fatalf("oversized event changed body")
	}
	if !snapshot.contentMeasured || snapshot.firstContentType != contentMetricText || snapshot.measurementComplete {
		t.Fatalf("oversized prefix must remain partial after valid event: %+v", snapshot)
	}

	partial := "data: {\"choices\":[{\"delta\":{\"content\":\"tail\"}}]}\n"
	got, snapshot = readContentMetrics(t, contentMetricProtocolChat, partial, 4)
	if string(got) != partial {
		t.Fatalf("partial body changed")
	}
	if snapshot.contentMeasured || snapshot.measurementComplete {
		t.Fatalf("partial event was reported complete/content: %+v", snapshot)
	}
}

func TestContentMetricsIncompleteObservationDoesNotReportLatencySample(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("x", contentMetricMaxDataBytes) + "}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: [DONE]\n\n"
	calls := 0
	metrics := newContentMetrics(contentMetricOptions{
		protocol: contentMetricProtocolChat,
		start:    time.Now().Add(-time.Second),
		onFirstContent: func(time.Time, string) {
			calls++
		},
	})
	wrapped := metrics.wrap(io.NopCloser(bytes.NewReader([]byte(body))))
	if _, err := io.Copy(io.Discard, wrapped); err != nil {
		t.Fatal(err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := metrics.snapshot()
	if !snapshot.contentMeasured || snapshot.measurementComplete || calls != 0 {
		t.Fatalf("incomplete observation reported as precise content: snapshot=%+v callback_calls=%d", snapshot, calls)
	}
}

func TestContentMetricsTerminalEventCompletesOnConverterClose(t *testing.T) {
	body := "event: response.output_text.delta\ndata: {\"delta\":\"ok\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"
	metrics := newContentMetrics(contentMetricOptions{protocol: contentMetricProtocolResponses, start: time.Now().Add(-time.Second)})
	wrapped := metrics.wrap(io.NopCloser(bytes.NewReader([]byte(body))))
	buf := make([]byte, len(body)+1)
	n, err := wrapped.Read(buf)
	if err != nil || string(buf[:n]) != body {
		t.Fatalf("read n=%d err=%v body=%q", n, err, buf[:n])
	}
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := metrics.snapshot()
	if !snapshot.contentMeasured || !snapshot.measurementComplete || snapshot.firstContentType != contentMetricText {
		t.Fatalf("terminal close marked stream partial: %+v", snapshot)
	}
}

func TestContentMetricProtocolUsesCompatibilityMetadata(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://example.test/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	response := &http.Response{Header: make(http.Header), Request: request}
	response.Header.Set("Content-Type", "text/event-stream")
	markUpstreamResponse(response, "chat", nil)
	if got := contentMetricProtocolForResponse(response, "openai", "/v1/responses"); got != contentMetricProtocolChat {
		t.Fatalf("protocol=%q, want chat", got)
	}
}

func TestContentMetricsClassifyTextReasoningAndToolsForEachProtocol(t *testing.T) {
	decode := func(value string) map[string]json.RawMessage {
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(value), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	cases := []struct {
		name     string
		protocol contentMetricProtocol
		event    string
		payload  string
		want     string
	}{
		{"responses text", contentMetricProtocolResponses, "response.output_text.delta", `{"delta":"x"}`, contentMetricText},
		{"responses reasoning", contentMetricProtocolResponses, "response.reasoning_summary_text.delta", `{"delta":"x"}`, contentMetricReasoning},
		{"responses tool", contentMetricProtocolResponses, "response.function_call_arguments.delta", `{"delta":"{"}`, contentMetricTool},
		{"chat text", contentMetricProtocolChat, "", `{"choices":[{"delta":{"content":"x"}}]}`, contentMetricText},
		{"chat reasoning", contentMetricProtocolChat, "", `{"choices":[{"delta":{"reasoning_content":"x"}}]}`, contentMetricReasoning},
		{"chat tool", contentMetricProtocolChat, "", `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{"}}]}}]}`, contentMetricTool},
		{"anthropic text", contentMetricProtocolAnthropic, "content_block_delta", `{"delta":{"type":"text_delta","text":"x"}}`, contentMetricText},
		{"anthropic reasoning", contentMetricProtocolAnthropic, "content_block_delta", `{"delta":{"type":"thinking_delta","thinking":"x"}}`, contentMetricReasoning},
		{"anthropic tool", contentMetricProtocolAnthropic, "content_block_delta", `{"delta":{"type":"input_json_delta","partial_json":"{"}}`, contentMetricTool},
		{"gemini text", contentMetricProtocolGemini, "", `{"candidates":[{"content":{"parts":[{"text":"x"}]}}]}`, contentMetricText},
		{"gemini reasoning", contentMetricProtocolGemini, "", `{"candidates":[{"content":{"parts":[{"text":"x","thought":true}]}}]}`, contentMetricReasoning},
		{"gemini tool", contentMetricProtocolGemini, "", `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f"}}]}}]}`, contentMetricTool},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := classifyContentMetricEvent(testCase.protocol, testCase.event, decode(testCase.payload)); got != testCase.want {
				t.Fatalf("classification=%q want %q", got, testCase.want)
			}
		})
	}
}

func TestRequestUsageRecorderTracksWireAttemptsSeparatelyFromRetries(t *testing.T) {
	recorder := newRequestUsageRecorder(nil, time.Now().Add(-time.Second), nil, "/v1/chat/completions", "chat")
	recorder.beginAttempt()
	recorder.beginAttempt()
	recorder.recordWireAttempt(false)
	recorder.recordWireAttempt(true)
	recorder.finish(http.StatusOK, localCompatUsage{}, "")
	if recorder.item.RetryCount != 1 || recorder.item.WireAttemptCount != 2 || recorder.item.TransportFallbackCount != 1 {
		t.Fatalf("trace metrics=%+v", recorder.item)
	}
}
