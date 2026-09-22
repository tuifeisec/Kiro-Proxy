//go:build desktop && windows

package main

import (
	"sync"
	"unsafe"

	webview "github.com/webview/webview_go"
	"golang.org/x/sys/windows"
)

const (
	wsOverlapped                = 0x00000000
	wsCaption                   = 0x00c00000
	wsSysMenu                   = 0x00080000
	wsMinimizeBox               = 0x00020000
	wsVisible                   = 0x10000000
	wsChild                     = 0x40000000
	wsClipSiblings              = 0x04000000
	wsClipChildren              = 0x02000000
	swpNoSize                   = 0x0001
	swpNoMove                   = 0x0002
	swpNoZOrder                 = 0x0004
	swpFrameChanged             = 0x0020
	swHide                      = 0
	swShow                      = 5
	swRestore                   = 9
	dwmwaWindowCornerPreference = 33
	dwmwaBorderColor            = 34
	dwmwcpRound                 = 2
	dwmwaColorDefault           = 0xffffffff
	wmDestroy                   = 0x0002
	wmSize                      = 0x0005
	// wmUserShow（0x0401）定义在 single_instance_windows.go，与跨进程激活协议放在一起维护。
	wmUserHide      = 0x0402
	wmUserQuit      = 0x0403
	wmNCCalcSize    = 0x0083
	wmNCLButtonDown = 0x00a1
	wmClose         = 0x0010
	htCaption       = 2
)

var (
	procCreateWindowExW          = user32.NewProc("CreateWindowExW")
	procDestroyWindow            = user32.NewProc("DestroyWindow")
	procDefWindowProcW           = user32.NewProc("DefWindowProcW")
	procEnumChildWindows         = user32.NewProc("EnumChildWindows")
	procGetClientRect            = user32.NewProc("GetClientRect")
	procMoveWindow               = user32.NewProc("MoveWindow")
	procRegisterClassExW         = user32.NewProc("RegisterClassExW")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procShowWindow               = user32.NewProc("ShowWindow")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procReleaseCapture           = user32.NewProc("ReleaseCapture")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procIsWindow                 = user32.NewProc("IsWindow")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procAttachThreadInput        = user32.NewProc("AttachThreadInput")
	procBringWindowToTop         = user32.NewProc("BringWindowToTop")
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	dwmapi                       = windows.NewLazySystemDLL("dwmapi.dll")
	procGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
	procGetCurrentThreadId       = kernel32.NewProc("GetCurrentThreadId")
	procDwmSetWindowAttribute    = dwmapi.NewProc("DwmSetWindowAttribute")
)

var desktopHostWndProc = windows.NewCallback(desktopHostWindowProc)
var resizeDesktopChildProc = windows.NewCallback(resizeDesktopHostChild)

var (
	desktopWindowMu      sync.RWMutex
	currentDesktopWindow *desktopWindowController
)

type rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

func setCurrentDesktopWindow(c *desktopWindowController) {
	desktopWindowMu.Lock()
	currentDesktopWindow = c
	desktopWindowMu.Unlock()
}

func getCurrentDesktopWindow() *desktopWindowController {
	desktopWindowMu.RLock()
	defer desktopWindowMu.RUnlock()
	return currentDesktopWindow
}

func newDesktopWebView(debug bool, c *desktopWindowController) webview.WebView {
	setCurrentDesktopWindow(c)
	hwnd := createDesktopHostWindow()
	c.setWindowHandle(hwnd)
	if hwnd != 0 {
		return webview.NewWindow(debug, unsafe.Pointer(hwnd))
	}
	return webview.New(debug)
}

func configureDesktopWindow(w webview.WebView) {
	if hwnd := topLevelWindow(w); hwnd != 0 {
		resizeDesktopHostChildren(hwnd)
		procShowWindow.Call(hwnd, swShow)
	}
}

func desktopWindowIsValid(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	ret, _, _ := procIsWindow.Call(hwnd)
	return ret != 0
}

func closeDesktopWindow(c *desktopWindowController) {
	hwnd := c.windowHandle()
	if hwnd != 0 && desktopWindowIsValid(hwnd) {
		procDestroyWindow.Call(hwnd)
	}
	c.setWindowHandle(0)
}

func destroyDesktopHostWindow(c *desktopWindowController) {
	closeDesktopWindow(c)
}

func hideDesktopWindow(c *desktopWindowController) {
	hwnd := c.windowHandle()
	if desktopWindowIsValid(hwnd) {
		procPostMessageW.Call(hwnd, wmUserHide, 0, 0)
	}
}

func hideDesktopWindowController(w webview.WebView) {
	if c := getCurrentDesktopWindow(); c != nil {
		if hwnd := c.windowHandle(); desktopWindowIsValid(hwnd) {
			procPostMessageW.Call(hwnd, wmUserHide, 0, 0)
			return
		}
	}
	if hwnd := topLevelWindow(w); desktopWindowIsValid(hwnd) {
		procPostMessageW.Call(hwnd, wmUserHide, 0, 0)
	}
}

func showDesktopWindow(c *desktopWindowController) {
	hwnd := c.windowHandle()
	if desktopWindowIsValid(hwnd) {
		procPostMessageW.Call(hwnd, wmUserShow, 0, 0)
	}
}

