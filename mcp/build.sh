#!/bin/bash
# 构建调试 CLI 并安装 MCP 调试通道的两个部件：
#   1. translate-cli   （C++，链接 translate_core）→ ~/.workbuddy/bin/translate-cli
#   2. translate_mcp.py（Python MCP stdio 服务端）  → 仓库内 mcp/，无需安装
# 用法:
#   mcp/build.sh        配置 + 构建 + 安装 translate-cli
#   mcp/build.sh clean  删除构建目录与已安装的 translate-cli
set -e

cd "$(dirname "$0")"
ROOT="$(cd .. && pwd)"
BUILD_DIR="$ROOT/.build-cli"
INSTALL_DIR="$HOME/.workbuddy/bin"

# cmake / brew 不在默认 PATH（Homebrew on Apple Silicon）
export PATH="/opt/homebrew/bin:$PATH"

clean() {
  echo "==> clean: 删除 $BUILD_DIR 与 $INSTALL_DIR/translate-cli"
  rm -rf "$BUILD_DIR"
  rm -f "$INSTALL_DIR/translate-cli"
}

install_cli() {
  echo "==> configure (cmake -S $ROOT -B $BUILD_DIR)"
  cmake -S "$ROOT" -B "$BUILD_DIR" -DCMAKE_BUILD_TYPE=Release
  echo "==> build translate_cli"
  cmake --build "$BUILD_DIR" --target translate_cli -j
  mkdir -p "$INSTALL_DIR"
  cp "$BUILD_DIR/core/translate_cli" "$INSTALL_DIR/translate-cli"
  echo "==> installed: $INSTALL_DIR/translate-cli"
}

case "${1:-}" in
  ""|build) install_cli ;;
  clean)    clean ;;
  *) echo "未知参数: $1（可用: build / clean）"; exit 1 ;;
esac
