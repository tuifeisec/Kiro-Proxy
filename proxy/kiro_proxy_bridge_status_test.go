package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func bridgeStatusCandidate(t *testing.T, adapter string) []byte {
	t.Helper()
	candidate, err := wrapKiroStageStructuralCandidate([]byte("before"), []byte("after"), "status-mock",
		adapter, []kiroStageStructuralEdit{{Offset: 0, Original: "before", Installed: "after"}})
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func TestKiroBridgeStatusKnownFileIdentities(t *testing.T) {
	manifest := kiroStageManifest{
		OriginalSHA256: kiroStageSHA256([]byte("original")), CandidateSHA256: kiroStageSHA256([]byte("current")),
		PreviousVersions: []kiroStagePreviousVersion{{CandidateSHA256: kiroStageSHA256([]byte("previous"))}},
	}
	for _, tc := range []struct {
		file, installation string
		verified           bool
	}{
		{"current", "legacy_previous", false},
		{"original", "original", false},
		{"previous", "legacy_previous", false},
		{"unknown", "unknown", false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			status := classifyKiroBridge([]byte(tc.file), "mock/extension.js", []kiroStageManifest{{}, manifest},
				func(string, string, string) (bool, error) {
					t.Fatal("known or unrecorded identity must not query the receipt cache")
					return false, nil
				})
			if status.Installation != tc.installation || status.Verified != tc.verified || status.BundleSHA256 != kiroStageSHA256([]byte(tc.file)) {
				t.Fatalf("status = %+v", status)
			}
			if status.AdapterSHA256 != "" {
				t.Fatalf("legacy manifest must not imply current adapter identity: %+v", status)
			}
		})
	}
}

func TestKiroBridgeStatusRecordRequiresTrustedReceipt(t *testing.T) {
	adapter := kiroStageStructuralAdapterHash()
	candidate := bridgeStatusCandidate(t, adapter)
	for _, tc := range []struct {
		name, installation string
		hit                bool
		err                error
	}{
		{"cache miss", "record_current", false, nil},
		{"trusted receipt", "verified_current", true, nil},
		{"receipt read failure", "error", false, errors.New("mock receipt denied")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			status := classifyKiroBridge(candidate, "mock/extension.js", nil, func(target, installed, gotAdapter string) (bool, error) {
				calls++
				if target != "mock/extension.js" || installed != kiroStageSHA256(candidate) || gotAdapter != adapter {
					t.Fatalf("receipt lookup has wrong binding: %q, %q, %q", target, installed, gotAdapter)
				}
				return tc.hit, tc.err
			})
			if calls != 1 || status.Installation != tc.installation || status.Verified != tc.hit {
				t.Fatalf("calls = %d, status = %+v", calls, status)
			}
			if tc.err != nil && !strings.Contains(status.Message, tc.err.Error()) {
				t.Fatalf("original receipt error lost: %q", status.Message)
			}
		})
	}
	if status := classifyKiroBridge(candidate, "mock/extension.js", nil, nil); status.Verified || status.Installation != "record_current" {
		t.Fatalf("a self-consistent mock record is not verification: %+v", status)
	}
	for _, tc := range []struct{ adapter, installation string }{
		{kiroStagePreviousStructuralAdapterHash(), "record_previous"},
		{kiroStage20261015StructuralAdapterHash(), "record_previous"},
		{kiroStageSHA256([]byte("unknown adapter")), "unknown"},
	} {
		status := classifyKiroBridge(bridgeStatusCandidate(t, tc.adapter), "mock/extension.js", nil,
			func(string, string, string) (bool, error) {
				t.Fatal("non-current adapter must not use a current receipt")
				return true, nil
			})
		if status.Installation != tc.installation || status.Verified {
			t.Fatalf("status = %+v", status)
		}
	}
}

