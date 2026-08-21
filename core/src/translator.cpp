#include "translator.hpp"
#include "http_curl.hpp"
#include "normalize_icu.hpp"

#include <nlohmann/json.hpp>

#include <algorithm>
#include <string>

namespace translate {
namespace {

struct ProviderInfo {
    const char *endpoint;
    const char *model;
};

constexpr ProviderInfo kProviders[] = {
    {"https://api.siliconflow.cn/v1/chat/completions", "Qwen/Qwen2.5-7B-Instruct"},
    {"https://open.bigmodel.cn/api/paas/v4/chat/completions", "glm-4-flash"},
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

} // namespace

void Translator::translate(Provider provider,
                           Target target,
                           const std::string &api_key,
                           const std::string &text,
                           Callback cb) {
    std::string key = trim(api_key);
    if (key.empty()) {
        cb({false, "", "Please fill in the API Key"});
        return;
    }
    std::string src = trim(text);
    if (src.empty()) {
        cb({false, "", "Please enter text to translate"});
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

    std::string system_prompt =
        "You are a professional translator. "
        "Translate the user's text exactly into the requested target language. "
        "Always provide a translation; never echo or return the original text "
        "unchanged. "
        "Output only the translated text, with no explanations, notes, "
        "examples, or quotation marks. " +
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
        {"stream", false}};

    std::string body = payload.dump();
    std::string response;
    std::string http_err;
    if (!http_post_json(endpoint, key, body, response, http_err)) {
        cb({false, "", http_err});
        return;
    }

    try {
        auto j = nlohmann::json::parse(response);
        if (j.contains("error") && j["error"].is_object() &&
            j["error"].contains("message")) {
            cb({false, "", "API error: " + j["error"]["message"].get<std::string>()});
            return;
        }
        if (j.contains("choices") && j["choices"].is_array() &&
            !j["choices"].empty() && j["choices"][0].contains("message") &&
            j["choices"][0]["message"].contains("content")) {
            std::string content =
                j["choices"][0]["message"]["content"].get<std::string>();
            content = trim(content);
            cb({true, normalize_if_chinese(content, target), ""});
            return;
        }
        std::string preview =
            response.size() > 200 ? response.substr(0, 200) : response;
        cb({false, "", "Unexpected response: " + preview});
    } catch (const std::exception &ex) {
        std::string preview =
            response.size() > 200 ? response.substr(0, 200) : response;
        cb({false, "", "Parse failed: " + preview});
    }
}

} // namespace translate
