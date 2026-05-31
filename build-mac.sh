#!/usr/bin/env bash
set -euo pipefail

echo "============================================================"
echo "  Kiro Proxy 桌面版打包 (macOS)"
echo "============================================================"
echo

echo "[1/2] 构建前端 (Vite)..."
(
  cd frontend
  npm run build
)
echo

echo "[2/2] 构建 Go 桌面壳二进制 (含嵌入前端资源)..."
go build -tags desktop -ldflags="-s -w" -trimpath -o kiro-proxy .

echo
echo "============================================================"
echo "  构建完成: ./kiro-proxy"
echo "  关闭桌面窗口会同步关闭后端代理进程"
echo "============================================================"
