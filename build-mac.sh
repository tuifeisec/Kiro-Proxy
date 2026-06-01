#!/usr/bin/env bash
set -euo pipefail

APP_NAME="Kiro Proxy"
BIN_NAME="kiro-proxy"
BUILD_DIR="build/mac"
APP_DIR="$BUILD_DIR/$APP_NAME.app"
CONTENTS_DIR="$APP_DIR/Contents"
MACOS_DIR="$CONTENTS_DIR/MacOS"
RESOURCES_DIR="$CONTENTS_DIR/Resources"
DIST_ZIP="build/kiro-proxy-mac.zip"

echo "============================================================"
echo "  Kiro Proxy 桌面版打包 (macOS)"
echo "============================================================"
echo

echo "[1/4] 构建前端 (Vite)..."
(
  cd frontend
  npm run build
)
echo

echo "[2/4] 构建 Go 桌面壳二进制 (含嵌入前端资源)..."
rm -rf "$APP_DIR" "$DIST_ZIP"
mkdir -p "$MACOS_DIR" "$RESOURCES_DIR"
go build -tags desktop -ldflags="-s -w" -trimpath -o "$MACOS_DIR/$BIN_NAME" .
chmod +x "$MACOS_DIR/$BIN_NAME"
echo

echo "[3/4] 生成 macOS .app 结构..."
cat > "$CONTENTS_DIR/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "https://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key>
  <string>kiro-proxy</string>
  <key>CFBundleIdentifier</key>
  <string>local.kiro-proxy.desktop</string>
  <key>CFBundleName</key>
  <string>Kiro Proxy</string>
  <key>CFBundleDisplayName</key>
  <string>Kiro Proxy</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleVersion</key>
  <string>1.0.0</string>
  <key>CFBundleShortVersionString</key>
  <string>1.0.0</string>
  <key>LSMinimumSystemVersion</key>
  <string>11.0</string>
  <key>NSHighResolutionCapable</key>
  <true/>
</dict>
</plist>
PLIST
echo

echo "[4/4] 生成分发压缩包..."
(
  cd "$BUILD_DIR"
  zip -qry "../kiro-proxy-mac.zip" "$APP_NAME.app"
)

echo
echo "============================================================"
echo "  构建完成: $APP_DIR"
echo "  分发包: $DIST_ZIP"
echo "  关闭桌面窗口会同步关闭后端代理进程"
echo "============================================================"
