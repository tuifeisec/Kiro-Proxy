package converter

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestSSEEventFramingPreservesMultilineDataAndIgnoresHeartbeat(t *testing.T) {
	wire := ": keepalive\r\n\r\nid: 2\r\nevent: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\r\ndata: \"delta\":\"x\"}\r\n\r\n"
	event, data, err := readSSEEvent(bufio.NewReader(strings.NewReader(wire)))
	if err != nil || event != "response.output_text.delta" || data != "{\"type\":\"response.output_text.delta\",\n\"delta\":\"x\"}" {
		t.Fatalf("event=%q data=%q err=%v", event, data, err)
	}
}

func TestSSEEventEOFDoesNotCompletePendingData(t *testing.T) {
	for _, wire := range []string{"data: [DONE]", "data: [DONE]\n", "data: [DONE]\r\n\r"} {
		event, data, err := readSSEEvent(bufio.NewReader(strings.NewReader(wire)))
		if err != io.EOF || event != "" || data != "" {
			t.Fatalf("incomplete event %q yielded event=%q data=%q err=%v", wire, event, data, err)
		}
	}
}

func TestSSEEventPreservesReadErrorBeforeDelimiter(t *testing.T) {
	want := errors.New("mock upstream disconnected")
	reader := io.MultiReader(strings.NewReader("data: [DONE]\n"), iotest.ErrReader(want))
	_, data, err := readSSEEvent(bufio.NewReader(reader))
	if !errors.Is(err, want) || data != "" {
		t.Fatalf("data=%q err=%v, want original error", data, err)
	}
}
