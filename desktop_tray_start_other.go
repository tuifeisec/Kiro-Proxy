//go:build desktop && !darwin

package main

func startDesktopTray(c *desktopTrayController) {
	go c.run()
}
