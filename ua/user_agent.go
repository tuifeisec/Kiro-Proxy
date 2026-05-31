package ua

import (
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/sys/windows/registry"
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

func windowsDescription() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return "Windows"
	}
	defer key.Close()

	major, _, _ := key.GetIntegerValue("CurrentMajorVersionNumber")
	minor, _, _ := key.GetIntegerValue("CurrentMinorVersionNumber")
	build, _, _ := key.GetStringValue("CurrentBuildNumber")
	if build == "" {
		build, _, _ = key.GetStringValue("CurrentBuild")
	}

	if major == 0 {
		version, _, _ := key.GetStringValue("CurrentVersion")
		parts := strings.Split(version, ".")
		if len(parts) >= 2 {
			fmt.Sscanf(parts[0], "%d", &major)
			fmt.Sscanf(parts[1], "%d", &minor)
		}
	}

	// Windows 11 为保持兼容，注册表主版本仍可能报告 10.0；Build >= 22000 视为 Windows 11。
	if major == 10 && windowsBuildNumber(build) >= 22000 {
		major = 11
		minor = 0
	}

	if major == 0 && build == "" {
		return "Windows"
	}
	if build == "" {
		return fmt.Sprintf("Windows %d.%d", major, minor)
	}
	return fmt.Sprintf("Windows %d.%d.%s", major, minor, build)
}

func windowsBuildNumber(build string) int {
	var n int
	fmt.Sscanf(build, "%d", &n)
	return n
}
