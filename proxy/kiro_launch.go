package proxy

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const kiroExtraCACertEnv = "NODE_EXTRA_CA_CERTS"

type kiroLaunchPlan struct {
	platform     string
	executable   string
	args         []string
	env          []string
	stageInstall *kiroStageInstallPlan
	proxyMode    string
}

type kiroLaunchPreparer func(injectCA bool) (kiroLaunchPlan, error)
type kiroLaunchRunner func(kiroLaunchPlan, *TrafficLogger) error

func replaceEnvironmentValue(env []string, key, value string, caseInsensitive bool) []string {
	updated := make([]string, 0, len(env)+1)
	for _, entry := range env {
		entryKey, _, hasValue := strings.Cut(entry, "=")
		if hasValue && environmentKeyEqual(entryKey, key, caseInsensitive) {
			continue
		}
		updated = append(updated, entry)
	}
	return append(updated, key+"="+value)
}

func environmentKeyEqual(left, right string, caseInsensitive bool) bool {
	if caseInsensitive {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func validateKiroCACertificate(certPath string) (string, error) {
	absPath, err := filepath.Abs(certPath)
	if err != nil {
		return "", fmt.Errorf("获取 CA 证书绝对路径失败: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("读取 CA 证书文件状态失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("CA 证书路径不是普通文件: %s", absPath)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("读取 CA 证书失败: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("CA 证书 PEM 解析失败: %s", absPath)
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return "", fmt.Errorf("CA 证书 x509 解析失败: %w", err)
	}
	return absPath, nil
}

func resolveKiroWindowsExecutable(localAppData, programFiles string, lookPath func(string) (string, error), stat func(string) (os.FileInfo, error)) (string, error) {
	var candidates []string
	if localAppData != "" {
		candidates = append(candidates, filepath.Join(localAppData, "Programs", "Kiro", "Kiro.exe"))
	}
	if programFiles != "" {
		candidates = append(candidates, filepath.Join(programFiles, "Kiro", "Kiro.exe"))
	}
	for _, candidate := range candidates {
		if info, err := stat(candidate); err == nil && info.Mode().IsRegular() {
			return filepath.Abs(candidate)
		}
	}

	cliPath, cliErr := lookPath("kiro.cmd")
	if cliErr != nil {
		return "", fmt.Errorf("未找到 Kiro.exe；查找 kiro.cmd 失败: %w", cliErr)
	}
	exePath := filepath.Clean(filepath.Join(filepath.Dir(cliPath), "..", "Kiro.exe"))
	info, err := stat(exePath)
	if err != nil {
		return "", fmt.Errorf("由 kiro.cmd 定位 Kiro.exe 失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("Kiro.exe 路径不是普通文件: %s", exePath)
	}
	absPath, err := filepath.Abs(exePath)
	if err != nil {
		return "", fmt.Errorf("获取 Kiro.exe 绝对路径失败: %w", err)
	}
	return absPath, nil
}

func prepareKiroLaunchPlan(injectCA bool) (kiroLaunchPlan, error) {
	switch runtime.GOOS {
	case "windows":
		executable, err := resolveKiroWindowsExecutable(
			os.Getenv("LOCALAPPDATA"),
			os.Getenv("PROGRAMFILES"),
			exec.LookPath,
			os.Stat,
		)
		if err != nil {
			return kiroLaunchPlan{}, err
		}
		return buildWindowsKiroLaunchPlan(executable, filepath.Join("certs", "ca.crt"), injectCA, os.Environ())
	case "darwin":
		appPath, openPath, err := locateDarwinKiro()
		if err != nil {
			return kiroLaunchPlan{}, err
		}
		return buildDarwinKiroLaunchPlan(appPath, openPath, filepath.Join("certs", "ca.crt"), injectCA)
	default:
		return kiroLaunchPlan{}, fmt.Errorf("当前系统暂不支持自动重启 Kiro: %s", runtime.GOOS)
	}
}

func buildWindowsKiroLaunchPlan(executable, certPath string, injectCA bool, inheritedEnv []string) (kiroLaunchPlan, error) {
	info, err := os.Stat(executable)
	if err != nil {
		return kiroLaunchPlan{}, fmt.Errorf("读取 Kiro.exe 状态失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return kiroLaunchPlan{}, fmt.Errorf("Kiro.exe 路径不是普通文件: %s", executable)
	}
	absPath, err := filepath.Abs(executable)
	if err != nil {
		return kiroLaunchPlan{}, fmt.Errorf("获取 Kiro.exe 绝对路径失败: %w", err)
	}
	plan := kiroLaunchPlan{platform: "windows", executable: absPath}
	if injectCA {
		absCertPath, err := validateKiroCACertificate(certPath)
		if err != nil {
			return kiroLaunchPlan{}, err
		}
		plan.env = replaceEnvironmentValue(inheritedEnv, kiroExtraCACertEnv, absCertPath, true)
	}
	return plan, nil
}

func buildDarwinKiroLaunchPlan(appPath, openPath, certPath string, injectCA bool) (kiroLaunchPlan, error) {
	appInfo, err := os.Stat(appPath)
	if err != nil {
		return kiroLaunchPlan{}, fmt.Errorf("读取 Kiro.app 状态失败: %w", err)
	}
	if !appInfo.IsDir() {
		return kiroLaunchPlan{}, fmt.Errorf("Kiro.app 路径不是应用目录: %s", appPath)
	}
	openInfo, err := os.Stat(openPath)
	if err != nil {
		return kiroLaunchPlan{}, fmt.Errorf("读取 open 命令状态失败: %w", err)
	}
	if !openInfo.Mode().IsRegular() {
		return kiroLaunchPlan{}, fmt.Errorf("open 命令路径不是普通文件: %s", openPath)
	}
	absOpenPath, err := filepath.Abs(openPath)
	if err != nil {
		return kiroLaunchPlan{}, fmt.Errorf("获取 open 命令绝对路径失败: %w", err)
	}
	plan := kiroLaunchPlan{platform: "darwin", executable: absOpenPath, args: darwinKiroOpenArgs("", false)}
	if injectCA {
		absCertPath, err := validateKiroCACertificate(certPath)
		if err != nil {
			return kiroLaunchPlan{}, err
		}
		plan.args = darwinKiroOpenArgs(absCertPath, true)
	}
	return plan, nil
}

func darwinKiroOpenArgs(certPath string, injectCA bool) []string {
	args := []string{"-n"}
	if injectCA {
		args = append(args, "--env", kiroExtraCACertEnv+"="+certPath)
	}
	return append(args, "-a", "Kiro")
}

func locateDarwinKiro() (appPath, openPath string, err error) {
	osascriptPath, err := exec.LookPath("osascript")
	if err != nil {
		return "", "", fmt.Errorf("定位 Kiro.app 失败，查找 osascript 命令失败: %w", err)
	}
	output, err := exec.Command(osascriptPath, "-e", `POSIX path of (path to application "Kiro")`).CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("定位 Kiro.app 失败: %w: %s", err, strings.TrimSpace(string(output)))
	}
	appPath = strings.TrimSpace(string(output))
	if appPath == "" {
		return "", "", fmt.Errorf("定位 Kiro.app 失败: LaunchServices 未返回应用路径")
	}
	openPath, err = exec.LookPath("open")
	if err != nil {
		return "", "", fmt.Errorf("查找 open 命令失败: %w", err)
	}
	return appPath, openPath, nil
}
