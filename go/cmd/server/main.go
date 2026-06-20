package main

import (
	"flag"
	"log"
	"net/http"

	licenseapp "kiro-proxy/go/internal/app"
	"kiro-proxy/go/internal/storage"
	"kiro-proxy/go/internal/transport"
	"kiro-proxy/go/web"
)

func main() {
	addr := flag.String("addr", ":20543", "后台监听地址")
	data := flag.String("data", "go/data/license-admin.json", "数据文件路径")
	flag.Parse()

	store, err := storage.NewJSONStore(*data)
	if err != nil {
		log.Fatalf("初始化存储失败: %v", err)
	}

	service := licenseapp.NewService(store)
	server := transport.NewServer(service, web.AdminFS)

	log.Printf("授权后台已启动: http://127.0.0.1%s/sjksjk", *addr)
	if err := http.ListenAndServe(*addr, server.Handler()); err != nil {
		log.Fatal(err)
	}
}