// forceDesktopWindowForeground 是 SetForegroundWindow 被系统拒绝时的兜底：
// 把本线程临时附加到当前前台窗口所在线程的输入队列，绕过"非前台进程不能设置前台窗口"的限制。
// 附加后必须立刻解除，否则两个线程会一直共享输入队列。
func forceDesktopWindowForeground(hwnd uintptr) {
	foreground, _, _ := procGetForegroundWindow.Call()
	if foreground == 0 || foreground == hwnd {
		procBringWindowToTop.Call(hwnd)
		return
	}

	foregroundThread, _, _ := procGetWindowThreadProcessId.Call(foreground, 0)
	currentThread, _, _ := procGetCurrentThreadId.Call()
	if foregroundThread != 0 && foregroundThread != currentThread {
		procAttachThreadInput.Call(currentThread, foregroundThread, 1)
		defer procAttachThreadInput.Call(currentThread, foregroundThread, 0)
	}

	procBringWindowToTop.Call(hwnd)
	procSetForegroundWindow.Call(hwnd)
}

func topLevelWindow(w webview.WebView) uintptr {
	return uintptr(w.Window())
}

func createDesktopHostWindow() uintptr {
	className, _ := windows.UTF16PtrFromString(desktopWindowClassName)
	title, _ := windows.UTF16PtrFromString("Kiro Proxy")
	instance, _, _ := procGetModuleHandleW.Call(0)
	wc := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:   desktopHostWndProc,
		Instance:  instance,
		ClassName: className,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		desktopHostWindowStyle(),
		100,
		100,
		desktopWindowWidth,
		desktopWindowHeight,
		0,
		0,
		instance,
		0,
	)
	if hwnd != 0 {
		applyDesktopWindowDwmAttributes(hwnd)
		procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoZOrder|swpFrameChanged)
	}
	return hwnd
}

func desktopHostWindowStyle() uintptr {
	return wsOverlapped | wsCaption | wsSysMenu | wsMinimizeBox | wsVisible | wsClipSiblings | wsClipChildren
}

func applyDesktopWindowDwmAttributes(hwnd uintptr) {
	cornerPreference := int32(dwmwcpRound)
	procDwmSetWindowAttribute.Call(
		hwnd,
		dwmwaWindowCornerPreference,
		uintptr(unsafe.Pointer(&cornerPreference)),
		unsafe.Sizeof(cornerPreference),
	)

	borderColor := uint32(dwmwaColorDefault)
	procDwmSetWindowAttribute.Call(
		hwnd,
		dwmwaBorderColor,
		uintptr(unsafe.Pointer(&borderColor)),
		unsafe.Sizeof(borderColor),
	)
}

func requestDesktopWindowQuit(c *desktopWindowController) {
	hwnd := c.windowHandle()
	if desktopWindowIsValid(hwnd) {
		procPostMessageW.Call(hwnd, wmUserQuit, 0, 0)
		return
	}
	if c.w != nil {
		c.w.Terminate()
	}
}

func desktopHostWindowProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmNCCalcSize:
		if wparam != 0 {
			return 0
		}
	case wmSize:
		resizeDesktopHostChildren(hwnd)
		return 0
	case wmUserShow:
		procShowWindow.Call(hwnd, swShow)
		procShowWindow.Call(hwnd, swRestore)
		// SetForegroundWindow 在非前台进程调用时会被系统拒绝，此时退回 AttachThreadInput 方案。
		if ret, _, _ := procSetForegroundWindow.Call(hwnd); ret == 0 {
			forceDesktopWindowForeground(hwnd)
		}
		resizeDesktopHostChildren(hwnd)
		return 0
	case wmUserHide:
		procShowWindow.Call(hwnd, swHide)
		return 0
	case wmUserQuit:
		if c := getCurrentDesktopWindow(); c != nil {
			c.quitting.Store(true)
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmClose:
		if c := getCurrentDesktopWindow(); c != nil && c.quitting.Load() {
			procDestroyWindow.Call(hwnd)
			return 0
		}
		procShowWindow.Call(hwnd, swHide)
		return 0
	case wmDestroy:
		if c := getCurrentDesktopWindow(); c != nil && c.windowMatches(hwnd) {
			c.setWindowHandle(0)
			if c.quitting.Load() {
				c.w.Terminate()
			}
		}
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wparam, lparam)
	return ret
}

func resizeDesktopHostChildren(hwnd uintptr) {
	var rc rect
	ret, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	if ret == 0 {
		return
	}
	width := uintptr(rc.Right - rc.Left)
	height := uintptr(rc.Bottom - rc.Top)
	if width == 0 || height == 0 {
		return
	}
	procEnumChildWindows.Call(hwnd, resizeDesktopChildProc, width|(height<<32))
}

func resizeDesktopHostChild(child uintptr, lparam uintptr) uintptr {
	width := int32(lparam & 0xffffffff)
	height := int32((lparam >> 32) & 0xffffffff)
	procMoveWindow.Call(child, 0, 0, uintptr(width), uintptr(height), 1)
	return 1
}

var _ = wsChild

func startDesktopWindowDrag(w webview.WebView) {
	if c := getCurrentDesktopWindow(); c != nil {
		if hwnd := c.windowHandle(); desktopWindowIsValid(hwnd) {
			procReleaseCapture.Call()
			procSendMessageW.Call(hwnd, wmNCLButtonDown, htCaption, 0)
			return
		}
	}
	if hwnd := topLevelWindow(w); desktopWindowIsValid(hwnd) {
		procReleaseCapture.Call()
		procSendMessageW.Call(hwnd, wmNCLButtonDown, htCaption, 0)
	}
}
