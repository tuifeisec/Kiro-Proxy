//go:build desktop && darwin

package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"github.com/getlantern/systray"
)

var macTrayIcon []byte

func desktopTrayIcon() []byte {
	if macTrayIcon != nil {
		return macTrayIcon
	}

	img := image.NewRGBA(image.Rect(0, 0, 18, 18))
	ink := color.RGBA{A: 255}

	// Monochrome template-style K icon for the macOS menu bar.
	for y := 3; y <= 15; y++ {
		setPixelBlock(img, 4, y, ink)
	}
	for i := 0; i <= 6; i++ {
		setPixelBlock(img, 5+i, 9-i, ink)
		setPixelBlock(img, 5+i, 9+i, ink)
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	macTrayIcon = buf.Bytes()
	return macTrayIcon
}

func setPixelBlock(img *image.RGBA, x, y int, c color.RGBA) {
	img.SetRGBA(x, y, c)
	img.SetRGBA(x+1, y, c)
}

func setDesktopTrayIcon() {
	icon := desktopTrayIcon()
	systray.SetTemplateIcon(icon, icon)
}
