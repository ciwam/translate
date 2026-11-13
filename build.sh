#!/bin/bash
# 构建脚本：测试 + 打包 Translate.app / DMG
set -euo pipefail

MYGO="go tool mygo"
APP="build/darwin-arm64/Translate.app"

usage() {
    cat <<EOF
用法: ./build.sh [命令]

命令:
  dmg     清理缓存 + 测试 + 打包 Translate.app 与 DMG
  run     清理缓存 + 测试 + 打包并启动 Translate.app
  test    清理缓存 + 运行测试
  clean   清理构建产物（build/ 与 .mygo/）与 Go 缓存
  help    显示本帮助
EOF
}

run_tests() {
    echo "==> 运行测试"
    go clean -cache -testcache
    go test ./...
}

cmd="${1:-help}"

case "$cmd" in
    test)
        run_tests
        ;;
    clean)
        rm -rf build .mygo
        go clean -cache -testcache
        echo "已清理 build/、.mygo/ 与 Go 缓存"
        ;;
    dmg)
        run_tests
        echo "==> 打包 Translate.app 与 DMG"
        $MYGO build
        # DMG is the distributable; the loose .app is redundant — remove it.
        rm -rf "$APP"
        echo "产物:"
        ls -lh build/darwin-arm64/ | awk '{print "  " $NF " (" $5 ")"}' | tail -1
        ;;
    run)
        run_tests
        echo "==> 打包并启动"
        $MYGO build
        open "$APP"
        ;;
    help|-h|--help)
        usage
        ;;
    *)
        echo "未知命令: $1" >&2
        usage >&2
        exit 1
        ;;
esac
