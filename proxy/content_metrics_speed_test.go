package proxy

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

func feedContentMetricEvent(metrics *contentMetrics, at time.Time, eventType, payload string) {
	var wire strings.Builder
	if eventType != "" {
		wire.WriteString("event: ")
		wire.WriteString(eventType)
		wire.WriteByte('\n')
	}
	wire.WriteString("data: ")
	wire.WriteString(payload)
	wire.WriteString("\n\n")
	metrics.consumeAt([]byte(wire.String()), at)
}

func TestContentMetricsSpeedUsesProtocolUsageAndOutputIntervals(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	at := func(seconds int) time.Time { return base.Add(time.Duration(seconds) * time.Second) }

	t.Run("chat reasoning excluded and details subtracted", func(t *testing.T) {
		metrics := newContentMetrics(contentMetricOptions{protocol: contentMetricProtocolChat, start: base})
		feedContentMetricEvent(metrics, at(0), "", `{"choices":[{"delta":{"reasoning_content":"hidden"}}]}`)
		feedContentMetricEvent(metrics, at(1), "", `{"choices":[{"delta":{"reasoning_content":"more","content":"a"}}]}`)
		feedContentMetricEvent(metrics, at(3), "", `{"choices":[{"delta":{"content":"b"}}]}`)
		feedContentMetricEvent(metrics, at(3), "", `{"choices":[],"usage":{"completion_tokens":1186,"completion_tokens_details":{"reasoning_tokens":1024,"rejected_prediction_tokens":10,"audio_tokens":0}}}`)
		feedContentMetricEvent(metrics, at(4), "", `[DONE]`)
		metrics.finish(true)

		snapshot := metrics.snapshot()
		if snapshot.speedOutputTokens != 152 || snapshot.speedDuration != 2*time.Second || snapshot.speedBasis != "visible_output" {
			t.Fatalf("speed metrics = %+v, want tokens=152 duration=2s basis=visible_output", snapshot)
		}
		if !snapshot.firstContentAt.Equal(at(0)) {
			t.Fatalf("reasoning should still establish first content: %s", snapshot.firstContentAt)
		}
	})

	t.Run("responses done snapshots do not extend interval", func(t *testing.T) {
		metrics := newContentMetrics(contentMetricOptions{protocol: contentMetricProtocolResponses, start: base})
		feedContentMetricEvent(metrics, at(0), "response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","delta":"summary"}`)
		feedContentMetricEvent(metrics, at(1), "response.output_text.delta", `{"type":"response.output_text.delta","delta":"a"}`)
		feedContentMetricEvent(metrics, at(2), "response.output_text.delta", `{"type":"response.output_text.delta","delta":"b"}`)
		feedContentMetricEvent(metrics, at(4), "response.output_text.done", `{"type":"response.output_text.done","text":"a full snapshot"}`)
		feedContentMetricEvent(metrics, at(5), "response.completed", `{"type":"response.completed","response":{"usage":{"output_tokens":100,"output_tokens_details":{"reasoning_tokens":60}}}}`)
		metrics.finish(true)

		snapshot := metrics.snapshot()
		if snapshot.speedOutputTokens != 40 || snapshot.speedDuration != time.Second || snapshot.speedBasis != "visible_output" {
			t.Fatalf("speed metrics = %+v, want tokens=40 duration=1s basis=visible_output", snapshot)
		}
		if !snapshot.measurementComplete {
			t.Fatalf("completed response marked incomplete: %+v", snapshot)
		}
	})

	t.Run("anthropic thinking and tools are in the speed interval", func(t *testing.T) {
		metrics := newContentMetrics(contentMetricOptions{protocol: contentMetricProtocolAnthropic, start: base})
		feedContentMetricEvent(metrics, at(0), "message_start", `{"type":"message_start","message":{"usage":{"output_tokens":0}}}`)
		feedContentMetricEvent(metrics, at(1), "content_block_delta", `{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"reasoning"}}`)
		feedContentMetricEvent(metrics, at(2), "content_block_delta", `{"type":"content_block_delta","delta":{"type":"text_delta","text":"text"}}`)
		feedContentMetricEvent(metrics, at(3), "content_block_delta", `{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"x\":"}}`)
		feedContentMetricEvent(metrics, at(4), "content_block_delta", `{"type":"content_block_delta","delta":{"type":"text_delta","text":"more"}}`)
		feedContentMetricEvent(metrics, at(6), "content_block_delta", `{"type":"content_block_delta","delta":{"type":"signature_delta","signature":"signed"}}`)
		feedContentMetricEvent(metrics, at(7), "message_delta", `{"type":"message_delta","usage":{"output_tokens":6}}`)
		feedContentMetricEvent(metrics, at(8), "message_stop", `{"type":"message_stop"}`)
		metrics.finish(true)

		snapshot := metrics.snapshot()
		if snapshot.speedOutputTokens != 6 || snapshot.speedDuration != 3*time.Second || snapshot.speedBasis != "output_with_reasoning" {
			t.Fatalf("speed metrics = %+v, want tokens=6 duration=3s basis=output_with_reasoning", snapshot)
		}
		if snapshot.lastContentAt.Equal(at(6)) {
			t.Fatalf("signature must not be treated as content: %+v", snapshot)
		}
	})

	t.Run("gemini thought is excluded even in mixed parts", func(t *testing.T) {
		metrics := newContentMetrics(contentMetricOptions{protocol: contentMetricProtocolGemini, start: base})
		feedContentMetricEvent(metrics, at(0), "", `{"candidates":[{"content":{"parts":[{"text":"thought","thought":true}]}}]}`)
		feedContentMetricEvent(metrics, at(1), "", `{"candidates":[{"content":{"parts":[{"text":"thought","thought":true},{"text":"visible"}]}}]}`)
		feedContentMetricEvent(metrics, at(3), "", `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}}}]}}]}`)
		feedContentMetricEvent(metrics, at(4), "", `{"usageMetadata":{"candidatesTokenCount":11,"thoughtsTokenCount":80}}`)
		metrics.finish(true)

		snapshot := metrics.snapshot()
		if snapshot.speedOutputTokens != 11 || snapshot.speedDuration != 2*time.Second || snapshot.speedBasis != "visible_output" {
			t.Fatalf("speed metrics = %+v, want tokens=11 duration=2s basis=visible_output", snapshot)
		}
		if !snapshot.firstContentAt.Equal(at(0)) {
			t.Fatalf("thought should establish first content: %s", snapshot.firstContentAt)
		}
	})
}

func TestContentMetricsSpeedMissingZeroAndUnsupportedUsage(t *testing.T) {
	base := time.Unix(1_800_000_100, 0)
	makeMetrics := func(t *testing.T, usage string, reasoning bool) contentMetricSnapshot {
		t.Helper()
		metrics := newContentMetrics(contentMetricOptions{protocol: contentMetricProtocolChat, start: base})
		if reasoning {
			feedContentMetricEvent(metrics, base, "", `{"choices":[{"delta":{"reasoning_content":"reasoning"}}]}`)
		}
		feedContentMetricEvent(metrics, base.Add(time.Second), "", `{"choices":[{"delta":{"content":"visible"}}]}`)
		if usage != "" {
			feedContentMetricEvent(metrics, base.Add(2*time.Second), "", `{"choices":[],"usage":`+usage+`}`)
		}
		return metrics.snapshot()
	}

	cases := []struct {
		name      string
		usage     string
		reasoning bool
		want      int
		wantBasis string
	}{
		{name: "missing usage", want: -1},
		{name: "reported zero", usage: `{"completion_tokens":0}`, want: 0},
		{name: "null total is missing", usage: `{"completion_tokens":null}`, want: -1},
		{name: "null reasoning with reasoning event is unsupported", usage: `{"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":null}}`, reasoning: true, want: -1},
		{name: "reasoning details absent after reasoning event", usage: `{"completion_tokens":20}`, reasoning: true, want: -1},
		{name: "negative total", usage: `{"completion_tokens":-1}`, want: -1},
		{name: "malformed total", usage: `{"completion_tokens":"20"}`, want: -1},
		{name: "details exceed total", usage: `{"completion_tokens":10,"completion_tokens_details":{"reasoning_tokens":7,"rejected_prediction_tokens":4}}`, want: -1, wantBasis: "visible_output"},
		{name: "audio output is unsupported", usage: `{"completion_tokens":10,"completion_tokens_details":{"audio_tokens":1}}`, want: -1},
		{name: "rejected tokens excluded even without reasoning details", usage: `{"completion_tokens":20,"completion_tokens_details":{"rejected_prediction_tokens":3}}`, want: 17},
		{name: "rejected tokens exceed total without reasoning details", usage: `{"completion_tokens":2,"completion_tokens_details":{"rejected_prediction_tokens":3}}`, want: -1},
		{name: "rejected tokens are subtracted once when reasoning is known", usage: `{"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":0,"rejected_prediction_tokens":3}}`, want: 17, wantBasis: "visible_output"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			snapshot := makeMetrics(t, testCase.usage, testCase.reasoning)
			if snapshot.speedOutputTokens != testCase.want {
				t.Fatalf("speedOutputTokens=%d want %d: %+v", snapshot.speedOutputTokens, testCase.want, snapshot)
			}
			wantBasis := testCase.wantBasis
			if wantBasis == "" {
				wantBasis = "reported_output"
			}
			if snapshot.speedBasis != wantBasis {
				t.Fatalf("speedBasis=%q want %s: %+v", snapshot.speedBasis, wantBasis, snapshot)
			}
		})
	}
}

func TestContentMetricsClassifierIgnoresSnapshotsEmptyToolsAndSignatures(t *testing.T) {
	decode := func(value string) map[string]json.RawMessage {
		t.Helper()
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
		{"responses text done snapshot", contentMetricProtocolResponses, "response.output_text.done", `{"text":"full output"}`, ""},
		{"responses item done snapshot", contentMetricProtocolResponses, "response.output_item.done", `{"item":{"type":"function_call","name":"f","arguments":"{}"}}`, ""},
		{"responses empty function metadata", contentMetricProtocolResponses, "response.output_item.added", `{"item":{"type":"function_call","name":"","arguments":""}}`, ""},
		{"responses empty custom metadata", contentMetricProtocolResponses, "response.output_item.added", `{"item":{"type":"custom_tool_call","name":"","input":{ }}}`, ""},
		{"responses function name", contentMetricProtocolResponses, "response.output_item.added", `{"item":{"type":"function_call","name":"f","arguments":""}}`, contentMetricTool},
		{"responses custom input delta", contentMetricProtocolResponses, "response.custom_tool_call_input.delta", `{"delta":"abc"}`, contentMetricTool},
		{"responses code delta", contentMetricProtocolResponses, "response.code_interpreter_call_code.delta", `{"delta":"print(1)"}`, contentMetricTool},
		{"responses refusal delta", contentMetricProtocolResponses, "response.refusal.delta", `{"delta":"cannot help"}`, contentMetricText},
		{"chat empty tool metadata", contentMetricProtocolChat, "", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","type":"function"}]}}]}`, ""},
		{"chat tool arguments", contentMetricProtocolChat, "", `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{"}}]}}]}`, contentMetricTool},
		{"chat refusal", contentMetricProtocolChat, "", `{"choices":[{"delta":{"refusal":"refused"}}]}`, contentMetricText},
		{"chat text array", contentMetricProtocolChat, "", `{"choices":[{"delta":{"content":[{"type":"text","text":"hello"}]}}]}`, contentMetricText},
		{"chat reasoning summary", contentMetricProtocolChat, "", `{"choices":[{"delta":{"reasoning_summary":"summary"}}]}`, contentMetricReasoning},
		{"anthropic signature", contentMetricProtocolAnthropic, "content_block_delta", `{"delta":{"type":"signature_delta","signature":"signed"}}`, ""},
		{"gemini function response", contentMetricProtocolGemini, "", `{"candidates":[{"content":{"parts":[{"functionResponse":{"name":"f","response":{"ok":true}}}]}}]}`, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := classifyContentMetricEvent(testCase.protocol, testCase.event, decode(testCase.payload)); got != testCase.want {
				t.Fatalf("classification=%q want %q", got, testCase.want)
			}
		})
	}
}

