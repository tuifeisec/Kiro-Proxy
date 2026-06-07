package proxy

import (
	"crypto/sha1"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

type caStatusResp struct {
	CertExists bool   `json:"cert_exists"`
	Installed  bool   `json:"installed"`
	Subject    string `json:"subject,omitempty"`
	NotAfter   string `json:"not_after,omitempty"`
	Error      string `json:"error,omitempty"`
}

type installResp struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// handleCAStatus 返回 CA 证书当前状态
func (s *Server) handleCAStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	resp := caStatusResp{}
	data, err := os.ReadFile("certs/ca.crt")
	if err != nil {
		json.NewEncoder(w).Encode(resp)
		return
	}
	resp.CertExists = true

	block, _ := pem.Decode(data)
	if block == nil {
		resp.Error = "PEM 解析失败"
		json.NewEncoder(w).Encode(resp)
		return
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		resp.Error = err.Error()
		json.NewEncoder(w).Encode(resp)
		return
	}
	resp.Subject = cert.Subject.CommonName
	resp.NotAfter = cert.NotAfter.Format("2006-01-02")
	resp.Installed = isCATrusted(cert)

	json.NewEncoder(w).Encode(resp)
}

// handleInstallCA 安装 CA 到系统信任链
func (s *Server) handleInstallCA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	certPath, err := filepath.Abs("certs/ca.crt")
	if err != nil {
		json.NewEncoder(w).Encode(installResp{OK: false, Message: "获取证书路径失败: " + err.Error()})
		return
	}
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		json.NewEncoder(w).Encode(installResp{OK: false, Message: "证书文件不存在"})
		return
	}
	cert, err := readCACert()
	if err != nil {
		json.NewEncoder(w).Encode(installResp{OK: false, Message: "读取证书失败: " + err.Error()})
		return
	}
	if isCATrusted(cert) {
		json.NewEncoder(w).Encode(installResp{
			OK:      true,
			Message: "✓ CA 证书已安装并验证成功，请完全重启 Kiro 后生效",
		})
		return
	}

	var installErr error
	switch runtime.GOOS {
	case "windows":
		installErr = installCAWindows(certPath)
	case "darwin":
		installErr = installCADarwin(certPath)
	default:
		json.NewEncoder(w).Encode(installResp{OK: false, Message: "当前仅支持 Windows 和 macOS"})
		return
	}

	// 等一下再检查，让系统证书库刷新
	for i := 0; i < 5; i++ {
		time.Sleep(time.Duration(i+1) * 400 * time.Millisecond)
		if isCATrusted(cert) {
			json.NewEncoder(w).Encode(installResp{
				OK:      true,
				Message: "✓ CA 证书已安装并验证成功，请完全重启 Kiro 后生效",
			})
			return
		}
	}

	if installErr == nil {
		json.NewEncoder(w).Encode(installResp{
			OK:      false,
			Message: "安装命令已执行，但未在 Windows 受信任根证书库中检测到该证书。请确认已允许 UAC 管理员授权，或手动安装 CA 证书。",
		})
	} else {
		json.NewEncoder(w).Encode(installResp{
			OK:      false,
			Message: installFailureMessage(installErr),
		})
	}
}

func readCACert() (*x509.Certificate, error) {
	data, err := os.ReadFile("certs/ca.crt")
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("PEM 解析失败")
	}
	return x509.ParseCertificate(block.Bytes)
}

func installFailureMessage(err error) string {
	switch runtime.GOOS {
	case "windows":
		return "安装失败（可能是用户取消了 UAC）: " + err.Error()
	case "darwin":
		return "安装失败（可能是用户取消了管理员授权）: " + err.Error()
	default:
		return "安装失败: " + err.Error()
	}
}

func installCAWindows(certPath string) error {
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
	tmpScript := filepath.Join(os.TempDir(), "kiro_install_ca.ps1")
	if err := os.WriteFile(tmpScript, []byte(psScript), 0600); err != nil {
		return fmt.Errorf("写临时脚本失败: %w", err)
	}
	defer os.Remove(tmpScript)

	// 用 Start-Process -Verb RunAs -Wait 触发 UAC 提权执行脚本
	// -WindowStyle Hidden 避免弹出黑色 PowerShell 窗口
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf(
			`Start-Process powershell -Verb RunAs -Wait -WindowStyle Hidden -ArgumentList '-NoProfile -NonInteractive -ExecutionPolicy Bypass -File "%s"'`,
			tmpScript,
		),
	)
	cmd.SysProcAttr = sysProcAttr() // 隐藏 Go 启动的 PowerShell 窗口

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}

	// 设置 NODE_EXTRA_CA_CERTS 用户环境变量（Kiro/Electron 需要）
	setEnvCmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf(`[System.Environment]::SetEnvironmentVariable('NODE_EXTRA_CA_CERTS', '%s', 'User')`, certPath),
	)
	setEnvCmd.SysProcAttr = sysProcAttr()
	setEnvCmd.Run() // 忽略错误，不影响主流程
	return nil
}

