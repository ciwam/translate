// Package pipeline is the cross-platform translation kernel: a faithful
// port of core/src/translator.cpp (splice v2.1). All functions are pure
// UTF-8 string logic; the orchestration lives in pipeline.go.
package pipeline

import (
	"strings"
	"unicode/utf8"
)

// isWSChar mirrors the C++ is_ws_char.
func isWSChar(c byte) bool { return c == ' ' || c == '\t' || c == '\r' }

// IsProtectedToken reports whether tok is "technical": predominantly ASCII
// with a structural character, or a mixed-case identifier. Build-log
// prefixes, file paths, identifiers, percentages, flags, shell variables,
// template placeholders, CSV cells and camelCase names cannot be reproduced
// by weak models, so the pipeline never sends them to the model.
//
// Sentence-final punctuation (`. , ; ! ?` at a token edge) is ordinary
// prose; the same characters count as structural only when INTERNAL
// (file extension, CSV separator, call parentheses) — "hello." is prose,
// "a,b,c" and "file.tar.gz" are not.
//
// Port of is_protected_token (translator.cpp:44).
func IsProtectedToken(tok string) bool {
	ascii, upper, lower := 0, 0, 0
	technical := false
	n := len(tok)
	for i := 0; i < n; i++ {
		c := tok[i]
		if c < 0x80 {
			ascii++
			if c >= 'A' && c <= 'Z' {
				upper++
			} else if c >= 'a' && c <= 'z' {
				lower++
			}
		}
		switch c {
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9',
			'_', '/', '%', '\\', '[', ']', '=', '<', '>', '$',
			'{', '}', '@', '#', '&', '+', '*', '~', '^', '|', '`':
			technical = true
		case '(', ')':
			// Parentheses glued to a word's OUTER edge ("(maximum",
			// "characters)") are sentence punctuation the model may
			// translate around — masking them shred prose into fragments
			// (run: "Name is too long (maximum is 40 characters)" masked
			// "(maximum" as [[1]] and four valid translations were
			// rejected). Only INTERNAL parens count ("foo(bar", "f(x)y").
			if i > 0 && i+1 < n {
				technical = true
			}
		case '.', ',', ';', ':', '!', '?':
			if i+1 < n {
				technical = true // internal: CSV/extension/call/URL
			}
		}
	}
	// Mixed-case identifier (NSFont, CFType, TranslateViewController). Pure
	// ALL-CAPS acronyms (CXX, OBJC) stay prose: the model already keeps them
	// verbatim inside spans, and carving them out shrinks spans into
	// fragments the model tends to drop.
	if upper >= 2 && lower >= 1 {
		technical = true
	}
	// NOTE: word+colon labels ("ERROR:", "client:") are deliberately PROSE.
	// Protecting them froze whole log lines when the model translated the
	// label (run r3); placeholders now guard the truly structural tokens.
	return technical && ascii*2 > n
}

// AnalyzeTechnical classifies the whole source: true when any token is
// protected, routing between streaming prose translation and the splice
// pipeline. Port of analyze_technical (translator.cpp:94).
func AnalyzeTechnical(src string) bool {
	i, n := 0, len(src)
	for i < n {
		for i < n && (src[i] == ' ' || src[i] == '\t' || src[i] == '\r' || src[i] == '\n') {
			i++
		}
		start := i
		for i < n && src[i] != ' ' && src[i] != '\t' && src[i] != '\r' && src[i] != '\n' {
			i++
		}
		if i == start {
			break
		}
		if IsProtectedToken(src[start:i]) {
			return true // one protected token is enough to route to splice mode
		}
	}
	return false
}

// lineHasProse is true when the line contains at least one token the model
// should translate. Port of line_has_prose (translator.cpp:117).
func lineHasProse(line string) bool {
	i, n := 0, len(line)
	for i < n {
		for i < n && isWSChar(line[i]) {
			i++
		}
		s := i
		for i < n && !isWSChar(line[i]) {
			i++
		}
		if i == s {
			break
		}
		if !IsProtectedToken(line[s:i]) {
			return true
		}
	}
	return false
}

