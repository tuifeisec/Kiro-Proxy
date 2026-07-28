package proxy

import (
	"errors"
	"strings"
	"testing"
)

func TestReadUntilValidSSEEventBuffersResponsesMetadata(t *testing.T) {
	stream := "event: response.created\r\ndata: {\"type\":\"response.created\"}\r\n\r\n" +
		": heartbeat\r\n\r\n" +
		"event: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\r\ndata: \"delta\":\"ok\"}\r\n\r\n"

	prefix, err := readUntilValidSSEEvent(strings.NewReader(stream), sseProbeResponses, len(stream)+1)
	if err != nil {
		t.Fatalf("probe stream: %v", err)
	}
	if string(prefix) != stream {
		t.Fatalf("buffered prefix changed:\n%s", prefix)
	}
}

func TestReadUntilValidSSEEventRejectsFailureAfterMetadata(t *testing.T) {
	stream := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n" +
		"event: response.failed\ndata: {\"type\":\"response.failed\"}\n\n"

	_, err := readUntilValidSSEEvent(strings.NewReader(stream), sseProbeResponses, len(stream)+1)
	if !errors.Is(err, errSSEPrecommitUpstream) {
		t.Fatalf("error = %v, want upstream precommit failure", err)
	}
}

func TestReadUntilValidSSEEventEnforcesBufferLimit(t *testing.T) {
	stream := ": " + strings.Repeat("x", 64) + "\n\n"

	_, err := readUntilValidSSEEvent(strings.NewReader(stream), sseProbeResponses, 16)
	if !errors.Is(err, errSSEPrecommitBufferExceeded) {
		t.Fatalf("error = %v, want buffer limit error", err)
	}
}

func TestClassifyResponsesProbeEventAcceptsFunctionCall(t *testing.T) {
	event := []byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"name\":\"lookup\"}}\n")

	accepted, err := classifySSEEvent(sseProbeResponses, event)
	if err != nil || !accepted {
		t.Fatalf("accepted = %v, error = %v", accepted, err)
	}
}
