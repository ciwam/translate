package pipeline

import (
	"strings"
	"sync"

	"ciwang/translate/internal/provider"
)

// translateSplice is Route B: the deterministic splice pipeline v2.1. The
// unit is a WHOLE line; technical-only lines are returned verbatim; every
// protected token is masked as a [[N]] placeholder; lines are sent in
// groups of 5 with max 3 concurrent workers; each reply line is validated
// (placeholder restore, punctuation, quality guards, token preservation);
// failed lines are retried as single-line rescue requests; incomplete
// output is a hard error, never mixed text.
//
// Port of the splice half of Translator::translate (translator.cpp:899-1333).
func translateSplice(sp spliceParams) Result {
	log := sp.log
	vlines := splitSourceLines(sp.src)
	proseLines := 0
	for _, vl := range vlines {
		if vl.hasProse {
			proseLines++
		}
	}
	if proseLines == 0 {
		// Entirely technical (a config, a pure build-log header): nothing to
		// translate — return the source verbatim, structure intact.
		sp.setRoute("splice-verbatim(no prose)")
		if log != nil {
			log.Infof("全部 %d 行均为技术行，无需翻译，原文返回", len(vlines))
		}
		return Result{OK: true, Text: normalizeIfChinese(sp.target, sp.src)}
	}
	sp.setRoute("splice(started " + itoa(proseLines) + " lines)")
	if log != nil {
		log.Infof("共 %d 行，其中 %d 行含可翻译内容", len(vlines), proseLines)
	}

	// Unique translation units: each prose line becomes one unit (split at
	// sentence boundaries first when it exceeds the context budget). Weak
	// models drop or merge repeated lines, which would break count
	// alignment, so only unique units are sent and each line maps back to
	// its unit(s).
	const contextBudget = 1500
	var uniq, uniqOrig []string
	var uniqTokens [][]string
	uniqMasked := make([]bool, 0)
	uniqIndex := make(map[string]int)
	var lineUnits [][]int
	addUnit := func(t string) int {
		if id, ok := uniqIndex[t]; ok {
			return id
		}
		masked, toks, m := MaskUnit(t)
		id := len(uniq)
		uniqIndex[t] = id
		if m {
			uniq = append(uniq, masked)
		} else {
			uniq = append(uniq, t)
		}
		uniqOrig = append(uniqOrig, t)
		uniqTokens = append(uniqTokens, toks)
		uniqMasked = append(uniqMasked, m)
		if log != nil {
			if m {
				log.Debugf("单元 #%d（掩码 %d 个 token）: %q", id, len(toks), masked)
			} else {
				log.Debugf("单元 #%d（未掩码）: %q", id, t)
			}
		}
		return id
	}
	// splitAtSentences adds text as translation units, splitting it at
	// sentence boundaries until every fragment re-masks to at most
	// maxUnitPlaceholders placeholders. Falls back to the whole text when
	// no clean sentence split exists. Units with many placeholders defeat
	// small models: they DROP middle placeholders deterministically (runs:
	// [[2]] swallowed by three separate replies; `0.x` split into two
	// placeholders). Splitting runs on the raw text because in masked form
	// sentence marks live INSIDE placeholders ("APIs." -> [[1]]) and no
	// split point would survive.
	splitAtSentences := func(text string) []int {
		var build func(string) []int
		build = func(s string) []int {
			_, toks, _ := MaskUnit(s)
			if len(toks) <= maxUnitPlaceholders {
				return []int{addUnit(s)}
			}
			mid := len(s) / 2
			best := -1
			bestDist := len(s) + 1
			for i := 0; i < len(s); i++ {
				if !rawSplitPoint(s, i) {
					continue
				}
				// The mark STAYS with the left fragment (split after it).
				left := strings.TrimRight(s[:i+1], " ")
				right := strings.TrimLeft(s[i+1:], " ")
				if left == "" || right == "" {
					continue
				}
				d := i - mid
				if d < 0 {
					d = -d
				}
				if d < bestDist {
					bestDist = d
					best = i
				}
			}
			if best < 0 {
				return []int{addUnit(s)} // no clean split: keep as one unit
			}
			out := []int{}
			out = append(out, build(strings.TrimRight(s[:best+1], " "))...)
			out = append(out, build(strings.TrimLeft(s[best+1:], " "))...)
			return out
		}
		return build(text)
	}
	for _, vl := range vlines {
		if !vl.hasProse {
			lineUnits = append(lineUnits, nil)
			continue
		}
		var units []int
		if len(vl.text) <= contextBudget {
			units = splitAtSentences(vl.text)
		} else {
			for _, p := range SplitPieces(vl.text, contextBudget) {
				units = append(units, splitAtSentences(p.Text)...)
			}
		}
		lineUnits = append(lineUnits, units)
	}

	// Two prompt styles with different WIRE FORMATS:
	//
	// - General instruct models (default): the masked form goes on the wire
	//   ([[N]] placeholders), the full system prompt teaches the protocol,
	//   and replies are restored from placeholders.
	//
	// - MT models (Hunyuan-MT): per their docs they are pure-text
	//   translators trained with no system prompt and no placeholder
	//   concept. Forcing the mask protocol on them degrades both fidelity
	//   and compliance (runs: invented glosses, bold markers, swallowed
	//   slots). So MT units go on the wire RAW (unmasked), with the
	//   official user-message template; correctness is enforced AFTER the
	//   reply by the same per-line guards (token preservation, bad-language
	//   checks, CJK presence) — a reply that drops or rewrites a technical
	//   token is rejected and retried, ultimately under the masked protocol
	//   as a fallback.
	systemPrompt := ""
	if !sp.mtStyle {
		systemPrompt = "You are a professional translator for software developers. The user's " +
			"text contains log, terminal, or documentation lines. Numbered " +
			"placeholders like [[1]] stand in for technical tokens (file paths, " +
			"code identifiers, commands, flags, numbers, timestamps, error codes): " +
			"copy every placeholder EXACTLY as written, in the position where its " +
			"content belongs, and translate only the natural-language parts into " +
			"the requested target language. Output exactly one translated line per " +
			"input line, in the same order. Your reply MUST contain exactly the " +
			"same number of lines as the input: never merge two input lines into " +
			"one, never split one input line across two output lines, never reflow " +
			"or wrap a long line, never add or drop a line break. Never translate, " +
			"renumber, or drop placeholders; output only the translations, without " +
			"numbering or added explanation."
		// Worked examples in the SAME masked form the model actually sees —
		// small models (GLM-4-Flash / Qwen-7B) follow examples far better than
		// abstract rules.
		if sp.target == Chinese {
			systemPrompt += " Example — input line: \"[[1]] cannot open [[2]] Permission " +
				"denied\"; output line: \"[[1]] 无法打开 [[2]] 权限被拒绝\". " +
				"Another example — input line: \"[[1]] Cannot read properties of " +
				"undefined [[2]] 'map')\"; output line: \"[[1]] 无法读取未定义的" +
				"属性 [[2]] 'map')\"."
		}
		systemPrompt += " " + sp.constraints
	}

	uniqTranslations := append([]string(nil), uniqOrig...) // default: original
	uniqTranslated := make([]bool, len(uniq))

	// Local pre-pass: units with nothing translatable are resolved in
	// process and never sent to the model — pure-symbol lines, key:value
	// entries with identifier values, glue-only fragments ("and [[1]] go
	// test [[2]]"), and fragments whose every prose word already matches
	// the target language. Each skipped unit is one fewer line in the
	// group requests, so groups also align more easily.
	untranslated := 0
	for k := 0; k < len(uniq); k++ {
		orig := uniqOrig[k]
		switch {
		case isLetterless(orig), isTechValueLine(orig),
			hasNoTranslatableContent(orig):
			uniqTranslations[k] = orig
			uniqTranslated[k] = true
			if log != nil {
				log.Infof("单元 #%d 无可译内容，本地放行（不请求模型）", k)
			}
		default:
			untranslated++
		}
	}
	if log != nil {
		log.Infof("本地放行 %d 个单元，%d 个交由模型翻译", len(uniq)-untranslated, untranslated)
	}

	// Translate the model-bound units in small groups, with bounded
	// parallelism. 3 lines per group: smaller groups align far more
	// reliably (the model merges/splits lines on longer groups, forcing
	// whole-group rescue), and with 4-way parallelism the extra requests
	// don't cost wall time. Groups index into toTranslate; group triage
	// and rescue below map their positions back to uniq ids.
	var toTranslate []int
	for k := 0; k < len(uniq); k++ {
		if !uniqTranslated[k] {
			toTranslate = append(toTranslate, k)
		}
	}
	const lineGroupSize = 3
	groups := (len(toTranslate) + lineGroupSize - 1) / lineGroupSize
	groupUniq := make([]string, len(toTranslate))
	for i, k := range toTranslate {
		// MT style sends RAW text (no placeholders on the wire); general
		// instruct sends the masked form.
		if sp.mtStyle {
			groupUniq[i] = uniqOrig[k]
		} else {
			groupUniq[i] = uniq[k]
		}
	}

	groupResults, groupLines := runGroupWorkers(sp, systemPrompt, groupUniq, groups, lineGroupSize)

	wantCJK := sp.target == Chinese || sp.target == 2 || sp.target == 3 // zh/ja/ko

	// Per-line validation shared by group triage and the rescue phase:
	// placeholder restore / punctuation normalization / quality guards /
	// token preservation — plus the two rejections the older guards missed:
	// the reply echoing the source line, and a CJK target whose reply
	// contains no CJK character at all.
	// validateWire checks one reply line. wireMasked: the line was sent in
	// the masked protocol (placeholders on the wire, restore from reply);
	// false: raw text on the wire (MT style), reply carries the tokens and
	// is verified by the preservation net.
	validateWire := func(k int, replyLine string, wireMasked bool) (string, bool) {
		reject := func(reason string) (string, bool) {
			if log != nil {
				log.Warnf("单元 #%d 校验拒绝（%s）: 回复 %q", k, reason, strings.Trim(replyLine, " \t\r\n"))
			}
			return "", false
		}
		orig := uniqOrig[k]
		rt := strings.Trim(replyLine, " \t\r\n")
		if rt == "" || rt == strings.Trim(orig, " \t\r\n") {
			// Echo of the source = untranslated — except a line already
			// written in the target language legitimately passes through
			// unchanged (mixed-language input, or an English target fed
			// English text): then the echo IS the correct translation.
			alreadyTarget := wantCJK && HasCJK(orig)
			// A line with no letters or digits ("---", "***", "| |") has
			// nothing to translate: the echo IS the correct translation.
			// (Run: YAML "---" echoed by the model was rejected as
			// untranslated and failed the whole translation.)
			alreadyTarget = alreadyTarget || isLetterless(orig)
			// A "key: value" line whose value is a technical token
			// ("name: mygo-maintenance", "version: 1.2.3") needs no
			// translation: the key is a field label and the value must stay
			// verbatim, so the echo IS the correct translation. (Run: the
			// model echoed this line every time and the run failed.)
			if !alreadyTarget && isTechValueLine(orig) {
				alreadyTarget = true
			}
			if !alreadyTarget && sp.target == 0 { // English target
				alreadyTarget = isPureASCII(orig)
			}
			if rt != "" && alreadyTarget {
				if log != nil {
					log.Debugf("单元 #%d 原文即目标语言，原文返回", k)
				}
				return orig, true
			}
			if rt == "" {
				return reject("空回复")
			}
			return reject("回声（未翻译）")
		}
		var candidate string
		var okLine bool
		var reason string
		if wireMasked && uniqMasked[k] {
			// Masked protocol: restore [[N]] placeholders verbatim; any
			// missing/malformed placeholder fails the line.
			candidate, okLine = RestoreUnit(replyLine, uniqTokens[k])
			if okLine {
				candidate = Repunct(candidate, orig)
				okLine = !IsBadTranslation(candidate)
				if !okLine {
					reason = "坏译文守卫"
				}
			} else {
				reason = "占位符恢复失败（缺失/畸形）"
			}
		} else {
			// Raw protocol (MT style, or an unmasked unit): punctuation
			// normalization + quality guard + verbatim token check. The MT
			// check is case-insensitive — translation models render tokens
			// with natural case drift ("handle.Focused(c)" ->
			// "handlefocused(c)"), and a case flip is not a loss.
			candidate = Repunct(replyLine, uniqOrig[k])
			bad := IsBadTranslation(candidate)
			kept := ProtectedTokensPreserved(uniqOrig[k], candidate)
			if !kept && sp.mtStyle {
				kept = ProtectedTokensPreservedLoose(uniqOrig[k], candidate)
			}
			okLine = !bad && kept
			switch {
			case bad:
				reason = "坏译文守卫"
			case !kept:
				reason = "技术 token 丢失"
			}
		}
		if okLine && wantCJK && !HasCJK(candidate) {
			// A fragment with no translatable content (masked away all
			// technical tokens; only glue words like "and", "or" remain —
			// run: "and [[1]] go test [[2]]" echoed by the model) cannot
			// produce CJK characters; the echo/original IS the correct
			// translation.
			if hasNoTranslatableContent(orig) {
				okLine = true
			} else {
				okLine = false
				reason = "无 CJK 字符"
			}
		}
		if okLine {
			if log != nil {
				log.Debugf("单元 #%d 校验通过: %q", k, candidate)
			}
			return candidate, true
		}
		return reject(reason)
	}
	// validateLine keeps the historical signature for the group triage:
	// the wire form is masked for general instruct, raw for MT.
	validateLine := func(k int, replyLine string) (string, bool) {
		return validateWire(k, replyLine, !sp.mtStyle)
	}

	// Group triage: a count mismatch (merged/split/wrapped lines) fails the
	// whole group — guessing an alignment could translate the wrong line —
	// and the group's units fall through to the per-line rescue below.
	firstError := ""
	okGroups := 0
	for gi := 0; gi < groups; gi++ {
		rr := groupResults[gi]
		if !rr.OK {
			if firstError == "" {
				firstError = rr.Error
			}
			continue
		}
		okGroups++
		g := gi * lineGroupSize
		gEnd := g + lineGroupSize
		if gEnd > len(toTranslate) {
			gEnd = len(toTranslate)
		}
		tr := groupLines[gi]
		// Drop empty lines so a leading/trailing blank line does not break
		// alignment.
		filtered := tr[:0]
		for _, l := range tr {
			if strings.Trim(l, " \t\r\n") != "" {
				filtered = append(filtered, l)
			}
		}
		if len(filtered) != gEnd-g {
			if log != nil {
				log.Warnf("组 %d 行数不对齐（回复 %d 行 / 输入 %d 行），整组转入逐行抢救",
					gi+1, len(filtered), gEnd-g)
			}
			continue
		}
		for pos := g; pos < gEnd; pos++ {
			k := toTranslate[pos] // group position -> uniq id
			if candidate, ok := validateLine(k, filtered[pos-g]); ok {
				uniqTranslations[k] = candidate
				uniqTranslated[k] = true
			}
		}
	}
	if okGroups == 0 && firstError != "" {
		return Result{Error: firstError}
	}

	// Rescue: every unit still untranslated — its group failed at the HTTP
	// level, the reply misaligned with the group's line count, or the line
	// was rejected by validation — is retried as an individual single-line
	// request. One line per request cannot be merged with neighbours or
	// dropped, so alignment is trivial; a reply wrapped across several lines
	// is rejoined. Sequential on purpose: this is the exception path.
	// Two passes: the model's placeholder slips are stochastic (run: it
	// invented a spurious [[14]] for a 13-token unit), so a rejected line
	// gets a second independent chance before the run fails. Masked units
	// get an explicit placeholder inventory in the prompt (run: the model
	// split `0.x` into TWO placeholders every single time — naming the
	// exact count fights that).
	rescue := func(round int) {
		pending := 0
		for _, t := range uniqTranslated {
			if !t {
				pending++
			}
		}
		if log != nil && pending > 0 {
			log.Infof("抢救第 %d 轮：%d 个单元进入逐行重试", round, pending)
		}
		// Parallel with the same bounded concurrency as the group phase:
		// 15 sequential single-line requests were 17s of the 31s run; the
		// requests are independent (one line in, one line out), so ordering
		// is irrelevant. Results land in the shared slices via mutex.
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, maxConcurrent)
		for k := 0; k < len(uniq); k++ {
			if uniqTranslated[k] {
				continue
			}
			wg.Add(1)
			go func(k int) {
				defer wg.Done()
				glossary := BuildGlossary(sp.glossary, uniqOrig[k])
				// MT style round plan: 1 = RAW text with the official
				// template (best prose), 2 = masked protocol (structure
				// forces tokens), 3 = RAW again with explicit keep-token
				// wording (models miss stochastically; a third raw chance
				// often lands). General instruct: masked every round.
				general := !sp.mtStyle || round >= 2 && round != 3
				var req provider.Request
				if sp.mtStyle && !general {
					stress := ""
					if round >= 3 && uniqMasked[k] && len(uniqTokens[k]) > 0 {
						var list strings.Builder
						for i, tok := range uniqTokens[k] {
							if i > 0 {
								list.WriteString("、")
							}
							list.WriteString(tok)
						}
						stress = "特别注意：以下技术记号必须原样出现在译文中，不得翻译、改写、增删或加注：" +
							list.String() + "。"
					}
					req = provider.Request{
						APIKey: sp.key,
						UserPrompt: sp.mtPreamble +
							"逐字保留数字、百分号、括号、文件路径、代码标识符、URL、命令名，不要改写、增删或加注。" +
							stress + glossary + "\n\n" + uniqOrig[k] + "\n",
					}
				} else {
					// Masked protocol (general instruct always; MT round 2+).
					rules := systemPrompt + glossary
					if uniqMasked[k] {
						var list strings.Builder
						for i, tok := range uniqTokens[k] {
							if i > 0 {
								list.WriteString(", ")
							}
							list.WriteString("[[" + itoa(i+1) + "]]=" + tok)
						}
						rules += " The input contains exactly " + itoa(len(uniqTokens[k])) +
							" placeholders: " + list.String() +
							". Your reply MUST contain every one of these placeholders EXACTLY once, and NO other [[N]] placeholder."
					}
					req = provider.Request{
						APIKey:       sp.key,
						SystemPrompt: rules,
						UserPrompt: "Translate the following single line into " +
							sp.targetName + ". Output only the translation:\n\n" +
							uniq[k] + "\n",
					}
				}
				sem <- struct{}{}
				rr := translateWithRetryLog(sp.ctx, sp.adapter, req, nil, log)
				<-sem
				if !rr.OK {
					if log != nil {
						log.Errorf("单元 #%d 抢救请求失败: %s", k, rr.Error)
					}
					return
				}
				var joined strings.Builder
				for _, l := range SplitLines(rr.Text) {
					if strings.Trim(l, " \t\r\n") == "" {
						continue
					}
					if joined.Len() > 0 {
						joined.WriteByte(' ')
					}
					joined.WriteString(l)
				}
				// Wire form of THIS request: general instruct always masked;
				// MT raw in round 1, masked (escalation) in round 2+.
				wireMasked := !sp.mtStyle || (round >= 2 && round != 3)
				if candidate, ok := validateWire(k, joined.String(), wireMasked); ok {
					mu.Lock()
					uniqTranslations[k] = candidate
					uniqTranslated[k] = true
					mu.Unlock()
					if log != nil {
						log.Infof("单元 #%d 抢救成功", k)
					}
				}
			}(k)
		}
		wg.Wait()
	}
	rescue(1)
	rescue(2)
	rescue(3)

	// Fail loudly instead of returning a mixed translated/English result:
	// after group attempts and per-line rescue, any surviving untranslated
	// unit means the model could not produce a verified translation for it,
	// and a half-translated log is exactly what this pipeline must never
	// emit (user decision: complete translation or an explicit error).
	translatedCount := 0
	for _, t := range uniqTranslated {
		if t {
			translatedCount++
		}
	}
	return spliceResult(sp, vlines, uniqTranslations, lineUnits, translatedCount, len(uniq))
}

