//go:build !windows

package proxy

import (
	"os/exec"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr {
	return nil
}

func hiddenCmd(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
