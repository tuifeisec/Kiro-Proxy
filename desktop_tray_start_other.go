//go:build desktop && !darwin

package main

import "runtime"

func startDesktopTray(c *desktopTrayController) {
	go func() {
		// 托盘 HWND 和 GetMessage 消息泵必须运行在同一个 OS 线程。
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		c.run()
	}()
}
