package proxy

import (
	"strings"
	"testing"
)

func TestStageBridgeLaunchEnvironmentIsExplicitAndPreservesCA(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		want := "0"
		if enabled {
			want = "1"
		}
		for _, platform := range []string{"windows", "darwin"} {
			plan := withKiroStageBridge(kiroLaunchPlan{platform: platform, env: []string{"NODE_EXTRA_CA_CERTS=proxy-ca", "kiro_proxy_stage_bridge=old"}, args: []string{"-n", "-a", "Kiro"}}, enabled, nil)
			count := 0
			for _, entry := range plan.env {
				if strings.HasPrefix(entry, kiroStageBridgeEnv+"=") {
					count++
					if entry != kiroStageBridgeEnv+"="+want {
						t.Fatalf("wrong bridge value: %v", plan.env)
					}
				}
			}
			if count != 1 || !strings.Contains(strings.Join(plan.env, ";"), "NODE_EXTRA_CA_CERTS=proxy-ca") {
				t.Fatalf("environment clobbered: %v", plan.env)
			}
			if platform == "darwin" && (plan.args[0] != "--env" || plan.args[1] != kiroStageBridgeEnv+"="+want) {
				t.Fatalf("macOS app environment missing: %v", plan.args)
			}
		}
	}
}

func TestKiroStageLaunchSkipsBridgePreparationAndInstallation(t *testing.T) {
	prepareCalled := false
	plan, err := prepareKiroStageLaunchPlan(true, ModeForward, []string{
		"NODE_EXTRA_CA_CERTS=proxy-ca", "KIRO_PROXY_STAGE_BRIDGE=1",
	}, func(injectCA bool) (kiroLaunchPlan, error) {
		prepareCalled = true
		if !injectCA {
			t.Fatal("CA preparation was not preserved")
		}
		return kiroLaunchPlan{platform: "windows", env: []string{"NODE_EXTRA_CA_CERTS=proxy-ca"}, caInjected: true}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !prepareCalled || plan.stageInstall != nil || !plan.caInjected || plan.proxyMode != ModeForward {
		t.Fatalf("launch plan = %+v, prepare called = %t", plan, prepareCalled)
	}
	if got := environmentValue(plan.env, kiroStageBridgeEnv, true); got != "0" {
		t.Fatalf("bridge launch environment = %q, want disabled", got)
	}
	if got := environmentValue(plan.env, kiroExtraCACertEnv, true); got != "proxy-ca" {
		t.Fatalf("proxy CA environment = %q, want preserved", got)
	}
}
