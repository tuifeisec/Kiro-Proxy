//go:build desktop

package main

import (
	"sync"

	"github.com/getlantern/systray"
)

type desktopTrayController struct {
	window   *desktopWindowController
	done     chan struct{}
	doneOnce sync.Once
}

func newDesktopTrayController(window *desktopWindowController) *desktopTrayController {
	return &desktopTrayController{
		window: window,
		done:   make(chan struct{}),
	}
}

func (c *desktopTrayController) run() {
	systray.Run(c.onReady, c.onExit)
}

func (c *desktopTrayController) quit() {
	systray.Quit()
	c.closeDone()
}

func (c *desktopTrayController) showWindow() {
	c.window.show()
}

func (c *desktopTrayController) wait() {
	<-c.done
}

func (c *desktopTrayController) requestExit() {
	c.window.quit()
	c.quit()
}

func (c *desktopTrayController) onReady() {
	setDesktopTrayIcon()
	systray.SetTitle(desktopTrayTitle)
	systray.SetTooltip(desktopTrayTooltip)

	mShow := systray.AddMenuItem("打开窗口", "打开 Kiro Proxy")
	mShow.SetIcon(desktopTrayIcon())
	mHide := systray.AddMenuItem("隐藏窗口", "隐藏窗口并保持代理运行")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "关闭窗口并停止代理")

	go func() {
		for {
			select {
			case <-mShow.ClickedCh:
				c.showWindow()
			case <-mHide.ClickedCh:
				c.window.hide()
			case <-mQuit.ClickedCh:
				c.requestExit()
				return
			}
		}
	}()
}

func (c *desktopTrayController) onExit() {
	c.closeDone()
}

func (c *desktopTrayController) closeDone() {
	c.doneOnce.Do(func() {
		close(c.done)
	})
}
