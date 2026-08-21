#!/bin/bash
# 编译并打包为 macOS 桌面 App（窗口式，进 Dock，纯 AppKit，零第三方依赖）
# 用法:
#   ./build.sh          仅构建 App
#   ./build.sh clean    清理构建缓存与已构建的 App（删除 .build）
#   ./build.sh build    先 clean，再构建 App
#   ./build.sh run      先 clean，再构建 App，随后运行
#   ./build.sh help     显示帮助

set -e

cd "$(dirname "$0")"

APP_NAME="Translate"
BUNDLE_ID="com.ciwang.translate"
BUILD_ROOT=".build"
APP_BUNDLE="$BUILD_ROOT/$APP_NAME.app"

print_help() {
  cat <<'EOF'
用法:
  ./build.sh           显示本帮助（不构建）
  ./build.sh clean     清理构建缓存与已构建的 App（删除 .build）
  ./build.sh build     先 clean，再构建 App
  ./build.sh run       先 clean，再构建 App，随后运行
  ./build.sh help      显示本帮助

环境变量:
  无
EOF
}

clean() {
  echo "==> clean: 删除 $BUILD_ROOT"
  rm -rf "$BUILD_ROOT"
}

build() {
  mkdir -p "$APP_BUNDLE/Contents/MacOS" "$APP_BUNDLE/Contents/Resources"
  echo "==> 编译 (swiftc -O) ..."
  swiftc -O \
    -framework Cocoa \
    Sources/Translate/main.swift \
    Sources/Translate/Translator.swift \
    -o "$APP_BUNDLE/Contents/MacOS/$APP_NAME"

  cat > "$APP_BUNDLE/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key><string>$APP_NAME</string>
    <key>CFBundleDisplayName</key><string>翻译</string>
    <key>CFBundleIdentifier</key><string>$BUNDLE_ID</string>
    <key>CFBundleVersion</key><string>1</string>
    <key>CFBundleShortVersionString</key><string>1.0</string>
    <key>CFBundleExecutable</key><string>$APP_NAME</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>LSMinimumSystemVersion</key><string>13.0</string>
    <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
EOF
  echo "==> 完成: $APP_BUNDLE"
}

run_app() {
  echo "==> run: open '$APP_BUNDLE'"
  open "$APP_BUNDLE"
}

case "${1:-}" in
  ""|help|-h|--help)
    print_help
    ;;
  clean)
    clean
    ;;
  build)
    clean
    build
    ;;
  run)
    clean
    build
    run_app
    ;;
  help|-h|--help)
    print_help
    ;;
  *)
    echo "未知参数: $1"
    print_help
    exit 1
    ;;
esac
