//go:build desktop

package main

import (
	"runtime"
	"sync"
	"sync/atomic"

	webview "github.com/webview/webview_go"
)

const (
	desktopWindowWidth  = 940
	desktopWindowHeight = 640
)

type desktopWindowController struct {
	w        webview.WebView
	url      string
	window   uintptr
	quitting atomic.Bool
	quitOnce sync.Once
}

func newDesktopWindowController(url string) *desktopWindowController {
	return &desktopWindowController{url: url}
}

func (c *desktopWindowController) create() {
	w := newDesktopWebView(false, c)
	c.w = w
	w.SetTitle("Kiro Proxy")
	setDesktopWindowIcon(w)
	w.SetSize(desktopWindowWidth, desktopWindowHeight, webview.HintFixed)
	bindDesktopWindowControls(w)
	w.Navigate(c.url)
	configureDesktopWindow(w)
}

func (c *desktopWindowController) run() {
	// webview/cocoa 要求 GUI 事件循环在主线程；systray 在后台 goroutine 中运行。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	c.w.Run()
}

func (c *desktopWindowController) destroy() {
	if c.w != nil {
		c.w.Destroy()
	}
	destroyDesktopHostWindow(c)
}

func (c *desktopWindowController) show() {
	if c.w == nil {
		return
	}
	showDesktopWindow(c)
}

func (c *desktopWindowController) hide() {
	if c.w == nil {
		return
	}
	hideDesktopWindow(c)
}

func (c *desktopWindowController) quit() {
	if c.w == nil {
		return
	}
	c.quitOnce.Do(func() {
		c.quitting.Store(true)
		requestDesktopWindowQuit(c)
	})
}

func bindDesktopWindowControls(w webview.WebView) {
	_ = w.Bind("desktopWindowStartDrag", func() error {
		startDesktopWindowDrag(w)
		return nil
	})
	_ = w.Bind("desktopWindowMinimize", func() error {
		hideDesktopWindowController(w)
		return nil
	})
	_ = w.Bind("desktopWindowClose", func() error {
		hideDesktopWindowController(w)
		return nil
	})
}
