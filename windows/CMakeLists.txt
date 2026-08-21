cmake_minimum_required(VERSION 3.20)
project(TranslateWin LANGUAGES CXX)

# ---------------------------------------------------------------------------
# Reserved Windows client (extension point).
#
# The shared C++ kernel in ../core exposes a plain C API:
#   ../core/include/translate/translate.h  (translate_run / UI helpers)
# A Win32 / WPF / Qt front-end should call it. The UI is NOT implemented yet.
#
# Suggested build (from this directory):
#   cmake -B build -S . -DCMAKE_TOOLCHAIN_FILE=<vcpkg>/scripts/buildsystems/vcpkg.cmake
#   cmake --build build
# Required native deps via vcpkg: libcurl, icu (uc i18n data)
# ---------------------------------------------------------------------------

add_subdirectory(../core core_build)

# TODO: implement the Windows UI and link the kernel, e.g.
#   add_executable(TranslateWin WIN32 windows_main.cpp)
#   target_link_libraries(TranslateWin PRIVATE translate_core)
message(STATUS "Windows client: translate_core ready (C API). UI not yet implemented.")
