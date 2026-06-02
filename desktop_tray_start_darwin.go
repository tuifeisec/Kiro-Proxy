//go:build desktop && darwin

package main

import "github.com/getlantern/systray"

func startDesktopTray(c *desktopTrayController) {
	systray.Register(c.onReady, c.onExit)
}
