package proxy

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

func installCAWindows(certPath string) error {
	return installCAWindowsWithRunner(certPath, runCAInstallCommand)
}

func installCAWindowsWithRunner(certPath string, run caInstallCommandRunner) error {
	// 方案：写一个临时 .ps1 脚本，用 Start-Process -Verb RunAs 提权执行
	// 比内联字符串拼接更可靠，避免引号转义问题
	psScript := fmt.Sprintf(`
$certPath = %s
$cert = New-Object System.Security.Cryptography.X509Certificates.X509Certificate2($certPath)
$store = New-Object System.Security.Cryptography.X509Certificates.X509Store('Root', 'LocalMachine')
$store.Open('ReadWrite')
$existing = $store.Certificates | Where-Object { $_.Subject -like '*Kiro Proxy*' }
foreach ($c in $existing) { $store.Remove($c) }
$store.Add($cert)
$store.Close()
Write-Output "OK"
`, powerShellString(certPath))

	// 写临时脚本文件
	scriptFile, err := os.CreateTemp(os.TempDir(), "kiro_install_ca-*.ps1")
	if err != nil {
		return fmt.Errorf("写临时脚本失败: %w", err)
	}
	tmpScript := scriptFile.Name()
	defer os.Remove(tmpScript)
	if _, err := scriptFile.Write([]byte(psScript)); err != nil {
		scriptFile.Close()
		return fmt.Errorf("写临时脚本失败: %w", err)
	}
	if err := scriptFile.Close(); err != nil {
		return fmt.Errorf("写临时脚本失败: %w", err)
	}

	// 用 Start-Process -Verb RunAs -Wait 触发 UAC 提权执行脚本
	// -WindowStyle Hidden 避免弹出黑色 PowerShell 窗口
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf(
			`Start-Process powershell -Verb RunAs -Wait -WindowStyle Hidden -ArgumentList '-NoProfile -NonInteractive -ExecutionPolicy Bypass -File "%s"'`,
			tmpScript,
		),
	)
	cmd.SysProcAttr = sysProcAttr() // 隐藏 Go 启动的 PowerShell 窗口

	out, err := run(cmd)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}

	return nil
}

func installCADarwin(certPath string) error {
	return installCADarwinWithRunner(certPath, runCAInstallCommand)
}

func installCADarwinWithRunner(certPath string, run caInstallCommandRunner) error {
	installCmd := fmt.Sprintf(
		"security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %s",
		shellQuote(certPath),
	)
	cmd := exec.Command("osascript", "-e", fmt.Sprintf("do shell script %s with administrator privileges", strconv.Quote(installCmd)))
	if out, err := run(cmd); err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}

	return nil
}

type caInstallCommandRunner func(*exec.Cmd) ([]byte, error)

func runCAInstallCommand(cmd *exec.Cmd) ([]byte, error) {
	return cmd.CombinedOutput()
}
