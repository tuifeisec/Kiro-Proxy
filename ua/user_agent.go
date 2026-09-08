package ua

import (
	"fmt"
	"runtime"
)

const codexTUIVersion = "0.135.0"

func CodexUserAgent() string {
	return fmt.Sprintf("codex-tui/%s (%s; %s) unknown (codex-tui; %s)", codexTUIVersion, osDescription(), archDescription(), codexTUIVersion)
}

// SDK 版本是固定的兼容标识，并非本程序实际引入的 SDK 依赖。
// 格式与版本来源见 docs/2026-09-08-opencode-go-session.md。
func ProviderUserAgent(provider string) string {
	switch provider {
	case "anthropic":
		return "Anthropic/Go 1.71.0"
	case "gemini":
		return "google-genai-sdk/1.71.0 gl-go/" + runtime.Version()
	case "codex":
		return CodexUserAgent()
	default:
		return "OpenAI/Go 3.56.0"
	}
}

func osDescription() string {
	if runtime.GOOS == "windows" {
		if desc := windowsDescription(); desc != "" {
			return desc
		}
	}
	return runtime.GOOS
}

func archDescription() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "386":
		return "x86"
	case "arm64":
		return "aarch64"
	default:
		return runtime.GOARCH
	}
}
