cmake_minimum_required(VERSION 3.20)
project(TranslateAndroid LANGUAGES CXX)

# ---------------------------------------------------------------------------
# Reserved Android client (extension point).
#
# Builds the C++ kernel as a static lib. A JNI bridge (TODO) exposes
# translate_run() to Java/Kotlin. UI not implemented.
#
# Build with the NDK toolchain (from this directory):
#   cmake -B build -S . \
#     -DCMAKE_TOOLCHAIN_FILE=$ANDROID_NDK/build/cmake/android.toolchain.cmake \
#     -DANDROID_ABI=arm64-v8a -DANDROID_PLATFORM=android-24
# Required native deps for Android: libcurl, ICU (uc i18n data)
# ---------------------------------------------------------------------------

add_subdirectory(../core core_build)

# TODO: add_library(translate_jni SHARED jni_bridge.cpp)
# target_link_libraries(translate_jni PRIVATE translate_core)
message(STATUS "Android client: translate_core ready (C API + JNI). UI not yet implemented.")
