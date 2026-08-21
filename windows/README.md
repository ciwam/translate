# Windows 客户端（预留扩展）

本目录是 **Windows 客户端** 的扩展占位，目前只搭建了构建结构与内核接入点，UI 尚未实现。

## 内核接入点（已就绪）

共享的 C++ 内核位于仓库 `core/`，对外暴露纯 C 接口：

- 头文件：`../core/include/translate/translate.h`
- 关键 API：
  - `translate_run(provider, target, api_key, text, callback, userdata)` —— 同步翻译，结果通过回调返回（UTF-8）。
  - `translate_provider_count` / `translate_provider_display_name` —— 填充模型下拉框。
  - `translate_target_count` / `translate_target_display_name` / `translate_default_target_index` —— 填充目标语言下拉框。

内核使用 **libcurl**（HTTP）与 **ICU**（繁→简兜底），与 macOS 端完全一致，保证跨端翻译逻辑统一。

## 后续实现步骤

1. 选 UI 技术：Win32 / WPF(C++/CLI 或 C# P/Invoke) / Qt。
2. 用 vcpkg 安装 `libcurl` 与 `icu`：
   ```
   vcpkg install curl icu
   ```
3. 配置并构建：
   ```
   cmake -B build -S . -DCMAKE_TOOLCHAIN_FILE=<vcpkg>/scripts/buildsystems/vcpkg.cmake
   cmake --build build
   ```
4. 在 `windows/CMakeLists.txt` 中 `add_executable` 你的 UI 目标，`target_link_libraries(... PRIVATE translate_core)`，
   仿照 macOS 的 `core_bridge.mm` 在后台线程调用 `translate_run`，主线程更新界面。
