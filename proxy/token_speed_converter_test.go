package proxy

import (
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/store"
)

type tokenSpeedConvertedStream interface {
	io.ReadCloser
	Outcome() converter.StreamOutcome
	Usage() (int, int)
}

func newTokenSpeedConvertedStream(protocol contentMetricProtocol, body io.ReadCloser) tokenSpeedConvertedStream {
	switch protocol {
	case contentMetricProtocolResponses:
		return converter.NewResponsesStreamConverter(body, "gpt-test", 2048, 0, 0, 0, 0, 1)
	case contentMetricProtocolChat:
		return converter.NewOpenAIStreamConverter(body, "gpt-test", 2048, 0, 0, 0, 0, 1)
	case contentMetricProtocolAnthropic:
		return converter.NewAnthropicStreamConverter(body, "model", 2048, 0, 0, 0, 0, 1)
	default:
		return converter.NewGeminiStreamConverter(body, "model", 2048, 0, 0, 0, 0, 1)
	}
}

// No EOF arrives after the last event: the converter must close the connection.
type tokenSpeedHeldBody struct {
	*tokenSpeedScriptBody
	closed   chan struct{}
	once     sync.Once
	waiting  chan struct{}
	waitOnce sync.Once
}

func (b *tokenSpeedHeldBody) Read(p []byte) (int, error) {
	if b.index < len(b.chunks) {
		return b.tokenSpeedScriptBody.Read(p)
	}
	if b.waiting != nil {
		b.waitOnce.Do(func() { close(b.waiting) })
	}
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *tokenSpeedHeldBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestTokenSpeedRealConvertersConsumeTerminalSSEBoundary(t *testing.T) {
	for _, protocol := range []contentMetricProtocol{contentMetricProtocolResponses, contentMetricProtocolChat, contentMetricProtocolAnthropic, contentMetricProtocolGemini} {
		for _, newline := range []struct{ name, value string }{{"LF", "\n"}, {"CRLF", "\r\n"}} {
			for _, complete := range []bool{true, false} {
				name := string(protocol) + "/" + newline.name + "/missing delimiter"
				if complete {
					name = string(protocol) + "/" + newline.name + "/held connection"
				}
				t.Run(name, func(t *testing.T) {
					db, err := store.Open(t.TempDir() + "/speed.db")
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					start := time.Now().Add(-10 * time.Second)
					model := &store.Model{ID: "converter-speed", Provider: "openai", ModelID: "gpt-test"}
					recorder := newRequestUsageRecorder(db, start, model, "/", string(protocol))
					eol := newline.value
					event := func(payload string) string { return "data: " + payload + eol + eol }
					var chunks []string
					var terminal string
					switch protocol {
					case contentMetricProtocolResponses:
						chunks = []string{event(`{"type":"response.output_text.delta","delta":"a"}`), event(`{"type":"response.output_text.delta","delta":"b"}`)}
						terminal = `{"type":"response.completed","response":{"usage":{"output_tokens":10,"output_tokens_details":{"reasoning_tokens":0}}}}`
					case contentMetricProtocolChat:
						chunks = []string{event(`{"choices":[{"delta":{"content":"a"}}]}`), event(`{"choices":[{"delta":{"content":"b"},"finish_reason":"stop"}]}`), event(`{"choices":[],"usage":{"completion_tokens":10,"completion_tokens_details":{"reasoning_tokens":0}}}`)}
						terminal = `[DONE]`
					case contentMetricProtocolAnthropic:
						chunks = []string{event(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"a"}}`), event(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"b"}}`), event(`{"type":"message_delta","usage":{"output_tokens":10}}`)}
						terminal = `{"type":"message_stop"}`
					case contentMetricProtocolGemini:
						chunks = []string{event(`{"candidates":[{"content":{"parts":[{"text":"a"}]}}]}`), event(`{"candidates":[{"content":{"parts":[{"text":"b"}]}}]}`)}
						terminal = `{"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}],"usageMetadata":{"candidatesTokenCount":10}}`
					}
					chunks = append(chunks, "data: "+terminal+eol)
					if complete {
						for _, char := range eol {
							chunks = append(chunks, string(char))
						}
					}
					now := start
					script := &tokenSpeedScriptBody{chunks: chunks, beforeRead: func(index int) { now = start.Add(time.Duration(index+1) * time.Second) }}
					var source io.ReadCloser = script
					if complete {
						source = &tokenSpeedHeldBody{tokenSpeedScriptBody: script, closed: make(chan struct{})}
					}
					req, err := http.NewRequestWithContext(withUpstreamContentProtocol(t.Context(), protocol), http.MethodPost, "https://upstream.invalid/custom", nil)
					if err != nil {
						t.Fatal(err)
					}
					resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Request: req, Body: source}
					recorder.observeUpstreamContent(resp, model, req.URL.String())
					metrics := recorder.content
					metrics.now = func() time.Time { return now }
					recorder.wrapResponseForModel(resp, model, "/custom", nil)
					stream := newTokenSpeedConvertedStream(protocol, resp.Body)
					defer stream.Close()
					readDone := make(chan error, 1)
					go func() { _, err := io.Copy(io.Discard, stream); readDone <- err }()
					select {
					case err := <-readDone:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(2 * time.Second):
						_ = stream.Close()
						t.Fatal("converter waited for EOF after a complete terminal event")
					}
					outcome := stream.Outcome()
					snapshot := metrics.snapshot()
					if !complete {
						if snapshot.measurementComplete {
							t.Fatalf("missing delimiter was treated as complete: %+v", snapshot)
						}
						return
					}
					if outcome != converter.StreamOutcomeCompleted || !snapshot.measurementComplete || snapshot.speedOutputTokens != 10 || snapshot.speedDuration != time.Second {
						t.Fatalf("terminal delimiter not consumed: outcome=%s snapshot=%+v", outcome, snapshot)
					}
					if script.index != len(script.chunks) {
						t.Fatal("converter left the final SSE delimiter unread")
					}
					_, output := stream.Usage()
					recorder.finish(http.StatusOK, localCompatUsage{OutputTokens: output}, "")
					page, err := db.QueryRequestUsagePage(store.RequestUsageQuery{})
					if err != nil || len(page.Items) != 1 {
						t.Fatalf("page=%+v err=%v", page, err)
					}
					if item := page.Items[0]; item.TokenRateStatus != "ok" || item.TokensPerSecond == nil || *item.TokensPerSecond != 9 {
						t.Fatalf("converter speed was not persisted: %+v", item)
					}
				})
			}
		}
	}
}

