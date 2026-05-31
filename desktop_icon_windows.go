//go:build desktop && windows

package main

import (
	"unsafe"

	webview "github.com/webview/webview_go"
	"golang.org/x/sys/windows"
)

const (
	appIconID = 1

	imageIcon    = 1
	loadResource = 0x00000040
	iconSmall    = 0
	iconBig      = 1
	wmSetIcon    = 0x0080
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procLoadImageW       = user32.NewProc("LoadImageW")
	procSendMessageW     = user32.NewProc("SendMessageW")
	procSetClassLongPtr  = user32.NewProc("SetClassLongPtrW")
	procGetWindowLongPtr = user32.NewProc("GetWindowLongPtrW")
)

func setDesktopWindowIcon(w webview.WebView) {
	hwnd := uintptr(w.Window())
	if hwnd == 0 {
		return
	}

	// WebView2 can return a child HWND; title bar/taskbar icon belongs to the top-level parent.
	for {
		parent, _, _ := procGetWindowLongPtr.Call(hwnd, ^uintptr(7))
		if parent == 0 {
			break
		}
		hwnd = parent
	}

	bigIcon := loadAppIcon(32)
	smallIcon := loadAppIcon(16)
	if bigIcon != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconBig, bigIcon)
		procSetClassLongPtr.Call(hwnd, ^uintptr(13), bigIcon) // GCLP_HICON
	}
	if smallIcon != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconSmall, smallIcon)
		procSetClassLongPtr.Call(hwnd, ^uintptr(33), smallIcon) // GCLP_HICONSM
	}
}

func loadAppIcon(size int) uintptr {
	h, _, _ := procLoadImageW.Call(
		0,
		uintptr(unsafe.Pointer(uintptr(appIconID))),
		imageIcon,
		uintptr(size),
		uintptr(size),
		loadResource,
	)
	return h
}