func TestContentMetricsMixedEventsTrackReasoningAndSpeedIndependently(t *testing.T) {
	base := time.Unix(1_800_000_200, 0)
	chat := newContentMetrics(contentMetricOptions{protocol: contentMetricProtocolChat, start: base})
	feedContentMetricEvent(chat, base, "", `{"choices":[{"delta":{"reasoning_content":"thinking","tool_calls":[{"function":{"arguments":"{"}}]}}]}`)
	feedContentMetricEvent(chat, base.Add(2*time.Second), "", `{"choices":[],"usage":{"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":15}}}`)
	chatSnapshot := chat.snapshot()
	if chatSnapshot.speedOutputTokens != 5 || chatSnapshot.speedDuration != 0 {
		t.Fatalf("mixed chat event should mark reasoning and tool output with a single speed timestamp: %+v", chatSnapshot)
	}

	gemini := newContentMetrics(contentMetricOptions{protocol: contentMetricProtocolGemini, start: base})
	feedContentMetricEvent(gemini, base, "", `{"candidates":[{"content":{"parts":[{"text":"thought","thought":true},{"text":"visible"}]}}]}`)
	feedContentMetricEvent(gemini, base.Add(time.Second), "", `{"usageMetadata":{"candidatesTokenCount":4}}`)
	geminiSnapshot := gemini.snapshot()
	if geminiSnapshot.speedOutputTokens != 4 || geminiSnapshot.speedDuration != 0 {
		t.Fatalf("mixed Gemini parts should include visible output from the single speed event: %+v", geminiSnapshot)
	}
}