func installCADarwin(certPath string) error {
	installCmd := fmt.Sprintf(
		"security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %s",
		shellQuote(certPath),
	)
	cmd := exec.Command("osascript", "-e", fmt.Sprintf("do shell script %s with administrator privileges", strconv.Quote(installCmd)))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}

	// Kiro/Electron 进程可能还会读取 Node 的额外 CA 环境变量；对当前登录会话设置即可。
	exec.Command("launchctl", "setenv", "NODE_EXTRA_CA_CERTS", certPath).Run()
	return nil
}

// isCATrusted 检查证书是否已在系统根证书池中
func isCATrusted(cert *x509.Certificate) bool {
	if runtime.GOOS == "windows" {
		return isCATrustedWindows(cert)
	}
	if runtime.GOOS == "darwin" {
		return isCATrustedDarwin(cert)
	}
	// 非 Windows 平台使用 Go 标准验证
	roots, err := x509.SystemCertPool()
	if err != nil {
		return false
	}
	_, err = cert.Verify(x509.VerifyOptions{Roots: roots})
	return err == nil
}

// isCATrustedWindows 直接查询 Windows 证书存储，避免 Go 的 CryptoAPI 缓存问题。
// Go 的 x509.SystemCertPool() 在 Windows 上受进程级 CryptoAPI 缓存影响，
// 安装证书后当前进程可能一直拿到旧的缓存结果，导致永远检测不到新装的证书。
func isCATrustedWindows(cert *x509.Certificate) bool {
	// Windows 证书存储用 SHA1 指纹作为标准索引
	thumbprint := certThumbprint(cert)

	if isCATrustedWindowsPowerShell(thumbprint) {
		return true
	}

	// PowerShell 未查到或失败时回退到 certutil。
	return isCATrustedCertutil(thumbprint)
}

func certThumbprint(cert *x509.Certificate) string {
	h := sha1.Sum(cert.Raw)
	return normalizeThumbprint(fmt.Sprintf("%X", h[:]))
}

func isCATrustedWindowsPowerShell(thumbprint string) bool {
	psScript := fmt.Sprintf(`
$target = %s
$stores = @(
  @('Root', 'LocalMachine'),
  @('Root', 'CurrentUser'),
  @('AuthRoot', 'LocalMachine'),
  @('AuthRoot', 'CurrentUser')
)

foreach ($entry in $stores) {
  $store = New-Object System.Security.Cryptography.X509Certificates.X509Store($entry[0], $entry[1])
  try {
    $store.Open([System.Security.Cryptography.X509Certificates.OpenFlags]::ReadOnly)
    foreach ($cert in $store.Certificates) {
      $certThumbprint = ($cert.Thumbprint -replace '[^0-9A-Fa-f]', '').ToUpperInvariant()
      if ($certThumbprint -eq $target) {
        Write-Output 'FOUND'
        exit 0
      }
    }
  } finally {
    $store.Close()
  }
}
exit 1
`, powerShellString(thumbprint))

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
	cmd.SysProcAttr = sysProcAttr()
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "FOUND"
}

// isCATrustedCertutil 使用 certutil 作为后备方案查询证书
func isCATrustedCertutil(thumbprint string) bool {
	for _, args := range [][]string{
		{"-store", "Root"},
		{"-user", "-store", "Root"},
		{"-store", "AuthRoot"},
		{"-user", "-store", "AuthRoot"},
	} {
		cmd := exec.Command("certutil", args...)
		cmd.SysProcAttr = sysProcAttr()
		out, err := cmd.Output()
		if err == nil && strings.Contains(normalizeThumbprint(string(out)), thumbprint) {
			return true
		}
	}
	return false
}

func normalizeThumbprint(s string) string {
	s = strings.ToUpper(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isCATrustedDarwin(cert *x509.Certificate) bool {
	h := sha1.Sum(cert.Raw)
	thumbprint := strings.ToUpper(fmt.Sprintf("%X", h[:]))

	out, err := exec.Command("security", "find-certificate", "-a", "-Z", "-c", cert.Subject.CommonName, "/Library/Keychains/System.keychain").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToUpper(string(out)), thumbprint)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func powerShellString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
