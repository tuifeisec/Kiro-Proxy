package ua

import (
	"fmt"
	"runtime"
)

const codexTUIVersion = "0.135.0"

func CodexUserAgent() string {
	return fmt.Sprintf("codex-tui/%s (%s; %s) unknown (codex-tui; %s)", codexTUIVersion, osDescription(), archDescription(), codexTUIVersion)
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
