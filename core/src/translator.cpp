#include "translator.hpp"

#include "itranslator.hpp"
#include "normalize_icu.hpp"

#include <algorithm>
#include <chrono>
#include <condition_variable>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <list>
#include <memory>
#include <mutex>
#include <string>
#include <thread>
#include <unordered_map>
#include <utility>
#include <vector>

namespace translate {
namespace {

const char *kPromptName[] = {
    "English",         // 0
    "Simplified Chinese", // 1
    "Japanese",        // 2
    "Korean",          // 3
    "French",          // 4
    "German",          // 5
    "Spanish",         // 6
};

std::string trim(const std::string &s) {
    const char *ws = " \t\r\n";
    size_t b = s.find_first_not_of(ws);
    if (b == std::string::npos) return "";
    size_t e = s.find_last_not_of(ws);
    return s.substr(b, e - b + 1);
}

bool is_ws_char(char c) { return c == ' ' || c == '\t' || c == '\r'; }

// A token is "technical" when it is predominantly ASCII and contains a
// structural character, or is an identifier/acronym. Build-log prefixes, file
// paths, identifiers, percentages, flags, shell variables ($var), template
// placeholders ({name}), CSV cells, and camelCase/uppercase type names (NSFont,
// CFType, TranslateViewController) cannot be reproduced by weak models, so the
// pipeline never sends them to the model: it extracts the surrounding prose
// and splices the translations back.
// Sentence-final punctuation (`. , ; ! ?` at a token edge, `.` at the end) is
// ordinary prose; the same characters count as structural only when INTERNAL
// (file extension, CSV separator, call parentheses) — "hello." is prose,
// "a,b,c" and "file.tar.gz" are not.
bool is_protected_token(const std::string &tok) {
    size_t ascii = 0, upper = 0, lower = 0;
    bool technical = false;
    const size_t n = tok.size();
    for (size_t i = 0; i < n; ++i) {
        unsigned char c = static_cast<unsigned char>(tok[i]);
        if (c < 0x80) {
            ++ascii;
            if (c >= 'A' && c <= 'Z') ++upper;
            else if (c >= 'a' && c <= 'z') ++lower;
        }
        if ((c >= '0' && c <= '9') || c == '_' || c == '/' || c == '%' ||
            c == '\\' || c == '[' || c == ']' || c == '=' ||
            c == '<' || c == '>' || c == '$' || c == '{' || c == '}' ||
            c == '@' || c == '#' || c == '&' || c == '+' || c == '*' ||
            c == '~' || c == '^' || c == '|' || c == '`') {
            technical = true;
        } else if ((c == '.' || c == ',' || c == ';' || c == '(' || c == ')' ||
                    c == ':' ||
                    c == '!' || c == '?') &&
                   i + 1 < n) {
            technical = true; // internal: CSV/extension/call/URL, not sentence-final
        }
    }
    // Mixed-case identifier (NSFont, CFType, TranslateViewController). Pure
    // ALL-CAPS acronyms (CXX, OBJC) stay prose: the model already keeps them
    // verbatim inside spans, and carving them out shrinks spans into fragments
    // the model tends to drop.
    if (upper >= 2 && lower >= 1) technical = true;
    // NOTE: word+colon labels ("ERROR:", "client:") are deliberately PROSE.
    // Protecting them froze whole log lines when the model translated the
    // label (run r3); placeholders now guard the truly structural tokens, and
    // a translated "WARNING:" -> "警告:" is acceptable for the audience.
    return technical && ascii * 2 > tok.size();
}

// Classify the whole source: have_tech is set when any token is protected.
// Used to route between streaming translation (no technical tokens to protect)
// and the deterministic splice pipeline.
void analyze_technical(const std::string &src, bool &have_tech) {
    have_tech = false;
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
        if (is_protected_token(src.substr(start, i - start))) {
            have_tech = true;
            return; // one protected token is enough to route to splice mode
        }
    }
}

// Splice v2 unit = a whole source line. A line is translatable when it holds
// at least one non-protected (prose) token; purely technical lines are kept
// verbatim and never sent to the model.
struct LineInfo {
    std::string text;       // line content without the trailing \r
    bool has_prose = false; // >= 1 translatable token
};

// True when the line contains at least one token the model should translate.
bool line_has_prose(const std::string &line) {
    size_t i = 0;
    const size_t n = line.size();
    while (i < n) {
        while (i < n && is_ws_char(line[i])) ++i;
        const size_t s = i;
        while (i < n && !is_ws_char(line[i])) ++i;
        if (i == s) break;
        if (!is_protected_token(line.substr(s, i - s))) return true;
    }
    return false;
}

// ---- 占位符掩码 (splice v2.1) ----
// Even with few-shot anchoring, small models still translated protected tokens
// inside whole lines ("CrashLoopBackOff" -> "崩溃回环", "WARNING:" -> "警告:").
// Masking replaces every protected token with a [[N]] placeholder before the
// request and restores it verbatim afterwards, making token preservation
// structural instead of a matter of model discipline.
std::string normalize_fullwidth_punct(const std::string &s);

// Replace protected tokens with [[N]] placeholders, preserving all whitespace.
// Returns false when the unit holds no protected token or already contains
// "[[" (placeholder collision) — the caller then sends the unit unmasked.
bool mask_unit(const std::string &unit, std::string &masked,
               std::vector<std::string> &tokens) {
    if (unit.find("[[") != std::string::npos) return false;
    masked.clear();
    tokens.clear();
    size_t i = 0;
    const size_t n = unit.size();
    while (i < n) {
        const size_t ws = i;
        while (i < n && is_ws_char(unit[i])) ++i;
        masked.append(unit, ws, i - ws);
        if (i >= n) break;
        const size_t s0 = i;
        while (i < n && !is_ws_char(unit[i])) ++i;
        std::string tok = unit.substr(s0, i - s0);
        if (is_protected_token(tok)) {
            // Keep sentence/quote characters OUT of the placeholder: a bare
            // number's sentence period ("… port 8080.") reads broken when
            // restored mid-sentence (run r6), and a swallowed closing quote
            // ("app-7d9f'") unbalances the masked line, luring the model into
            // adding a stray second quote (run r7).
            std::string body = tok;
            std::string tail;
            if (body.size() >= 2 && body.back() == '.') {
                const std::string prefix = body.substr(0, body.size() - 1);
                if (!prefix.empty() &&
                    prefix.find_first_not_of("0123456789") == std::string::npos) {
                    body = prefix;
                    tail = ".";
                }
            }
            while (body.size() >= 2 &&
                   (body.back() == '\'' || body.back() == '"')) {
                tail = body.back() + tail;
                body.pop_back();
            }
            if (body.empty()) { // degenerate: pure punctuation — keep verbatim
                masked += tok;
            } else {
                masked += "[[" + std::to_string(tokens.size() + 1) + "]]" + tail;
                tokens.push_back(std::move(body));
            }
        } else {
            masked += tok;
        }
    }
    return !tokens.empty();
}

// Map the full-width punctuation small models like to emit back to ASCII, so
// placeholder scanning works regardless of the model's punctuation fashion.
std::string normalize_fullwidth_punct(const std::string &s) {
    std::string r;
    r.reserve(s.size());
    size_t i = 0;
    const size_t n = s.size();
    while (i < n) {
        unsigned char c0 = static_cast<unsigned char>(s[i]);
        if (c0 == 0xEF && i + 2 < n) { // U+FF00 half-width forms block
            unsigned char c1 = static_cast<unsigned char>(s[i + 1]);
            unsigned char c2 = static_cast<unsigned char>(s[i + 2]);
            if (c1 == 0xBC) {
                switch (c2) {
                    case 0x8C: r += ','; i += 3; continue; // ，
                    case 0x9A: r += ':'; i += 3; continue; // ：
                    case 0x9B: r += ';'; i += 3; continue; // ；
                    case 0x81: r += '!'; i += 3; continue; // ！
                    case 0x9F: r += '?'; i += 3; continue; // ？
                    case 0x88: r += '('; i += 3; continue; // （
                    case 0x89: r += ')'; i += 3; continue; // ）
                    case 0xBB: r += '['; i += 3; continue; // ［
                    case 0xBD: r += ']'; i += 3; continue; // ］
                }
            }
        } else if (c0 == 0xE3 && i + 2 < n) { // CJK punctuation block
            unsigned char c1 = static_cast<unsigned char>(s[i + 1]);
            unsigned char c2 = static_cast<unsigned char>(s[i + 2]);
            if (c1 == 0x80 && c2 == 0x90) { r += '['; i += 3; continue; } // 【
            if (c1 == 0x80 && c2 == 0x91) { r += ']'; i += 3; continue; } // 】
        } else if (c0 == 0xE2 && i + 2 < n) {
            unsigned char c1 = static_cast<unsigned char>(s[i + 1]);
            unsigned char c2 = static_cast<unsigned char>(s[i + 2]);
            if (c1 == 0x80 && (c2 == 0x9C || c2 == 0x9D)) {
                r += '"'; i += 3; continue; // “ ”
            }
        }
        r += s[i];
        ++i;
    }
    return r;
}

// Restore [[N]] placeholders (after full-width normalization). Fails when ANY
// placeholder of the unit is missing, malformed, or out of range — the line
// then falls back to its original text, never to a half-restored hybrid.
// (Run r7: the model dropped [[1]] entirely and restore "succeeded", leaving
// "WARNING:" out of the output; the whole-text net then rejected the result.)
bool restore_unit(const std::string &reply,
                  const std::vector<std::string> &tokens, std::string &out) {
    std::string s = normalize_fullwidth_punct(reply);
    out.clear();
    out.reserve(s.size());
    std::vector<char> seen(tokens.size(), 0);
    size_t i = 0;
    const size_t n = s.size();
    while (i < n) {
        if (s[i] == '[' && i + 1 < n && s[i + 1] == '[') {
            const size_t e = s.find("]]", i + 2);
            if (e == std::string::npos || e - (i + 2) > 3) return false;
            const std::string num = s.substr(i + 2, e - i - 2);
            if (num.empty() ||
                num.find_first_not_of("0123456789") != std::string::npos) {
                return false;
            }
            unsigned long idx = 0;
            try {
                idx = std::stoul(num);
            } catch (...) {
                return false;
            }
            if (idx == 0 || idx > tokens.size()) return false;
            out += tokens[idx - 1];
            seen[idx - 1] = 1;
            i = e + 2;
        } else {
            out += s[i];
            ++i;
        }
    }
    for (const char c : seen) {
        if (!c) return false; // a placeholder never made it into the reply
    }
    return true;
}

// Split a model reply into translated lines.
std::vector<std::string> split_lines(const std::string &s) {
    std::vector<std::string> out;
    size_t p = 0;
    const size_t n = s.size();
    while (p <= n) {
        size_t q = s.find('\n', p);
        std::string line =
            s.substr(p, q == std::string::npos ? std::string::npos : q - p);
        if (!line.empty() && line.back() == '\r') line.pop_back();
        out.push_back(line);
        if (q == std::string::npos) break;
        p = q + 1;
    }
    return out;
}

// ---- 上下文粘连分句 (设计二.1) ----
// A code point's length in bytes at position `i` (1 for ASCII / malformed).
size_t utf8_len_at(const std::string &s, size_t i, size_t n) {
    unsigned char c = static_cast<unsigned char>(s[i]);
    if (c < 0x80) return 1;
    if ((c & 0xE0) == 0xC0 && i + 1 < n) return 2;
    if ((c & 0xF0) == 0xE0 && i + 2 < n) return 3;
    if ((c & 0xF8) == 0xF0 && i + 3 < n) return 4;
    return 1;
}

// True when position `i` is a sentence-final mark (ASCII .!? or CJK 。！？)
// followed by whitespace or end of text — a natural chunk boundary.
bool sentence_end_at(const std::string &s, size_t i, size_t n) {
    size_t len = utf8_len_at(s, i, n);
    if (len == 1) {
        char c = s[i];
        if (c != '.' && c != '!' && c != '?') return false;
    } else if (len == 3) {
        unsigned char c0 = static_cast<unsigned char>(s[i]);
        unsigned char c1 = static_cast<unsigned char>(s[i + 1]);
        unsigned char c2 = static_cast<unsigned char>(s[i + 2]);
        bool cjk_period = c0 == 0xE3 && c1 == 0x80 && c2 == 0x82; // 。
        bool cjk_bang = c0 == 0xEF && c1 == 0xBC && c2 == 0x81;   // ！
        bool cjk_question = c0 == 0xEF && c1 == 0xBC && c2 == 0x9F; // ？
        if (!cjk_period && !cjk_bang && !cjk_question) return false;
    } else {
        return false;
    }
    size_t j = i + len;
    return j >= n || s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r';
}

// A chunk of over-long text: the text to translate, plus the separator that
// joined it to the next chunk in the original (so chunking never changes the
// line structure — line cuts keep "\n", sentence/word cuts keep " ").
struct Piece {
    std::string text;
    std::string sep;
};

// Split `text` into pieces of at most `budget` bytes, cutting preferentially at
// a line boundary, then after sentence-final punctuation, then at a space, and
// finally hard at the budget (backed off to a code-point boundary). Order is
// preserved; rejoining the pieces' translations with each piece's `sep`
// reconstructs the original sentence flow.
std::vector<Piece> split_pieces(const std::string &text, size_t budget) {
    std::vector<Piece> pieces;
    const size_t n = text.size();
    size_t start = 0;
    while (start < n) {
        size_t limit = std::min(start + budget, n);
        std::string sep = " ";
        size_t cut;
        if (limit == n) {
            cut = n;
            sep = "";
        } else {
            size_t line_cut = std::string::npos;
            size_t sent_cut = std::string::npos;
            size_t space_cut = std::string::npos;
            size_t i = start;
            while (i < limit) {
                size_t len = utf8_len_at(text, i, n);
                if (text[i] == '\n') line_cut = i + 1;
                else if (sentence_end_at(text, i, n)) sent_cut = i + len;
                else if (text[i] == ' ' || text[i] == '\t' || text[i] == '\r')
                    space_cut = i + 1;
                i += len;
            }
            if (line_cut != std::string::npos && line_cut - start >= budget / 2) {
                cut = line_cut;
                sep = "\n";
            } else if (sent_cut != std::string::npos && sent_cut - start >= budget / 2) {
                cut = sent_cut;
                sep = " ";
            } else if (space_cut != std::string::npos && space_cut - start >= budget / 2) {
                cut = space_cut;
                sep = " ";
            } else {
                cut = limit; // hard cut at the budget, at a code-point boundary
                while (cut > start && cut < n &&
                       (static_cast<unsigned char>(text[cut]) & 0xC0) == 0x80) --cut;
                sep = " ";
            }
        }
        std::string piece = text.substr(start, cut - start);
        size_t b = piece.find_first_not_of(" \t\r\n");
        size_t e = piece.find_last_not_of(" \t\r\n");
        if (b == std::string::npos) {
            pieces.push_back({"", sep});
        } else {
            pieces.push_back({piece.substr(b, e - b + 1), sep});
        }
        start = cut;
    }
    return pieces;
}

// ---- 模型专属后验过滤器 (设计三.2) ----
// Each provider's known "bad habits" map to a post-filter config. Swapping the
// model only changes which row is loaded; the pipeline logic is unchanged.
struct FilterConfig {
    bool newline_guard;      // restore dropped line breaks (Qwen's habit)
    bool strip_explanations; // remove model-added "译文：" style labels (GPT)
    bool repunct;            // force half-width delimiters for structured logs
};
constexpr FilterConfig kFilters[] = {
    {true, true, true}, // Zhipu · GLM-4-Flash
    {true, true, true}, // SiliconFlow · Qwen2.5-7B-Instruct
};

// Strip a leading explanation label the model sometimes prepends despite the
// prompt ("译文：", "Translation:", …).
std::string strip_explanations(const std::string &s) {
    std::string r = trim(s);
    const char *prefixes[] = {"译文：", "译文:", "翻译：", "翻译:", "翻译如下：",
                              "以下是翻译：", "Translation:", "翻译结果："};
    for (const char *p : prefixes) {
        size_t len = std::strlen(p);
        if (r.size() >= len && r.compare(0, len, p) == 0) {
            r = trim(r.substr(len));
            break;
        }
    }
    return r;
}

// ---- 标点强制归位 (设计二.4b) ----
// Weak models convert half-width punctuation to full-width when writing
// Chinese, which destroys structured sources (CSV rows, log columns, paths):
// "nginx[812]:" became "nginx[812]：" in real runs. Force the full-width
// counterparts back to half-width — per class, and only when the source line
// actually uses that class: delimiters , : ; (>= 2 occurrences), parens, and
// double quotes. Sentence-final 。 is left full-width (narrative style).
std::string repunct(const std::string &out, const std::string &src) {
    size_t half = 0;
    bool has_paren = false, has_dquote = false;
    for (char c : src) {
        if (c == ',' || c == ':' || c == ';') ++half;
        else if (c == '(' || c == ')') has_paren = true;
        else if (c == '"') has_dquote = true;
    }
    if (half < 2 && !has_paren && !has_dquote) return out;
    std::string r;
    r.reserve(out.size());
    const size_t n = out.size();
    size_t i = 0;
    while (i < n) {
        if (i + 2 < n && static_cast<unsigned char>(out[i]) == 0xEF &&
            static_cast<unsigned char>(out[i + 1]) == 0xBC) {
            unsigned char b3 = static_cast<unsigned char>(out[i + 2]);
            if (half >= 2 && b3 == 0x8C) { r += ','; i += 3; continue; }  // ，-> ,
            if (half >= 2 && b3 == 0x9A) { r += ':'; i += 3; continue; }  // ：-> :
            if (half >= 2 && b3 == 0x9B) { r += ';'; i += 3; continue; }  // ；-> ;
            if (has_paren && b3 == 0x88) { r += '('; i += 3; continue; }  // （-> (
            if (has_paren && b3 == 0x89) { r += ')'; i += 3; continue; }  // ）-> )
        }
        if (has_dquote && i + 2 < n &&
            static_cast<unsigned char>(out[i]) == 0xE2 &&
            static_cast<unsigned char>(out[i + 1]) == 0x80) {
            unsigned char b3 = static_cast<unsigned char>(out[i + 2]);
            if (b3 == 0x9C || b3 == 0x9D) { r += '"'; i += 3; continue; } // “”-> "
        }
        r += out[i];
        ++i;
    }
    return r;
}

// Strip leading AND trailing punctuation (ASCII and full-width counterparts)
// from a token. Used by the preservation check: models turn a token's edge
// punctuation full-width ("8080." -> "8080。") or drop wrapping parens when
// they reorder a line ("at UserList (src/x.ts:1)" -> "在 src/x.ts:1 的
// UserList"); the token BODY must still survive verbatim.
std::string strip_edge_punct(const std::string &tok) {
    std::string r = tok;
    auto trim_end = [&r]() {
        for (;;) {
            if (r.empty()) return false;
            unsigned char c = static_cast<unsigned char>(r.back());
            if (c == '.' || c == ',' || c == ':' || c == ';' || c == '!' ||
                c == '?' || c == ')' || c == ']' || c == '}' || c == '"' ||
                c == '\'') {
                r.pop_back();
                continue;
            }
            if (r.size() >= 3) {
                unsigned char c0 = static_cast<unsigned char>(r[r.size() - 3]);
                unsigned char c1 = static_cast<unsigned char>(r[r.size() - 2]);
                unsigned char c2 = c;
                if (c0 == 0xEF && c1 == 0xBC) { // ，：；！？（）
                    r.resize(r.size() - 3);
                    continue;
                }
                if (c0 == 0xE2 && c1 == 0x80 &&
                    (c2 == 0x9C || c2 == 0x9D)) { // “ ”
                    r.resize(r.size() - 3);
                    continue;
                }
            }
            return true;
        }
    };
    for (;;) {
        if (!trim_end()) break;      // trailing edge
        if (r.empty()) break;
        unsigned char c = static_cast<unsigned char>(r.front());
        if (c == '(' || c == '[' || c == '{' || c == '"' || c == '\'') {
            r.erase(r.begin());      // leading edge
            continue;
        }
        if (r.size() >= 3 &&
            static_cast<unsigned char>(r[0]) == 0xEF &&
            static_cast<unsigned char>(r[1]) == 0xBC) { // （［｛
            r.erase(0, 3);
            continue;
        }
        break;
    }
    return r;
}

// ---- 置信度评分 (设计五) ----
// Safety net: every protected token from the source must survive verbatim in
// the output. Structure is preserved by construction, so a miss is a pipeline
// bug; returning the original beats emitting broken text. A token whose only
// difference is trailing punctuation width ("8080." vs "8080。") still counts
// as preserved.
bool protected_tokens_preserved(const std::string &src, const std::string &out) {
    size_t i = 0;
    const size_t n = src.size();
    while (i < n) {
        while (i < n && (src[i] == ' ' || src[i] == '\t' || src[i] == '\r' ||
                         src[i] == '\n')) {
            ++i;
        }
        const size_t s = i;
        while (i < n && src[i] != ' ' && src[i] != '\t' && src[i] != '\r' &&
               src[i] != '\n') {
            ++i;
        }
        if (i == s) break;
        std::string tok = src.substr(s, i - s);
        if (!is_protected_token(tok)) continue;
        if (out.find(tok) != std::string::npos) continue;
        std::string core = strip_edge_punct(tok);
        if (!core.empty() && out.find(core) != std::string::npos) continue;
        return false;
    }
    return true;
}

// Quality guard (设计：置信度评分 + 保守模式). Flags a translated prose line
// that the weak model produced as stutter, a system-prompt echo, or gibberish;
// such lines are replaced with the original span text so the output is never
// corrupted — only left untranslated.
bool is_bad_translation(const std::string &text) {
    std::string s = trim(text);
    if (s.empty()) return true;
    const size_t n = s.size();
    // Prompt echo: markers that would never appear inside a translation.
    // Must match every user-prompt wording the kernel sends ("Translate each
    // of the following N lines...", "Translate the following single line...",
    // and the prose path's "Translate the following text...").
    if (s.find("翻译以下内容") != std::string::npos ||
        s.find("禁止使用繁体字") != std::string::npos ||
        s.find("Translate each line") != std::string::npos ||
        s.find("Translate the following") != std::string::npos ||
        s.find("please translate") != std::string::npos) {
        return true;
    }
    // Stutter: the head of the line repeats consecutively (Qwen's degenerate
    // loop produces "BuildingBuilding C...", "连接连接C连接CXX...").
    for (size_t L = 2; L * 2 <= n && L < 64; ++L) {
        if (s.compare(0, L, s, L, L) == 0) return true;
    }
    // Gibberish: three identical CJK characters in a row, or a high share of
    // characters that are neither CJK, ASCII-printable, nor common punctuation.
    size_t total_cp = 0, bad_cp = 0, run_len = 0;
    uint32_t prev_cp = 0;
    size_t i = 0;
    while (i < n) {
        unsigned char c = static_cast<unsigned char>(s[i]);
        uint32_t cp;
        size_t len;
        if (c < 0x80) { cp = c; len = 1; }
        else if ((c & 0xE0) == 0xC0 && i + 1 < n) {
            cp = ((c & 0x1Fu) << 6) | (s[i + 1] & 0x3Fu); len = 2;
        } else if ((c & 0xF0) == 0xE0 && i + 2 < n) {
            cp = ((c & 0x0Fu) << 12) | ((s[i + 1] & 0x3Fu) << 6) | (s[i + 2] & 0x3Fu);
            len = 3;
        } else if ((c & 0xF8) == 0xF0 && i + 3 < n) {
            cp = ((c & 0x07u) << 18) | ((s[i + 1] & 0x3Fu) << 12) |
                 ((s[i + 2] & 0x3Fu) << 6) | (s[i + 3] & 0x3Fu);
            len = 4;
        } else {
            cp = 0xFFFD; len = 1; // invalid byte / replacement char
        }
        i += len;
        ++total_cp;
        bool is_cjk = cp >= 0x4E00 && cp <= 0x9FFF;
        bool good = is_cjk || (cp >= 0x3400 && cp <= 0x4DBF) ||
                    (cp >= 0x3040 && cp <= 0x30FF) || // kana
                    (cp >= 0xFF01 && cp <= 0xFF5E) || // full-width forms
                    (cp >= 0x3000 && cp <= 0x303F) || // CJK punctuation
                    (cp >= 0x20 && cp <= 0x7E) ||     // ASCII printable
                    cp == 0x2018 || cp == 0x2019 || cp == 0x201C || cp == 0x201D ||
                    cp == 0x2014 || cp == 0x2026 || cp == 0x00A0;
        if (!good) ++bad_cp;
        if (is_cjk) run_len = (cp == prev_cp) ? run_len + 1 : 1;
        else run_len = 0;
        if (run_len >= 3) return true; // "可可可可" style repetition
        prev_cp = cp;
    }
    if (total_cp == 0) return true;
    return bad_cp * 10 > total_cp * 2; // > 20% unrecognized characters
}

// Parse a user terminology dictionary ("term = translation", "term -> x", or
// "term → x", one per line) into a prompt snippet appended to the system prompt
// so the model forces consistent terminology. Only terms that appear verbatim
// in `context` are included — an absent term would only confuse a weak model
// into misapplying it elsewhere. Returns empty when nothing matches.
std::string build_glossary(const std::string &glossary, const std::string &context) {
    std::string out;
    size_t p = 0;
    const size_t n = glossary.size();
    while (p <= n) {
        size_t q = glossary.find('\n', p);
        std::string line =
            glossary.substr(p, q == std::string::npos ? std::string::npos : q - p);
        if (!line.empty() && line.back() == '\r') line.pop_back();
        std::string t = trim(line);
        if (!t.empty()) {
            size_t eq = t.find('=');
            size_t arrow = t.find("->");
            if (arrow == std::string::npos) arrow = t.find("→");
            size_t sep = std::min(eq, arrow);
            if (sep != std::string::npos) {
                std::string term = trim(t.substr(0, sep));
                std::string trans = trim(t.substr(sep + (sep == arrow ? 2 : 1)));
                if (!term.empty() && !trans.empty() &&
                    context.find(term) != std::string::npos) {
                    out += term + " = " + trans + "\n";
                }
            }
        }
        if (q == std::string::npos) break;
        p = q + 1;
    }
    if (out.empty()) return "";
    return "\n\nUse exactly these term translations:\n" + out;
}

// Run the adapter with up to kMaxAttempts total attempts. Deltas are forwarded
// from every attempt (the UI overwrites with the running result, so a retry
// just restarts the overwrite). The first ok result is delivered; if every
// attempt fails, the last error is delivered. `done` is called exactly once.
// "API 限流" errors (429 / rate-limit wording) are transient and retried with
// exponential backoff; auth and other provider faults ("API 错误") are not.
void translate_with_retry(ITranslator &adapter, const TranslateRequest &req,
                          DeltaCallback delta, Callback done) {
    constexpr int kMaxAttempts = 3;
    bool delivered = false;
    for (int attempt = 0; attempt < kMaxAttempts && !delivered; ++attempt) {
        if (attempt > 0) {
            const int backoff_ms = 400 << (attempt - 1);
            const int jitter = std::rand() % 250;
            std::this_thread::sleep_for(
                std::chrono::milliseconds(backoff_ms + jitter));
        }
        Result r;
        adapter.translate(req, delta, [&](const Result &res) { r = res; });
        const bool throttle = r.error.find("API 限流") != std::string::npos;
        const bool hard_error = r.error.find("API 错误") != std::string::npos;
        const bool retryable = !r.ok && (throttle || !hard_error) &&
                               attempt < kMaxAttempts - 1;
        delivered = r.ok || !retryable;
        if (delivered) done(r);
    }
}

// ---- In-process LRU result cache (设计：本地缓存索引) ----
// Keyed by a hash of (source, target, provider, glossary). Identical text that
// is translated again — a log pasted twice, an auto-translate re-fire after
// reverting an edit — returns the cached result instantly and, more importantly,
// deterministically, so the same input never yields a different translation
// across runs (the model is stochastic). Cap bounds memory.
uint64_t fnv1a64(const std::string &s) {
    uint64_t h = 1469598103934665603ULL;
    for (unsigned char c : s) {
        h ^= c;
        h *= 1099511628211ULL;
    }
    return h;
}

class ResultCache {
public:
    explicit ResultCache(size_t cap) : cap_(cap) {}
    bool get(const std::string &key, std::string &out) {
        std::lock_guard<std::mutex> lk(mu_);
        auto it = map_.find(key);
        if (it == map_.end()) return false;
        order_.splice(order_.begin(), order_, it->second.second); // MRU
        out = it->second.first;
        return true;
    }
    void put(const std::string &key, const std::string &val) {
        std::lock_guard<std::mutex> lk(mu_);
        auto it = map_.find(key);
        if (it != map_.end()) {
            order_.splice(order_.begin(), order_, it->second.second);
            it->second.first = val;
            return;
        }
        order_.push_front(key);
        map_[key] = {val, order_.begin()};
        if (map_.size() > cap_) { // evict LRU
            map_.erase(order_.back());
            order_.pop_back();
        }
    }

private:
    size_t cap_;
    std::mutex mu_;
    std::list<std::string> order_; // most-recently-used at the front
    std::unordered_map<
        std::string, std::pair<std::string, std::list<std::string>::iterator>>
        map_;
};

// Process-lifetime cache shared across translate() calls.
ResultCache &result_cache() {
    static ResultCache cache(64);
    return cache;
}

std::string make_cache_key(Provider provider, Target target,
                           const std::string &src, const std::string &glossary) {
    std::string s = src;
    s += '\x01';
    s += std::to_string(static_cast<int>(provider));
    s += '\x01';
    s += std::to_string(static_cast<int>(target));
    s += '\x01';
    s += glossary;
    char buf[17];
    std::snprintf(buf, sizeof buf, "%016llx",
                  static_cast<unsigned long long>(fnv1a64(s)));
    return buf;
}

} // namespace