func TestKiroBridgeStatusTrustedCacheBindsPathAndWholeFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "extension.js")
	candidate := bridgeStatusCandidate(t, kiroStageStructuralAdapterHash())
	cache := newTestKiroStageVerificationCache(filepath.Join(root, "cache"), testKiroStageCacheHash("status-validator"))
	if err := cache.Store(target, kiroStageSHA256(candidate), kiroStageSHA256([]byte("before")), kiroStageStructuralAdapterHash()); err != nil {
		t.Fatal(err)
	}
	lookup := func(target, installed, adapter string) (bool, error) {
		_, hit, err := cache.Lookup(target, installed, adapter)
		return hit, err
	}
	if status := classifyKiroBridge(candidate, target, nil, lookup); !status.Verified {
		t.Fatalf("trusted receipt not used: %+v", status)
	}
	if status := classifyKiroBridge(candidate, filepath.Join(root, "another.js"), nil, lookup); status.Verified {
		t.Fatalf("receipt leaked across installation paths: %+v", status)
	}
	changed, err := wrapKiroStageStructuralCandidate([]byte("before"), []byte("different"), "status-mock",
		kiroStageStructuralAdapterHash(), []kiroStageStructuralEdit{{Offset: 0, Original: "before", Installed: "different"}})
	if err != nil {
		t.Fatal(err)
	}
	if status := classifyKiroBridge(changed, target, nil, lookup); status.Verified || status.Installation != "record_current" {
		t.Fatalf("receipt leaked across file contents: %+v", status)
	}
}

func TestKiroBridgeStatusDamagedRecordPreservesError(t *testing.T) {
	for _, damage := range []string{"payload", "trailer"} {
		t.Run(damage, func(t *testing.T) {
			candidate := bridgeStatusCandidate(t, kiroStageStructuralAdapterHash())
			if damage == "payload" {
				candidate[0] = '!'
			} else {
				candidate = candidate[:len(candidate)-1]
			}
			_, _, _, expected := parseKiroStageStructuralCandidate(candidate)
			status := classifyKiroBridge(candidate, "mock/extension.js", nil, func(string, string, string) (bool, error) {
				t.Fatal("damaged file must not use a receipt")
				return true, nil
			})
			if expected == nil || status.Verified || status.Installation != "error" || status.Message != expected.Error() {
				t.Fatalf("status = %+v, original error = %v", status, expected)
			}
		})
	}
}

func TestKiroBridgeRestartPolicyReflectsSettingsAndSubmittedPlan(t *testing.T) {
	matching := kiroLaunchSnapshot{ProxyMode: ModeForward, BridgeEnabled: true, BundleSHA256: "bundle", AdapterSHA256: "adapter"}
	disabled := kiroLaunchSnapshot{ProxyMode: ModeCapture, BridgeEnabled: false}
	changed := matching
	changed.BundleSHA256 = "old-bundle"
	for _, tc := range []struct {
		name, installation, mode, reason  string
		owned, routing, required, restart bool
		snapshot                          *kiroLaunchSnapshot
	}{
		{"unconfigured", "record_previous", ModeForward, "proxy_unconfigured", false, true, false, false, nil},
		{"capture no plan", "record_previous", ModeCapture, "capture", true, true, false, true, nil},
		{"capture disables bridge", "verified_current", ModeCapture, "capture", true, true, false, true, &matching},
		{"capture already submitted", "record_previous", ModeCapture, "capture", true, true, false, false, &disabled},
		{"routing disabled", "verified_current", ModeForward, "routing_disabled", true, false, false, true, &matching},
		{"routing disabled already submitted", "record_previous", ModeForward, "routing_disabled", true, false, false, false, &disabled},
		{"unverified current", "record_current", ModeForward, "", true, true, true, true, &matching},
		{"old bridge", "record_previous", ModeForward, "", true, true, true, true, &matching},
		{"verified no plan", "verified_current", ModeForward, "", true, true, true, true, nil},
		{"enable after capture", "verified_current", ModeForward, "", true, true, true, true, &disabled},
		{"file changed", "verified_current", ModeForward, "", true, true, true, true, &changed},
		{"matching submitted plan", "verified_current", ModeForward, "", true, true, true, false, &matching},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := kiroBridgeStatus{Installation: tc.installation, BundleSHA256: "bundle", ExpectedAdapterSHA256: "adapter", Message: "mock file identity"}
			applyKiroBridgePolicy(&status, tc.owned, tc.routing, tc.mode, tc.snapshot)
			if status.Required != tc.required || status.RestartRequired != tc.restart || status.Reason != tc.reason || status.LastLaunch != tc.snapshot || status.RestartReason == "" {
				t.Fatalf("status = %+v", status)
			}
			if tc.name == "matching submitted plan" && !strings.Contains(status.RestartReason, "仍需实际请求验证") {
				t.Fatalf("submitted plan must not claim actual loading: %q", status.RestartReason)
			}
		})
	}
}

