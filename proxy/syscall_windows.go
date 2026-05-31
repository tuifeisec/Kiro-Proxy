//go:build windows

package proxy

import (
	"os/exec"
	"syscall"
)

// sysProcAttr 返回 Windows 下隐藏控制台窗口的进程属性
// CREATE_NO_WINDOW = 0x08000000
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}

// hiddenCmd 创建一个不弹出黑色窗口的 exec.Cmd
func hiddenCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = sysProcAttr()
	return cmd
}