// MaskUnit replaces protected tokens with [[N]] placeholders, preserving all
// whitespace. Returns false when the unit holds no protected token or
// already contains "[[" (placeholder collision) — the caller then sends the
// unit unmasked.
//
// Even with few-shot anchoring, small models still translated protected
// tokens inside whole lines ("CrashLoopBackOff" -> "崩溃回环"). Masking
// makes token preservation structural instead of a matter of model
// discipline. Port of mask_unit (translator.cpp:149).
func MaskUnit(unit string) (masked string, tokens []string, ok bool) {
	if strings.Contains(unit, "[[") {
		return "", nil, false
	}
	var b strings.Builder
	b.Grow(len(unit))
	i, n := 0, len(unit)
	for i < n {
		ws := i
		for i < n && isWSChar(unit[i]) {
			i++
		}
		b.WriteString(unit[ws:i])
		if i >= n {
			break
		}
		s0 := i
		for i < n && !isWSChar(unit[i]) {
			i++
		}
		tok := unit[s0:i]
		if IsProtectedToken(tok) {
			// Keep sentence/quote characters OUT of the placeholder: a bare
			// number's sentence period ("… port 8080.") reads broken when
			// restored mid-sentence (run r6), and a swallowed closing quote
			// ("app-7d9f'") unbalances the masked line (run r7).
			body := tok
			tail := ""
			if len(body) >= 2 && body[len(body)-1] == '.' {
				prefix := body[:len(body)-1]
				if prefix != "" && strings.Trim(prefix, "0123456789") == "" {
					body = prefix
					tail = "."
				}
			}
			for len(body) >= 2 && (body[len(body)-1] == '\'' || body[len(body)-1] == '"') {
				tail = string(body[len(body)-1]) + tail
				body = body[:len(body)-1]
			}
			if body == "" { // degenerate: pure punctuation — keep verbatim
				b.WriteString(tok)
			} else {
				b.WriteString("[[" + itoa(len(tokens)+1) + "]]" + tail)
				tokens = append(tokens, body)
			}
		} else {
			b.WriteString(tok)
		}
	}
	return b.String(), tokens, len(tokens) > 0
}

