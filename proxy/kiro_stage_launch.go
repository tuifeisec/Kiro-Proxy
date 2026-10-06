package proxy

import "os"

const kiroStageBridgeEnv = "KIRO_PROXY_STAGE_BRIDGE"

func (s *Server) prepareKiroStageLaunch(injectCA bool) (kiroLaunchPlan, error) {
	plan, err := prepareKiroLaunchPlan(injectCA)
	if err != nil {
		return plan, err
	}
	enabled := injectCA && !IsCaptureMode() && s.router.StageRoutingEnabled()
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
