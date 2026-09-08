package ua

import (
	"runtime"
	"strings"
	"testing"
)

func TestProviderUserAgent(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{provider: "openai", want: "OpenAI/Go 3.56.0"},
		{provider: "anthropic", want: "Anthropic/Go 1.71.0"},
		{provider: "gemini", want: "google-genai-sdk/1.71.0 gl-go/" + runtime.Version()},
		{provider: "codex", want: CodexUserAgent()},
		{provider: "", want: "OpenAI/Go 3.56.0"},
		{provider: "custom", want: "OpenAI/Go 3.56.0"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := ProviderUserAgent(tt.provider)
			if got != tt.want {
				t.Fatalf("ProviderUserAgent(%q) = %q, want %q", tt.provider, got, tt.want)
			}
			if strings.Contains(got, "kiro-proxy") {
				t.Fatalf("project identity leaked in User-Agent: %q", got)
			}
		})
	}
}
