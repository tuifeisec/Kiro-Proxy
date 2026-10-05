package proxy

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestInstallCAWindowsRunsOnlyRootInstallCommand(t *testing.T) {
	var calls []*exec.Cmd
	run := func(cmd *exec.Cmd) ([]byte, error) {
		calls = append(calls, cmd)
		return []byte("OK"), nil
	}

	if err := installCAWindowsWithRunner(`C:\certs\ca.crt`, run); err != nil {
		t.Fatalf("installCAWindowsWithRunner() error = %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("command calls = %d, want 1", len(calls))
	}
	if calls[0].Args[0] != "powershell" {
		t.Fatalf("requested command = %q, want powershell", calls[0].Args[0])
	}
}

func TestInstallCAWindowsPreservesInstallFailure(t *testing.T) {
	wantErr := errors.New("UAC cancelled")
	var calls int
	run := func(*exec.Cmd) ([]byte, error) {
		calls++
		return []byte("installer output"), wantErr
	}

	err := installCAWindowsWithRunner(`C:\certs\ca.crt`, run)
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("installCAWindowsWithRunner() error = %v, want wrapped %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "installer output") {
		t.Fatalf("error = %q, want installer output preserved", err)
	}
	if calls != 1 {
		t.Fatalf("command calls = %d, want 1", calls)
	}
}

func TestInstallCADarwinRunsOnlySystemTrustCommand(t *testing.T) {
	var calls []*exec.Cmd
	run := func(cmd *exec.Cmd) ([]byte, error) {
		calls = append(calls, cmd)
		return []byte("OK"), nil
	}

	if err := installCADarwinWithRunner("/certs/ca.crt", run); err != nil {
		t.Fatalf("installCADarwinWithRunner() error = %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("command calls = %d, want 1", len(calls))
	}
	if calls[0].Args[0] != "osascript" {
		t.Fatalf("requested command = %q, want osascript", calls[0].Args[0])
	}
}

func TestInstallCADarwinPreservesInstallFailure(t *testing.T) {
	wantErr := errors.New("administrator authorization cancelled")
	var calls int
	run := func(*exec.Cmd) ([]byte, error) {
		calls++
		return []byte("installer output"), wantErr
	}

	err := installCADarwinWithRunner("/certs/ca.crt", run)
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("installCADarwinWithRunner() error = %v, want wrapped %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "installer output") {
		t.Fatalf("error = %q, want installer output preserved", err)
	}
	if calls != 1 {
		t.Fatalf("command calls = %d, want 1", calls)
	}
}
