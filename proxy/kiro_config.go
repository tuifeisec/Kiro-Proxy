// kiro_config.go
// 一键配置/还原 Kiro 的代理设置，并重启 Kiro 进程

package proxy

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

// getKiroSettingsPath 返回 Kiro settings.json 路径
func getKiroSettingsPath() string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "Kiro", "User", "settings.json")
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "Kiro", "User", "settings.json")
	default:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".config", "Kiro", "User", "settings.json")
	}
}

type kiroProxyStatus struct {
	Configured    bool   `json:"configured"`     // 是否已配置为我们的代理
	CurrentProxy  string `json:"current_proxy"`  // 当前代理值
	TargetProxy   string `json:"target_proxy"`   // 我们的代理地址
	OriginalProxy string `json:"original_proxy"` // 原始代理值（配置前备份的）
	SettingsPath  string `json:"settings_path"`
	Error         string `json:"error,omitempty"`
}

type kiroProxyResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// handleKiroProxy GET: 查询状态 / POST: 配置代理 / DELETE: 还原代理
func (s *Server) handleKiroProxy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	targetProxy := fmt.Sprintf("http://127.0.0.1:%d", s.cfg.Proxy.Port)

	switch r.Method {
	case http.MethodGet:
		status := getKiroProxyStatus(targetProxy)
		json.NewEncoder(w).Encode(status)

	case http.MethodPost:
		result := configureKiroProxy(targetProxy, s.logger)
		json.NewEncoder(w).Encode(result)

	case http.MethodDelete:
		result := restoreKiroProxy(s.logger)
		json.NewEncoder(w).Encode(result)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleKiroRestart POST: 只重启 Kiro，不修改代理配置
func (s *Server) handleKiroRestart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	restartKiro(s.logger)
	json.NewEncoder(w).Encode(kiroProxyResult{OK: true, Message: "✓ Kiro 正在重启..."})
}

func getKiroProxyStatus(targetProxy string) kiroProxyStatus {
	settingsPath := getKiroSettingsPath()
	status := kiroProxyStatus{
		TargetProxy:  targetProxy,
		SettingsPath: settingsPath,
	}

	settings, err := readKiroSettings(settingsPath)
	if err != nil {
		status.Error = "无法读取 Kiro 设置: " + err.Error()
		return status
	}

	currentProxy, _ := settings["http.proxy"].(string)
	status.CurrentProxy = currentProxy
	status.Configured = currentProxy == targetProxy

	// 读取备份的原始值
	backupPath := settingsPath + ".proxy-backup"
	if data, err := os.ReadFile(backupPath); err == nil {
		status.OriginalProxy = strings.TrimSpace(string(data))
	}

	return status
}

func configureKiroProxy(targetProxy string, logger *TrafficLogger) kiroProxyResult {
	settingsPath := getKiroSettingsPath()

	settings, err := readKiroSettings(settingsPath)
	if err != nil {
		return kiroProxyResult{OK: false, Message: "读取 Kiro 设置失败: " + err.Error()}
	}

	// 备份原始代理值
	originalProxy, _ := settings["http.proxy"].(string)
	backupPath := settingsPath + ".proxy-backup"
	os.WriteFile(backupPath, []byte(originalProxy), 0644)

	// 写入新的代理设置
	settings["http.proxy"] = targetProxy
	settings["http.proxyStrictSSL"] = false

	if err := writeKiroSettings(settingsPath, settings); err != nil {
		return kiroProxyResult{OK: false, Message: "写入设置失败: " + err.Error()}
	}

	logger.Info("kiro-config", fmt.Sprintf("代理已配置: %s → %s", originalProxy, targetProxy))

	// 重启 Kiro
	restartKiro(logger)

	return kiroProxyResult{OK: true, Message: fmt.Sprintf("✓ 代理已配置为 %s，Kiro 正在重启...", targetProxy)}
}

