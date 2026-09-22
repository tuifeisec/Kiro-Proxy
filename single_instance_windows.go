//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 跨进程窗口激活协议：第 2 个实例靠这两个标识找到并唤醒已运行的桌面窗口。
// 必须与 desktop_window_windows.go 中注册的窗口类名、窗口过程处理的消息保持一致。
const (
	desktopWindowClassName = "KiroProxyFramelessWindow"
	wmUserShow             = 0x0401
)

const singleInstanceMessage = "当前已经有运行了，无法重复运行"

var (
	singleInstanceUser32                       = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW                            = singleInstanceUser32.NewProc("MessageBoxW")
	procEnumWindows                            = singleInstanceUser32.NewProc("EnumWindows")
	procGetClassNameW                          = singleInstanceUser32.NewProc("GetClassNameW")
	procSingleInstancePostMessageW             = singleInstanceUser32.NewProc("PostMessageW")
	procAllowSetForegroundWindow               = singleInstanceUser32.NewProc("AllowSetForegroundWindow")
	procSingleInstanceGetWindowThreadProcessId = singleInstanceUser32.NewProc("GetWindowThreadProcessId")
	singleInstanceName                         = windows.StringToUTF16Ptr(`Local\KiroProxySingleInstance`)
)

func acquireSingleInstanceLock() (func(), bool, error) {
	handle, err := windows.CreateMutex(nil, false, singleInstanceName)
	if handle == 0 {
		return nil, false, fmt.Errorf("创建单实例锁失败: %w", err)
	}
	if err == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(handle)
		return nil, false, nil
	}
	return func() { windows.CloseHandle(handle) }, true, nil
}

// EnumWindows 只接受函数指针，回调必须是包级变量。
// 查找只发生在重复启动时的一次调用里，不涉及并发。
var (
	foundDesktopWindow    uintptr
	enumDesktopWindowProc = windows.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var buf [256]uint16
		n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n == 0 || windows.UTF16ToString(buf[:n]) != desktopWindowClassName {
			return 1
		}
		foundDesktopWindow = hwnd
		return 0
	})
)

// findDesktopWindow 按类名枚举定位已运行的桌面窗口，未找到返回 0。
// 这里不用 FindWindow：它在跨进程、不同完整性级别的场景下可能漏掉目标窗口。
func findDesktopWindow() uintptr {
	foundDesktopWindow = 0
	procEnumWindows.Call(enumDesktopWindowProc, 0)
	return foundDesktopWindow
}

// activateExistingDesktopWindow 请求已运行的桌面窗口显示并前置。
// 返回 false 表示没有可激活的窗口，例如已有实例以 -no-window 启动，或当前运行的是控制台版。
func activateExistingDesktopWindow() bool {
	hwnd := findDesktopWindow()
	if hwnd == 0 {
		return false
	}

	// 本进程刚由用户启动，通常还能获得前台权限；把"设置前台窗口"的权限授予宿主进程，
	// 否则宿主的 SetForegroundWindow 会被系统拒绝，窗口虽然恢复可见却仍留在其他窗口后面。
	var pid uint32
	procSingleInstanceGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != 0 {
		procAllowSetForegroundWindow.Call(uintptr(pid))
	}

	ret, _, _ := procSingleInstancePostMessageW.Call(hwnd, wmUserShow, 0, 0)
	return ret != 0
}

func notifySingleInstanceRunning() {
	fmt.Println(singleInstanceMessage)

	// 已成功请求唤醒已有窗口时不再弹窗，避免用"无法重复运行"误导正在找界面的用户。
	if activateExistingDesktopWindow() {
		return
	}

	title := windows.StringToUTF16Ptr("Kiro Proxy")
	message := windows.StringToUTF16Ptr(singleInstanceMessage)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x00000030)
}
