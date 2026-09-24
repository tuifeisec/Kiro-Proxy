//go:build windows

package main

import (
	"fmt"
	"os"
	"time"
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

const (
	desktopWindowProbeTimeout = 200 // 毫秒
	smtoBlock                 = 0x0001
	smtoAbortIfHung           = 0x0002
)

var (
	singleInstanceUser32                       = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW                            = singleInstanceUser32.NewProc("MessageBoxW")
	procEnumWindows                            = singleInstanceUser32.NewProc("EnumWindows")
	procGetClassNameW                          = singleInstanceUser32.NewProc("GetClassNameW")
	procSingleInstancePostMessageW             = singleInstanceUser32.NewProc("PostMessageW")
	procSingleInstanceGetForegroundWindow      = singleInstanceUser32.NewProc("GetForegroundWindow")
	procSingleInstanceIsWindowVisible          = singleInstanceUser32.NewProc("IsWindowVisible")
	procSingleInstanceSetForegroundWindow      = singleInstanceUser32.NewProc("SetForegroundWindow")
	procAllowSetForegroundWindow               = singleInstanceUser32.NewProc("AllowSetForegroundWindow")
	procSingleInstanceGetWindowThreadProcessId = singleInstanceUser32.NewProc("GetWindowThreadProcessId")
	procSingleInstanceIsWindow                 = singleInstanceUser32.NewProc("IsWindow")
	procSingleInstanceSendMessageTimeoutW      = singleInstanceUser32.NewProc("SendMessageTimeoutW")
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
// 只有窗口已显示且已成为前台窗口才返回 true，避免把 PostMessage 的投递成功误认为恢复成功。
func activateExistingDesktopWindow() (bool, string) {
	hwnd := findDesktopWindow()
	if hwnd == 0 {
		return false, "未找到桌面宿主窗口（已有实例可能以 -no-window 运行）"
	}

	// 新实例可能具备用户启动带来的前台权限；授权给宿主，同时在本进程中尝试激活。
	var pid uint32
	procSingleInstanceGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != 0 {
		procAllowSetForegroundWindow.Call(uintptr(pid))
	}
	if ret, _, err := procSingleInstancePostMessageW.Call(hwnd, wmUserShow, 0, 0); ret == 0 {
		return false, fmt.Sprintf("投递打开消息失败: hwnd=%#x pid=%d error=%v", hwnd, pid, err)
	}

	// PostMessage 只是入队。等窗口过程实际执行完毕后再检查显示状态；
	// 若宿主的 SetForegroundWindow 被系统拒绝，利用当前启动进程的前台权限补一次激活。
	deadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(deadline) {
		visible, _, _ := procSingleInstanceIsWindowVisible.Call(hwnd)
		if visible != 0 {
			if foreground, _, _ := procSingleInstanceGetForegroundWindow.Call(); foreground == hwnd {
				return true, ""
			}
			procSingleInstanceSetForegroundWindow.Call(hwnd)
			if foreground, _, _ := procSingleInstanceGetForegroundWindow.Call(); foreground == hwnd {
				return true, ""
			}
		}
		time.Sleep(40 * time.Millisecond)
	}

	valid, _, _ := procSingleInstanceIsWindow.Call(hwnd)
	visible, _, _ := procSingleInstanceIsWindowVisible.Call(hwnd)
	foreground, _, _ := procSingleInstanceGetForegroundWindow.Call()
	probe := "未探测（窗口已失效）"
	if valid != 0 {
		probe = probeDesktopWindowMessageLoop(hwnd)
	}
	return false, fmt.Sprintf("打开窗口超时: hwnd=%#x pid=%d valid=%t visible=%t foreground=%#x WM_NULL=%s", hwnd, pid, valid != 0, visible != 0, foreground, probe)
}

// WM_NULL 只用于诊断宿主消息循环能否及时响应，不能证明 WebView 或窗口前置成功。
// 应从宿主 GUI 消息循环外调用，避免同步探测自身。
func probeDesktopWindowMessageLoop(hwnd uintptr) string {
	var result uintptr
	ret, _, err := procSingleInstanceSendMessageTimeoutW.Call(hwnd, 0, 0, 0, smtoBlock|smtoAbortIfHung, desktopWindowProbeTimeout, uintptr(unsafe.Pointer(&result)))
	if ret == 0 {
		return fmt.Sprintf("未在 %dms 内响应或调用失败（error=%v）", desktopWindowProbeTimeout, err)
	}
	return "已响应"
}

func notifySingleInstanceRunning() {
	fmt.Println(singleInstanceMessage)

	// 已成功请求唤醒已有窗口时不再弹窗，避免用"无法重复运行"误导正在找界面的用户。
	if ok, detail := activateExistingDesktopWindow(); ok {
		return
	} else {
		message := fmt.Sprintf("%s\n%s", singleInstanceMessage, detail)
		// 第二实例尚未执行 startApp，需自行写入应用日志；弹窗也保留诊断，避免日志路径不可写时丢失现场。
		if file, err := os.OpenFile("kiro-proxy.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
			_, writeErr := fmt.Fprintf(file, "%s [desktop] 唤醒失败: %s\n", time.Now().Format(time.RFC3339), detail)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				message += fmt.Sprintf("\n写入诊断日志失败: %v %v", writeErr, closeErr)
			}
		} else {
			message += fmt.Sprintf("\n打开诊断日志失败: %v", err)
		}
		title := windows.StringToUTF16Ptr("Kiro Proxy")
		text := windows.StringToUTF16Ptr(message)
		procMessageBoxW.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x00000030)
	}
}
