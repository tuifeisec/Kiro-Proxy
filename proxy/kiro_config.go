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
	"sync"

	json "github.com/goccy/go-json"
)

// 将配置、还原与重启的预检到启动过程串行化，避免并发请求交错改写 Kiro 状态。
var kiroLifecycleMu sync.Mutex

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

	targetProxy := fmt.Sprintf("http://%s:%d", s.proxyDisplayHost(), s.cfg.Proxy.Port)

	switch r.Method {
	case http.MethodGet:
		status := getKiroProxyStatus(targetProxy)
		json.NewEncoder(w).Encode(status)

	case http.MethodPost:
		result := configureKiroProxyAt(targetProxy, s.logger, getKiroSettingsPath(), s.prepareKiroStageLaunch, launchPreparedKiro)
		json.NewEncoder(w).Encode(result)

	case http.MethodDelete:
		result := restoreKiroProxyAt(s.logger, getKiroSettingsPath(), s.prepareKiroStageLaunch, launchPreparedKiro)
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

	json.NewEncoder(w).Encode(restartKiroAt(s.logger, fmt.Sprintf("http://%s:%d", s.proxyBindHost(), s.cfg.Proxy.Port), getKiroSettingsPath(), s.prepareKiroStageLaunch, launchPreparedKiro))
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
	return configureKiroProxyAt(targetProxy, logger, getKiroSettingsPath(), prepareKiroLaunchPlan, launchPreparedKiro)
}

func configureKiroProxyAt(targetProxy string, logger *TrafficLogger, settingsPath string, prepare kiroLaunchPreparer, launch kiroLaunchRunner) kiroProxyResult {
	kiroLifecycleMu.Lock()
	defer kiroLifecycleMu.Unlock()

	settings, err := readKiroSettings(settingsPath)
	if err != nil {
		return kiroProxyResult{OK: false, Message: "读取 Kiro 设置失败: " + err.Error()}
	}
	plan, err := prepare(true)
	if err != nil {
		return kiroProxyResult{OK: false, Message: "启动 Kiro 预检失败: " + err.Error()}
	}

	// 备份原始代理值
	originalProxy, _ := settings["http.proxy"].(string)
	backupPath := settingsPath + ".proxy-backup"
	if err := os.WriteFile(backupPath, []byte(originalProxy), 0644); err != nil {
		return kiroProxyResult{OK: false, Message: "备份原始代理失败: " + err.Error()}
	}

	// 写入新的代理设置
	settings["http.proxy"] = targetProxy
	settings["http.proxyStrictSSL"] = false

	if err := writeKiroSettings(settingsPath, settings); err != nil {
		return kiroProxyResult{OK: false, Message: "写入设置失败: " + err.Error()}
	}

	logKiroConfig(logger, fmt.Sprintf("代理已配置: %s → %s", originalProxy, targetProxy))

	// 重启 Kiro
	if err := launch(plan, logger); err != nil {
		return kiroProxyResult{OK: false, Message: fmt.Sprintf("代理已配置为 %s，但 Kiro 重启失败: %v", targetProxy, err)}
	}

	return kiroProxyResult{OK: true, Message: fmt.Sprintf("✓ 代理已配置为 %s，Kiro 正在重启...", targetProxy)}
}

func restoreKiroProxy(logger *TrafficLogger) kiroProxyResult {
	return restoreKiroProxyAt(logger, getKiroSettingsPath(), prepareKiroLaunchPlan, launchPreparedKiro)
}

func restoreKiroProxyAt(logger *TrafficLogger, settingsPath string, prepare kiroLaunchPreparer, launch kiroLaunchRunner) kiroProxyResult {
	kiroLifecycleMu.Lock()
	defer kiroLifecycleMu.Unlock()

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
	plan, err := prepare(false)
	if err != nil {
		return kiroProxyResult{OK: false, Message: "还原前启动 Kiro 预检失败: " + err.Error()}
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

	logKiroConfig(logger, fmt.Sprintf("代理已还原: → %s", originalProxy))

	// 重启 Kiro
	if err := launch(plan, logger); err != nil {
		return kiroProxyResult{OK: false, Message: fmt.Sprintf("代理已还原为「%s」，但 Kiro 重启失败: %v", originalProxy, err)}
	}
	if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
		return kiroProxyResult{OK: false, Message: "代理已还原且 Kiro 已启动，但删除原始代理备份失败: " + err.Error()}
	}

	return kiroProxyResult{OK: true, Message: fmt.Sprintf("✓ 代理已还原为「%s」，Kiro 正在重启...", originalProxy)}
}

func restartKiro(logger *TrafficLogger, targetProxy string) kiroProxyResult {
	return restartKiroAt(logger, targetProxy, getKiroSettingsPath(), prepareKiroLaunchPlan, launchPreparedKiro)
}

func restartKiroAt(logger *TrafficLogger, targetProxy, settingsPath string, prepare kiroLaunchPreparer, launch kiroLaunchRunner) kiroProxyResult {
	kiroLifecycleMu.Lock()
	defer kiroLifecycleMu.Unlock()

	settings, err := readKiroSettings(settingsPath)
	if err != nil {
		return kiroProxyResult{OK: false, Message: "读取 Kiro 设置失败: " + err.Error()}
	}
	currentProxy, _ := settings["http.proxy"].(string)
	injectCA, err := kiroCAInjectionDecision(currentProxy, targetProxy)
	if err != nil {
		return kiroProxyResult{OK: false, Message: "识别 Kiro 代理归属失败: " + err.Error()}
	}
	plan, err := prepare(injectCA)
	if err != nil {
		return kiroProxyResult{OK: false, Message: "启动 Kiro 预检失败: " + err.Error()}
	}
	if err := launch(plan, logger); err != nil {
		return kiroProxyResult{OK: false, Message: "Kiro 重启失败: " + err.Error()}
	}
	return kiroProxyResult{OK: true, Message: "✓ Kiro 启动命令已提交"}
}

func logKiroConfig(logger *TrafficLogger, message string) {
	if logger != nil {
		logger.Info("kiro-config", message)
	}
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
