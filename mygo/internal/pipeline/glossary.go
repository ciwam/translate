package pipeline

import "strings"

// BuildGlossary parses a user terminology dictionary ("term = translation",
// "term -> x", or "term → x", one per line) into a prompt snippet appended
// to the system prompt so the model forces consistent terminology. Only
// terms that appear verbatim in context are included — an absent term would
// only confuse a weak model into misapplying it elsewhere. Returns empty
// when nothing matches. Port of build_glossary (translator.cpp:625).
func BuildGlossary(glossary, context string) string {
	var out strings.Builder
	p := 0
	n := len(glossary)
	for {
		q := strings.IndexByte(glossary[p:], '\n')
		var line string
		if q < 0 {
			line = glossary[p:]
		} else {
			line = glossary[p : p+q]
		}
		line = strings.TrimSuffix(line, "\r")
		t := strings.Trim(line, " \t\r\n")
		if t != "" {
			eq := strings.IndexByte(t, '=')
			arrow := strings.Index(t, "->")
			if arrow < 0 {
				arrow = strings.Index(t, "→")
			}
			sep, sepLen := eq, 1
			if arrow >= 0 && (eq < 0 || arrow < eq) {
				sep, sepLen = arrow, 2
			}
			if sep >= 0 {
				term := strings.Trim(t[:sep], " \t\r\n")
				trans := strings.Trim(t[sep+sepLen:], " \t\r\n")
				if term != "" && trans != "" && strings.Contains(context, term) {
					out.WriteString(term + " = " + trans + "\n")
				}
			}
		}
		if q < 0 {
			break
		}
		p += q + 1
		if p > n {
			break
		}
	}
	s := out.String()
	if s == "" {
		return ""
	}
	return "\n\nUse exactly these term translations:\n" + s
}
