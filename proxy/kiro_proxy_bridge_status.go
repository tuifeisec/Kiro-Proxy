package proxy

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

type kiroBridgeStatus struct {
	Required              bool                `json:"required"`
	Reason                string              `json:"reason"`
	Installation          string              `json:"installation"`
	Message               string              `json:"message"`
	AdapterSHA256         string              `json:"adapter_sha256"`
	ExpectedAdapterSHA256 string              `json:"expected_adapter_sha256"`
	BundleSHA256          string              `json:"bundle_sha256"`
	Verified              bool                `json:"verified"`
	RestartRequired       bool                `json:"restart_required"`
	RestartReason         string              `json:"restart_reason"`
	LastLaunch            *kiroLaunchSnapshot `json:"last_launch"`
}

// 仅记录本代理提交过的启动计划，不用于宣称当前进程或扩展已经加载。
type kiroLaunchSnapshot struct {
	SubmittedAt   string `json:"submitted_at"`
	ProxyMode     string `json:"proxy_mode"`
	BridgeEnabled bool   `json:"bridge_enabled"`
	BundleSHA256  string `json:"bundle_sha256"`
	AdapterSHA256 string `json:"adapter_sha256"`
}

type kiroLaunchTracker struct {
	mu       sync.RWMutex
	snapshot *kiroLaunchSnapshot
}

func (t *kiroLaunchTracker) load() *kiroLaunchSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.snapshot == nil {
		return nil
	}
	copy := *t.snapshot
	return &copy
}

func (t *kiroLaunchTracker) clear() {
	t.mu.Lock()
	t.snapshot = nil
	t.mu.Unlock()
}

func (t *kiroLaunchTracker) record(plan kiroLaunchPlan, submitted time.Time) {
	snapshot := &kiroLaunchSnapshot{
		SubmittedAt: submitted.UTC().Format(time.RFC3339Nano),
		ProxyMode:   plan.proxyMode, BridgeEnabled: kiroStageBridgeEnabledInEnv(plan.env),
	}
	if plan.stageInstall != nil {
		snapshot.BundleSHA256 = plan.stageInstall.candidateHash
		snapshot.AdapterSHA256 = plan.stageInstall.adapterHash
	}
	t.mu.Lock()
	t.snapshot = snapshot
	t.mu.Unlock()
}

func (s *Server) launchKiroWithStatus(plan kiroLaunchPlan, logger *TrafficLogger) error {
	return s.launchKiroWithStatusUsing(plan, logger, launchPreparedKiro)
}

func (s *Server) launchKiroWithStatusUsing(plan kiroLaunchPlan, logger *TrafficLogger, launch kiroLaunchRunner) error {
	s.kiroLaunchStatus.clear()
	if err := launch(plan, logger); err != nil {
		return err
	}
	s.kiroLaunchStatus.record(plan, time.Now())
	return nil
}

func (s *Server) kiroProxyStatus(target string) kiroProxyStatus {
	status := getKiroProxyStatus(target)
	mode := GetProxyMode()
	status.ProxyMode = mode
	bridge := inspectInstalledKiroBridge()
	status.StageBridge = &bridge
	if status.Error != "" {
		bridge.RestartReason = "无法读取 Kiro 代理归属：" + status.Error
		status.StageBridge = &bridge
		return status
	}
	// 启动和状态查询使用相同的归属判定，兼容 localhost 和局域网别名。
	boundTarget := fmt.Sprintf("http://%s:%d", s.proxyBindHost(), s.cfg.Proxy.Port)
	owned, err := kiroCAInjectionDecision(status.CurrentProxy, boundTarget)
	if err != nil {
		bridge.Installation = "error"
		bridge.Message = "识别 Kiro 代理归属失败：" + err.Error()
		bridge.RestartReason = bridge.Message
		status.StageBridge = &bridge
		return status
	}
	status.Configured = owned
	routingEnabled := s.router != nil && s.router.StageRoutingEnabled()
	applyKiroBridgePolicy(&bridge, owned, routingEnabled, mode, s.kiroLaunchStatus.load())
	status.StageBridge = &bridge
	return status
}

func inspectInstalledKiroBridge() kiroBridgeStatus {
	if runtime.GOOS != "windows" {
		return kiroBridgeStatus{Installation: "unsupported", Message: "当前平台无法自动诊断桥接安装；运行加载状态未核实。"}
	}
	executable, err := resolveKiroWindowsExecutable(os.Getenv("LOCALAPPDATA"), os.Getenv("PROGRAMFILES"), exec.LookPath, os.Stat)
	if err != nil {
		return kiroBridgeStatus{Installation: "error", Message: "定位 Kiro 桥接失败：" + err.Error()}
	}
	current, err := readKiroStageBundle(kiroStageBundlePath(executable))
	if err != nil {
		return kiroBridgeStatus{Installation: "error", Message: err.Error()}
	}
	var manifests []kiroStageManifest
	for _, name := range []string{"kiro_stage_bridge_manifest.json", "kiro_stage_bridge_130_manifest.json"} {
		manifest, err := readKiroStageManifest(name)
		if err != nil {
			return kiroBridgeStatus{Installation: "error", Message: err.Error()}
		}
		manifests = append(manifests, manifest)
	}
	// 此处仅查询可信凭据。绝不编译 JS、安装桥接或写入凭据。
	return classifyKiroBridge(current, kiroStageBundlePath(executable), manifests, func(target, installed, adapter string) (bool, error) {
		cache, err := defaultKiroStageVerificationCache()
		if err != nil {
			return false, err
		}
		_, hit, err := cache.Lookup(target, installed, adapter)
		return hit, err
	})
}

