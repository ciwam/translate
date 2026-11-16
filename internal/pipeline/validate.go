package pipeline

import (
	"strings"
	"unicode/utf8"
)

// StripEdgePunct strips leading AND trailing punctuation (ASCII and
// full-width counterparts) from a token. Used by the preservation check:
// models turn a token's edge punctuation full-width ("8080." -> "8080。")
// or drop wrapping parens when they reorder a line ("at UserList
// (src/x.ts:1)" -> "在 src/x.ts:1 的 UserList"); the token BODY must still
// survive verbatim. Port of strip_edge_punct (translator.cpp:470).
func StripEdgePunct(tok string) string {
	r := tok
	trimEnd := func() bool {
		for {
			if r == "" {
				return false
			}
			c := r[len(r)-1]
			if c == '.' || c == ',' || c == ':' || c == ';' || c == '!' ||
				c == '?' || c == ')' || c == ']' || c == '}' || c == '"' || c == '\'' {
				r = r[:len(r)-1]
				continue
			}
			if len(r) >= 3 {
				c0, c1, c2 := r[len(r)-3], r[len(r)-2], c
				if c0 == 0xEF && c1 == 0xBC { // ，：；！？（）
					r = r[:len(r)-3]
					continue
				}
				if c0 == 0xE2 && c1 == 0x80 && (c2 == 0x9C || c2 == 0x9D) { // “ ”
					r = r[:len(r)-3]
					continue
				}
			}
			return true
		}
	}
	for {
		if !trimEnd() {
			break // trailing edge
		}
		if r == "" {
			break
		}
		c := r[0]
		if c == '(' || c == '[' || c == '{' || c == '"' || c == '\'' {
			r = r[1:] // leading edge
			continue
		}
		if len(r) >= 3 && r[0] == 0xEF && r[1] == 0xBC { // （［｛
			r = r[3:]
			continue
		}
		break
	}
	return r
}

// ProtectedTokensPreserved is the safety net: every protected token from the
// source must survive verbatim in the output. Structure is preserved by
// construction, so a miss is a pipeline bug; returning the original beats
// emitting broken text. A token whose only difference is trailing
// punctuation width ("8080." vs "8080。") still counts as preserved.
// Port of protected_tokens_preserved (translator.cpp:526).
func ProtectedTokensPreserved(src, out string) bool {
	return protectedTokensPreservedMode(src, out, false)
}

// ProtectedTokensPreservedLoose is the tolerant variant for the MT wire
// path. Translation-specialized models naturally drift inside tokens:
// case flips ("handle.Focused(c)" -> "handlefocused(c)"), plural stripping
// ("APIs" -> "API"), and hyphen-suffix splits ("MyGo-specific" -> "MyGo").
// A token counts as preserved when the output contains it modulo case, a
// trailing plural 's', or a hyphenated suffix. Exact matching remains the
// rule for the masked protocol, where the model promised verbatim copies.
func ProtectedTokensPreservedLoose(src, out string) bool {
	return protectedTokensPreservedMode(src, out, true)
}

// tokenVariants enumerates the tolerated spellings of a token: itself
// lowercased, without a trailing plural 's', its hyphen/underscore/dot head,
// and — for slash-joined chains ("A()/B()/C()") — each slash-separated part
// (the model may rejoin them with Chinese enumeration commas).
func tokenVariants(tok string) []string {
	low := strings.ToLower(tok)
	variants := []string{low}
	if strings.HasSuffix(low, "s") && len(low) > 3 {
		variants = append(variants, low[:len(low)-1])
	}
	for _, sep := range []string{"-", "_", "."} {
		if idx := strings.Index(low, sep); idx > 0 {
			variants = append(variants, low[:idx])
		}
	}
	if strings.Contains(low, "/") {
		for _, part := range strings.Split(low, "/") {
			part = strings.Trim(part, ".,;:!?()[]'\"`")
			if len(part) >= 4 {
				variants = append(variants, part)
			}
		}
	}
	return variants
}