// maxUnitPlaceholders bounds how many masked placeholders one translation
// unit may carry. Small models deterministically drop middle placeholders
// on placeholder-dense lines; three or fewer survives reliably.
const maxUnitPlaceholders = 3

// rawSplitPoint reports whether position i in RAW (unmasked) text is a
// safe sentence split. Primary marks: . ! ? (and CJK 。！？) followed by
// whitespace, ')' or end of text. Secondary marks: , ; (and CJK ，；) —
// used only by the caller when no primary split bounded the placeholders.
// A mark inside a backtick code span never splits (a dot inside `0.x` or
// a path would corrupt a technical token).
func rawSplitPoint(s string, i int) bool {
	// Not inside backticks: count them before i; odd means inside.
	if strings.Count(s[:i], "`")%2 != 0 {
		return false
	}
	switch s[i] {
	case '.', '!', '?', ',', ';':
		// Require whitespace, ')' or end of text after the mark so dotted
		// numbers/identifiers ("0.2.x", "file.md") never split.
		j := i + 1
		if j < len(s) && s[j] != ' ' && s[j] != '\t' && s[j] != ')' {
			return false
		}
		return true
	default:
		// CJK sentence/comma marks are 3-byte UTF-8 (E3 80 82 / EF BC 81 /
		// EF BC 9F / EF BC 8C / EF BC 9B); check their lead bytes.
		if i+2 < len(s) {
			c0, c1, c2 := s[i], s[i+1], s[i+2]
			if c0 == 0xE3 && c1 == 0x80 && c2 == 0x82 ||
				c0 == 0xEF && c1 == 0xBC && (c2 == 0x81 || c2 == 0x9F || c2 == 0x8C || c2 == 0x9B) {
				j := i + 3
				return j >= len(s) || s[j] == ' ' || s[j] == '\t' || s[j] == ')'
			}
		}
		return false
	}
}