void Translator::translate(Provider provider,
                           Target target,
                           const std::string &api_key,
                           const std::string &glossary,
                           const std::string &text,
                           Callback cb,
                           DeltaCallback delta_cb,
                           std::string *debug_route) {
    auto set_route = [debug_route](const std::string &r) {
        if (debug_route) *debug_route = r;
    };
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
    std::string glossary_block = build_glossary(glossary, src);

    // In-process cache: identical input returns the identical, deterministic
    // result immediately, and never lets the stochastic model produce a
    // different translation for the same text across runs.
    const std::string ckey = make_cache_key(provider, target, src, glossary);
    std::string cached;
    if (result_cache().get(ckey, cached)) {
        set_route("cache");
        cb({true, cached, ""});
        return;
    }
    auto wrapped_cb = [cb, ckey](const Result &r) {
        if (r.ok) result_cache().put(ckey, r.text);
        cb(r);
    };

    size_t pIdx = static_cast<size_t>(provider);
    if (pIdx >= 2) pIdx = 0; // Provider { Zhipu, SiliconFlow }
    size_t tIdx = static_cast<size_t>(target);
    if (tIdx >= sizeof(kPromptName) / sizeof(kPromptName[0])) tIdx = 0;

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
            target_name = kPromptName[tIdx];
            constraints = "";
            break;
    }

    auto adapter = create_translator(static_cast<Provider>(pIdx));

    // ---- Routing ----
    // Text with any technical token goes through the deterministic splice
    // pipeline: the model receives only prose spans (never numbers, paths, or
    // brackets), so structure is preserved by the code regardless of model
    // fidelity. Pure prose streams as before.
    const FilterConfig &filter = kFilters[pIdx];
    bool has_tech = false;
    analyze_technical(src, has_tech);
    if (!has_tech) {
        std::string system_prompt =
            "You are a professional translator. Translate the user's text "
            "exactly into the requested target language, in natural, fluent "
            "phrasing. Always provide a translation; never echo or return the "
            "original text unchanged. "
            "Preserve verbatim every non-language token exactly as written: "
            "numbers, percent signs, brackets, file paths, code identifiers, "
            "URLs, and command names. Never reword, reorder, drop, or recompute "
            "them. Keep the exact line structure and translate the text "
            "completely without truncating or omitting any line. Output only the "
            "translated text, with no explanations, notes, examples, or "
            "quotation marks. " +
            constraints + glossary_block;
        auto emit_prose = [wrapped_cb, target, filter](const std::string &raw) {
            std::string res = trim(raw);
            if (filter.strip_explanations) res = strip_explanations(res);
            wrapped_cb({true, normalize_if_chinese(res, target), ""});
        };
        // 二.1: chunk over-long prose at line / sentence boundaries so no single
        // request crosses the context window; translate sequentially and rejoin
        // with the original separators (deltas stream the running result).
        constexpr size_t kProseChunkBudget = 1500;
        if (src.size() > kProseChunkBudget) {
            std::vector<Piece> pieces = split_pieces(src, kProseChunkBudget);
            set_route("prose-chunked(" + std::to_string(pieces.size()) + " pieces)");
            std::string result;
            for (size_t i = 0; i < pieces.size(); ++i) {
                TranslateRequest req{
                    key, system_prompt,
                    "Translate the following text into " + target_name +
                        ". Provide only the translation:\n\n" + pieces[i].text};
                Result rr;
                translate_with_retry(*adapter, req, nullptr,
                                     [&](const Result &r) { rr = r; });
                if (!rr.ok) {
                    wrapped_cb(rr);
                    return;
                }
                result += rr.text;
                if (i + 1 < pieces.size()) result += pieces[i].sep;
                if (delta_cb) delta_cb(result); // running full result so far
            }
            if (!result.empty()) emit_prose(result);
            return;
        }
        set_route("prose");
        std::string user_prompt =
            "Translate the following text into " + target_name +
            ". Provide only the translation:\n\n" + src;
        TranslateRequest req{key, system_prompt, user_prompt};
        translate_with_retry(*adapter, req, delta_cb,
                             [wrapped_cb, target, filter](const Result &r) {
            if (r.ok) {
                std::string res = trim(r.text);
                if (filter.strip_explanations) res = strip_explanations(res);
                wrapped_cb({true, normalize_if_chinese(res, target), ""});
            } else {
                wrapped_cb(r);
            }
        });
        return;
    }

    // ---- Splice mode v2 (technical tokens present) ----
    // The unit is a WHOLE line. v1 carved prose tokens out of technical lines
    // and translated the fragments in isolation; out of context a weak model
    // either failed them (build logs fell back wholesale — run 2026-09-16 r1,
    // log1: translated 0/4) or mistranslated ("field" -> "田地"). v2 sends full
    // lines, instructs the model to keep technical tokens verbatim and in
    // place, and verifies afterwards: every protected token of a line must
    // survive in its translation, else that line falls back to the original.
    // Structure is preserved by one-line-in / one-line-out alignment.
    std::vector<LineInfo> vlines;
    {
        size_t pos = 0;
        for (;;) {
            size_t nl = src.find('\n', pos);
            std::string line = src.substr(
                pos, nl == std::string::npos ? std::string::npos : nl - pos);
            if (!line.empty() && line.back() == '\r') line.pop_back();
            LineInfo li;
            li.text = std::move(line);
            li.has_prose = line_has_prose(li.text);
            vlines.push_back(std::move(li));
            if (nl == std::string::npos) break;
            pos = nl + 1;
        }
    }
    size_t prose_lines = 0;
    for (const auto &li : vlines) {
        if (li.has_prose) ++prose_lines;
    }
    if (prose_lines == 0) {
        // Entirely technical (a config, a pure build-log header): nothing to
        // translate — return the source verbatim, structure intact.
        set_route("splice-verbatim(no prose)");
        wrapped_cb({true, normalize_if_chinese(src, target), ""});
        return;
    }
    set_route("splice(started " + std::to_string(prose_lines) + " lines)");
    // Unique translation units: each prose line becomes one unit (split at
    // sentence boundaries first when it exceeds the context budget). Weak
    // models drop or merge repeated lines, which would break count alignment,
    // so only unique units are sent and each line maps back to its unit(s).
    constexpr size_t kContextBudget = 1500;
    std::vector<std::string> uniq;                     // unit SENT to the model
    std::vector<std::string> uniq_orig;                // original unit text
    std::vector<std::vector<std::string>> uniq_tokens; // placeholder -> token
    std::vector<char> uniq_masked;                     // 1 = restore on reply
    std::unordered_map<std::string, size_t> uniq_index;
    std::vector<std::vector<int>> line_units(vlines.size()); // line -> uniq ids
    auto add_unit = [&](const std::string &t) {
        auto it = uniq_index.find(t);
        if (it == uniq_index.end()) {
            it = uniq_index.emplace(t, uniq.size()).first;
            std::string masked;
            std::vector<std::string> toks;
            const bool m = mask_unit(t, masked, toks);
            uniq.push_back(m ? masked : t);
            uniq_orig.push_back(t);
            uniq_tokens.push_back(std::move(toks));
            uniq_masked.push_back(m ? 1 : 0);
        }
        return static_cast<int>(it->second);
    };
    for (size_t li = 0; li < vlines.size(); ++li) {
        if (!vlines[li].has_prose) continue;
        if (vlines[li].text.size() <= kContextBudget) {
            line_units[li].push_back(add_unit(vlines[li].text));
        } else {
            for (const auto &p : split_pieces(vlines[li].text, kContextBudget)) {
                line_units[li].push_back(add_unit(p.text));
            }
        }
    }
    std::string system_prompt =
        "You are a professional translator for software developers. The user's "
        "text contains log, terminal, or documentation lines. Numbered "
        "placeholders like [[1]] stand in for technical tokens (file paths, "
        "code identifiers, commands, flags, numbers, timestamps, error codes): "
        "copy every placeholder EXACTLY as written, in the position where its "
        "content belongs, and translate only the natural-language parts into "
        "the requested target language. Output exactly one translated line per "
        "input line, in the same order. Your reply MUST contain exactly the "
        "same number of lines as the input: never merge two input lines into "
        "one, never split one input line across two output lines, never reflow "
        "or wrap a long line, never add or drop a line break. Never translate, "
        "renumber, or drop placeholders; output only the translations, without "
        "numbering or added explanation.";
    // Worked examples in the SAME masked form the model actually sees —
    // small models (GLM-4-Flash / Qwen-7B) follow examples far better than
    // abstract rules.
    if (target == Target::Chinese) {
        system_prompt +=
            " Example — input line: \"[[1]] cannot open [[2]] Permission "
            "denied\"; output line: \"[[1]] 无法打开 [[2]] 权限被拒绝\". "
            "Another example — input line: \"[[1]] Cannot read properties of "
            "undefined [[2]] 'map')\"; output line: \"[[1]] 无法读取未定义的"
            "属性 [[2]] 'map')\".";
    }
    system_prompt += " " + constraints;
    // The glossary is added per group (see below) so only terms actually
    // present in a group's lines are injected — an absent term would send a
    // weak model looking for it in every line and corrupt unrelated ones.

    // Translate the unique lines in small groups, with bounded parallelism (a
    // simultaneous burst invites provider rate limits, which would then need
    // retries and stall the whole batch). A dropped or merged line in one
    // group only costs that group — the rest still translates; each failed
    // line keeps its original text and goes through per-line rescue below.
    // Each worker does its own blocking HTTP call and writes only its own
    // result slot, so no locks are needed beyond joining the threads.
    constexpr size_t kLineGroupSize = 5;
    constexpr size_t kMaxConcurrent = 3;
    const size_t nGroups = (uniq.size() + kLineGroupSize - 1) / kLineGroupSize;
    std::vector<std::string> uniq_translations = uniq_orig; // default: original
    std::vector<char> uniq_translated(uniq.size(), 0);
    {
        std::vector<Result> group_results(nGroups);
        std::vector<std::vector<std::string>> group_lines(nGroups);
        std::vector<std::string> group_blocks(nGroups);
        std::vector<std::thread> workers;
        workers.reserve(nGroups);
        ITranslator *adapter_ptr = adapter.get();
        // Minimal counting semaphore (C++17: no std::counting_semaphore).
        struct Semaphore {
            explicit Semaphore(size_t n) : count(n) {}
            std::mutex mu;
            std::condition_variable cv;
            size_t count;
            void acquire() {
                std::unique_lock<std::mutex> lk(mu);
                cv.wait(lk, [this] { return count > 0; });
                --count;
            }
            void release() {
                {
                    std::lock_guard<std::mutex> lk(mu);
                    ++count;
                }
                cv.notify_one();
            }
        } sem(kMaxConcurrent);
        for (size_t gi = 0; gi < nGroups; ++gi) {
            const size_t g = gi * kLineGroupSize;
            const size_t gEnd = std::min(g + kLineGroupSize, uniq.size());
            std::string block;
            for (size_t k = g; k < gEnd; ++k) block += uniq[k] + "\n";
            group_blocks[gi] = block;
            const size_t group_n = gEnd - g;
            TranslateRequest req{key,
                                 system_prompt + build_glossary(glossary, block),
                                 "Translate each of the following " +
                                     std::to_string(group_n) +
                                     " lines into " + target_name +
                                     ":\n\n" + block};
            workers.emplace_back([adapter_ptr, req = std::move(req), &group_results,
                                  &group_lines, gi, &sem]() {
                sem.acquire();
                Result rr;
                translate_with_retry(*adapter_ptr, req, nullptr,
                                     [&](const Result &r) { rr = r; });
                sem.release();
                group_results[gi] = rr;
                if (rr.ok) group_lines[gi] = split_lines(rr.text);
            });
        }
        for (auto &w : workers) w.join();

        // Quality triage (TRANSLATE_DEBUG=1): dump each group's request block
        // and raw reply to stderr so a failing group can be diagnosed offline.
        const bool trace = std::getenv("TRANSLATE_DEBUG") != nullptr;

        // Per-line validation shared by group triage and the rescue phase:
        // placeholder restore / punctuation normalization / quality guards /
        // token preservation — plus two rejections the older guards missed,
        // which let untranslated English pass every check and surface as
        // half-English output: the reply echoing the source line, and a CJK
        // target whose reply contains no CJK character at all.
        const bool want_cjk = target == Target::Chinese ||
                              target == Target::Japanese ||
                              target == Target::Korean;
        auto has_cjk = [](const std::string &s) {
            size_t i = 0;
            while (i < s.size()) {
                unsigned char c = static_cast<unsigned char>(s[i]);
                uint32_t cp;
                size_t len;
                if (c < 0x80) { cp = c; len = 1; }
                else if ((c & 0xE0) == 0xC0 && i + 1 < s.size()) {
                    cp = ((c & 0x1Fu) << 6) | (s[i + 1] & 0x3Fu); len = 2;
                } else if ((c & 0xF0) == 0xE0 && i + 2 < s.size()) {
                    cp = ((c & 0x0Fu) << 12) | ((s[i + 1] & 0x3Fu) << 6) |
                         (s[i + 2] & 0x3Fu);
                    len = 3;
                } else if ((c & 0xF8) == 0xF0 && i + 3 < s.size()) {
                    cp = ((c & 0x07u) << 18) | ((s[i + 1] & 0x3Fu) << 12) |
                         ((s[i + 2] & 0x3Fu) << 6) | (s[i + 3] & 0x3Fu);
                    len = 4;
                } else { cp = 0xFFFD; len = 1; }
                i += len;
                if ((cp >= 0x4E00 && cp <= 0x9FFF) ||
                    (cp >= 0x3400 && cp <= 0x4DBF) ||
                    (cp >= 0x3040 && cp <= 0x30FF) ||
                    (cp >= 0xAC00 && cp <= 0xD7AF)) {
                    return true;
                }
            }
            return false;
        };
        auto validate_line = [&](size_t k, const std::string &reply_line,
                                 std::string &out) -> bool {
            const std::string &orig = uniq_orig[k];
            const std::string rt = trim(reply_line);
            if (rt.empty() || rt == trim(orig)) {
                // Echo of the source = untranslated — except a line already
                // written in the target language legitimately passes through
                // unchanged (mixed-language input, or an English target fed
                // English text): then the echo IS the correct translation.
                bool already_target =
                    (want_cjk && has_cjk(orig)) ||
                    (target == Target::English &&
                     orig.find_first_not_of(
                         " \t\r\n!\"#$%&'()*+,-./0123456789:;<=>?@"
                         "ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`"
                         "abcdefghijklmnopqrstuvwxyz{|}~") ==
                         std::string::npos);
                if (!rt.empty() && already_target) {
                    out = orig;
                    return true;
                }
                if (trace) {
                    std::fprintf(stderr,
                                 "[splice] line %zu REJECT echo <<<%s>>>\n",
                                 k, rt.c_str());
                }
                return false;
            }
            std::string candidate;
            bool ok_line;
            if (uniq_masked[k]) {
                // Masked unit: restore [[N]] placeholders verbatim; any
                // missing/malformed placeholder fails the whole line.
                ok_line = restore_unit(reply_line, uniq_tokens[k], candidate);
                if (ok_line) {
                    candidate = repunct(candidate, orig);
                    ok_line = !is_bad_translation(candidate);
                }
                if (trace) {
                    std::fprintf(stderr,
                                 "[splice] line %zu %s masked <<<%s>>>\n",
                                 k, ok_line ? "ACCEPT" : "REJECT",
                                 ok_line ? candidate.c_str() : rt.c_str());
                }
            } else {
                // Unmasked unit (no protected tokens): punctuation
                // normalization + quality guard + verbatim check.
                candidate = repunct(reply_line, uniq[k]);
                bool bad = is_bad_translation(candidate);
                bool kept = protected_tokens_preserved(orig, candidate);
                ok_line = !bad && kept;
                if (trace) {
                    if (kept) {
                        std::fprintf(stderr,
                                     "[splice] line %zu %s bad=%d <<<%s>>>\n",
                                     k, ok_line ? "ACCEPT" : "REJECT",
                                     bad ? 1 : 0, candidate.c_str());
                    } else {
                        // locate the first missing token for offline triage
                        std::string miss;
                        size_t p = 0;
                        const std::string &line = orig;
                        while (p < line.size()) {
                            while (p < line.size() &&
                                   (line[p] == ' ' || line[p] == '\t' ||
                                    line[p] == '\r'))
                                ++p;
                            size_t s0 = p;
                            while (p < line.size() && line[p] != ' ' &&
                                   line[p] != '\t' && line[p] != '\r' &&
                                   line[p] != '\n')
                                ++p;
                            if (p == s0) break;
                            std::string tok = line.substr(s0, p - s0);
                            if (!is_protected_token(tok)) continue;
                            if (candidate.find(tok) != std::string::npos)
                                continue;
                            std::string core = strip_edge_punct(tok);
                            if (!core.empty() &&
                                candidate.find(core) != std::string::npos)
                                continue;
                            miss = tok;
                            break;
                        }
                        std::fprintf(stderr,
                                     "[splice] line %zu REJECT token-miss "
                                     "<<<%s>>> in <<<%s>>>\n", k,
                                     miss.c_str(), candidate.c_str());
                    }
                }
            }
            if (ok_line && want_cjk && !has_cjk(candidate)) {
                ok_line = false;
                if (trace) {
                    std::fprintf(stderr,
                                 "[splice] line %zu REJECT no-cjk <<<%s>>>\n",
                                 k, candidate.c_str());
                }
            }
            if (ok_line) out = candidate;
            return ok_line;
        };

        std::string first_error; // surfaced only when EVERY group failed
        size_t ok_groups = 0;
        for (size_t gi = 0; gi < nGroups; ++gi) {
            Result &rr = group_results[gi];
            if (trace) {
                std::fprintf(stderr,
                             "[splice] group %zu/%zu ok=%d\n--- block ---\n%s"
                             "--- reply ---\n%s--- end ---\n",
                             gi + 1, nGroups, rr.ok ? 1 : 0,
                             group_blocks[gi].c_str(),
                             rr.ok ? rr.text.c_str() : rr.error.c_str());
            }
            if (!rr.ok) {
                if (first_error.empty()) first_error = rr.error;
                continue; // a failed group's lines go through rescue below
            }
            ++ok_groups;
            const size_t g = gi * kLineGroupSize;
            const size_t gEnd = std::min(g + kLineGroupSize, uniq.size());
            // Align the reply to this group by line count; drop empty lines so
            // a leading/trailing blank line does not break alignment. A count
            // mismatch (merged/split/wrapped lines) fails the whole group —
            // guessing an alignment could translate the wrong line — and the
            // group's units fall through to the per-line rescue below.
            std::vector<std::string> &tr = group_lines[gi];
            tr.erase(std::remove_if(tr.begin(), tr.end(),
                                    [](const std::string &l) { return trim(l).empty(); }),
                     tr.end());
            if (tr.size() != gEnd - g) continue;
            for (size_t k = g; k < gEnd; ++k) {
                std::string candidate;
                if (validate_line(k, tr[k - g], candidate)) {
                    uniq_translations[k] = candidate;
                    uniq_translated[k] = 1;
                }
            }
        }
        if (ok_groups == 0 && !first_error.empty()) {
            wrapped_cb({false, "", first_error});
            return;
        }

        // Rescue: every unit still untranslated — its group failed at the
        // HTTP level, the reply misaligned with the group's line count, or
        // the line was rejected by validation — is retried as an individual
        // single-line request. One line per request cannot be merged with
        // neighbours or dropped, so alignment is trivial; a reply wrapped
        // across several lines is rejoined. Sequential on purpose: this is
        // the exception path, and it runs only for units the group pass
        // failed.
        for (size_t k = 0; k < uniq.size(); ++k) {
            if (uniq_translated[k]) continue;
            TranslateRequest req{key,
                                 system_prompt + build_glossary(glossary, uniq[k]),
                                 "Translate the following single line into " +
                                     target_name +
                                     ". Output only the translation:\n\n" +
                                     uniq[k] + "\n"};
            Result rr;
            translate_with_retry(*adapter_ptr, req, nullptr,
                                 [&](const Result &r) { rr = r; });
            if (!rr.ok) continue;
            std::vector<std::string> rl = split_lines(rr.text);
            rl.erase(std::remove_if(rl.begin(), rl.end(),
                                    [](const std::string &l) { return trim(l).empty(); }),
                     rl.end());
            std::string joined;
            for (const auto &l : rl) {
                if (!joined.empty()) joined += ' ';
                joined += l;
            }
            std::string candidate;
            if (validate_line(k, joined, candidate)) {
                uniq_translations[k] = candidate;
                uniq_translated[k] = 1;
            }
            if (trace) {
                std::fprintf(stderr, "[splice] rescue line %zu %s <<<%s>>>\n",
                             k, uniq_translated[k] ? "OK" : "FAIL",
                             uniq_translations[k].c_str());
            }
        }
    }
    // Fail loudly instead of returning a mixed translated/English result:
    // after group attempts and per-line rescue, any surviving untranslated
    // unit means the model could not produce a verified translation for it,
    // and a half-translated log is exactly what this pipeline must never
    // emit (user decision: complete translation or an explicit error).
    size_t translated_count =
        static_cast<size_t>(std::count(uniq_translated.begin(), uniq_translated.end(), 1));
    if (translated_count < uniq.size()) {
        set_route("splice(incomplete translated " +
                  std::to_string(translated_count) + "/" +
                  std::to_string(uniq.size()) + ")");
        wrapped_cb({false, "", "翻译失败：有 " +
                                  std::to_string(uniq.size() - translated_count) +
                                  " 行多次重试后仍未完成翻译，请重试"});
        return;
    }
    set_route("splice(translated " + std::to_string(translated_count) + "/" +
                  std::to_string(uniq.size()) + ")");
    // Reassemble: technical-only lines verbatim, prose lines from their unit
    // translations (untranslated units already hold the original text).
    std::string out;
    for (size_t li = 0; li < vlines.size(); ++li) {
        if (!vlines[li].has_prose) {
            out += vlines[li].text;
        } else {
            std::string joined;
            for (const int u : line_units[li]) {
                if (!joined.empty()) joined += " ";
                joined += uniq_translations[static_cast<size_t>(u)];
            }
            out += joined;
        }
        if (li + 1 < vlines.size()) out += '\n';
    }
    if (filter.repunct) out = repunct(out, src);
    // Safety net: every technical token must still be present verbatim. The
    // per-line checks above already enforce this; a whole-text miss is a
    // pipeline bug, and returning untranslated source would hide it.
    if (!protected_tokens_preserved(src, out)) {
        set_route("splice(token-miss, translated " +
                  std::to_string(translated_count) + "/" +
                  std::to_string(uniq.size()) + ")");
        wrapped_cb({false, "", "翻译失败：译文丢失了源文本中的技术标记，请重试"});
        return;
    }
    wrapped_cb({true, normalize_if_chinese(trim(out), target), ""});
}

} // namespace translate