package main

import "embed"

// 将 frontend/dist 目录嵌入到二进制文件中
//
//go:embed frontend/dist
var frontendFS embed.FS
