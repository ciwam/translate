# Android 客户端（预留扩展）

本目录是 **Android 客户端** 的扩展占位，目前只搭建了构建结构与内核接入点，UI 尚未实现。

## 内核接入点（已就绪）

共享的 C++ 内核位于仓库 `core/`，对外暴露纯 C 接口：

- 头文件：`../core/include/translate/translate.h`
- 关键 API：`translate_run(...)`（同步翻译，回调返回 UTF-8 结果）、`translate_provider_*` / `translate_target_*`（填充下拉框）。

内核使用 **libcurl** 与 **ICU**，与 macOS / Windows 端完全一致。

## 后续实现步骤

1. 用 NDK 工具链把内核编成静态库（见 `android/CMakeLists.txt` 的 `cmake` 命令）。
2. 编写 JNI 桥（`jni_bridge.cpp`），在后台线程调用 `translate_run`，把结果回传 Java/Kotlin。
   ```
   external fun translate(provider: Int, target: Int, apiKey: String, text: String): String
   ```
3. 上层用 Kotlin/Compose 实现界面，仿照 macOS 的 `core_bridge.mm` 把网络调用放到后台线程、结果切回主线程。
4. 通过 Prefab / CMake 把 `translate_core` 链接进你的 JNI 库。
