#include "translate.h"

#include "translator.hpp"

#include <cstdlib>
#include <cstring>

namespace {

const char *kProviderNames[] = {
    "硅基流动 · DeepSeek-V3.2",
    "智谱 · GLM-4-Flash",
    "硅基流动 · Qwen2.5-7B-Instruct",
};

const char *kTargetNames[] = {
    "英语",
    "简体中文",
    "日语",
    "韩语",
    "法语",
    "德语",
    "西班牙语",
};

constexpr int kProviderCount = 3;
constexpr int kTargetCount = 7;
constexpr int kDefaultTarget = 1; // Simplified Chinese

inline translate::Provider to_cpp(translate_provider_t p) {
    switch (p) {
        case TRANSLATE_PROVIDER_ZHIPU:
            return translate::Provider::Zhipu;
        case TRANSLATE_PROVIDER_SILICONFLOW_QWEN:
            return translate::Provider::SiliconFlow_Qwen;
        default:
            return translate::Provider::SiliconFlow;
    }
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

void translate_run_stream(translate_provider_t provider,
                          translate_target_t target,
                          const char *api_key,
                          const char *text,
                          translate_stream_callback_t callback,
                          void *userdata) {
    translate::Translator engine;
    std::string key = api_key ? api_key : "";
    std::string src = text ? text : "";
    bool done = false; // translate() always invokes cb exactly once
    engine.translate(
        to_cpp(provider), to_cpp(target), key, src,
        [callback, userdata, &done](const translate::Result &r) {
            if (done) return;
            done = true;
            if (r.ok) {
                char *dup = strdup(r.text.c_str());
                callback(TRANSLATE_EVENT_DONE, dup, nullptr, userdata);
            } else {
                char *dup = strdup(r.error.c_str());
                callback(TRANSLATE_EVENT_ERROR, nullptr, dup, userdata);
            }
        },
        [callback, userdata](const std::string &partial) {
            char *dup = strdup(partial.c_str());
            callback(TRANSLATE_EVENT_DELTA, dup, nullptr, userdata);
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
