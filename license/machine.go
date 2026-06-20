package license

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"runtime"
	"sort"
	"strings"
)

func MachineCode() (string, error) {
	parts := platformMachineParts()
	if len(parts) == 0 {
		macs, err := stableMACs()
		if err != nil {
			return "", err
		}
		parts = append(parts, macs...)
	}
	if len(parts) == 0 {
		parts = append(parts, runtime.GOOS, runtime.GOARCH)
	}

	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	encoded := strings.ToUpper(hex.EncodeToString(sum[:]))
	return encoded[:8] + "-" + encoded[8:16] + "-" + encoded[16:24] + "-" + encoded[24:32], nil
}

func platformMachineParts() []string {
	switch runtime.GOOS {
	case "darwin":
		return darwinMachineParts()
	case "windows":
		return windowsMachineParts()
	case "linux":
		return linuxMachineParts()
	default:
		return nil
	}
}

func darwinMachineParts() []string {
	values := darwinPlatformValues()
	return cleanParts([]string{
		"os:darwin",
		"platform_uuid:" + values["IOPlatformUUID"],
		"platform_serial:" + values["IOPlatformSerialNumber"],
	})
}

func darwinPlatformValues() map[string]string {
	out, err := hiddenCommand("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return nil
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.Trim(strings.TrimSpace(parts[0]), "\"")
		value := strings.Trim(strings.TrimSpace(parts[1]), "\"")
		if key == "IOPlatformUUID" || key == "IOPlatformSerialNumber" {
			values[key] = value
		}
	}
	return values
}

func windowsMachineParts() []string {
	parts := cleanParts([]string{
		"os:windows",
		"csproduct_uuid:" + powershellLine("(Get-CimInstance Win32_ComputerSystemProduct).UUID"),
		"baseboard_serial:" + powershellLine("(Get-CimInstance Win32_BaseBoard).SerialNumber"),
		"bios_serial:" + powershellLine("(Get-CimInstance Win32_BIOS).SerialNumber"),
	})
	if len(parts) > 1 {
		return parts
	}
	return cleanParts([]string{"os:windows", "machine_guid:" + windowsMachineGuid()})
}

func powershellLine(command string) string {
	out, err := hiddenCommand("powershell", "-NoProfile", "-NonInteractive", "-Command", command).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if value := strings.TrimSpace(line); value != "" {
			return value
		}
	}
	return ""
}

func windowsMachineGuid() string {
	out, err := hiddenCommand("reg", "query", `HKLM\SOFTWARE\Microsoft\Cryptography`, "/v", "MachineGuid").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	for i, field := range fields {
		if strings.EqualFold(field, "MachineGuid") && i+2 < len(fields) {
			return strings.TrimSpace(fields[i+2])
		}
	}
	return ""
}

func linuxMachineParts() []string {
	parts := cleanParts([]string{
		"os:linux",
		"product_uuid:" + readFirstLine("/sys/class/dmi/id/product_uuid"),
		"product_serial:" + readFirstLine("/sys/class/dmi/id/product_serial"),
		"board_serial:" + readFirstLine("/sys/class/dmi/id/board_serial"),
		"chassis_serial:" + readFirstLine("/sys/class/dmi/id/chassis_serial"),
	})
	if len(parts) > 1 {
		return parts
	}
	return cleanParts([]string{"os:linux", "machine_id:" + linuxMachineID()})
}

func linuxMachineID() string {
	for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if value := readFirstLine(path); value != "" {
			return value
		}
	}
	return ""
}

func readFirstLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if value := strings.TrimSpace(line); value != "" {
			return value
		}
	}
	return ""
}

func cleanParts(parts []string) []string {
	cleaned := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		key, value, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		value = normalizeMachineValue(value)
		if value == "" || unstablePlaceholder(value) {
			continue
		}
		item := key + ":" + value
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		cleaned = append(cleaned, item)
	}
	return cleaned
}

func normalizeMachineValue(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.Trim(value, " .\t\r\n")
	return value
}

func unstablePlaceholder(value string) bool {
	if value == "" || value == "none" || value == "null" || value == "unknown" || value == "default string" || value == "to be filled by o.e.m" || value == "to be filled by oem" || value == "system serial number" {
		return true
	}
	if strings.Trim(value, "0-") == "" {
		return true
	}
	return false
}

func stableMACs() ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	macs := make([]string, 0, len(interfaces))
	for _, item := range interfaces {
		if item.Flags&net.FlagLoopback != 0 || len(item.HardwareAddr) == 0 {
			continue
		}
		name := strings.ToLower(item.Name)
		if strings.Contains(name, "awdl") || strings.Contains(name, "llw") || strings.Contains(name, "utun") || strings.Contains(name, "bridge") {
			continue
		}
		macs = append(macs, "mac:"+strings.ToLower(item.HardwareAddr.String()))
	}
	sort.Strings(macs)
	return macs, nil
}
