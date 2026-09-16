#pragma once

#include <string>
#include <functional>

namespace translate {

// Enum order mirrors translate_provider_t; kProviders[] in translator.cpp is
// indexed by these values.
enum class Provider { Zhipu, SiliconFlow };
enum class Target { English, Chinese, Japanese, Korean, French, German, Spanish };

struct Result {
    bool ok = false;
    std::string text;
    std::string error;
};

using Callback = std::function<void(const Result &)>;
using DeltaCallback = std::function<void(const std::string &partial)>;

/* Cross-platform translation kernel. Networking uses libcurl, traditional ->
   simplified Chinese normalization uses ICU. All logic lives here so the macOS
   (Objective-C), Windows and Android front-ends share one implementation. */
class Translator {
public:
    /* Synchronous. Invokes `cb` on the same thread before returning. When
       `delta_cb` is non-null it is invoked with the translation accumulated so
       far (always a valid UTF-8 prefix of the final result — never split
       mid-character) so the caller can render progressively. `glossary` is a
       user-defined terminology dictionary ("term = translation" per line); it
       is injected into the prompt to force consistent terminology.

       `debug_route` (optional, may be null) receives a one-line diagnostic
       describing which pipeline path served the request and its counters —
       "cache", "prose", "prose-chunked(3)", "splice(translated 8/9)",
       "splice-fallback(rate=22%)", "splice-fallback(token-miss)" — for the
       debug CLI / quality triage. Never written when null. */
    void translate(Provider provider,
                   Target target,
                   const std::string &api_key,
                   const std::string &glossary,
                   const std::string &text,
                   Callback cb,
                   DeltaCallback delta_cb = nullptr,
                   std::string *debug_route = nullptr);
};

} // namespace translate
