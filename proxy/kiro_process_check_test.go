package proxy

import (
	"errors"
	"strings"
	"testing"
)

func TestKiroTasklistProcessDetection(t *testing.T) {
	t.Run("matches Kiro image in CSV", func(t *testing.T) {
		var gotName string
		var gotArgs []string
		running, err := checkKiroProcessWith("windows", func(name string, args ...string) kiroProcessCommandResult {
			gotName, gotArgs = name, append([]string(nil), args...)
			return kiroProcessCommandResult{output: []byte(`"Kiro.exe","42","Console","1:23:45","12,345 K"`), exitCode: 0}
		})
		if err != nil || !running {
			t.Fatalf("running=%t error=%v", running, err)
		}
		if gotName != "tasklist" || strings.Join(gotArgs, "|") != "/FI|IMAGENAME eq Kiro.exe|/FO|CSV|/NH" {
			t.Fatalf("tasklist call = %s %v", gotName, gotArgs)
		}
	})

	t.Run("no task info row means absent", func(t *testing.T) {
		running, err := checkKiroProcessWith("windows", func(string, ...string) kiroProcessCommandResult {
			return kiroProcessCommandResult{output: []byte("INFO: No tasks are running which match the specified criteria.\r\n"), exitCode: 0}
		})
		if err != nil || running {
			t.Fatalf("running=%t error=%v", running, err)
		}
	})

	t.Run("query and parse errors propagate output", func(t *testing.T) {
		_, queryFailure := checkKiroProcessWith("windows", func(string, ...string) kiroProcessCommandResult {
			return kiroProcessCommandResult{output: []byte("tasklist denied"), exitCode: 5, err: errors.New("exit status 5")}
		})
		if queryFailure == nil || !strings.Contains(queryFailure.Error(), "tasklist denied") {
			t.Fatalf("query failure = %v", queryFailure)
		}
		_, parseErr := checkKiroProcessWith("windows", func(string, ...string) kiroProcessCommandResult {
			return kiroProcessCommandResult{output: []byte(`"Kiro.exe","unterminated`), exitCode: 0}
		})
		if parseErr == nil {
			t.Fatal("malformed tasklist CSV was accepted")
		}
	})
}

func TestKiroPgrepProcessDetection(t *testing.T) {
	t.Run("PID output means running", func(t *testing.T) {
		var gotName string
		var gotArgs []string
		running, err := checkKiroProcessWith("darwin", func(name string, args ...string) kiroProcessCommandResult {
			gotName, gotArgs = name, append([]string(nil), args...)
			return kiroProcessCommandResult{output: []byte("701\n702\n"), exitCode: 0}
		})
		if err != nil || !running {
			t.Fatalf("running=%t error=%v", running, err)
		}
		if gotName != "pgrep" || strings.Join(gotArgs, "|") != "-f|/Kiro.app/" {
			t.Fatalf("pgrep call = %s %v", gotName, gotArgs)
		}
	})

	t.Run("exit one means no match", func(t *testing.T) {
		running, err := checkKiroProcessWith("darwin", func(string, ...string) kiroProcessCommandResult {
			return kiroProcessCommandResult{exitCode: 1, err: errors.New("exit status 1")}
		})
		if err != nil || running {
			t.Fatalf("running=%t error=%v", running, err)
		}
	})

	t.Run("other failure and invalid output propagate", func(t *testing.T) {
		_, queryErr := checkKiroProcessWith("darwin", func(string, ...string) kiroProcessCommandResult {
			return kiroProcessCommandResult{output: []byte("pgrep: denied"), exitCode: 2, err: errors.New("exit status 2")}
		})
		if queryErr == nil || !strings.Contains(queryErr.Error(), "pgrep: denied") {
			t.Fatalf("query error = %v", queryErr)
		}
		_, parseErr := checkKiroProcessWith("darwin", func(string, ...string) kiroProcessCommandResult {
			return kiroProcessCommandResult{output: []byte("not-a-pid"), exitCode: 0}
		})
		if parseErr == nil {
			t.Fatal("invalid PID output was accepted")
		}
	})
}

func TestStopKiroProcessesPreservesPlatformPolicy(t *testing.T) {
	t.Run("Windows normal exit falls back to force", func(t *testing.T) {
		var calls []string
		results := []kiroProcessCommandResult{
			{output: []byte("normal denied"), exitCode: 1, err: errors.New("normal failure")},
			{},
		}
		err := stopKiroProcessesWith("windows", func(name string, args ...string) kiroProcessCommandResult {
			calls = append(calls, name+" "+strings.Join(args, " "))
			result := results[0]
			results = results[1:]
			return result
		}, nil)
		if err != nil || strings.Join(calls, "|") != "taskkill /T /IM Kiro.exe|taskkill /F /T /IM Kiro.exe" {
			t.Fatalf("error=%v calls=%v", err, calls)
		}
	})

	t.Run("Darwin pkill no-match is normal", func(t *testing.T) {
		calls := 0
		err := stopKiroProcessesWith("darwin", func(name string, args ...string) kiroProcessCommandResult {
			calls++
			if name == "osascript" {
				return kiroProcessCommandResult{}
			}
			return kiroProcessCommandResult{exitCode: 1, err: errors.New("exit status 1")}
		}, nil)
		if err != nil || calls != 3 {
			t.Fatalf("error=%v command count=%d", err, calls)
		}
	})

	t.Run("Darwin command failure keeps output", func(t *testing.T) {
		calls := 0
		err := stopKiroProcessesWith("darwin", func(name string, args ...string) kiroProcessCommandResult {
			calls++
			if name == "osascript" {
				return kiroProcessCommandResult{}
			}
			if calls == 2 {
				return kiroProcessCommandResult{output: []byte("permission denied"), exitCode: 2, err: errors.New("exit status 2")}
			}
			return kiroProcessCommandResult{}
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("stop error = %v", err)
		}
	})
}

func TestKiroProcessCommandErrorIncludesOriginalOutput(t *testing.T) {
	err := kiroProcessCommandError("probe failed", kiroProcessCommandResult{
		output: []byte("raw command output"),
		err:    errors.New("process error"),
	})
	if err == nil || !strings.Contains(err.Error(), "raw command output") || !strings.Contains(err.Error(), "process error") {
		t.Fatalf("command error = %v", err)
	}
}