func TestContentMetricsFirstContentHookCanBeAttachedOnceAfterObservation(t *testing.T) {
	base := time.Unix(1_800_000_300, 0)
	metrics := newContentMetrics(contentMetricOptions{protocol: contentMetricProtocolChat, start: base})
	feedContentMetricEvent(metrics, base.Add(time.Second), "", `{"choices":[{"delta":{"content":"first"}}]}`)
	firstCalls, duplicateCalls := 0, 0
	metrics.setFirstContentHook(func(at time.Time, contentType string) {
		firstCalls++
		if !at.Equal(base.Add(time.Second)) || contentType != contentMetricText {
			t.Errorf("hook args=(%s,%q)", at, contentType)
		}
	})
	metrics.setFirstContentHook(func(time.Time, string) { duplicateCalls++ })
	feedContentMetricEvent(metrics, base.Add(2*time.Second), "", `{"choices":[{"delta":{"content":"second"}}]}`)
	if firstCalls != 1 || duplicateCalls != 0 {
		t.Fatalf("hook calls first=%d duplicate=%d", firstCalls, duplicateCalls)
	}
}

func TestContentMetricsReadUsesOneTimestampForAllBytes(t *testing.T) {
	base := time.Unix(1_800_000_400, 0)
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\n"
	nowCalls := 0
	metrics := newContentMetrics(contentMetricOptions{
		protocol: contentMetricProtocolChat,
		start:    base,
		now: func() time.Time {
			nowCalls++
			return base.Add(time.Duration(nowCalls) * time.Second)
		},
	})
	wrapped := metrics.wrap(&contentMetricSingleReadCloser{reader: bytes.NewReader([]byte(body))})
	if _, err := io.Copy(io.Discard, wrapped); err != nil {
		t.Fatal(err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := metrics.snapshot()
	if nowCalls != 1 || snapshot.speedDuration != 0 || !snapshot.firstContentAt.Equal(base.Add(time.Second)) {
		t.Fatalf("now calls=%d snapshot=%+v", nowCalls, snapshot)
	}
}

type contentMetricSingleReadCloser struct {
	reader *bytes.Reader
	read   bool
}

func (r *contentMetricSingleReadCloser) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	return r.reader.Read(p)
}

func (r *contentMetricSingleReadCloser) Close() error { return nil }

func TestContentMetricsFailureEventsCannotCompleteMeasurement(t *testing.T) {
	base := time.Unix(1_800_000_500, 0)
	metrics := newContentMetrics(contentMetricOptions{protocol: contentMetricProtocolResponses, start: base})
	feedContentMetricEvent(metrics, base, "response.output_text.delta", `{"type":"response.output_text.delta","delta":"partial"}`)
	feedContentMetricEvent(metrics, base.Add(time.Second), "response.failed", `{"type":"response.failed","error":{"message":"upstream failed"},"response":{"usage":{"output_tokens":7}}}`)
	metrics.finish(true)
	snapshot := metrics.snapshot()
	if !snapshot.contentMeasured || snapshot.speedOutputTokens != 7 || snapshot.measurementComplete {
		t.Fatalf("failed stream must keep observed fields while remaining incomplete: %+v", snapshot)
	}
}

func TestContentMetricsMCPArgumentsDefineToolSpeedInterval(t *testing.T) {
	base := time.Unix(1_800_000_600, 0)
	for _, withText := range []bool{false, true} {
		name := "MCP only"
		if withText {
			name = "MCP followed by text"
		}
		t.Run(name, func(t *testing.T) {
			metrics := newContentMetrics(contentMetricOptions{protocol: contentMetricProtocolResponses, start: base})
			feedContentMetricEvent(metrics, base, "response.mcp_call_arguments.delta", `{"type":"response.mcp_call_arguments.delta","delta":"{\"q\":"}`)
			feedContentMetricEvent(metrics, base.Add(time.Second), "response.mcp_call_arguments.delta", `{"type":"response.mcp_call_arguments.delta","delta":"\"x\"}"}`)
			wantDuration := time.Second
			if withText {
				feedContentMetricEvent(metrics, base.Add(3*time.Second), "response.output_text.delta", `{"type":"response.output_text.delta","delta":"result"}`)
				wantDuration = 3 * time.Second
			}
			feedContentMetricEvent(metrics, base.Add(4*time.Second), "response.mcp_call_arguments.done", `{"type":"response.mcp_call_arguments.done","arguments":"{\"q\":\"x\"}"}`)
			feedContentMetricEvent(metrics, base.Add(5*time.Second), "response.completed", `{"type":"response.completed","response":{"usage":{"output_tokens":10,"output_tokens_details":{"reasoning_tokens":0}}}}`)
			metrics.finish(true)
			snapshot := metrics.snapshot()
			if !snapshot.contentMeasured || !snapshot.measurementComplete || snapshot.firstContentType != contentMetricTool || snapshot.speedOutputTokens != 10 || snapshot.speedDuration != wantDuration {
				t.Fatalf("MCP interval=%+v want duration %v", snapshot, wantDuration)
			}
		})
	}
}

func TestContentMetricsObservedAudioCannotProduceTextSpeed(t *testing.T) {
	base := time.Unix(1_800_000_700, 0)
	for _, tc := range []struct {
		name                      string
		protocol                  contentMetricProtocol
		first, audio, last, usage string
	}{
		{"Responses", contentMetricProtocolResponses,
			`{"type":"response.output_text.delta","delta":"a"}`,
			`{"type":"response.audio.delta","delta":"YWJj"}`,
			`{"type":"response.output_text.delta","delta":"b"}`,
			`{"type":"response.completed","response":{"usage":{"output_tokens":100,"output_tokens_details":{"reasoning_tokens":0}}}}`},
		{"Chat", contentMetricProtocolChat,
			`{"choices":[{"delta":{"content":"a"}}]}`,
			`{"choices":[{"delta":{"audio":{"data":"YWJj"}}}]}`,
			`{"choices":[{"delta":{"content":"b"}}]}`,
			`{"choices":[],"usage":{"completion_tokens":100,"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0}}}`},
		{"Gemini", contentMetricProtocolGemini,
			`{"candidates":[{"content":{"parts":[{"text":"a"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/pcm","data":"YWJj"}}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"b"}]}}]}`,
			`{"usageMetadata":{"candidatesTokenCount":100}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metrics := newContentMetrics(contentMetricOptions{protocol: tc.protocol, start: base})
			for i, payload := range []string{tc.first, tc.audio, tc.last, tc.usage} {
				feedContentMetricEvent(metrics, base.Add(time.Duration(i)*time.Second), "", payload)
			}
			metrics.finish(true)
			snapshot := metrics.snapshot()
			if !snapshot.contentMeasured || !snapshot.measurementComplete || snapshot.speedOutputTokens != -1 {
				t.Fatalf("mixed audio/text must not have text speed: %+v", snapshot)
			}
		})
	}
}