// glueWords are English function words that carry nothing translatable when
// they are all that remains of a masked fragment.
var glueWords = map[string]bool{
	"and": true, "or": true, "of": true, "the": true, "a": true, "an": true,
	"to": true, "in": true, "on": true, "for": true, "with": true, "at": true,
	"by": true, "from": true, "is": true, "are": true, "be": true,
}

// hasNoTranslatableContent reports whether the masked fragment holds only
// glue words, placeholders, and short ASCII words that are pieces of the
// masked technical tokens or command syntax ("and [[1]] go test [[2]]" —
// "go"/"test" are command words the model cannot and should not translate).
// Such a fragment's echo is the correct translation even for a CJK target.
// The fragment must be short: longer all-ASCII spans (a full English
// sentence) still need a real translation and keep the no-CJK rejection.
func hasNoTranslatableContent(s string) bool {
	masked, toks, _ := MaskUnit(s)
	if len(toks) == 0 {
		return false // unmasked unit: ordinary prose checks apply
	}
	if len(masked) > 80 {
		return false // long fragment: a real translation is expected
	}
	for _, w := range strings.Fields(masked) {
		if strings.HasPrefix(w, "[[") && strings.HasSuffix(w, "]]") {
			continue // placeholder
		}
		word := strings.ToLower(strings.Trim(w, ".,;:!?()[]'\"`"))
		if word == "" || glueWords[word] {
			continue
		}
		// Short all-lowercase ASCII word: command syntax or a piece of a
		// masked token ("go", "test", "list"). Reject mixed-case or long
		// words, which read as prose (e.g. "availability").
		if len(word) <= 6 && word == strings.ToLower(word) && isASCIILettersOrDots(word) {
			continue
		}
		// A word that only exists as a piece of one of the masked tokens
		// is technical, not prose.
		inToken := false
		for _, tok := range toks {
			if strings.Contains(strings.ToLower(tok), word) {
				inToken = true
				break
			}
		}
		if !inToken {
			return false
		}
	}
	return true
}

