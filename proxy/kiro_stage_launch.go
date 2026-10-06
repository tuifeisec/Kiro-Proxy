package proxy

import (
	"os"
	"strings"
)

const kiroStageBridgeEnv = "KIRO_PROXY_STAGE_BRIDGE"

func (s *Server) prepareKiroStageLaunch(injectCA bool) (kiroLaunchPlan, error) {
	plan, err := prepareKiroLaunchPlan(injectCA)
	if err != nil {
		return plan, err
	}
	enabled := injectCA && !IsCaptureMode() && s.router.StageRoutingEnabled()
	if enabled && plan.platform == "windows" {
		install, err := prepareKiroStageInstall(plan.executable)
		if err != nil {
			if s.logger != nil {
				s.logger.Error("kiro-stage-bridge", "安装预检失败: "+err.Error())
			}
			return kiroLaunchPlan{}, err
		}
		plan.stageInstall = install
		if s.logger != nil {
			s.logger.Info("kiro-stage-bridge", "安装预检通过: "+install.target+" SHA256="+install.expectedHash+" 候选SHA256="+install.candidateHash)
		}
	}
	return withKiroStageBridge(plan, enabled, os.Environ()), nil
}

func withKiroStageBridge(plan kiroLaunchPlan, enabled bool, inherited []string) kiroLaunchPlan {
	value := "0"
	if enabled {
		value = "1"
	}
	if plan.env == nil {
		plan.env = append([]string(nil), inherited...)
	}
	plan.env = replaceEnvironmentValue(plan.env, kiroStageBridgeEnv, value, plan.platform == "windows")
	if plan.platform == "darwin" {
		// open --env 才能把环境传入新应用进程；普通 shell 环境不足以覆盖已有 app。
		plan.args = append([]string{"--env", kiroStageBridgeEnv + "=" + value}, plan.args...)
	}
	return plan
}

func kiroStageBridgeEnabledInEnv(env []string) bool {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, kiroStageBridgeEnv) && value == "1" {
			return true
		}
	}
	return false
}
