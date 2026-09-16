// Debug CLI front-end for the translation kernel.
//
// Reads one JSON request from stdin and writes one JSON response line to
// stdout. Built for the MCP debug channel (translate-mcp) and for manual
// quality triage — the GUI is not involved.
//
// Request:
//   {
//     "provider": 0 | "zhipu" | "siliconflow" | 1,   // default: 0 (Zhipu)
//     "target":   0..6 | "en"|"zh"|"ja"|"ko"|"fr"|"de"|"es",  // default: 1 (zh)
//     "text":     "source text",                     // required
//     "glossary": "term = translation\n...",         // optional
//     "api_key":  "sk-..."                           // optional; falls back to
//   }                                                // TRANSLATE_API_KEY_ZHIPU /
//                                                    // TRANSLATE_API_KEY_SILICONFLOW
// Response (single line, compact):
//   {"ok":true, "text":"...", "route":"splice(translated 8/9)",
//    "provider":"siliconflow", "target":"zh", "elapsed_ms":812}
//   {"ok":false, "error":"...", "provider":"...", "target":"...", "elapsed_ms":N}
//
// Exit code: 0 on success, 1 on failure (the MCP wrapper reads the JSON
// either way).

#include "translate/translator.hpp"

#include <nlohmann/json.hpp>

#include <chrono>
#include <cstdlib>
#include <iostream>
#include <sstream>
#include <string>

namespace {

using json = nlohmann::json;

const char *kTargetCodes[] = {"en", "zh", "ja", "ko", "fr", "de", "es"};
const char *kProviderNames[] = {"zhipu", "siliconflow"};

bool parse_provider(const json &j, translate::Provider &out, std::string &err) {
    if (j.is_number_integer()) {
        int v = j.get<int>();
        if (v < 0 || v > 1) { err = "provider 越界（0=Zhipu, 1=SiliconFlow）"; return false; }
        out = static_cast<translate::Provider>(v);
        return true;
    }
    if (j.is_string()) {
        std::string s = j.get<std::string>();
        for (int i = 0; i < 2; ++i) {
            if (s == kProviderNames[i]) {
                out = static_cast<translate::Provider>(i);
                return true;
            }
        }
        err = "未知 provider \"" + s + "\"（可选 zhipu / siliconflow）";
        return false;
    }
    err = "provider 类型非法";
    return false;
}

bool parse_target(const json &j, translate::Target &out, std::string &err) {
    if (j.is_number_integer()) {
        int v = j.get<int>();
        if (v < 0 || v > 6) { err = "target 越界（0..6）"; return false; }
        out = static_cast<translate::Target>(v);
        return true;
    }
    if (j.is_string()) {
        std::string s = j.get<std::string>();
        for (int i = 0; i < 7; ++i) {
            if (s == kTargetCodes[i]) {
                out = static_cast<translate::Target>(i);
                return true;
            }
        }
        err = "未知 target \"" + s + "\"（可选 en/zh/ja/ko/fr/de/es）";
        return false;
    }
    err = "target 类型非法";
    return false;
}

// Key resolution: explicit field wins, then the provider-specific env var.
// Never persisted anywhere by this tool.
std::string resolve_api_key(translate::Provider provider, const json &req) {
    if (req.contains("api_key") && req["api_key"].is_string()) {
        return req["api_key"].get<std::string>();
    }
    const char *env_name =
        provider == translate::Provider::SiliconFlow ? "TRANSLATE_API_KEY_SILICONFLOW"
                                                     : "TRANSLATE_API_KEY_ZHIPU";
    const char *env = std::getenv(env_name);
    return env ? env : "";
}

std::string read_all_stdin() {
    std::ostringstream ss;
    ss << std::cin.rdbuf();
    return ss.str();
}

} // namespace

int main() {
    const auto t0 = std::chrono::steady_clock::now();
    auto elapsed_ms = [&t0] {
        return std::chrono::duration_cast<std::chrono::milliseconds>(
                   std::chrono::steady_clock::now() - t0)
            .count();
    };

    translate::Provider provider = translate::Provider::Zhipu;
    translate::Target target = translate::Target::Chinese;

    auto fail = [&elapsed_ms, &provider, &target](const std::string &msg) {
        json out = {{"ok", false},
                    {"error", msg},
                    {"provider", kProviderNames[static_cast<int>(provider)]},
                    {"target", kTargetCodes[static_cast<int>(target)]},
                    {"elapsed_ms", elapsed_ms()}};
        std::cout << out.dump() << std::endl;
        return 1;
    };

    std::string input = read_all_stdin();
    json req = json::parse(input, nullptr, false);
    std::string parse_err;
    if (req.is_discarded()) {
        return fail("stdin 不是合法 JSON");
    }
    if (req.contains("provider") &&
        !parse_provider(req["provider"], provider, parse_err)) {
        return fail(parse_err);
    }
    if (req.contains("target") && !parse_target(req["target"], target, parse_err)) {
        return fail(parse_err);
    }
    if (!req.contains("text") || !req["text"].is_string() ||
        req["text"].get<std::string>().empty()) {
        return fail("缺少 text 字段（要翻译的文本）");
    }

    std::string api_key = resolve_api_key(provider, req);
    std::string glossary = req.contains("glossary") && req["glossary"].is_string()
                               ? req["glossary"].get<std::string>()
                               : "";

    std::string route;
    translate::Translator engine;
    translate::Result result;
    engine.translate(provider, target, api_key, glossary,
                     req["text"].get<std::string>(),
                     [&](const translate::Result &r) { result = r; },
                     nullptr, &route);

    json out = {{"ok", result.ok},
                {"provider", kProviderNames[static_cast<int>(provider)]},
                {"target", kTargetCodes[static_cast<int>(target)]},
                {"route", route},
                {"elapsed_ms", elapsed_ms()}};
    if (result.ok) {
        out["text"] = result.text;
    } else {
        out["error"] = result.error;
    }
    std::cout << out.dump() << std::endl;
    return result.ok ? 0 : 1;
}