func protectedTokensPreservedMode(src, out string, looseCase bool) bool {
	cmpOut := out
	if looseCase {
		cmpOut = strings.ToLower(out)
		// Thousand separators: models render 257628 as "257,628"; compare
		// against a comma-stripped copy so "257628" still matches.
		cmpOut = stripDigitCommas(cmpOut)
	}
	i, n := 0, len(src)
	for i < n {
		for i < n && (src[i] == ' ' || src[i] == '\t' || src[i] == '\r' || src[i] == '\n') {
			i++
		}
		s := i
		for i < n && src[i] != ' ' && src[i] != '\t' && src[i] != '\r' && src[i] != '\n' {
			i++
		}
		if i == s {
			break
		}
		tok := src[s:i]
		if !IsProtectedToken(tok) {
			continue
		}
		needle := tok
		if looseCase {
			needle = strings.ToLower(tok)
		}
		if strings.Contains(cmpOut, needle) {
			continue
		}
		core := StripEdgePunct(needle)
		if core != "" && strings.Contains(cmpOut, core) {
			continue
		}
		if looseCase {
			matched := false
			for _, v := range tokenVariants(needle) {
				if v != "" && strings.Contains(cmpOut, v) {
					matched = true
					break
				}
			}
			if matched {
				continue
			}
			// Number-anchor fallback: a token whose content is dominated by
			// a number ("(99.4% reduction).") is preserved when that number
			// survives — the model legitimately translates the surrounding
			// prose word ("reduction" -> "减少").
			if anchor := numericAnchor(needle); anchor != "" && strings.Contains(cmpOut, anchor) {
				continue
			}
		}
		return false
	}
	return true
}

// stripDigitCommas removes commas between digits ("257,628" -> "257628"),
// leaving punctuation attached to words untouched.
func stripDigitCommas(s string) string {
	if !strings.Contains(s, ",") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ',' && i > 0 && i+1 < len(s) && s[i-1] >= '0' && s[i-1] <= '9' && s[i+1] >= '0' && s[i+1] <= '9' {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// numericAnchor returns the longest digit run in s ("" when none).
func numericAnchor(s string) string {
	best, cur := "", ""
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			cur += string(s[i])
			if len(cur) > len(best) {
				best = cur
			}
		} else {
			cur = ""
		}
	}
	if len(best) >= 2 {
		return best
	}
	return ""
}

