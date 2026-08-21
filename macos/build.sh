#!/bin/bash
# 编译并打包为 macOS 桌面 App（Objective-C UI + C++ 内核，CMake 统管）
# 用法:
#   ./build.sh          显示本帮助（不构建）
#   ./build.sh clean    清理构建缓存与已构建的 App（删除 .build）
#   ./build.sh build    先 clean，再构建 App
#   ./build.sh run      先 clean，再构建 App，随后运行
#   ./build.sh help     显示本帮助

set -e

cd "$(dirname "$0")"
ROOT="$(cd .. && pwd)"
APP_NAME="Translate"
BUILD_DIR=".build"
BUNDLE="$BUILD_DIR/macos/$APP_NAME.app"

print_help() {
  cat <<'EOF'
用法:
  ./build.sh           显示本帮助（不构建）
  ./build.sh clean     清理构建缓存与已构建的 App（删除 .build）
  ./build.sh build     先 clean，再构建 App
  ./build.sh run       先 clean，再构建 App，随后运行
  ./build.sh help      显示本帮助
EOF
}

clean() {
  echo "==> clean: 删除 $BUILD_DIR"
  rm -rf "$BUILD_DIR"
}

build() {
  clean
  echo "==> configure (cmake -S $ROOT -B $BUILD_DIR)"
  cmake -S "$ROOT" -B "$BUILD_DIR" -DCMAKE_BUILD_TYPE=Release
  echo "==> build ($APP_NAME)"
  cmake --build "$BUILD_DIR" --target "$APP_NAME"
}

run_app() {
  echo "==> run: open '$BUNDLE'"
  open "$BUNDLE"
}

case "${1:-}" in
  ""|help|-h|--help)
    print_help
    ;;
  clean)
    clean
    ;;
  build)
    build
    ;;
  run)
    build
    run_app
    ;;
  *)
    echo "未知参数: $1"
    print_help
    exit 1
    ;;
esac
