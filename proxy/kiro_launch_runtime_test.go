package proxy

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecuteKiroRestartSkipsStopWhenNoInstanceExists(t *testing.T) {
	var stopCalled, waitCalled, startCalled bool
	err := executeKiroRestart(kiroLaunchPlan{platform: "windows"}, nil, kiroRestartDriver{
		running: func() (bool, error) { return false, nil },
		stop: func(*TrafficLogger) error {
			stopCalled = true
			return nil
		},
		waitForExit: func() (bool, error) { waitCalled = true; return false, nil },
		start: func(kiroLaunchPlan) error {
			startCalled = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stopCalled || waitCalled || !startCalled {
		t.Fatalf("stop=%t wait=%t start=%t; absent Kiro should skip stop and wait", stopCalled, waitCalled, startCalled)
	}
}

func TestExecuteKiroRestartStopsAndRechecksBeforeStarting(t *testing.T) {
	var order []string
	err := executeKiroRestart(kiroLaunchPlan{platform: "darwin"}, nil, kiroRestartDriver{
		running: func() (bool, error) {
			order = append(order, "probe")
			return true, nil
		},
		stop: func(*TrafficLogger) error {
			order = append(order, "stop")
			return nil
		},
		waitForExit: func() (bool, error) {
			order = append(order, "wait")
			return false, nil
		},
		start: func(kiroLaunchPlan) error {
			order = append(order, "start")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(order, ","), "probe,stop,wait,start"; got != want {
		t.Fatalf("operation order = %q, want %q", got, want)
	}
}

func TestWaitForKiroExitUsesOnlyNecessaryDelay(t *testing.T) {
	probeFailure := errors.New("tasklist unavailable")
	for _, tc := range []struct {
		name        string
		exitAfter   int
		probeError  error
		wantRunning bool
		wantWait    time.Duration
	}{
		{"immediate", 1, nil, false, 0},
		{"delayed", 4, nil, false, 300 * time.Millisecond},
		{"timeout", 0, nil, true, 2 * time.Second},
		{"query failure", 1, probeFailure, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := time.Unix(0, 0)
			started := clock
			probes := 0
			running, err := waitForKiroExit(func() (bool, error) {
				probes++
				if tc.probeError != nil {
					return true, tc.probeError
				}
				return tc.exitAfter == 0 || probes < tc.exitAfter, nil
			}, func() time.Time { return clock }, func(delay time.Duration) {
				if delay <= 0 || delay > 100*time.Millisecond {
					t.Fatalf("invalid poll delay: %s", delay)
				}
				clock = clock.Add(delay)
			})
			if running != tc.wantRunning || !errors.Is(err, tc.probeError) || clock.Sub(started) != tc.wantWait {
				t.Fatalf("running=%t error=%v delay=%s", running, err, clock.Sub(started))
			}
		})
	}
}

func TestExecuteKiroRestartWaitErrorsPreserveStopFailure(t *testing.T) {
	stopFailure := errors.New("taskkill denied")
	probeFailure := errors.New("tasklist unavailable")
	for _, tc := range []struct {
		name    string
		running bool
		err     error
	}{
		{"query failure", false, probeFailure},
		{"timeout", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := false
			err := executeKiroRestart(kiroLaunchPlan{}, nil, kiroRestartDriver{
				running:     func() (bool, error) { return true, nil },
				stop:        func(*TrafficLogger) error { return stopFailure },
				waitForExit: func() (bool, error) { return tc.running, tc.err },
				start:       func(kiroLaunchPlan) error { started = true; return nil },
			})
			if err == nil || started || !strings.Contains(err.Error(), stopFailure.Error()) {
				t.Fatalf("start=%t err=%v", started, err)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("lost probe error: %v", err)
			}
		})
	}
}

func TestExecuteKiroRestartBlocksStartWhenStopFailsOrProcessRemains(t *testing.T) {
	stopFailure := errors.New("taskkill denied")
	var startCalled bool
	checks := []bool{true, true}
	checkIndex := 0
	err := executeKiroRestart(kiroLaunchPlan{}, nil, kiroRestartDriver{
		running: func() (bool, error) {
			result := checks[checkIndex]
			checkIndex++
			return result, nil
		},
		stop: func(*TrafficLogger) error { return stopFailure },
		start: func(kiroLaunchPlan) error {
			startCalled = true
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), stopFailure.Error()) || !strings.Contains(err.Error(), "旧进程仍在运行") {
		t.Fatalf("restart error = %v", err)
	}
	if startCalled {
		t.Fatal("start ran after stop failed and Kiro remained running")
	}
}

func TestExecuteKiroRestartRechecksStopRaceBeforeStarting(t *testing.T) {
	stopFailure := errors.New("taskkill process race")
	checks := []bool{true, false}
	checkIndex := 0
	var startCalled bool
	err := executeKiroRestart(kiroLaunchPlan{}, nil, kiroRestartDriver{
		running: func() (bool, error) {
			result := checks[checkIndex]
			checkIndex++
			return result, nil
		},
		stop: func(*TrafficLogger) error { return stopFailure },
		start: func(kiroLaunchPlan) error {
			startCalled = true
			return nil
		},
	})
	if err != nil || !startCalled {
		t.Fatalf("restart after verified process exit = %v, start called = %t", err, startCalled)
	}
}

func TestExecuteKiroRestartProbeFailuresPreventStopOrStart(t *testing.T) {
	probeFailure := errors.New("tasklist unavailable")
	t.Run("initial status probe", func(t *testing.T) {
		var stopCalled, startCalled bool
		err := executeKiroRestart(kiroLaunchPlan{}, nil, kiroRestartDriver{
			running: func() (bool, error) { return false, probeFailure },
			stop: func(*TrafficLogger) error {
				stopCalled = true
				return nil
			},
			start: func(kiroLaunchPlan) error {
				startCalled = true
				return nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), probeFailure.Error()) || stopCalled || startCalled {
			t.Fatalf("error=%v stop=%t start=%t", err, stopCalled, startCalled)
		}
	})

	t.Run("post stop status probe", func(t *testing.T) {
		var probeCount int
		var startCalled bool
		err := executeKiroRestart(kiroLaunchPlan{}, nil, kiroRestartDriver{
			running: func() (bool, error) {
				probeCount++
				if probeCount == 1 {
					return true, nil
				}
				return false, probeFailure
			},
			stop: func(*TrafficLogger) error { return nil },
			start: func(kiroLaunchPlan) error {
				startCalled = true
				return nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), probeFailure.Error()) || startCalled {
			t.Fatalf("error=%v start=%t", err, startCalled)
		}
	})
}

func TestExecuteKiroRestartStillRunningAndStartErrorsPropagate(t *testing.T) {
	t.Run("old process still running", func(t *testing.T) {
		var startCalled bool
		err := executeKiroRestart(kiroLaunchPlan{}, nil, kiroRestartDriver{
			running: func() (bool, error) { return true, nil },
			stop:    func(*TrafficLogger) error { return nil },
			start: func(kiroLaunchPlan) error {
				startCalled = true
				return nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "旧 Kiro 进程仍在运行") || startCalled {
			t.Fatalf("error=%v start=%t", err, startCalled)
		}
	})

	t.Run("start failure", func(t *testing.T) {
		startFailure := errors.New("CreateProcess failed")
		checks := 0
		err := executeKiroRestart(kiroLaunchPlan{}, nil, kiroRestartDriver{
			running: func() (bool, error) {
				checks++
				return false, nil
			},
			start: func(kiroLaunchPlan) error { return startFailure },
		})
		if err == nil || !strings.Contains(err.Error(), startFailure.Error()) || checks != 1 {
			t.Fatalf("error=%v probe count=%d", err, checks)
		}
	})
}
