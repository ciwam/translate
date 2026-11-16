#!/bin/bash
# 构建脚本：测试 + 打包 Translate.app / DMG
#
# 用法示例:
#   ./build.sh dmg --cases                        # 打包前先跑全部翻译用例
#   ./build.sh dmg --cases-args "-case paren1,log1"    # 只跑指定用例
#   ./build.sh dmg --cases-args "-provider siliconflow" # 指定模型
#   ./build.sh run --cases                        # 打包+启动前跑用例
#   ./build.sh cases                              # 只跑用例集，不打包
set -euo pipefail

MYGO="go tool mygo"
APP="build/darwin-arm64/Translate.app"
CASES_BIN="build/translate-cases"

usage() {
    cat <<EOF
用法: ./build.sh [命令] [--cases [--cases-args "..."]]

命令:
  dmg     清理缓存 + 测试 + 打包 Translate.app 与 DMG
  run     清理缓存 + 测试 + 打包并启动 Translate.app
  test    清理缓存 + 运行测试
  cases   构建 translate-cases 并跑翻译质量用例集（需 API Key 环境变量）
  clean   清理构建产物（build/ 与 .mygo/）与 Go 缓存
  help    显示本帮助

选项（须跟在命令后）:
  --cases                          打包前先跑翻译用例集（全部）
  --cases-args "<参数>"            传给 translate-cases 的参数
                                   （如 "-case paren1,log1 -provider hunyuan-mt"）

translate-cases 与桌面应用走同一条管线代码（internal/pipeline），
测试效果与打包出的桌面应用一致。Key 经环境变量
TRANSLATE_API_KEY_<PROVIDER>（如 TRANSLATE_API_KEY_HUNYUAN_MT）
或 TRANSLATE_API_KEY 传递。
EOF
}

run_tests() {
    echo "==> 运行测试"
    go clean -cache -testcache
    go test ./...
}

# build_cases compiles the quality-cases CLI from the same pipeline code as
# the desktop app.
build_cases() {
    echo "==> 构建 translate-cases"
    go build -o "$CASES_BIN" ./cmd/translate-cases
}

# run_cases 跑翻译质量用例集。需要 API Key 环境变量；失败即中止构建。
run_cases() {
    local extra="${1:-}"
    echo "==> 翻译质量用例集"
    if [ -z "${TRANSLATE_API_KEY:-}" ] && ! env | grep -q "^TRANSLATE_API_KEY_"; then
        echo "错误：缺少 TRANSLATE_API_KEY / TRANSLATE_API_KEY_* 环境变量，无法跑用例集" >&2
        echo "      导出示例: export TRANSLATE_API_KEY_HUNYUAN_MT=<key>" >&2
        exit 1
    fi
    # shellcheck disable=SC2086
    "$CASES_BIN" $extra
    echo "==> 用例集通过"
}

cmd="${1:-help}"
shift || true

# Parse options after the command.
run_cases_flag=0
cases_args=""
while [ $# -gt 0 ]; do
    case "$1" in
        --cases)
            run_cases_flag=1
            ;;
        --cases-args)
            shift
            cases_args="${1:-}"
            run_cases_flag=1
            ;;
        *)
            echo "未知选项: $1" >&2
            usage >&2
            exit 1
            ;;
    esac
    shift
done

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
        [ "$run_cases_flag" = 1 ] && { build_cases; run_cases "$cases_args"; }
        echo "==> 打包 Translate.app 与 DMG"
        $MYGO build
        # DMG is the distributable; the loose .app is redundant — remove it.
        rm -rf "$APP"
        echo "产物:"
        ls -lh build/darwin-arm64/ | awk '{print "  " $NF " (" $5 ")"}' | tail -1
        ;;
    run)
        run_tests
        [ "$run_cases_flag" = 1 ] && { build_cases; run_cases "$cases_args"; }
        echo "==> 打包并启动"
        $MYGO build
        open "$APP"
        ;;
    cases)
        build_cases
        run_cases "$cases_args"
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
