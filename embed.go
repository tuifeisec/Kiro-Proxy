package main

import "embed"

// 将 frontend/dist 目录嵌入到二进制文件中
// 必须用 all: 前缀：Vite 构建产物包含 _ 开头的共享 chunk（如 _plugin-vue_export-helper-*.js），
// 不带 all: 的 go:embed 会排除 _ 和 . 开头的文件，导致前端模块图 404 白屏
//
//go:embed all:frontend/dist
var frontendFS embed.FS
