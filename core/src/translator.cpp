#include "translator.hpp"

#include "itranslator.hpp"
#include "normalize_icu.hpp"

#include <algorithm>
#include <cstdio>
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
            c == '\\' || c == '[' || c == ']' || c == ':' || c == '=' ||
            c == '<' || c == '>' || c == '$' || c == '{' || c == '}' ||
            c == '@' || c == '#' || c == '&' || c == '+' || c == '*' ||
            c == '~' || c == '^' || c == '|' || c == '`') {
            technical = true;
        } else if ((c == '.' || c == ',' || c == ';' || c == '(' || c == ')' ||
                    c == '!' || c == '?') &&
                   i + 1 < n) {
            technical = true; // internal: CSV/extension/call, not sentence-final
        }
    }
    // Mixed-case identifier (NSFont, CFType, TranslateViewController). Pure
    // ALL-CAPS acronyms (CXX, OBJC) stay prose: the model already keeps them
    // verbatim inside spans, and carving them out shrinks spans into fragments
    // the model tends to drop.
    if (upper >= 2 && lower >= 1) technical = true;
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

// One whitespace-bounded token of a source line. Protected tokens are kept
// verbatim; prose tokens carry the index of the span that translates them.
struct Seg {
    std::string text; // whitespace prefix + token (or a whitespace-only run)
    bool protected_token = false;
    int span = -1; // index into the prose span list when prose, else -1
};

// Split `src` into lines and segments. Protected tokens become standalone
// segments (spliced verbatim on reassembly); contiguous prose tokens are
// grouped into spans. Returns the trimmed text of each prose span, in order.
// The model never sees this structure — it receives only the span texts.
std::vector<std::string> build_lines(const std::string &src,
                                     std::vector<std::vector<Seg>> &out_lines) {
    out_lines.clear();
    std::vector<std::string> spans;
    size_t pos = 0;
    const size_t n = src.size();
    for (;;) {
        size_t nl = src.find('\n', pos);
        std::string line =
            src.substr(pos, nl == std::string::npos ? std::string::npos : nl - pos);
        if (!line.empty() && line.back() == '\r') line.pop_back();
        std::vector<Seg> segs;
        size_t i = 0;
        const size_t L = line.size();
        while (i < L) {
            size_t wb = i;
            while (i < L && is_ws_char(line[i])) ++i;
            std::string ws = line.substr(wb, i - wb);
            if (i == L) { // trailing whitespace stays attached to the last seg
                if (!segs.empty()) segs.back().text += ws;
                else if (!ws.empty()) segs.push_back({ws, false, -1});
                break;
            }
            size_t tb = i;
            while (i < L && !is_ws_char(line[i])) ++i;
            std::string tok = line.substr(tb, i - tb);
            segs.push_back({ws + tok, is_protected_token(tok), -1});
        }
        // Group contiguous prose tokens into spans.
        for (size_t k = 0; k < segs.size();) {
            if (segs[k].protected_token) { ++k; continue; }
            size_t e = k;
            std::string combined;
            while (e < segs.size() && !segs[e].protected_token) {
                combined += segs[e].text;
                ++e;
            }
            if (combined.find_first_not_of(" \t\r") == std::string::npos) {
                k = e; // whitespace-only run: no translatable prose
                continue;
            }
            size_t b = combined.find_first_not_of(" \t\r");
            size_t en = combined.find_last_not_of(" \t\r");
            int id = static_cast<int>(spans.size());
            spans.push_back(combined.substr(b, en - b + 1));
            for (size_t q = k; q < e; ++q) segs[q].span = id;
            k = e;
        }
        out_lines.push_back(std::move(segs));
        if (nl == std::string::npos) break;
        pos = nl + 1;
    }
    return spans;
}

