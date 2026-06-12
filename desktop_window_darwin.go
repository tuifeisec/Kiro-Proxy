//go:build desktop && darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <Cocoa/Cocoa.h>

@interface KiroProxyWindowDelegate : NSObject <NSWindowDelegate>
@end

@implementation KiroProxyWindowDelegate
- (BOOL)windowShouldClose:(id)sender {
	[(NSWindow *)sender orderOut:nil];
	return NO;
}
@end

static KiroProxyWindowDelegate *kiroProxyWindowDelegate;

static NSWindow *kiroProxyWindow(void *window) {
	if (window == nil) {
		return nil;
	}
	return (__bridge NSWindow *)window;
}

static void kiroProxyInstallWindowDelegate(void *window) {
	NSWindow *w = kiroProxyWindow(window);
	if (w == nil) {
		return;
	}
	if (kiroProxyWindowDelegate == nil) {
		kiroProxyWindowDelegate = [KiroProxyWindowDelegate new];
	}
	[w setDelegate:kiroProxyWindowDelegate];
}

static void kiroProxyHideWindow(void *window) {
	NSWindow *w = kiroProxyWindow(window);
	if (w != nil) {
		[w orderOut:nil];
	}
}

static void kiroProxyShowWindow(void *window) {
	NSWindow *w = kiroProxyWindow(window);
	if (w != nil) {
		[w makeKeyAndOrderFront:nil];
		[NSApp activateIgnoringOtherApps:YES];
	}
}
*/
import "C"

import webview "github.com/webview/webview_go"

func newDesktopWebView(debug bool, c *desktopWindowController) webview.WebView {
	return webview.New(debug)
}

func configureDesktopWindow(w webview.WebView) {
	C.kiroProxyInstallWindowDelegate(w.Window())
}

func requestDesktopWindowQuit(c *desktopWindowController) {
	if c.w != nil {
		c.w.Dispatch(func() {
			c.w.Terminate()
		})
	}
}

func destroyDesktopHostWindow(c *desktopWindowController) {}

func hideDesktopWindow(c *desktopWindowController) {
	if c.w != nil {
		c.w.Dispatch(func() {
			C.kiroProxyHideWindow(c.w.Window())
		})
	}
}

func hideDesktopWindowController(w webview.WebView) {
	w.Dispatch(func() {
		C.kiroProxyHideWindow(w.Window())
	})
}

func startDesktopWindowDrag(w webview.WebView) {}

func showDesktopWindow(c *desktopWindowController) {
	if c.w != nil {
		c.w.Dispatch(func() {
			C.kiroProxyShowWindow(c.w.Window())
		})
	}
}