// isASCIILettersOrDots reports whether s holds only ASCII letters, digits,
// dots and slashes — command/identifier material, not prose.
func isASCIILettersOrDots(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '/' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// isPureASCII reports whether s holds only printable ASCII and whitespace —
// the English-target echo exception.
func isPureASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c <= 0x7E {
			continue
		}
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			continue
		}
		return false
	}
	return true
}

// isLetterless reports whether s holds no ASCII letter and no CJK character
// — pure punctuation/symbol lines ("---", "***", "===", "|---|") that no
// model can "translate"; its echo is the correct translation.
func isLetterless(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return false
		}
		if r >= 0x3000 { // CJK and full-width ranges start here
			return false
		}
	}
	return true
}

// isTechValueLine reports whether s is a short "key: value" line whose value
// side is a single non-CJK word — a YAML/config field entry like
// "name: mygo-maintenance", "version: 1.2.3" or "id: user_42". The label may
// be translated or not; the value is an identifier/number that must stay
// verbatim, so the model echoing the line is the correct translation. Long
// lines and multi-word values (real prose may follow) do NOT qualify.
func isTechValueLine(s string) bool {
	t := strings.Trim(s, " \t\r\n")
	if len(t) > 80 {
		return false
	}
	c := strings.Index(t, ":")
	if c <= 0 {
		return false
	}
	value := strings.Trim(t[c+1:], " \t")
	if value == "" || strings.ContainsAny(value, " \t") {
		return false // empty or multi-word: real prose may follow
	}
	// The value must carry no translatable CJK content; ASCII identifiers
	// (kebab-case, dotted versions, snake_case, numbers) all qualify.
	return !HasCJK(value)
}