// Rebuild the translated text: protected tokens verbatim, each prose span
// replaced by its translation (falling back to the original span text when the
// translation is empty or was rejected by the quality guard).
std::string reassemble(const std::vector<std::vector<Seg>> &lines,
                       const std::vector<std::string> &spans,
                       const std::vector<std::string> &translations) {
    std::string out;
    for (const auto &segs : lines) {
        for (size_t i = 0; i < segs.size(); ++i) {
            const Seg &seg = segs[i];
            if (!seg.protected_token && seg.span >= 0) {
                if (i == 0 || segs[i - 1].span != seg.span) { // span start
                    size_t m = 0;
                    while (m < seg.text.size() && is_ws_char(seg.text[m])) ++m;
                    out.append(seg.text, 0, m); // leading whitespace
                    const std::string &tr = translations[static_cast<size_t>(seg.span)];
                    out += tr.empty() ? spans[static_cast<size_t>(seg.span)] : tr;
                }
                // continuation segments of a span are covered by the one piece
            } else {
                out += seg.text;
            }
        }
        out += '\n';
    }
    return out;
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
// Sources that use half-width delimiters (CSV rows, JSON, log columns) must keep
// them: force the spliced translation's full-width comma/colon/semicolon back to
// half-width so the format survives. Sentence-final periods are left full-width
// (narrative style, not structure). Triggered only when the source already uses
// >= 2 half-width delimiters.
std::string repunct(const std::string &out, const std::string &src) {
    size_t half = 0;
    for (char c : src) {
        if (c == ',' || c == ':' || c == ';') ++half;
    }
    if (half < 2) return out;
    std::string r;
    r.reserve(out.size());
    const size_t n = out.size();
    size_t i = 0;
    while (i < n) {
        if (i + 2 < n && static_cast<unsigned char>(out[i]) == 0xEF &&
            static_cast<unsigned char>(out[i + 1]) == 0xBC) {
            unsigned char b3 = static_cast<unsigned char>(out[i + 2]);
            if (b3 == 0x8C) { r += ','; i += 3; continue; }  // ，-> ,
            if (b3 == 0x9A) { r += ':'; i += 3; continue; }  // ：-> :
            if (b3 == 0x9B) { r += ';'; i += 3; continue; }  // ；-> ;
        }
        r += out[i];
        ++i;
    }
    return r;
}

// ---- 置信度评分 (设计五) ----
// Safety net: every protected token from the source must survive verbatim in
// the spliced output. Structure is preserved by construction, so a miss is a
// pipeline bug; returning the original beats emitting broken text.
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
        if (is_protected_token(tok) && out.find(tok) == std::string::npos) {
            return false;
        }
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
    if (s.find("翻译以下内容") != std::string::npos ||
        s.find("禁止使用繁体字") != std::string::npos ||
        s.find("Translate each line") != std::string::npos ||
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
void translate_with_retry(ITranslator &adapter, const TranslateRequest &req,
                          DeltaCallback delta, Callback done) {
    constexpr int kMaxAttempts = 3;
    bool delivered = false;
    for (int attempt = 0; attempt < kMaxAttempts && !delivered; ++attempt) {
        Result r;
        adapter.translate(req, delta, [&](const Result &res) { r = res; });
        bool retryable = !r.ok && r.error.find("API 错误") == std::string::npos &&
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
    std::string glossary_block = build_glossary(glossary, src);

    // In-process cache: identical input returns the identical, deterministic
    // result immediately, and never lets the stochastic model produce a
    // different translation for the same text across runs.
    const std::string ckey = make_cache_key(provider, target, src, glossary);
    std::string cached;
    if (result_cache().get(ckey, cached)) {
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
            "exactly into the requested target language. Always provide a "
            "translation; never echo or return the original text unchanged. "
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

    // ---- Splice mode (technical tokens present) ----
    std::vector<std::vector<Seg>> lines;
    std::vector<std::string> spans = build_lines(src, lines);
    if (spans.empty()) {
        // Entirely technical (a config, a pure build-log header): nothing to
        // translate — return the source verbatim, structure intact.
        wrapped_cb({true, normalize_if_chinese(src, target), ""});
        return;
    }
    // 二.1: a single prose span beyond the context window is split at sentence
    // boundaries into pieces; each piece is an independent translation unit and
    // the pieces are joined back in order so sentence flow is preserved.
    constexpr size_t kContextBudget = 1500;
    std::vector<std::string> pieces;           // flattened translation units
    std::vector<std::vector<int>> span_pieces; // span -> piece indices
    span_pieces.reserve(spans.size());
    for (const auto &sp : spans) {
        if (sp.size() <= kContextBudget) {
            span_pieces.push_back({static_cast<int>(pieces.size())});
            pieces.push_back(sp);
        } else {
            std::vector<Piece> ps = split_pieces(sp, kContextBudget);
            span_pieces.emplace_back();
            for (const auto &p : ps) {
                span_pieces.back().push_back(static_cast<int>(pieces.size()));
                pieces.push_back(p.text);
            }
        }
    }
    // Deduplicate the pieces: weak models drop or merge repeated lines, which
    // would break count alignment, so send only the unique prose and map each
    // piece back to its unique block line when splicing.
    std::vector<std::string> uniq;
    std::unordered_map<std::string, size_t> uniq_index;
    for (const auto &pc : pieces) {
        if (uniq_index.find(pc) == uniq_index.end()) {
            uniq_index[pc] = uniq.size();
            uniq.push_back(pc);
        }
    }
    std::string system_prompt =
        "You are a professional translator. The user's text contains one prose "
        "fragment per line. Translate each fragment into the requested target "
        "language. Output exactly one line of translation per input line, in "
        "the same order, without numbering, quotation marks, or any added "
        "explanation. Never merge, split, reorder, or omit lines; output only "
        "the translations. " +
        constraints;
    // The glossary is added per group (see below) so only terms actually
    // present in a group's fragments are injected — an absent term would send
    // a weak model looking for it in every line and corrupt unrelated ones.

    // Translate the unique pieces in small groups, in parallel. The model
    // occasionally drops or merges a line (short trailing fragments are the
    // usual victim); a single all-or-nothing request would then fall the whole
    // document back untranslated, so a dropped line in one group only costs
    // that group's prose — the rest is still spliced. Structure is always
    // preserved. Each worker does its own blocking HTTP call and writes only
    // its own result slot, so no locks are needed beyond joining the threads.
    constexpr size_t kSpanGroupSize = 5;
    const size_t nGroups = (uniq.size() + kSpanGroupSize - 1) / kSpanGroupSize;
    std::vector<std::string> uniq_translations = uniq; // default: original text
    std::vector<char> uniq_translated(uniq.size(), 0);
    {
        std::vector<Result> group_results(nGroups);
        std::vector<std::vector<std::string>> group_lines(nGroups);
        std::vector<std::thread> workers;
        workers.reserve(nGroups);
        ITranslator *adapter_ptr = adapter.get();
        for (size_t gi = 0; gi < nGroups; ++gi) {
            const size_t g = gi * kSpanGroupSize;
            const size_t gEnd = std::min(g + kSpanGroupSize, uniq.size());
            std::string block;
            for (size_t k = g; k < gEnd; ++k) block += uniq[k] + "\n";
            TranslateRequest req{key,
                                 system_prompt + build_glossary(glossary, block),
                                 "Translate each line into " + target_name +
                                     ":\n\n" + block};
            workers.emplace_back([adapter_ptr, req = std::move(req), &group_results,
                                  &group_lines, gi]() {
                Result rr;
                translate_with_retry(*adapter_ptr, req, nullptr,
                                     [&](const Result &r) { rr = r; });
                group_results[gi] = rr;
                if (rr.ok) group_lines[gi] = split_lines(rr.text);
            });
        }
        for (auto &w : workers) w.join();

        for (size_t gi = 0; gi < nGroups; ++gi) {
            Result &rr = group_results[gi];
            if (!rr.ok) {
                wrapped_cb(rr);
                return;
            }
            const size_t g = gi * kSpanGroupSize;
            const size_t gEnd = std::min(g + kSpanGroupSize, uniq.size());
            // Align the reply to this group by line count; drop empty lines so
            // a leading/trailing blank line does not break alignment.
            std::vector<std::string> &tr = group_lines[gi];
            tr.erase(std::remove_if(tr.begin(), tr.end(),
                                    [](const std::string &l) { return trim(l).empty(); }),
                     tr.end());
            if (tr.size() != gEnd - g) continue; // group stays as the original
            for (size_t k = g; k < gEnd; ++k) {
                std::string candidate = tr[k - g];
                if (!is_bad_translation(candidate)) {
                    uniq_translations[k] = candidate;
                    uniq_translated[k] = 1;
                }
            }
        }
    }
    // 置信度评分 (设计五): when the model actually translated too few of the
    // unique pieces, the output would be mostly untranslated English — return
    // the original instead (conservative mode). Structure is never degraded.
    size_t translated_count =
        static_cast<size_t>(std::count(uniq_translated.begin(), uniq_translated.end(), 1));
    const double rate =
        uniq.empty() ? 0.0 : static_cast<double>(translated_count) / uniq.size();
    if (rate < 0.5) {
        wrapped_cb({true, normalize_if_chinese(src, target), ""});
        return;
    }
    // Map each span to its pieces' translations joined in order; untranslated
    // pieces keep the original text (already in uniq_translations).
    std::vector<std::string> translations(spans.size());
    for (size_t i = 0; i < spans.size(); ++i) {
        std::string joined;
        for (const int pc : span_pieces[i]) {
            const std::string &t = uniq_translations[uniq_index.at(pieces[pc])];
            if (!joined.empty()) joined += " ";
            joined += t;
        }
        translations[i] = joined;
    }
    std::string out = reassemble(lines, spans, translations);
    if (filter.repunct) out = repunct(out, src);
    // Safety net: every technical token must still be present verbatim.
    if (!protected_tokens_preserved(src, out)) {
        wrapped_cb({true, normalize_if_chinese(src, target), ""});
        return;
    }
    wrapped_cb({true, normalize_if_chinese(trim(out), target), ""});
}

} // namespace translate