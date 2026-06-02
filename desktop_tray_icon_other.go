//go:build desktop && !darwin

package main

import (
	_ "embed"

	"github.com/getlantern/systray"
)

//go:embed build/windows/kiro-proxy.ico
var trayIcon []byte

func desktopTrayIcon() []byte {
	return trayIcon
}

func setDesktopTrayIcon() {
	systray.SetIcon(desktopTrayIcon())
}
