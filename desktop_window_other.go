//go:build desktop && !windows && !darwin

package main

import webview "github.com/webview/webview_go"

func newDesktopWebView(debug bool, c *desktopWindowController) webview.WebView {
	return webview.New(debug)
}

func configureDesktopWindow(w webview.WebView) {}

func requestDesktopWindowQuit(c *desktopWindowController) {
	if c.w != nil {
		c.w.Terminate()
	}
}

func destroyDesktopHostWindow(c *desktopWindowController) {}

func hideDesktopWindow(c *desktopWindowController) {
	// webview_go 没有跨平台隐藏 API。非 Windows 平台保留原生窗口样式。
}

func hideDesktopWindowController(w webview.WebView) {}

func startDesktopWindowDrag(w webview.WebView) {}

func showDesktopWindow(c *desktopWindowController) {}