func restoreKiroProxy(logger *TrafficLogger) kiroProxyResult {
	settingsPath := getKiroSettingsPath()
	backupPath := settingsPath + ".proxy-backup"

	// 读取备份的原始值
	backupData, err := os.ReadFile(backupPath)
	if err != nil {
		return kiroProxyResult{OK: false, Message: "未找到原始代理备份，无法还原"}
	}
	originalProxy := strings.TrimSpace(string(backupData))

	settings, err := readKiroSettings(settingsPath)
	if err != nil {
		return kiroProxyResult{OK: false, Message: "读取设置失败: " + err.Error()}
	}

	// 还原代理设置
	if originalProxy == "" {
		delete(settings, "http.proxy")
	} else {
		settings["http.proxy"] = originalProxy
	}
	// 还原 SSL 验证（默认 true）
	delete(settings, "http.proxyStrictSSL")

	if err := writeKiroSettings(settingsPath, settings); err != nil {
		return kiroProxyResult{OK: false, Message: "写入设置失败: " + err.Error()}
	}

	// 删除备份文件
	os.Remove(backupPath)

	logger.Info("kiro-config", fmt.Sprintf("代理已还原: → %s", originalProxy))

	// 重启 Kiro
	restartKiro(logger)

	return kiroProxyResult{OK: true, Message: fmt.Sprintf("✓ 代理已还原为「%s」，Kiro 正在重启...", originalProxy)}
}

func readKiroSettings(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// 文件不存在时返回空 map
		if os.IsNotExist(err) {
			return make(map[string]interface{}), nil
		}
		return nil, err
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func writeKiroSettings(path string, settings map[string]interface{}) error {
	// 确保目录存在
	os.MkdirAll(filepath.Dir(path), 0755)
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func restartKiro(logger *TrafficLogger) {
	logger.Info("kiro-config", "正在重启 Kiro...")

	switch runtime.GOOS {
	case "windows":
		restartKiroWindows(logger)
	case "darwin":
		restartKiroDarwin(logger)
	default:
		logger.Info("kiro-config", "当前系统暂不支持自动重启 Kiro: "+runtime.GOOS)
	}
}

func restartKiroWindows(logger *TrafficLogger) {
	// /T 结束完整进程树，避免只杀掉主进程后残留最新启动的子进程。
	if err := hiddenCmd("taskkill", "/T", "/IM", "Kiro.exe").Run(); err != nil {
		logger.Info("kiro-config", "正常关闭 Kiro 进程树失败，尝试强制结束: "+err.Error())
		_ = hiddenCmd("taskkill", "/F", "/T", "/IM", "Kiro.exe").Run()
	}

	time.Sleep(2 * time.Second)

	kiroPaths := []string{
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Kiro", "Kiro.exe"),
		filepath.Join(os.Getenv("PROGRAMFILES"), "Kiro", "Kiro.exe"),
	}
	for _, p := range kiroPaths {
		if _, err := os.Stat(p); err == nil {
			if err := hiddenCmd(p).Start(); err != nil {
				logger.Info("kiro-config", "启动 Kiro 失败: "+err.Error())
				continue
			}
			logger.Info("kiro-config", "Kiro 已启动: "+p)
			return
		}
	}

	cmd := hiddenCmd(
		"powershell",
		"-NoProfile",
		"-NonInteractive",
		"-WindowStyle",
		"Hidden",
		"-Command",
		"Start-Process -FilePath Kiro -WindowStyle Hidden",
	)
	if err := cmd.Start(); err != nil {
		logger.Info("kiro-config", "通过 PATH 启动 Kiro 失败: "+err.Error())
		return
	}
	logger.Info("kiro-config", "尝试通过 PATH 启动 Kiro")
}

func restartKiroDarwin(logger *TrafficLogger) {
	if err := hiddenCmd("osascript", "-e", `tell application "Kiro" to quit`).Run(); err != nil {
		logger.Info("kiro-config", "正常退出 Kiro 失败，尝试结束所有 Kiro 进程: "+err.Error())
	}
	if err := hiddenCmd("pkill", "-x", "Kiro").Run(); err != nil {
		logger.Info("kiro-config", "结束 Kiro 主进程失败或进程不存在: "+err.Error())
	}
	if err := hiddenCmd("pkill", "-f", "/Kiro.app/").Run(); err != nil {
		logger.Info("kiro-config", "结束 Kiro 进程树失败或进程不存在: "+err.Error())
	}

	time.Sleep(2 * time.Second)

	if err := hiddenCmd("open", "-a", "Kiro").Start(); err != nil {
		logger.Info("kiro-config", "启动 Kiro 失败: "+err.Error())
		return
	}
	logger.Info("kiro-config", "Kiro 已启动")
}
