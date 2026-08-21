#pragma once

#include <string>
#include <functional>

namespace translate {

enum class Provider { SiliconFlow, Zhipu };
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
       mid-character) so the caller can render progressively. */
    void translate(Provider provider,
                   Target target,
                   const std::string &api_key,
                   const std::string &text,
                   Callback cb,
                   DeltaCallback delta_cb = nullptr);
};

} // namespace translate