func TestTokenSpeedRealConvertersFailWithoutWaitingForEOF(t *testing.T) {
	for _, protocol := range []contentMetricProtocol{contentMetricProtocolResponses, contentMetricProtocolChat, contentMetricProtocolAnthropic, contentMetricProtocolGemini} {
		t.Run(string(protocol), func(t *testing.T) {
			metrics := newContentMetrics(contentMetricOptions{protocol: protocol})
			source := &tokenSpeedHeldBody{tokenSpeedScriptBody: &tokenSpeedScriptBody{chunks: []string{"data: {\"type\":\"error\",\"error\":{\"message\":\"mock failure\"}}\n\n"}}, closed: make(chan struct{})}
			stream := newTokenSpeedConvertedStream(protocol, metrics.wrap(source))
			defer stream.Close()
			done := make(chan error, 1)
			go func() { _, err := io.Copy(io.Discard, stream); done <- err }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				_ = stream.Close()
				t.Fatal("converter waited for EOF after an error event")
			}
			if outcome := stream.Outcome(); outcome != converter.StreamOutcomeUpstreamProtocolFailure {
				t.Fatalf("outcome=%s", outcome)
			}
			if metrics.snapshot().measurementComplete {
				t.Fatal("upstream failure was treated as a valid speed sample")
			}
		})
	}
}

func TestTokenSpeedRealConvertersCancelWhileAwaitingSSEBoundary(t *testing.T) {
	for _, protocol := range []contentMetricProtocol{contentMetricProtocolResponses, contentMetricProtocolChat, contentMetricProtocolAnthropic, contentMetricProtocolGemini} {
		t.Run(string(protocol), func(t *testing.T) {
			source := &tokenSpeedHeldBody{tokenSpeedScriptBody: &tokenSpeedScriptBody{chunks: []string{"data: {\"partial\":true}\n"}}, closed: make(chan struct{}), waiting: make(chan struct{})}
			stream := newTokenSpeedConvertedStream(protocol, source)
			defer stream.Close()
			done := make(chan error, 1)
			go func() { _, err := io.Copy(io.Discard, stream); done <- err }()
			select {
			case <-source.waiting:
			case <-time.After(2 * time.Second):
				_ = stream.Close()
				t.Fatal("converter did not await the pending SSE boundary")
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("cancel did not unblock the converter")
			}
			if outcome := stream.Outcome(); outcome != converter.StreamOutcomeClientCanceled {
				t.Fatalf("outcome=%s", outcome)
			}
		})
	}
}
