package proxy

import (
	"errors"
	"strings"
	"testing"
)

func TestExecuteKiroRestartSkipsStopWhenNoInstanceExists(t *testing.T) {
	var stopCalled, waitCalled, startCalled bool
	err := executeKiroRestart(kiroLaunchPlan{platform: "windows"}, nil, kiroRestartDriver{
		running: func() (bool, error) { return false, nil },
		stop: func(*TrafficLogger) error {
			stopCalled = true
			return nil
		},
		wait: func() { waitCalled = true },
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
	checks := []bool{true, false}
	checkIndex := 0
	err := executeKiroRestart(kiroLaunchPlan{platform: "darwin"}, nil, kiroRestartDriver{
		running: func() (bool, error) {
			order = append(order, "probe")
			result := checks[checkIndex]
			checkIndex++
			return result, nil
		},
		stop: func(*TrafficLogger) error {
			order = append(order, "stop")
			return nil
		},
		wait: func() { order = append(order, "wait") },
		start: func(kiroLaunchPlan) error {
			order = append(order, "start")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(order, ","), "probe,stop,wait,probe,start"; got != want {
		t.Fatalf("operation order = %q, want %q", got, want)
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
