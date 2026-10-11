package converter

import (
	"io"
	"strings"
	"testing"
)

func TestAnthropicStreamReadsFinalUsageFromMessageDelta(t *testing.T) {
	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":999}}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":123,"output_tokens":4,"cache_read_input_tokens":700,"cache_creation_input_tokens":100}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")

	converter := NewAnthropicStreamConverter(
		io.NopCloser(strings.NewReader(sse)),
		"model",
		2048,
		0, 0, 0, 0, 1,
	)
	stream, err := io.ReadAll(converter)
	if err != nil {
		t.Fatalf("read converted stream: %v", err)
	}

	input, output := converter.Usage()
	if input != 923 || output != 4 {
		t.Fatalf("usage = (%d, %d), want (923, 4)", input, output)
	}
	cacheRead, cacheWrite := converter.CacheTokens()
	if cacheRead != 700 || cacheWrite != 100 {
		t.Fatalf("cache usage = (%d, %d), want (700, 100)", cacheRead, cacheWrite)
	}

	foundMetadata := false
	foundContextUsage := false
	for _, frame := range decodeFrames(t, stream) {
		if frame.eventType == "metadataEvent" && strings.Contains(frame.payload, `"stopReason":"END_TURN"`) {
			foundMetadata = true
		}
		if frame.eventType == "contextUsageEvent" && strings.Contains(frame.payload, `"contextUsagePercentage":45.26`) {
			foundContextUsage = true
		}
	}
	if !foundMetadata {
		t.Fatal("metadata should use the Kiro metadataEvent stop reason")
	}
	if !foundContextUsage {
		t.Fatal("context usage should include uncached, cache-read, cache-created, and output tokens")
	}
}
