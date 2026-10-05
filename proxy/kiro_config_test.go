package proxy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureKiroProxyPreflightsBeforeWritesAndPropagatesStartError(t *testing.T) {
	t.Run("preflight failure leaves settings untouched", func(t *testing.T) {
		root := t.TempDir()
		settingsPath := filepath.Join(root, "settings.json")
		initial := []byte(`{"http.proxy":"http://old-proxy:8080","http.proxyStrictSSL":true}`)
		if err := os.WriteFile(settingsPath, initial, 0600); err != nil {
			t.Fatal(err)
		}
		launchCalled := false
		failure := errors.New("invalid certificate")
		result := configureKiroProxyAt("http://127.0.0.1:18081", nil, settingsPath,
			func(inject bool) (kiroLaunchPlan, error) {
				if !inject {
					t.Fatal("configuration should preflight with CA injection")
				}
				return kiroLaunchPlan{}, failure
			},
			func(kiroLaunchPlan, *TrafficLogger) error {
				launchCalled = true
				return nil
			},
		)
		if result.OK || !strings.Contains(result.Message, failure.Error()) {
			t.Fatalf("configuration result = %+v", result)
		}
		got, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(initial) {
			t.Fatalf("settings changed before preflight: %s", got)
		}
		if _, err := os.Stat(settingsPath + ".proxy-backup"); !os.IsNotExist(err) {
			t.Fatalf("backup should not be written before preflight, stat error = %v", err)
		}
		if launchCalled {
			t.Fatal("launch ran after failed preflight")
		}
	})

	t.Run("start error reaches API result", func(t *testing.T) {
		root := t.TempDir()
		settingsPath := filepath.Join(root, "settings.json")
		if err := os.WriteFile(settingsPath, []byte(`{"http.proxy":"http://old-proxy:8080"}`), 0600); err != nil {
			t.Fatal(err)
		}
		startFailure := errors.New("Kiro process creation failed")
		prepared := false
		stopCalled := false
		runningChecks := []bool{true, false}
		runningCheck := 0
		result := configureKiroProxyAt("http://127.0.0.1:18081", nil, settingsPath,
			func(inject bool) (kiroLaunchPlan, error) {
				prepared = inject
				return kiroLaunchPlan{platform: "windows"}, nil
			},
			func(plan kiroLaunchPlan, logger *TrafficLogger) error {
				return executeKiroRestart(plan, logger, kiroRestartDriver{
					running: func() (bool, error) {
						result := runningChecks[runningCheck]
						runningCheck++
						return result, nil
					},
					stop:  func(*TrafficLogger) error { stopCalled = true; return nil },
					start: func(kiroLaunchPlan) error { return startFailure },
				})
			},
		)
		if !prepared || !stopCalled || result.OK || !strings.Contains(result.Message, startFailure.Error()) {
			t.Fatalf("configuration result = %+v, prepared with CA = %t, stop called = %t", result, prepared, stopCalled)
		}
		backup, err := os.ReadFile(settingsPath + ".proxy-backup")
		if err != nil {
			t.Fatal(err)
		}
		if string(backup) != "http://old-proxy:8080" {
			t.Fatalf("original proxy backup = %q", backup)
		}
	})
}

func TestRestoreKiroProxyDoesNotInjectAndKeepsBackupOnStartFailure(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, "settings.json")
	backupPath := settingsPath + ".proxy-backup"
	if err := os.WriteFile(settingsPath, []byte(`{"http.proxy":"http://127.0.0.1:18081","http.proxyStrictSSL":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, []byte("http://old-proxy:8080"), 0600); err != nil {
		t.Fatal(err)
	}
	startFailure := errors.New("open failed")
	prepared := true
	stopCalled := false
	runningChecks := []bool{true, false}
	runningCheck := 0
	result := restoreKiroProxyAt(nil, settingsPath,
		func(inject bool) (kiroLaunchPlan, error) {
			prepared = inject
			return kiroLaunchPlan{platform: "darwin"}, nil
		},
		func(plan kiroLaunchPlan, logger *TrafficLogger) error {
			return executeKiroRestart(plan, logger, kiroRestartDriver{
				running: func() (bool, error) {
					result := runningChecks[runningCheck]
					runningCheck++
					return result, nil
				},
				stop:  func(*TrafficLogger) error { stopCalled = true; return nil },
				start: func(kiroLaunchPlan) error { return startFailure },
			})
		},
	)
	if prepared || !stopCalled || result.OK || !strings.Contains(result.Message, "已还原") || !strings.Contains(result.Message, startFailure.Error()) {
		t.Fatalf("restore result = %+v, prepared with CA = %t, stop called = %t", result, prepared, stopCalled)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup must remain after restart failure: %v", err)
	}
	settings, err := readKiroSettings(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := settings["http.proxy"]; got != "http://old-proxy:8080" {
		t.Fatalf("restored proxy = %v", got)
	}
}

func TestManualRestartUsesConfiguredProxyDecisionAndReturnsLaunchError(t *testing.T) {
	const target = "http://127.0.0.1:18081"
	for _, test := range []struct {
		name          string
		proxy         string
		wantInjection bool
	}{
		{name: "local proxy", proxy: target, wantInjection: true},
		{name: "other proxy", proxy: "http://other-proxy:8080", wantInjection: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			settingsPath := filepath.Join(t.TempDir(), "settings.json")
			data := []byte(fmt.Sprintf(`{"http.proxy":%q}`, test.proxy))
			if err := os.WriteFile(settingsPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			startFailure := errors.New("launcher returned error")
			var preparedWithCA bool
			result := restartKiroAt(nil, target, settingsPath,
				func(inject bool) (kiroLaunchPlan, error) {
					preparedWithCA = inject
					return kiroLaunchPlan{}, nil
				},
				func(kiroLaunchPlan, *TrafficLogger) error { return startFailure },
			)
			if preparedWithCA != test.wantInjection || result.OK || !strings.Contains(result.Message, startFailure.Error()) {
				t.Fatalf("restart result = %+v, prepared with CA = %t", result, preparedWithCA)
			}
		})
	}
}
