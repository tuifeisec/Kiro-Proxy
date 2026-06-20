//go:build desktop

package main

import (
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	releaseSingleInstance, ok, err := acquireSingleInstanceLock()
	if err != nil {
		log.Fatalf("启动失败: %v", err)
	}
	if !ok {
		notifySingleInstanceRunning()
		return
	}
	defer releaseSingleInstance()

	configFile := flag.String("config", "config.yaml", "配置文件路径")
	port := flag.Int("port", 0, "代理端口 (覆盖配置文件)")
	dbPath := flag.String("db", "kiro-proxy.db", "数据库文件路径")
	noWindow := flag.Bool("no-window", false, "不启动桌面窗口，仅运行后端服务")
	flag.Parse()

	if err := useDesktopDataDir(); err != nil {
		log.Fatalf("启动失败: %v", err)
	}

	runner, err := startApp(*configFile, *port, *dbPath)
	if err != nil {
		log.Fatalf("启动失败: %v", err)
	}
	defer runner.Close()

	if *noWindow {
		waitForSignal()
		return
	}

	runDesktopWindow(runner.URL())
}

func runDesktopWindow(url string) {
	waitForDesktopWebUI(url)

	window := newDesktopWindowController(url)
	tray := newDesktopTrayController(window)
	startDesktopTray(tray)
	defer tray.quit()

	window.create()
	defer window.destroy()

	window.run()
	tray.quit()
	tray.wait()
}

func waitForDesktopWebUI(url string) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < http.StatusInternalServerError {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}
