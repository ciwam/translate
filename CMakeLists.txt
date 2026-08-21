cmake_minimum_required(VERSION 3.20)

project(Translate LANGUAGES C CXX OBJC OBJCXX)

# Shared C++ kernel (the "core" of the app, platform agnostic).
add_subdirectory(core)

# Platform front-ends. Windows/Android are reserved as extension points:
# their CMakeLists live in windows/ and android/ and are configured
# independently with the appropriate toolchain (see their READMEs).
if(APPLE)
    add_subdirectory(macos)
endif()

if(WIN32)
    add_subdirectory(windows)
endif()
