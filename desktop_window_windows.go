//go:build desktop && windows

package main

import (
	"log"
	"sync"
	"time"
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
	procCreateWindowExW       = user32.NewProc("CreateWindowExW")
	procDestroyWindow         = user32.NewProc("DestroyWindow")
	procDefWindowProcW        = user32.NewProc("DefWindowProcW")
	procFindWindowExW         = user32.NewProc("FindWindowExW")
	procGetClientRect         = user32.NewProc("GetClientRect")
	procGetWindowRect         = user32.NewProc("GetWindowRect")
	procMoveWindow            = user32.NewProc("MoveWindow")
	procMapWindowPoints       = user32.NewProc("MapWindowPoints")
	procRegisterClassExW      = user32.NewProc("RegisterClassExW")
	procSetWindowPos          = user32.NewProc("SetWindowPos")
	procShowWindow            = user32.NewProc("ShowWindow")
	procSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	procReleaseCapture        = user32.NewProc("ReleaseCapture")
	procPostMessageW          = user32.NewProc("PostMessageW")
	procIsWindow              = user32.NewProc("IsWindow")
	procGetForegroundWindow   = user32.NewProc("GetForegroundWindow")
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	dwmapi                    = windows.NewLazySystemDLL("dwmapi.dll")
	procGetModuleHandleW      = kernel32.NewProc("GetModuleHandleW")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

var desktopHostWndProc = windows.NewCallback(desktopHostWindowProc)

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
	if !desktopWindowIsValid(hwnd) {
		log.Printf("[desktop] 无法打开窗口: 宿主窗口无效 hwnd=%#x", hwnd)
		return
	}
	if ret, _, err := procPostMessageW.Call(hwnd, wmUserShow, 0, 0); ret == 0 {
		log.Printf("[desktop] 无法投递打开窗口消息: hwnd=%#x error=%v", hwnd, err)
		return
	}
	// 托盘运行在独立线程；延迟检查不会阻塞 GUI 消息循环。
	go func() {
		time.Sleep(1200 * time.Millisecond)
		valid := desktopWindowIsValid(hwnd)
		visible, _, _ := procSingleInstanceIsWindowVisible.Call(hwnd)
		foreground, _, _ := procSingleInstanceGetForegroundWindow.Call()
		if valid && visible != 0 {
			return
		}
		probe := "未探测（窗口已失效）"
		if valid {
			probe = probeDesktopWindowMessageLoop(hwnd)
		}
		log.Printf("[desktop] 托盘打开窗口未显示: hwnd=%#x valid=%t visible=%t foreground=%#x WM_NULL=%s", hwnd, valid, visible != 0, foreground, probe)
	}()
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
		if ret, _, err := procSetForegroundWindow.Call(hwnd); ret == 0 {
			foreground, _, _ := procGetForegroundWindow.Call()
			log.Printf("[desktop] 请求窗口前台失败: hwnd=%#x foreground=%#x error=%v", hwnd, foreground, err)
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
	// WebView2 会在 webview_widget 下创建跨进程子窗口；这里只调整直属容器，
	// 让 WebView2 自己维护内部窗口尺寸，避免直接同步移动跨进程后代窗口。
	className, _ := windows.UTF16PtrFromString("webview_widget")
	child, _, _ := procFindWindowExW.Call(hwnd, 0, uintptr(unsafe.Pointer(className)), 0)
	if child == 0 {
		return
	}

	var client, bounds rect
	if ret, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client))); ret == 0 {
		return
	}
	width, height := client.Right-client.Left, client.Bottom-client.Top
	if width <= 0 || height <= 0 {
		return
	}
	if ret, _, _ := procGetWindowRect.Call(child, uintptr(unsafe.Pointer(&bounds))); ret == 0 {
		return
	}
	// 返回值为 0 也可能表示两个窗口原点重合，不能据此判为失败。
	procMapWindowPoints.Call(0, hwnd, uintptr(unsafe.Pointer(&bounds)), 2)
	if bounds.Left == client.Left && bounds.Top == client.Top &&
		bounds.Right-bounds.Left == width && bounds.Bottom-bounds.Top == height {
		return
	}
	procMoveWindow.Call(child, 0, 0, uintptr(width), uintptr(height), 1)
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
