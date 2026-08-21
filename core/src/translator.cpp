#include "translator.hpp"
#include "http_curl.hpp"
#include "normalize_icu.hpp"

#include <nlohmann/json.hpp>

#include <algorithm>
#include <string>
#include <vector>

namespace translate {
namespace {

struct ProviderInfo {
    const char *endpoint;
    const char *model;
    // structure_fragile marks models that corrupt numbers, paths, and brackets
    // when asked to translate them. Such providers route rebuild-style or
    // verbatim-passthrough sources; faithful models get the raw text.
    bool structure_fragile;
};

constexpr ProviderInfo kProviders[] = {
    {"https://api.siliconflow.cn/v1/chat/completions", "deepseek-ai/DeepSeek-V3.2", false},
    {"https://open.bigmodel.cn/api/paas/v4/chat/completions", "glm-4-flash", true},
    {"https://api.siliconflow.cn/v1/chat/completions", "Qwen/Qwen2.5-7B-Instruct", true},
};

struct TargetInfo {
    const char *prompt_name;
};

constexpr TargetInfo kTargets[] = {
    {"English"},         // 0
    {"Simplified Chinese"}, // 1
    {"Japanese"},        // 2
    {"Korean"},          // 3
    {"French"},          // 4
    {"German"},          // 5
    {"Spanish"},         // 6
};

std::string trim(const std::string &s) {
    const char *ws = " \t\r\n";
    size_t b = s.find_first_not_of(ws);
    if (b == std::string::npos) return "";
    size_t e = s.find_last_not_of(ws);
    return s.substr(b, e - b + 1);
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

// Adaptive total-operation timeout: a base allowance plus per-KB headroom for
// generation (output length tracks input length). The primary stall guard for
// the streaming path is the LOW_SPEED rule inside http_curl.cpp; this total is
// only a slow-but-alive backstop.
constexpr long kTimeoutBaseSeconds = 60;
constexpr long kTimeoutPerKbSeconds = 20;
constexpr long kTimeoutMaxSeconds = 900;

// A token is "technical" when it is predominantly ASCII and contains a
// structural character (digit or one of _ / % \ . [] : = < >). Build-log
// prefixes, file paths, identifiers, percentages, and flags cannot be
// reproduced by this small model, so translate() does not let it touch text
// that is mostly technical tokens: such sources pass through verbatim.
// A '.' counts only when it is internal (file extension, version number) —
// a sentence-final period is ordinary punctuation.
bool is_protected_token(const std::string &tok) {
    size_t ascii = 0;
    bool technical = false;
    const size_t n = tok.size();
    for (size_t i = 0; i < n; ++i) {
        unsigned char c = static_cast<unsigned char>(tok[i]);
        if (c < 0x80) ++ascii;
        if ((c >= '0' && c <= '9') || c == '_' || c == '/' || c == '%' ||
            c == '\\' || c == '[' || c == ']' || c == ':' || c == '=' ||
            c == '<' || c == '>') {
            technical = true;
        } else if (c == '.' && i + 1 < n) {
            technical = true;
        }
    }
    return technical && ascii * 2 > tok.size();
}

// Classify the whole source: have_tech is set when any token is protected, and
// prose_fraction is the share of non-whitespace bytes in non-protected tokens.
// Used to route between streaming translation (mostly prose) and verbatim
// passthrough (mostly technical, e.g. build logs and configs).
void analyze_technical(const std::string &src, bool &have_tech,
                       double &prose_fraction) {
    have_tech = false;
    size_t total = 0, prose = 0;
    size_t i = 0;
    const size_t n = src.size();
    while (i < n) {
        while (i < n && (src[i] == ' ' || src[i] == '\t' || src[i] == '\r' ||
                         src[i] == '\n')) {
            ++i;
        }
        const size_t start = i;
        while (i < n && src[i] != ' ' && src[i] != '\t' && src[i] != '\r' &&
               src[i] != '\n') {
            ++i;
        }
        if (i == start) break;
        std::string tok = src.substr(start, i - start);
        total += tok.size();
        if (is_protected_token(tok)) {
            have_tech = true;
        } else {
            prose += tok.size();
        }
    }
    prose_fraction = total == 0 ? 0.0 : static_cast<double>(prose) / total;
}

} // namespace

void Translator::translate(Provider provider,
                           Target target,
                           const std::string &api_key,
                           const std::string &text,
                           Callback cb,
                           DeltaCallback delta_cb) {
    std::string key = trim(api_key);
    if (key.empty()) {
        cb({false, "", "请填写 API Key"});
        return;
    }
    std::string src = trim(text);
    if (src.empty()) {
        cb({false, "", "请输入要翻译的文本"});
        return;
    }

    size_t pIdx = static_cast<size_t>(provider);
    if (pIdx >= sizeof(kProviders) / sizeof(kProviders[0])) pIdx = 0;
    size_t tIdx = static_cast<size_t>(target);
    if (tIdx >= sizeof(kTargets) / sizeof(kTargets[0])) tIdx = 0;

    const char *endpoint = kProviders[pIdx].endpoint;
    const char *model = kProviders[pIdx].model;
    const char *prompt_name = kTargets[tIdx].prompt_name;

    // Strengthen the prompt against the two known failure modes:
    //  - echoing the source unchanged (short words/phrases)
    //  - emitting Traditional Chinese for a Simplified-Chinese target.
    std::string target_name;
    std::string constraints;
    switch (target) {
        case Target::Chinese:
            target_name = "Mainland China Simplified Chinese "
                          "(中国大陆简体字，禁止使用繁体字)";
            constraints = "Use only Mainland China standard simplified "
                          "characters. Do NOT use traditional Chinese characters.";
            break;
        default:
            target_name = prompt_name;
            constraints = "";
            break;
    }

    // Classify the source. Models that corrupt structure (structure_fragile)
    // must not receive text dominated by technical tokens — build logs, configs,
    // code — so it passes through verbatim: such a model cannot reproduce
    // numbers, brackets, or paths, and any attempt to translate them corrupts
    // the document. Faithful models translate the whole text, trusting the
    // prompt below to preserve non-language tokens.
    bool has_tech = false;
    double prose_fraction = 0.0;
    constexpr double kMinProseFraction = 0.5;
    if (kProviders[pIdx].structure_fragile) {
        analyze_technical(src, has_tech, prose_fraction);
        if (has_tech && prose_fraction < kMinProseFraction) {
            cb({true, normalize_if_chinese(src, target), ""});
            return;
        }
    }

    std::string system_prompt =
        "You are a professional translator. Translate the user's text "
        "exactly into the requested target language. Always provide a "
        "translation; never echo or return the original text unchanged. "
        "Preserve verbatim every non-language token exactly as written: "
        "numbers, percent signs, brackets, file paths, code identifiers, "
        "URLs, and command names. Never reword, reorder, drop, or recompute "
        "them. Keep the exact line structure and translate the text "
        "completely without truncating or omitting any line. Output only the "
        "translated text, with no explanations, notes, examples, or "
        "quotation marks. " +
        constraints;
    std::string user_prompt =
        "Translate the following text into " + target_name +
        ". Provide only the translation:\n\n" + src;
    nlohmann::json payload = {
        {"model", model},
        {"messages",
         {{{"role", "system"}, {"content", system_prompt}},
          {{"role", "user"}, {"content", user_prompt}}}},
        {"temperature", 0.1},
        {"stream", true}};

    long src_kb = static_cast<long>(src.size() / 1024) + 1;
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
        if (appended && delta_cb) {
            size_t vp = utf8_complete_prefix(result);
            if (vp > forwarded) { // forward only new complete code points
                forwarded = vp;
                delta_cb(result.substr(0, vp));
            }
        }
        if (choice.contains("finish_reason") && !choice["finish_reason"].is_null()) {
            stream_done = true; // final chunk; "[DONE]" may not follow
        }
    };

    long http_status = 0;
    if (!http_post_sse(endpoint, key, body, total_timeout, handle_data, http_err,
                       &http_status)) {
        cb({false, "", http_err});
        return;
    }

    if (!stream_err.empty()) {
        cb({false, "", "API 错误：" + stream_err});
        return;
    }
    if (trim(result).empty()) {
        std::string msg = http_status >= 400
            ? "API 错误：HTTP " + std::to_string(http_status)
            : "响应异常：未收到内容（HTTP " +
              std::to_string(http_status) + "）";
        cb({false, "", msg});
        return;
    }
    cb({true, normalize_if_chinese(trim(result), target), ""});
}

} // namespace translate