// IsBadTranslation flags a translated prose line that the weak model
// produced as stutter, a system-prompt echo, or gibberish; such lines are
// replaced with the original span text so the output is never corrupted —
// only left untranslated. Port of is_bad_translation (translator.cpp:558).
func IsBadTranslation(text string) bool {
	s := strings.Trim(text, " \t\r\n")
	if s == "" {
		return true
	}
	n := len(s)
	// Prompt echo: markers that would never appear inside a translation.
	// Must match every user-prompt wording the kernel sends.
	for _, marker := range []string{
		"翻译以下内容", "禁止使用繁体字", "Translate each line",
		"Translate the following", "please translate",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	// Stutter: the head of the line repeats consecutively (Qwen's degenerate
	// loop produces "BuildingBuilding C...", "连接连接C连接CXX..."; Hunyuan
	// produces "Apple Apple" — case-insensitive so spaced variants catch
	// too).
	lower := strings.ToLower(s)
	for l := 2; l*2 <= n && l < 64; l++ {
		if lower[:l] == lower[l:l*2] {
			return true
		}
	}
	// Gibberish: three identical CJK characters in a row, or a high share of
	// characters that are neither CJK, ASCII-printable, nor common
	// punctuation.
	totalCP, badCP, runLen := 0, 0, 0
	prevCP := uint32(0)
	for i := 0; i < n; {
		c := s[i]
		var cp uint32
		var l int
		switch {
		case c < 0x80:
			cp, l = uint32(c), 1
		case c&0xE0 == 0xC0 && i+1 < n:
			cp = (uint32(c&0x1F) << 6) | uint32(s[i+1]&0x3F)
			l = 2
		case c&0xF0 == 0xE0 && i+2 < n:
			cp = (uint32(c&0x0F) << 12) | (uint32(s[i+1]&0x3F) << 6) | uint32(s[i+2]&0x3F)
			l = 3
		case c&0xF8 == 0xF0 && i+3 < n:
			cp = (uint32(c&0x07) << 18) | (uint32(s[i+1]&0x3F) << 12) | (uint32(s[i+2]&0x3F) << 6) | uint32(s[i+3]&0x3F)
			l = 4
		default:
			cp, l = 0xFFFD, 1
		}
		i += l
		totalCP++
		isCJK := cp >= 0x4E00 && cp <= 0x9FFF
		good := isCJK ||
			(cp >= 0x3400 && cp <= 0x4DBF) ||
			(cp >= 0x3040 && cp <= 0x30FF) || // kana
			(cp >= 0xFF01 && cp <= 0xFF5E) || // full-width forms
			(cp >= 0x3000 && cp <= 0x303F) || // CJK punctuation
			(cp >= 0x20 && cp <= 0x7E) || // ASCII printable
			cp == 0x2018 || cp == 0x2019 || cp == 0x201C || cp == 0x201D ||
			cp == 0x2014 || cp == 0x2026 || cp == 0x00A0
		if !good {
			badCP++
		}
		if isCJK {
			if cp == prevCP {
				runLen++
			} else {
				runLen = 1
			}
		} else {
			runLen = 0
		}
		if runLen >= 3 {
			return true // "可可可可" style repetition
		}
		prevCP = cp
	}
	if totalCP == 0 {
		return true
	}
	return badCP*10 > totalCP*2 // > 20% unrecognized characters
}

// StripExplanations removes a leading explanation label the model sometimes
// prepends despite the prompt ("译文：", "Translation:", …). Port of
// strip_explanations (translator.cpp:431).
func StripExplanations(s string) string {
	r := strings.Trim(s, " \t\r\n")
	for _, p := range []string{
		"译文：", "译文:", "翻译：", "翻译:", "翻译如下：",
		"以下是翻译：", "Translation:", "翻译结果：",
	} {
		if strings.HasPrefix(r, p) {
			r = strings.Trim(r[len(p):], " \t\r\n")
			break
		}
	}
	return r
}

// Repunct forces the full-width counterparts back to half-width — per
// class, and only when the source line actually uses that class:
// delimiters , : ; (>= 2 occurrences), parens, and double quotes.
// Sentence-final 。 is left full-width (narrative style). Weak models
// convert half-width punctuation to full-width when writing Chinese, which
// destroys structured sources (CSV rows, log columns, paths):
// "nginx[812]:" became "nginx[812]：" in real runs.
// Port of repunct (translator.cpp:454).
func Repunct(out, src string) string {
	half := 0
	hasParen, hasDQuote := false, false
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case ',', ':', ';':
			half++
		case '(', ')':
			hasParen = true
		case '"':
			hasDQuote = true
		}
	}
	if half < 2 && !hasParen && !hasDQuote {
		return out
	}
	var b strings.Builder
	b.Grow(len(out))
	i, n := 0, len(out)
	for i < n {
		if i+2 < n && out[i] == 0xEF && out[i+1] == 0xBC {
			b3 := out[i+2]
			if half >= 2 && b3 == 0x8C {
				b.WriteByte(',')
				i += 3
				continue
			}
			if half >= 2 && b3 == 0x9A {
				b.WriteByte(':')
				i += 3
				continue
			}
			if half >= 2 && b3 == 0x9B {
				b.WriteByte(';')
				i += 3
				continue
			}
			if hasParen && b3 == 0x88 {
				b.WriteByte('(')
				i += 3
				continue
			}
			if hasParen && b3 == 0x89 {
				b.WriteByte(')')
				i += 3
				continue
			}
		}
		if hasDQuote && i+2 < n && out[i] == 0xE2 && out[i+1] == 0x80 {
			b3 := out[i+2]
			if b3 == 0x9C || b3 == 0x9D {
				b.WriteByte('"')
				i += 3
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(out[i:])
		b.WriteString(out[i : i+size])
		i += size
	}
	return b.String()
}

// HasCJK reports whether s contains at least one CJK/Hangul character.
// Port of the has_cjk lambda (translator.cpp:1197).
func HasCJK(s string) bool {
	for _, r := range s {
		if (r >= 0x4E00 && r <= 0x9FFF) ||
			(r >= 0x3400 && r <= 0x4DBF) ||
			(r >= 0x3040 && r <= 0x30FF) ||
			(r >= 0xAC00 && r <= 0xD7AF) {
			return true
		}
	}
	return false
}
