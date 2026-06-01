//go:build desktop

package main

import (
	"flag"
	"log"
)

func main() {
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
	window := newDesktopWindowController(url)
	window.create()
	defer window.destroy()

	tray := newDesktopTrayController(window)
	go tray.run()
	defer tray.quit()

	window.run()
	tray.quit()
	tray.wait()
}