type kiroBridgeReceiptLookup func(target, installed, adapter string) (bool, error)

func classifyKiroBridge(current []byte, target string, manifests []kiroStageManifest, lookup kiroBridgeReceiptLookup) kiroBridgeStatus {
	status := kiroBridgeStatus{
		Installation: "unknown", Message: "桥接文件身份未识别，重启时会执行完整预检；运行加载状态未核实。",
		BundleSHA256: kiroStageSHA256(current), ExpectedAdapterSHA256: kiroStageStructuralAdapterHash(),
	}
	for _, manifest := range manifests {
		switch status.BundleSHA256 {
		case manifest.CandidateSHA256:
			// 这些清单供旧版恢复和迁移使用，不证明当前结构 adapter 的身份。
			status.Installation = "legacy_previous"
			status.Message = "检测到旧清单候选桥接，重启时会恢复原始文件并预检升级。"
			return status
		case manifest.OriginalSHA256:
			status.Installation = "original"
			status.Message = "安装目录为原始文件，尚未安装本版桥接。"
			return status
		}
		for _, previous := range manifest.PreviousVersions {
			if status.BundleSHA256 == previous.CandidateSHA256 {
				status.Installation = "legacy_previous"
				status.Message = "检测到已知旧版桥接，重启时会预检并升级。"
				return status
			}
		}
	}
	_, record, found, err := parseKiroStageStructuralCandidate(current)
	if err != nil {
		status.Installation, status.Message = "error", err.Error()
		return status
	}
	if !found {
		return status
	}
	status.AdapterSHA256 = record.AdapterSHA256
	switch record.AdapterSHA256 {
	case status.ExpectedAdapterSHA256:
		status.Installation = "record_current"
		status.Message = "检测到当前桥接记录，尚无本验证器的可信核验；重启时执行安装预检。"
		if lookup != nil {
			hit, err := lookup(target, status.BundleSHA256, status.ExpectedAdapterSHA256)
			if err != nil {
				status.Installation, status.Message = "error", "读取桥接验证凭据失败："+err.Error()
				return status
			}
			if hit {
				status.Installation, status.Verified = "verified_current", true
				status.Message = "当前桥接文件已有本验证器的可信核验；运行中的 Kiro 是否加载仍需请求验证。"
			}
		}
	case kiroStagePreviousStructuralAdapterHash(), kiroStage20261015StructuralAdapterHash():
		status.Installation = "record_previous"
		status.Message = "检测到旧桥接记录，尚未应用本版修复；重启时会严格预检后升级。"
	default:
		status.Message = "恢复记录来自未识别的桥接版本，重启预检将明确校验；未修改文件。"
	}
	return status
}

func applyKiroBridgePolicy(status *kiroBridgeStatus, owned, routing bool, mode string, snapshot *kiroLaunchSnapshot) {
	status.Required = owned && mode == ModeForward && routing
	status.LastLaunch = snapshot
	switch {
	case !owned:
		status.Reason = "proxy_unconfigured"
		status.RestartReason = "先配置 Kiro 代理地址，才能应用本代理的桥接启动设置。"
		return
	case mode == ModeCapture:
		status.Reason = "capture"
	case !routing:
		status.Reason = "routing_disabled"
	}
	status.RestartRequired = true
	switch {
	case status.Required && status.Installation != "verified_current":
		status.RestartReason = "需要重启以预检并应用当前桥接；" + status.Message
	case snapshot == nil:
		status.RestartReason = "本代理尚无最近启动计划记录，重启以应用当前设置；运行加载状态未核实。"
	case snapshot.BridgeEnabled != status.Required:
		status.RestartReason = "最近提交的启动计划与当前桥接设置不同，需要重启同步。"
	case status.Required && (snapshot.ProxyMode != mode || snapshot.BundleSHA256 != status.BundleSHA256 || snapshot.AdapterSHA256 != status.ExpectedAdapterSHA256):
		status.RestartReason = "桥接文件或模式与最近提交的启动计划不同，需要重启同步。"
	default:
		status.RestartRequired = false
		status.RestartReason = "最近提交的启动计划与桥接设置一致；当前进程加载状态仍需实际请求验证。"
	}
}

func (s *Server) proxyModeStatus() map[string]any {
	response := map[string]any{"mode": GetProxyMode(), "kiro_restart_required": false, "kiro_restart_reason": ""}
	if s.cfg == nil {
		response["kiro_restart_reason"] = "Kiro 代理状态未核实。"
		return response
	}
	status := s.kiroProxyStatus(fmt.Sprintf("http://%s:%d", s.proxyDisplayHost(), s.cfg.Proxy.Port))
	response["mode"] = status.ProxyMode
	response["kiro_restart_required"] = status.StageBridge.RestartRequired
	response["kiro_restart_reason"] = status.StageBridge.RestartReason
	return response
}
