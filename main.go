//go:build !desktop

package main

import (
	"flag"
	"log"
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
	flag.Parse()

	runner, err := startApp(*configFile, *port, *dbPath)
	if err != nil {
		log.Fatalf("启动失败: %v", err)
	}
	defer runner.Close()

	waitForSignal()
}