func TestKiroLaunchStatusRecordsOnlySuccessfulSubmission(t *testing.T) {
	s := &Server{}
	plan := kiroLaunchPlan{proxyMode: ModeForward, env: []string{kiroStageBridgeEnv + "=1"},
		stageInstall: &kiroStageInstallPlan{candidateHash: "mock-bundle", adapterHash: "mock-adapter"}}
	called := false
	if err := s.launchKiroWithStatusUsing(plan, nil, func(got kiroLaunchPlan, logger *TrafficLogger) error {
		called = true
		if got.proxyMode != plan.proxyMode || got.stageInstall != plan.stageInstall || logger != nil || s.kiroLaunchStatus.load() != nil {
			t.Fatalf("wrong submitted plan or premature success snapshot: %+v", got)
		}
		return nil
	}); err != nil || !called {
		t.Fatalf("launch error = %v, called = %t", err, called)
	}
	snapshot := s.kiroLaunchStatus.load()
	if snapshot == nil || snapshot.ProxyMode != ModeForward || !snapshot.BridgeEnabled || snapshot.BundleSHA256 != "mock-bundle" || snapshot.AdapterSHA256 != "mock-adapter" {
		t.Fatalf("submitted snapshot = %+v", snapshot)
	}
	if _, err := time.Parse(time.RFC3339Nano, snapshot.SubmittedAt); err != nil {
		t.Fatalf("invalid submission time: %v", err)
	}
	failure := errors.New("mock Kiro launch rejected")
	if err := s.launchKiroWithStatusUsing(plan, nil, func(kiroLaunchPlan, *TrafficLogger) error { return failure }); err != failure || s.kiroLaunchStatus.load() != nil {
		t.Fatalf("failed launch retained success or lost original error: %v, %+v", err, s.kiroLaunchStatus.load())
	}
	capture := kiroLaunchPlan{proxyMode: ModeCapture, env: []string{kiroStageBridgeEnv + "=0"}}
	if err := s.launchKiroWithStatusUsing(capture, nil, func(kiroLaunchPlan, *TrafficLogger) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if snapshot := s.kiroLaunchStatus.load(); snapshot.BridgeEnabled || snapshot.ProxyMode != ModeCapture || snapshot.BundleSHA256 != "" || snapshot.AdapterSHA256 != "" {
		t.Fatalf("capture submission = %+v", snapshot)
	}
}

func TestKiroLaunchTrackerConcurrentReadsReturnCopies(t *testing.T) {
	var tracker kiroLaunchTracker
	plan := kiroLaunchPlan{proxyMode: ModeCapture}
	tracker.record(plan, time.Unix(123, 0))
	copy := tracker.load()
	copy.ProxyMode = "mutated"
	if tracker.load().ProxyMode != ModeCapture {
		t.Fatal("caller mutated the tracked snapshot")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				tracker.record(plan, time.Unix(123, 0))
				if snapshot := tracker.load(); snapshot != nil && snapshot.ProxyMode != ModeCapture {
					t.Errorf("inconsistent snapshot: %+v", snapshot)
				}
				tracker.clear()
			}
		}()
	}
	wg.Wait()
}

func TestProxyModeResponseIncludesKiroSynchronizationHints(t *testing.T) {
	previous := GetProxyMode()
	t.Cleanup(func() { SetProxyMode(previous) })
	SetProxyMode(ModeCapture)
	s := &Server{} // 无外部配置，隔离文件读取与真实 Kiro 启动。
	for _, method := range []string{http.MethodPut, http.MethodGet} {
		w := httptest.NewRecorder()
		s.handleProxyMode(w, httptest.NewRequest(method, "/api/proxy-mode", strings.NewReader(`{"mode":"forward"}`)))
		var response struct {
			Mode     string `json:"mode"`
			Required *bool  `json:"kiro_restart_required"`
			Reason   string `json:"kiro_restart_reason"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusOK || response.Mode != ModeForward || response.Required == nil || response.Reason == "" {
			t.Fatalf("%s response = %s, error = %v", method, w.Body.String(), err)
		}
	}
	if s.kiroLaunchStatus.load() != nil {
		t.Fatal("changing the Go proxy mode must not submit a Kiro launch")
	}
}
