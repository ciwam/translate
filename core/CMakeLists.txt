cmake_minimum_required(VERSION 3.20)

# C++ translation kernel. Produces a static library exposing a C API
# (include/translate/translate.h) usable from Objective-C, C#, and JNI.
add_library(translate_core STATIC
    src/translator.cpp
    src/openai_translator.cpp
    src/http_curl.cpp
    src/normalize_icu.cpp
    src/translate_capi.cpp
)

target_compile_features(translate_core PUBLIC cxx_std_17)

target_include_directories(translate_core
    PUBLIC
        $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
        $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include/translate>
        $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/vendor>
    PRIVATE
        ${CMAKE_CURRENT_SOURCE_DIR}/src
)

# --- HTTP: libcurl (cross-platform, single code path) ---
find_package(CURL REQUIRED)
target_link_libraries(translate_core PUBLIC CURL::libcurl)

# --- Text normalization: ICU (Hant -> Hans) ---
# Modern macOS SDKs no longer ship public ICU headers, so prefer Homebrew.
if(APPLE)
    execute_process(
        COMMAND brew --prefix icu4c
        OUTPUT_VARIABLE ICU_HINT
        OUTPUT_STRIP_TRAILING_WHITESPACE
        ERROR_QUIET
    )
    if(ICU_HINT)
        set(ICU_ROOT "${ICU_HINT}" CACHE PATH "ICU root (Homebrew)" FORCE)
    endif()
endif()

find_package(ICU REQUIRED COMPONENTS uc i18n data)
target_link_libraries(translate_core PUBLIC ICU::uc ICU::i18n ICU::data)

# --- Debug CLI: JSON-over-stdin console front-end (MCP debug channel) ---
# Not part of the app bundles; built on demand via
#   cmake --build <build-dir> --target translate_cli
add_executable(translate_cli tools/translate_cli.cpp)
target_link_libraries(translate_cli PRIVATE translate_core)
