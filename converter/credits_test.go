package converter

import (
	"io"
	"math"
	"strings"
	"testing"

	json "github.com/goccy/go-json"

	"kiro-proxy/store"
)

func TestStreamMeteringMatchesStoreCredits(t *testing.T) {
	tests := []struct {
		name   string
		model  *store.Model
		run    func() []byte
		input  int
		output int
		read   int
		write  int
	}{
		{
			name:  "openai defaults",
			model: &store.Model{Provider: "openai"},
			run: func() []byte {
				sse := `data: {"choices":[],"usage":{"prompt_tokens":1000000,"completion_tokens":1000000,"prompt_tokens_details":{"cached_tokens":100000,"cache_write_tokens":50000}}}` + "\n\ndata: [DONE]\n\n"
				c := NewOpenAIStreamConverter(io.NopCloser(strings.NewReader(sse)), "model", 2_000_000, 0, 0, 0, 0, 1)
				stream, err := io.ReadAll(c)
				if err != nil {
					t.Fatalf("read OpenAI stream: %v", err)
				}
				return stream
			},
			input: 1_000_000, output: 1_000_000, read: 100_000, write: 50_000,
		},
		{
			name:  "responses defaults",
			model: &store.Model{Provider: "openai"},
			run: func() []byte {
				sse := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1000000,\"output_tokens\":1000000,\"input_tokens_details\":{\"cached_tokens\":100000}}}}\n\n"
				c := NewResponsesStreamConverter(io.NopCloser(strings.NewReader(sse)), "model", 2_000_000, 0, 0, 0, 0, 1)
				stream, err := io.ReadAll(c)
				if err != nil {
					t.Fatalf("read Responses stream: %v", err)
				}
				return stream
			},
			input: 1_000_000, output: 1_000_000, read: 100_000,
		},
		{
			name:  "anthropic defaults",
			model: &store.Model{Provider: "anthropic"},
			run: func() []byte {
				sse := "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":850000,\"output_tokens\":1000000,\"cache_read_input_tokens\":100000,\"cache_creation_input_tokens\":50000}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				c := NewAnthropicStreamConverter(io.NopCloser(strings.NewReader(sse)), "model", 2_000_000, 0, 0, 0, 0, 1)
				stream, err := io.ReadAll(c)
				if err != nil {
					t.Fatalf("read Anthropic stream: %v", err)
				}
				return stream
			},
			input: 1_000_000, output: 1_000_000, read: 100_000, write: 50_000,
		},
		{
			name:  "gemini defaults",
			model: &store.Model{Provider: "gemini"},
			run: func() []byte {
				sse := "data: {\"usageMetadata\":{\"promptTokenCount\":950000,\"candidatesTokenCount\":1000000,\"cachedContentTokenCount\":100000},\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n"
				c := NewGeminiStreamConverter(io.NopCloser(strings.NewReader(sse)), "model", 2_000_000, 0, 0, 0, 0, 1)
				stream, err := io.ReadAll(c)
				if err != nil {
					t.Fatalf("read Gemini stream: %v", err)
				}
				return stream
			},
			input: 950_000, output: 1_000_000, read: 100_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got float64
			found := false
			for _, frame := range decodeFrames(t, tt.run()) {
				if frame.eventType != "meteringEvent" {
					continue
				}
				var payload struct {
					Usage float64 `json:"usage"`
				}
				if err := json.Unmarshal([]byte(frame.payload), &payload); err != nil {
					t.Fatalf("decode metering payload: %v", err)
				}
				got, found = payload.Usage, true
			}
			if !found {
				t.Fatal("stream has no meteringEvent")
			}
			want := store.CalculateCredits(tt.input, tt.output, tt.read, tt.write, tt.model)
			if math.Abs(got-want) > 1e-9 {
				t.Fatalf("metering credits = %.12f, want store calculation %.12f", got, want)
			}
		})
	}
}

func TestMeteringFramePreservesCreditPrecision(t *testing.T) {
	for _, want := range []float64{0.0004, 0.1711507941956882} {
		frames := decodeFrames(t, MeteringFrame(want))
		if len(frames) != 1 {
			t.Fatalf("frame count = %d, want 1", len(frames))
		}
		var payload struct {
			Usage float64 `json:"usage"`
		}
		if err := json.Unmarshal([]byte(frames[0].payload), &payload); err != nil {
			t.Fatalf("decode metering payload: %v", err)
		}
		if payload.Usage != want {
			t.Fatalf("round-trip usage = %.17g, want %.17g", payload.Usage, want)
		}
	}
}
