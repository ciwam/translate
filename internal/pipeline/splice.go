package pipeline

import (
	"strings"

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
	for _, vl := range vlines {
		if !vl.hasProse {
			lineUnits = append(lineUnits, nil)
			continue
		}
		var units []int
		if len(vl.text) <= contextBudget {
			units = append(units, addUnit(vl.text))
		} else {
			for _, p := range SplitPieces(vl.text, contextBudget) {
				units = append(units, addUnit(p.Text))
			}
		}
		lineUnits = append(lineUnits, units)
	}

	systemPrompt := "You are a professional translator for software developers. The user's " +
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

	// Translate the unique lines in small groups, with bounded parallelism.
	const lineGroupSize = 5
	groups := (len(uniq) + lineGroupSize - 1) / lineGroupSize
	uniqTranslations := append([]string(nil), uniqOrig...) // default: original
	uniqTranslated := make([]bool, len(uniq))

	groupResults, groupLines := runGroupWorkers(sp, systemPrompt, uniq, groups, lineGroupSize)

	wantCJK := sp.target == Chinese || sp.target == 2 || sp.target == 3 // zh/ja/ko

	// Per-line validation shared by group triage and the rescue phase:
	// placeholder restore / punctuation normalization / quality guards /
	// token preservation — plus the two rejections the older guards missed:
	// the reply echoing the source line, and a CJK target whose reply
	// contains no CJK character at all.
	validateLine := func(k int, replyLine string) (string, bool) {
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
		if uniqMasked[k] {
			// Masked unit: restore [[N]] placeholders verbatim; any
			// missing/malformed placeholder fails the whole line.
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
			// Unmasked unit (no protected tokens): punctuation
			// normalization + quality guard + verbatim check.
			candidate = Repunct(replyLine, uniqOrig[k])
			bad := IsBadTranslation(candidate)
			kept := ProtectedTokensPreserved(uniqOrig[k], candidate)
			okLine = !bad && kept
			switch {
			case bad:
				reason = "坏译文守卫"
			case !kept:
				reason = "技术 token 丢失"
			}
		}
		if okLine && wantCJK && !HasCJK(candidate) {
			okLine = false
			reason = "无 CJK 字符"
		}
		if okLine {
			if log != nil {
				log.Debugf("单元 #%d 校验通过: %q", k, candidate)
			}
			return candidate, true
		}
		return reject(reason)
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
		if gEnd > len(uniq) {
			gEnd = len(uniq)
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
		for k := g; k < gEnd; k++ {
			if candidate, ok := validateLine(k, filtered[k-g]); ok {
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
		for k := 0; k < len(uniq); k++ {
			if uniqTranslated[k] {
				continue
			}
			system := systemPrompt + BuildGlossary(sp.glossary, uniq[k])
			if uniqMasked[k] {
				var list strings.Builder
				for i, tok := range uniqTokens[k] {
					if i > 0 {
						list.WriteString(", ")
					}
					list.WriteString("[[" + itoa(i+1) + "]]=" + tok)
				}
				system += " The input contains exactly " + itoa(len(uniqTokens[k])) +
					" placeholders: " + list.String() +
					". Your reply MUST contain every one of these placeholders EXACTLY once, and NO other [[N]] placeholder."
				if log != nil {
					log.Debugf("单元 #%d 抢救提示含占位符清单（%d 个）", k, len(uniqTokens[k]))
				}
			}
			req := provider.Request{
				APIKey:       sp.key,
				SystemPrompt: system,
				UserPrompt: "Translate the following single line into " +
					sp.targetName + ". Output only the translation:\n\n" +
					uniq[k] + "\n",
			}
			rr := translateWithRetryLog(sp.ctx, sp.adapter, req, nil, log)
			if !rr.OK {
				if log != nil {
					log.Errorf("单元 #%d 抢救请求失败: %s", k, rr.Error)
				}
				continue
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
			if candidate, ok := validateLine(k, joined.String()); ok {
				uniqTranslations[k] = candidate
				uniqTranslated[k] = true
				if log != nil {
					log.Infof("单元 #%d 抢救成功", k)
				}
			}
		}
	}
	rescue(1)
	rescue(2)

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
