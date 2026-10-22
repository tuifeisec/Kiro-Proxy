package proxy

import (
	"os"
	"strings"
)

const kiroStageBridgeEnv = "KIRO_PROXY_STAGE_BRIDGE"

func (s *Server) prepareKiroStageLaunch(injectCA bool) (kiroLaunchPlan, error) {
	return prepareKiroStageLaunchPlan(injectCA, GetProxyMode(), os.Environ(), prepareKiroLaunchPlan, s.logger)
}

func prepareKiroStageLaunchPlan(injectCA bool, mode string, inherited []string, prepare kiroLaunchPreparer, logger *TrafficLogger) (kiroLaunchPlan, error) {
	plan, err := prepare(injectCA)
	if err != nil {
		return plan, err
	}
	plan.proxyMode = mode
	if logger != nil {
		logger.Info("kiro-stage-bridge", "activity 驱动 Auto 路由不依赖阶段桥接；跳过桥接预检与安装")
	}
	return withKiroStageBridge(plan, false, inherited), nil
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
