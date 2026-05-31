package proxy

import (
	"crypto/sha1"
	"crypto/x509"
	json "github.com/goccy/go-json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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

	if runtime.GOOS != "windows" {
		json.NewEncoder(w).Encode(installResp{OK: false, Message: "当前仅支持 Windows"})
		return
	}

	certPath, err := filepath.Abs("certs/ca.crt")
	if err != nil {
		json.NewEncoder(w).Encode(installResp{OK: false, Message: "获取证书路径失败: " + err.Error()})
		return
	}
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		json.NewEncoder(w).Encode(installResp{OK: false, Message: "证书文件不存在"})
		return
	}

	// 方案：写一个临时 .ps1 脚本，用 Start-Process -Verb RunAs 提权执行
	// 比内联字符串拼接更可靠，避免引号转义问题
	psScript := fmt.Sprintf(`
$certPath = '%s'
$cert = New-Object System.Security.Cryptography.X509Certificates.X509Certificate2($certPath)
$store = New-Object System.Security.Cryptography.X509Certificates.X509Store('Root', 'LocalMachine')
$store.Open('ReadWrite')
$existing = $store.Certificates | Where-Object { $_.Subject -like '*Kiro Proxy*' }
foreach ($c in $existing) { $store.Remove($c) }
$store.Add($cert)
$store.Close()
Write-Output "OK"
`, certPath)

	// 写临时脚本文件
	tmpScript := filepath.Join(os.TempDir(), "kiro_install_ca.ps1")
	if err := os.WriteFile(tmpScript, []byte(psScript), 0600); err != nil {
		json.NewEncoder(w).Encode(installResp{OK: false, Message: "写临时脚本失败: " + err.Error()})
		return
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
		json.NewEncoder(w).Encode(installResp{
			OK:      false,
			Message: "安装失败（可能是用户取消了 UAC）: " + err.Error() + "\n" + string(out),
		})
		return
	}

	// 设置 NODE_EXTRA_CA_CERTS 用户环境变量（Kiro/Electron 需要）
	setEnvCmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf(`[System.Environment]::SetEnvironmentVariable('NODE_EXTRA_CA_CERTS', '%s', 'User')`, certPath),
	)
	setEnvCmd.SysProcAttr = sysProcAttr()
	setEnvCmd.Run() // 忽略错误，不影响主流程

	// 等一下再检查，让系统证书库刷新
	time.Sleep(500 * time.Millisecond)

	// 验证安装结果
	data, err := os.ReadFile("certs/ca.crt")
	if err != nil {
		json.NewEncoder(w).Encode(installResp{OK: true, Message: "✓ 安装命令已执行，请重启 Kiro 后生效"})
		return
	}
	block, _ := pem.Decode(data)
	if block == nil {
		json.NewEncoder(w).Encode(installResp{OK: true, Message: "✓ 安装命令已执行，请重启 Kiro 后生效"})
		return
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		json.NewEncoder(w).Encode(installResp{OK: true, Message: "✓ 安装命令已执行，请重启 Kiro 后生效"})
		return
	}

	if isCATrusted(cert) {
		json.NewEncoder(w).Encode(installResp{
			OK:      true,
			Message: "✓ CA 证书已安装并验证成功，请完全重启 Kiro 后生效",
		})
	} else {
		json.NewEncoder(w).Encode(installResp{
			OK:      true,
			Message: "✓ 安装命令已执行（系统证书库刷新可能需要几秒），请重启 Kiro 后生效",
		})
	}
}

// isCATrusted 检查证书是否已在系统根证书池中
func isCATrusted(cert *x509.Certificate) bool {
	if runtime.GOOS == "windows" {
		return isCATrustedWindows(cert)
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
	h := sha1.Sum(cert.Raw)
	thumbprint := fmt.Sprintf("%X", h[:])

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf(
			`Get-ChildItem -Path Cert:\LocalMachine\Root | Where-Object { $_.Thumbprint -eq '%s' } | Select-Object -First 1 | ForEach-Object { Write-Output $_.Thumbprint }`,
			thumbprint,
		),
	)
	cmd.SysProcAttr = sysProcAttr()
	out, err := cmd.Output()
	if err != nil {
		// PowerShell 失败时回退到 certutil
		return isCATrustedCertutil(thumbprint)
	}

	result := strings.TrimSpace(string(out))
	return len(result) > 0
}

// isCATrustedCertutil 使用 certutil 作为后备方案查询证书
func isCATrustedCertutil(thumbprint string) bool {
	cmd := exec.Command("certutil", "-store", "Root")
	cmd.SysProcAttr = sysProcAttr()
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	// certutil 输出中搜索指纹（不区分大小写）
	return strings.Contains(strings.ToLower(string(out)), strings.ToLower(thumbprint))
}

