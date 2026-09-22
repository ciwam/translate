#include "itranslator.hpp"

#include "http_curl.hpp"

#include <nlohmann/json.hpp>

#include <algorithm>
#include <memory>
#include <string>
#include <utility>

namespace translate {
namespace {

// Endpoint/model table, indexed by translate::Provider value (Zhipu=0,
// SiliconFlow=1) — keep in sync with the enum in translator.hpp.
struct EndpointInfo {
    const char *endpoint;
    const char *model;
};

constexpr EndpointInfo kEndpoints[] = {
    {"https://open.bigmodel.cn/api/paas/v4/chat/completions", "glm-4-flash"},
    {"https://api.siliconflow.cn/v1/chat/completions", "Qwen/Qwen2.5-7B-Instruct"},
};

// Adaptive total-operation timeout: a base allowance plus per-KB headroom for
// generation (output length tracks input length). The primary stall guard for
// the streaming path is the LOW_SPEED rule inside http_curl.cpp; this total is
// only a slow-but-alive backstop.
constexpr long kTimeoutBaseSeconds = 60;
constexpr long kTimeoutPerKbSeconds = 20;
constexpr long kTimeoutMaxSeconds = 900;

std::string trim(const std::string &s) {
    const char *ws = " \t\r\n";
    size_t b = s.find_first_not_of(ws);
    if (b == std::string::npos) return "";
    size_t e = s.find_last_not_of(ws);
    return s.substr(b, e - b + 1);
}

// Rate-limit / throttling faults are transient: the retry layer re-sends with
// backoff. Auth and other provider faults are not retried. Matched against the
// HTTP status and the provider message (message wording differs per provider).
bool is_throttle(long http_status, const std::string &msg) {
    if (http_status == 429) return true;
    for (const char *kw : {"429", "限流", "频繁", "rate", "throttl"}) {
        if (msg.find(kw) != std::string::npos) return true;
    }
    return false;
}

// Length of the longest prefix of `s` that ends on a UTF-8 code-point boundary.
// A code point spans at most 4 bytes, so at most 3 trailing bytes can be an
// incomplete tail. Used so progressive deltas never carry a partial character
// across the NSString conversion boundary in the UI.
size_t utf8_complete_prefix(const std::string &s) {
    const size_t n = s.size();
    if (n == 0) return 0;
    size_t i = n > 4 ? n - 4 : 0;
    while (i < n && (static_cast<unsigned char>(s[i]) & 0xC0) == 0x80) ++i;
    if (i == n) return 0; // malformed: everything in the tail is a continuation byte
    unsigned char c = static_cast<unsigned char>(s[i]);
    size_t len;
    if ((c & 0x80) == 0) len = 1;
    else if ((c & 0xE0) == 0xC0) len = 2;
    else if ((c & 0xF0) == 0xE0) len = 3;
    else if ((c & 0xF8) == 0xF0) len = 4;
    else return i; // invalid lead byte: keep only what precedes it
    return (i + len <= n) ? n : i;
}

// OpenAI-compatible chat-completions adapter: builds the JSON payload, streams
// the SSE response via libcurl, and forwards deltas / the final result.
class OpenAICompatTranslator : public ITranslator {
public:
    OpenAICompatTranslator(std::string endpoint, std::string model)
        : endpoint_(std::move(endpoint)), model_(std::move(model)) {}

    void translate(const TranslateRequest &req, DeltaCallback delta,
                   Callback done) override {
        nlohmann::json payload = {
            {"model", model_},
            {"messages",
             {{{"role", "system"}, {"content", req.system_prompt}},
              {{"role", "user"}, {"content", req.user_prompt}}}},
            {"temperature", 0.1},
            {"max_tokens", 4095},
            {"stream", true}};

        long src_kb = static_cast<long>(req.user_prompt.size() / 1024) + 1;
        long total_timeout = std::min(kTimeoutMaxSeconds,
                                      kTimeoutBaseSeconds + kTimeoutPerKbSeconds * src_kb);

        std::string body = payload.dump();
        std::string http_err;
        std::string result;
        size_t forwarded = 0; // number of result bytes already sent to delta_cb
        bool stream_done = false;
        std::string stream_err;

        auto handle_data = [&](const std::string &data) {
            if (stream_done) return;
            if (data == "[DONE]") {
                stream_done = true;
                return;
            }
            nlohmann::json j;
            try {
                j = nlohmann::json::parse(data);
            } catch (const std::exception &) {
                return; // not JSON (keep-alive / stray payload): ignore
            }
            if (j.contains("error") && j["error"].is_object() &&
                j["error"].contains("message")) {
                stream_err = j["error"]["message"].get<std::string>();
                stream_done = true;
                return;
            }
            if (!j.contains("choices") || !j["choices"].is_array() ||
                j["choices"].empty()) {
                return;
            }
            const nlohmann::json &choice = j["choices"][0];
            bool appended = false;
            if (choice.contains("delta") && choice["delta"].is_object()) {
                // Streaming shape: incremental content deltas.
                if (choice["delta"].contains("content") &&
                    choice["delta"]["content"].is_string()) {
                    result += choice["delta"]["content"].get<std::string>();
                    appended = true;
                }
            } else if (choice.contains("message") && choice["message"].is_object() &&
                       choice["message"].contains("content") &&
                       choice["message"]["content"].is_string()) {
                // Provider ignored "stream": true: the whole completion arrived at once.
                result += choice["message"]["content"].get<std::string>();
                appended = true;
                stream_done = true;
            }
            if (appended && delta) {
                size_t vp = utf8_complete_prefix(result);
                if (vp > forwarded) { // forward only new complete code points
                    forwarded = vp;
                    delta(result.substr(0, vp));
                }
            }
            if (choice.contains("finish_reason") &&
                choice["finish_reason"].is_string()) {
                // A truncated reply ("length") must fail loudly: silently
                // keeping the partial text drops lines and breaks alignment.
                if (choice["finish_reason"].get<std::string>() == "length") {
                    stream_err = "输出被截断（finish_reason=length）";
                }
                stream_done = true; // final chunk; "[DONE]" may not follow
            }
        };

        long http_status = 0;
        if (!http_post_sse(endpoint_, req.api_key, body, total_timeout, handle_data,
                           http_err, &http_status)) {
            done({false, "", http_err});
            return;
        }
        if (!stream_err.empty()) {
            const char *prefix = is_throttle(http_status, stream_err)
                                     ? "API 限流：" : "API 错误：";
            done({false, "", prefix + stream_err});
            return;
        }
        if (trim(result).empty()) {
            const bool throttle = is_throttle(http_status, result);
            std::string msg = http_status >= 400
                ? std::string(throttle ? "API 限流：HTTP " : "API 错误：HTTP ") +
                      std::to_string(http_status)
                : "响应异常：未收到内容（HTTP " +
                  std::to_string(http_status) + "）";
            done({false, "", msg});
            return;
        }
        done({true, trim(result), ""});
    }

private:
    std::string endpoint_;
    std::string model_;
};

} // namespace

std::shared_ptr<ITranslator> create_translator(Provider provider) {
    size_t idx = static_cast<size_t>(provider);
    if (idx >= sizeof(kEndpoints) / sizeof(kEndpoints[0])) idx = 0;
    return std::make_shared<OpenAICompatTranslator>(kEndpoints[idx].endpoint,
                                                    kEndpoints[idx].model);
}

} // namespace translate