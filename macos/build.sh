#!/bin/bash
# 编译并打包为 macOS 桌面 App（Objective-C UI + C++ 内核，CMake 统管）
# 用法:
#   ./build.sh           显示本帮助（不构建）
#   ./build.sh clean     清理构建缓存与已构建的 App（删除 .build）
#   ./build.sh build     先 clean，再构建 App
#   ./build.sh dmg       先 build，再打包可分发 DMG（内嵌 ICU 运行库 + ad-hoc 签名）
#   ./build.sh run       先 clean，再构建 App，随后运行
#   ./build.sh help      显示本帮助

set -e

cd "$(dirname "$0")"
ROOT="$(cd .. && pwd)"
APP_NAME="Translate"
BUILD_DIR=".build"
BUNDLE="$BUILD_DIR/macos/$APP_NAME.app"
DIST_DIR="dist"

# cmake 等工具不在默认 PATH（Homebrew on Apple Silicon）
export PATH="/opt/homebrew/bin:$PATH"

print_help() {
  cat <<'EOF'
用法:
  ./build.sh           显示本帮助（不构建）
  ./build.sh clean     清理构建缓存与已构建的 App（删除 .build）
  ./build.sh build     先 clean，再构建 App
  ./build.sh dmg       先 build，再打包可分发 DMG（内嵌 ICU 运行库 + ad-hoc 签名）
  ./build.sh run       先 clean，再构建 App，随后运行
  ./build.sh help      显示本帮助

DMG 产物: dist/Translate-<版本>-<架构>.dmg
dmg 打包完成后默认删除中间 .app；TRANSLATE_KEEP_APP=1 ./build.sh dmg 可保留。
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

# 内嵌 Homebrew ICU dylib（icuuc/icui18n/icudata），并把依赖改写为 @rpath，
# 使 App 脱离本机 Homebrew 独立运行——否则 DMG 拷到别的机器无法启动。
bundle_icu() {
  local BIN="$BUNDLE/Contents/MacOS/$APP_NAME"
  local FW="$BUNDLE/Contents/Frameworks"
  local ICU_LIBS
  ICU_LIBS=$(otool -L "$BIN" | grep -o '/opt/homebrew/opt/icu4c[^ ]*\.dylib' | sort -u || true)
  if [ -z "$ICU_LIBS" ]; then
    echo "==> ICU: 无 Homebrew 依赖，跳过内嵌"
    return 0
  fi
  echo "==> 内嵌 ICU 运行库: $(echo "$ICU_LIBS" | xargs -n1 basename | tr '\n' ' ')"
  mkdir -p "$FW"
  local lib base dep
  for lib in $ICU_LIBS; do
    base=$(basename "$lib")
    cp -L "$lib" "$FW/$base"
    install_name_tool -id "@rpath/$base" "$FW/$base"
    install_name_tool -change "$lib" "@rpath/$base" "$BIN"
    # 内嵌副本自身对其他 ICU 库的依赖同样改写为 @rpath
    for dep in $(otool -L "$FW/$base" | grep -o '/opt/homebrew/opt/icu4c[^ ]*\.dylib' | sort -u); do
      install_name_tool -change "$dep" "@rpath/$(basename "$dep")" "$FW/$base"
    done
  done
  install_name_tool -add_rpath "@executable_path/../Frameworks" "$BIN"
}

# ad-hoc 签名：仅本机/测试分发足够；正式对外分发需开发者证书 + 公证。
sign_app() {
  echo "==> ad-hoc 签名"
  # 清 Finder 扩展属性，避免 codesign 报 resource fork 冲突
  xattr -cr "$BUNDLE" 2>/dev/null || true
  codesign --force --deep --sign - "$BUNDLE"
  codesign -v --strict "$BUNDLE"
}

make_dmg() {
  command -v hdiutil >/dev/null 2>&1 || { echo "ERROR: DMG 只能在 macOS 上生成"; exit 1; }
  local ARCH VERSION DMG STAGE MNT
  ARCH=$(uname -m)
  VERSION=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$BUNDLE/Contents/Info.plist")
  DMG="$DIST_DIR/$APP_NAME-$VERSION-$ARCH.dmg"
  STAGE="$BUILD_DIR/dmg-stage"

  echo "==> 打包 DMG: $DMG"
  mkdir -p "$DIST_DIR"
  rm -f "$DMG"
  rm -rf "$STAGE"
  mkdir -p "$STAGE"
  # ditto 保留签名/扩展属性（cp -R 可能丢，导致镜像内签名校验失败）
  ditto "$BUNDLE" "$STAGE/$APP_NAME.app"
  ln -s /Applications "$STAGE/Applications"
  find "$STAGE" -name '.DS_Store' -delete 2>/dev/null || true
  find "$STAGE" -name '._*' -delete 2>/dev/null || true
  # UDZO = zlib 压缩只读镜像，分发标准格式；卷名固定不带版本（避免重测时挂载点冲突）
  hdiutil create -quiet -volname "$APP_NAME" -srcfolder "$STAGE" -ov -format UDZO "$DMG"
  rm -rf "$STAGE"

  echo "==> 验证 DMG（CRC + 镜像内签名）"
  hdiutil verify "$DMG" >/dev/null && echo "  CRC ✓"
  MNT=$(hdiutil attach -nobrowse -readonly "$DMG" | awk -F'\t' 'END{print $NF}')
  if [ ! -d "$MNT/$APP_NAME.app" ]; then
    echo "ERROR: 镜像内缺少 $APP_NAME.app"; hdiutil detach "$MNT" >/dev/null; exit 1
  fi
  codesign -v --deep --strict "$MNT/$APP_NAME.app" && echo "  签名 ✓（镜像内验证）"
  lipo -info "$MNT/$APP_NAME.app/Contents/MacOS/$APP_NAME"
  otool -L "$MNT/$APP_NAME.app/Contents/MacOS/$APP_NAME" | grep -q "@rpath" \
    && echo "  ICU @rpath ✓（无本机绝对路径）"
  hdiutil detach "$MNT" >/dev/null

  echo "==> DMG 就绪: $DMG ($(du -sh "$DMG" | cut -f1))"
  shasum -a 256 "$DMG" | awk '{print "  SHA256: " $1}'
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
  dmg)
    build
    bundle_icu
    sign_app
    make_dmg
    # 目录纪律：dist/ 只留分发包，中间 .app 打包验证完成后即清理。
    # 需要本地保留（调试/性能测量）时用 TRANSLATE_KEEP_APP=1。
    if [ "${TRANSLATE_KEEP_APP:-0}" = "1" ]; then
      echo "==> 保留中间 App（TRANSLATE_KEEP_APP=1）: $BUNDLE"
    else
      echo "==> 清理中间 App: $BUNDLE"
      rm -rf "$BUNDLE"
    fi
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