// NormalizeFullwidthPunct maps the full-width punctuation small models like
// to emit back to ASCII, so placeholder scanning works regardless of the
// model's punctuation fashion. Port of normalize_fullwidth_punct
// (translator.cpp:196).
func NormalizeFullwidthPunct(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c0 := s[i]
		if c0 == 0xEF && i+2 < len(s) { // U+FF00 half-width forms block
			c1, c2 := s[i+1], s[i+2]
			if c1 == 0xBC {
				switch c2 {
				case 0x8C:
					b.WriteByte(',')
					i += 3
					continue // ，
				case 0x9A:
					b.WriteByte(':')
					i += 3
					continue // ：
				case 0x9B:
					b.WriteByte(';')
					i += 3
					continue // ；
				case 0x81:
					b.WriteByte('!')
					i += 3
					continue // ！
				case 0x9F:
					b.WriteByte('?')
					i += 3
					continue // ？
				case 0x88:
					b.WriteByte('(')
					i += 3
					continue // （
				case 0x89:
					b.WriteByte(')')
					i += 3
					continue // ）
				case 0xBB:
					b.WriteByte('[')
					i += 3
					continue // ［
				case 0xBD:
					b.WriteByte(']')
					i += 3
					continue // ］
				}
			}
		} else if c0 == 0xE3 && i+2 < len(s) { // CJK punctuation block
			c1, c2 := s[i+1], s[i+2]
			if c1 == 0x80 && c2 == 0x90 {
				b.WriteByte('[')
				i += 3
				continue // 【
			}
			if c1 == 0x80 && c2 == 0x91 {
				b.WriteByte(']')
				i += 3
				continue // 】
			}
		} else if c0 == 0xE2 && i+2 < len(s) {
			c1, c2 := s[i+1], s[i+2]
			if c1 == 0x80 && (c2 == 0x9C || c2 == 0x9D) {
				b.WriteByte('"')
				i += 3
				continue // “ ”
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		_ = r
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

// RestoreUnit restores [[N]] placeholders (after full-width normalization).
// Fails when ANY placeholder of the unit is missing, malformed, or out of
// range — the line then falls back to its original text, never to a
// half-restored hybrid. (Run r7: the model dropped [[1]] entirely and
// restore "succeeded", leaving "WARNING:" out of the output.)
//
// Port of restore_unit (translator.cpp:246).
func RestoreUnit(reply string, tokens []string) (string, bool) {
	s := NormalizeFullwidthPunct(reply)
	var b strings.Builder
	b.Grow(len(s))
	seen := make([]bool, len(tokens))
	i, n := 0, len(s)
	for i < n {
		if s[i] == '[' && i+1 < n && s[i+1] == '[' {
			e := strings.Index(s[i+2:], "]]")
			if e < 0 || e > 3 {
				return "", false
			}
			e += i + 2
			num := s[i+2 : e]
			if num == "" || strings.Trim(num, "0123456789") != "" {
				return "", false
			}
			idx := 0
			for _, d := range num {
				idx = idx*10 + int(d-'0')
				if idx > len(tokens) {
					return "", false
				}
			}
			if idx == 0 || idx > len(tokens) {
				return "", false
			}
			b.WriteString(tokens[idx-1])
			seen[idx-1] = true
			i = e + 2
		} else {
			_, size := utf8.DecodeRuneInString(s[i:])
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	for _, ok := range seen {
		if !ok {
			return "", false // a placeholder never made it into the reply
		}
	}
	return b.String(), true
}

// SplitLines splits a model reply into translated lines. Port of
// split_lines (translator.cpp:299).
func SplitLines(s string) []string {
	var out []string
	p := 0
	for {
		q := strings.IndexByte(s[p:], '\n')
		var line string
		if q < 0 {
			line = s[p:]
		} else {
			line = s[p : p+q]
		}
		line = strings.TrimSuffix(line, "\r")
		out = append(out, line)
		if q < 0 {
			break
		}
		p += q + 1
	}
	return out
}

// utf8LenAt is a code point's length in bytes at position i (1 for
// ASCII/malformed). Port of utf8_len_at (translator.cpp:324).
func utf8LenAt(s string, i int) int {
	c := s[i]
	switch {
	case c < 0x80:
		return 1
	case c&0xE0 == 0xC0 && i+1 < len(s):
		return 2
	case c&0xF0 == 0xE0 && i+2 < len(s):
		return 3
	case c&0xF8 == 0xF0 && i+3 < len(s):
		return 4
	}
	return 1
}

// sentenceEndAt is true when position i is a sentence-final mark (ASCII .!?
// or CJK 。！？) followed by whitespace or end of text — a natural chunk
// boundary. Port of sentence_end_at (translator.cpp:333).
func sentenceEndAt(s string, i int) bool {
	l := utf8LenAt(s, i)
	if l == 1 {
		c := s[i]
		if c != '.' && c != '!' && c != '?' {
			return false
		}
	} else if l == 3 {
		c0, c1, c2 := s[i], s[i+1], s[i+2]
		cjkPeriod := c0 == 0xE3 && c1 == 0x80 && c2 == 0x82 // 。
		cjkBang := c0 == 0xEF && c1 == 0xBC && c2 == 0x81   // ！
		cjkQuestion := c0 == 0xEF && c1 == 0xBC && c2 == 0x9F
		if !cjkPeriod && !cjkBang && !cjkQuestion {
			return false
		}
	} else {
		return false
	}
	j := i + l
	return j >= len(s) || s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r'
}

// Piece is a chunk of over-long text: the text to translate, plus the
// separator that joined it to the next chunk in the original (so chunking
// never changes the line structure — line cuts keep "\n", sentence/word
// cuts keep " ").
type Piece struct {
	Text string
	Sep  string
}

// SplitPieces splits text into pieces of at most budget bytes, cutting
// preferentially at a line boundary, then after sentence-final punctuation,
// then at a space, and finally hard at the budget (backed off to a
// code-point boundary). Order is preserved; rejoining the pieces'
// translations with each piece's Sep reconstructs the original sentence
// flow. Port of split_pieces (translator.cpp:364).
func SplitPieces(text string, budget int) []Piece {
	var pieces []Piece
	n := len(text)
	start := 0
	for start < n {
		limit := start + budget
		if limit > n {
			limit = n
		}
		sep := " "
		var cut int
		if limit == n {
			cut = n
			sep = ""
		} else {
			lineCut, sentCut, spaceCut := -1, -1, -1
			for i := start; i < limit; {
				l := utf8LenAt(text, i)
				if text[i] == '\n' {
					lineCut = i + 1
				} else if sentenceEndAt(text, i) {
					sentCut = i + l
				} else if text[i] == ' ' || text[i] == '\t' || text[i] == '\r' {
					spaceCut = i + 1
				}
				i += l
			}
			switch {
			case lineCut >= 0 && lineCut-start >= budget/2:
				cut, sep = lineCut, "\n"
			case sentCut >= 0 && sentCut-start >= budget/2:
				cut, sep = sentCut, " "
			case spaceCut >= 0 && spaceCut-start >= budget/2:
				cut, sep = spaceCut, " "
			default:
				cut = limit // hard cut at the budget, at a code-point boundary
				for cut > start && cut < n && text[cut]&0xC0 == 0x80 {
					cut--
				}
				sep = " "
			}
		}
		piece := text[start:cut]
		t := strings.Trim(piece, " \t\r\n")
		pieces = append(pieces, Piece{Text: t, Sep: sep})
		start = cut
	}
	return pieces
}

// itoa avoids strconv for tiny placeholder numbers.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
