cmake_minimum_required(VERSION 3.20)

project(TranslateApp OBJC OBJCXX)

set(APP_NAME "Translate")
set(BUNDLE_ID "com.ciwang.translate")

find_library(COCOA_FRAMEWORK Cocoa REQUIRED)
find_library(FOUNDATION_FRAMEWORK Foundation REQUIRED)

set(SOURCES
    Sources/Translate/main.m
    Sources/Translate/AppDelegate.m
    Sources/Translate/TranslateViewController.m
    Sources/Translate/core_bridge.mm
)

add_executable(${APP_NAME} MACOSX_BUNDLE ${SOURCES})

set_target_properties(${APP_NAME} PROPERTIES
    MACOSX_BUNDLE_INFO_PLIST ${CMAKE_CURRENT_SOURCE_DIR}/Info.plist.in
    MACOSX_BUNDLE_BUNDLE_NAME "Translate"
    MACOSX_BUNDLE_BUNDLE_DISPLAY_NAME "Translate"
    MACOSX_BUNDLE_GUI_IDENTIFIER ${BUNDLE_ID}
    MACOSX_BUNDLE_BUNDLE_VERSION "1.0"
    MACOSX_BUNDLE_SHORT_VERSION_STRING "1.0"
    MACOSX_BUNDLE_ICON_FILE "AppIcon"
)

target_link_libraries(${APP_NAME} PRIVATE
    translate_core
    ${COCOA_FRAMEWORK}
    ${FOUNDATION_FRAMEWORK}
)

target_include_directories(${APP_NAME} PRIVATE
    ${CMAKE_CURRENT_SOURCE_DIR}/Sources/Translate
)

# The UI uses ARC (strong properties, __bridge_transfer in the C++ bridge).
target_compile_options(${APP_NAME} PRIVATE -fobjc-arc)

# Copy the app icon into the bundle's Resources.
add_custom_command(TARGET ${APP_NAME} POST_BUILD
    COMMAND ${CMAKE_COMMAND} -E copy
        ${CMAKE_CURRENT_SOURCE_DIR}/AppIcon.icns
        $<TARGET_BUNDLE_CONTENT_DIR:${APP_NAME}>/Resources/AppIcon.icns
    COMMENT "Copying AppIcon.icns into bundle"
)
