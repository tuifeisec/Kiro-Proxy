//go:build desktop

package main

import _ "embed"

//go:embed build/windows/kiro-proxy.ico
var trayIcon []byte

func desktopTrayIcon() []byte {
	return trayIcon
}
