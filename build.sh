#!/bin/bash
# 构建脚本：测试 + 打包 Translate.app / DMG
#
# 用法:
#   ./build.sh            # 测试 + 打包 .app 和 DMG
#   ./build.sh test       # 只跑测试
#   ./build.sh app        # 测试 + 打包 .app（跳过 DMG）
#   ./build.sh dmg        # 测试 + 打包 .app 和 DMG
#   ./build.sh run        # 测试 + 打包并启动 .app
#   ./build.sh dev        # 开发模式（热重载，Ctrl+C 退出）
#   ./build.sh clean      # 清理构建产物
set -euo pipefail

MYGO="go tool mygo"
APP="build/darwin-arm64/Translate.app"

run_tests() {
    echo "==> 运行测试"
    go test ./...
}

case "${1:-dmg}" in
    test)
        run_tests
        ;;
    dev)
        $MYGO dev
        ;;
    clean)
        rm -rf build .mygo
        echo "已清理 build/ 与 .mygo/"
        ;;
    app)
        run_tests
        echo "==> 打包 Translate.app"
        $MYGO build
        echo "产物: $APP"
        ;;
    dmg|*)
        run_tests
        echo "==> 打包 Translate.app 与 DMG"
        $MYGO build
        echo "产物:"
        ls -lh build/darwin-arm64/ | awk '{print "  " $NF " (" $5 ")"}' | tail -2
        ;;
    run)
        run_tests
        echo "==> 打包并启动"
        $MYGO build
        open "$APP"
        ;;
esac
