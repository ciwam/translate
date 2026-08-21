#include "translate.h"

#include "translator.hpp"

#include <cstdlib>
#include <cstring>

namespace {

const char *kProviderNames[] = {
    "SiliconFlow · Qwen2.5-7B-Instruct",
    "Zhipu · GLM-4-Flash",
};

const char *kTargetNames[] = {
    "English",
    "Simplified Chinese",
    "Japanese",
    "Korean",
    "French",
    "German",
    "Spanish",
};

constexpr int kProviderCount = 2;
constexpr int kTargetCount = 7;
constexpr int kDefaultTarget = 1; // Simplified Chinese

inline translate::Provider to_cpp(translate_provider_t p) {
    return (p == TRANSLATE_PROVIDER_ZHIPU) ? translate::Provider::Zhipu
                                           : translate::Provider::SiliconFlow;
}

inline translate::Target to_cpp(translate_target_t t) {
    return static_cast<translate::Target>(t);
}

} // namespace

void translate_run(translate_provider_t provider,
                   translate_target_t target,
                   const char *api_key,
                   const char *text,
                   translate_callback_t callback,
                   void *userdata) {
    translate::Translator engine;
    std::string key = api_key ? api_key : "";
    std::string src = text ? text : "";
    engine.translate(to_cpp(provider), to_cpp(target), key, src,
                     [callback, userdata](const translate::Result &r) {
                         char *out_text = r.ok ? strdup(r.text.c_str()) : nullptr;
                         char *out_err = (!r.ok) ? strdup(r.error.c_str()) : nullptr;
                         callback(r.ok ? 1 : 0, out_text, out_err, userdata);
                     });
}

void translate_free(char *ptr) {
    free(ptr); // NOLINT
}

int translate_provider_count(void) { return kProviderCount; }

const char *translate_provider_display_name(int index) {
    if (index < 0 || index >= kProviderCount) return "";
    return kProviderNames[index];
}

int translate_target_count(void) { return kTargetCount; }

const char *translate_target_display_name(int index) {
    if (index < 0 || index >= kTargetCount) return "";
    return kTargetNames[index];
}

int translate_default_target_index(void) { return kDefaultTarget; }
