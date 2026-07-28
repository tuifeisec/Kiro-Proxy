package converter

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestResponsesStreamFailedEmitsControlledFailure(t *testing.T) {
	sse := "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\"}}}\n\n"
	c := NewResponsesStreamConverter(io.NopCloser(strings.NewReader(sse)), "model", 2048, 0, 0, 0, 0, 1)
	stream, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read converted stream: %v", err)
	}
	assertControlledFailure(t, decodeFrames(t, stream))
	if got := c.Outcome(); got != StreamOutcomeUpstreamProtocolFailure {
		t.Fatalf("outcome = %q, want %q", got, StreamOutcomeUpstreamProtocolFailure)
	}
	if c.Error() == nil || c.Succeeded() {
		t.Fatal("failed stream should expose an error and report unsuccessful")
	}
}

func TestOpenAIStreamMissingTerminalEmitsControlledFailure(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"\"}]}\n\n"
	c := NewOpenAIStreamConverter(io.NopCloser(strings.NewReader(sse)), "model", 2048, 0, 0, 0, 0, 1)
	stream, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read converted stream: %v", err)
	}
	assertControlledFailure(t, decodeFrames(t, stream))
	if got := c.Outcome(); got != StreamOutcomeMissingTerminal {
		t.Fatalf("outcome = %q, want %q", got, StreamOutcomeMissingTerminal)
	}
}

func TestAnthropicStreamMessageStopCompletes(t *testing.T) {
	sse := "data: {\"type\":\"message_stop\"}\n\n"
	c := NewAnthropicStreamConverter(io.NopCloser(strings.NewReader(sse)), "model", 2048, 0, 0, 0, 0, 1)
	stream, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read converted stream: %v", err)
	}
	if got := c.Outcome(); got != StreamOutcomeCompleted {
		t.Fatalf("outcome = %q, want %q", got, StreamOutcomeCompleted)
	}
	frames := decodeFrames(t, stream)
	if countEvent(frames, "metadataEvent") != 1 || countEvent(frames, "exceptionEvent") != 0 {
		t.Fatalf("unexpected terminal frames: %+v", frames)
	}
}

func TestGeminiStreamFinishReasonCompletes(t *testing.T) {
	sse := "data: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}]}\n\n"
	c := NewGeminiStreamConverter(io.NopCloser(strings.NewReader(sse)), "model", 2048, 0, 0, 0, 0, 1)
	stream, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read converted stream: %v", err)
	}
	if got := c.Outcome(); got != StreamOutcomeCompleted {
		t.Fatalf("outcome = %q, want %q", got, StreamOutcomeCompleted)
	}
	frames := decodeFrames(t, stream)
	if countEvent(frames, "metadataEvent") != 1 || countEvent(frames, "exceptionEvent") != 0 {
		t.Fatalf("unexpected terminal frames: %+v", frames)
	}
}

func TestOpenAIStreamCloseCancelsSource(t *testing.T) {
	source, upstreamWriter := io.Pipe()
	c := NewOpenAIStreamConverter(source, "model", 2048, 0, 0, 0, 0, 1)
	if err := c.Close(); err != nil {
		t.Fatalf("close converter: %v", err)
	}
	if got := c.Outcome(); got != StreamOutcomeClientCanceled {
		t.Fatalf("outcome = %q, want %q", got, StreamOutcomeClientCanceled)
	}
	if _, err := upstreamWriter.Write([]byte("data: ignored\n\n")); err == nil {
		t.Fatal("upstream writer should observe the closed source")
	}
	_ = upstreamWriter.Close()
}

func TestCompletedStreamClosePreservesOutcome(t *testing.T) {
	tests := []struct {
		name string
		new  func() interface {
			io.ReadCloser
			Outcome() StreamOutcome
		}
	}{
		{name: "responses", new: func() interface {
			io.ReadCloser
			Outcome() StreamOutcome
		} {
			return NewResponsesStreamConverter(io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{}}\n\n")), "model", 2048, 0, 0, 0, 0, 1)
		}},
		{name: "anthropic", new: func() interface {
			io.ReadCloser
			Outcome() StreamOutcome
		} {
			return NewAnthropicStreamConverter(io.NopCloser(strings.NewReader("data: {\"type\":\"message_stop\"}\n\n")), "model", 2048, 0, 0, 0, 0, 1)
		}},
		{name: "gemini", new: func() interface {
			io.ReadCloser
			Outcome() StreamOutcome
		} {
			return NewGeminiStreamConverter(io.NopCloser(strings.NewReader("data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")), "model", 2048, 0, 0, 0, 0, 1)
		}},
		{name: "openai", new: func() interface {
			io.ReadCloser
			Outcome() StreamOutcome
		} {
			return NewOpenAIStreamConverter(io.NopCloser(strings.NewReader("data: [DONE]\n\n")), "model", 2048, 0, 0, 0, 0, 1)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := tt.new()
			if _, err := io.ReadAll(stream); err != nil {
				t.Fatalf("read stream: %v", err)
			}
			if err := stream.Close(); err != nil {
				t.Fatalf("close stream: %v", err)
			}
			if got := stream.Outcome(); got != StreamOutcomeCompleted {
				t.Fatalf("outcome after close = %q, want %q", got, StreamOutcomeCompleted)
			}
		})
	}
}

func TestOpenAIDoneEndsStreamWithoutUpstreamEOF(t *testing.T) {
	source, upstreamWriter := io.Pipe()
	c := NewOpenAIStreamConverter(source, "model", 2048, 0, 0, 0, 0, 1)
	go func() {
		_, _ = io.WriteString(upstreamWriter, "data: [DONE]\n\n")
	}()

	readDone := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(c)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("converter kept waiting after [DONE]")
	}
	_ = upstreamWriter.Close()
}

func assertControlledFailure(t *testing.T, frames []decodedFrame) {
	t.Helper()
	if countEvent(frames, "exceptionEvent") != 1 {
		t.Fatalf("controlled failure frame missing: %+v", frames)
	}
	if countEvent(frames, "metadataEvent") != 0 || countEvent(frames, "contextUsageEvent") != 0 || countEvent(frames, "meteringEvent") != 0 {
		t.Fatalf("failed stream emitted normal terminal frames: %+v", frames)
	}
}
