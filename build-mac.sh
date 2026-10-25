#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
cd "$SCRIPT_DIR"
if [[ "$(uname -s)" != Darwin ]]; then
  echo '[错误] macOS 桌面版必须在 macOS 上使用原生工具链构建。' >&2
  exit 1
fi
APP_VERSION=${1:-}
if [[ -z "$APP_VERSION" ]]; then
  read -r -p '请输入版本号（例如 1.0.0）：' APP_VERSION
fi
if [[ ! "$APP_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo '[错误] 版本号必须是三段数字，例如 1.0.0。' >&2
  exit 1
fi
for tool in go npm codesign plutil ditto shasum xcrun lipo git; do
  command -v "$tool" >/dev/null
 done
APP_ARCH=$(go env GOARCH)
case "$(uname -m):$APP_ARCH" in
  arm64:arm64|x86_64:amd64) ;;
  *) echo '[错误] GOARCH 必须与当前 Mac 架构一致。' >&2; exit 1 ;;
esac
export CGO_ENABLED=1
export GOOS=darwin
export MACOSX_DEPLOYMENT_TARGET=${MACOSX_DEPLOYMENT_TARGET:-12.0}
if [[ "$MACOSX_DEPLOYMENT_TARGET" != '12.0' ]]; then
  echo '[错误] 当前发行契约要求 MACOSX_DEPLOYMENT_TARGET=12.0。' >&2
  exit 1
fi
APP_NAME='Kiro Proxy'
BUILD_DIR="$SCRIPT_DIR/build/mac/$APP_VERSION-$APP_ARCH"
APP_DIR="$BUILD_DIR/$APP_NAME.app"
MACOS_DIR="$APP_DIR/Contents/MacOS"
RESOURCES_DIR="$APP_DIR/Contents/Resources"
RELEASE_DIR="$SCRIPT_DIR/release"
ARCHIVE_NAME="kiro-proxy-v$APP_VERSION-darwin-$APP_ARCH-unsigned.zip"
DIST_ZIP="$RELEASE_DIR/$ARCHIVE_NAME"
if [[ -e "$BUILD_DIR" || -e "$DIST_ZIP" ]]; then
  echo '[错误] 该版本构建产物已存在，请先备份旧产物。' >&2
  exit 1
fi
mkdir -p "$MACOS_DIR" "$RESOURCES_DIR" "$RELEASE_DIR"
npm --prefix frontend ci
npm --prefix frontend run build
go build -tags desktop -ldflags="-s -w -X kiro-proxy/buildinfo.AppVersion=$APP_VERSION" -trimpath -o "$MACOS_DIR/kiro-proxy" .
chmod +x "$MACOS_DIR/kiro-proxy"
cat > "$APP_DIR/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "https://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key><string>kiro-proxy</string>
  <key>CFBundleIdentifier</key><string>local.kiro-proxy.desktop</string>
  <key>CFBundleName</key><string>Kiro Proxy</string>
  <key>CFBundleDisplayName</key><string>Kiro Proxy</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleVersion</key><string>$APP_VERSION</string>
  <key>CFBundleShortVersionString</key><string>$APP_VERSION</string>
  <key>LSMinimumSystemVersion</key><string>12.0</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST
plutil -lint "$APP_DIR/Contents/Info.plist"
MACH_ARCH=x86_64
if [[ "$APP_ARCH" == arm64 ]]; then MACH_ARCH=arm64; fi
lipo -verify_arch "$MACH_ARCH" "$MACOS_DIR/kiro-proxy"
BUILD_VERSION_OUTPUT=$(xcrun vtool -show-build "$MACOS_DIR/kiro-proxy")
printf '%s\n' "$BUILD_VERSION_OUTPUT"
MIN_OS=$(printf '%s\n' "$BUILD_VERSION_OUTPUT" | awk '$1 == "minos" { print $2 }')
if [[ "$MIN_OS" != '12.0' && "$MIN_OS" != '12.0.0' ]]; then
  echo "[错误] Mach-O 最低系统版本为 $MIN_OS，与声明的 macOS 12.0 不一致。" >&2
  exit 1
fi
REVISION=$(git rev-parse HEAD)
cat > "$RESOURCES_DIR/README.txt" <<TEXT
Kiro Proxy $APP_VERSION - macOS $APP_ARCH (macOS 12.0+)
本版本不需要机器授权或激活。解压后将 Kiro Proxy.app 放入 Applications。
本发行包仅作 ad-hoc 签名，未获得 Developer ID 签名或 Apple 公证。
Gatekeeper 可能阻止首次打开；确认来源后，可在系统设置“隐私与安全性”中允许打开。
窗口关闭后仍在菜单栏运行，使用菜单栏菜单退出。
源码提交：$REVISION
TEXT
# ad-hoc 签名仅保证包内代码一致性，不是 Developer ID 签名，也不代表 Apple 公证。
codesign --force --sign - "$APP_DIR"
codesign --verify --deep --strict --verbose=2 "$APP_DIR"
ditto -c -k --sequesterRsrc --keepParent "$APP_DIR" "$DIST_ZIP"
EXEC_SHA256=$(shasum -a 256 "$MACOS_DIR/kiro-proxy" | awk '{print $1}')
ZIP_SHA256=$(shasum -a 256 "$DIST_ZIP" | awk '{print $1}')
printf '%s  %s\n' "$ZIP_SHA256" "$ARCHIVE_NAME" > "$DIST_ZIP.sha256"
cat > "$RELEASE_DIR/darwin-$APP_ARCH-manifest.json" <<JSON
{"version":"$APP_VERSION","revision":"$REVISION","platform":"darwin","arch":"$APP_ARCH","binary_sha256":"$EXEC_SHA256","archive":"$ARCHIVE_NAME","archive_sha256":"$ZIP_SHA256","developer_signed":false,"notarized":false,"ad_hoc_signed":true}
JSON
printf '构建完成：%s\nSHA256：%s\n' "$DIST_ZIP" "$ZIP_SHA256"