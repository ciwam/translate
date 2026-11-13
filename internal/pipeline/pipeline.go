// Pipeline orchestration: a faithful port of Translator::translate
// (translator.cpp:758). Route A streams pure prose; Route B is the
// deterministic splice pipeline for text with technical tokens.
package pipeline

import (
	"context"
	"strings"
	"sync"

	"ciwang/translate/internal/provider"
)

// FilterConfig maps each provider's known "bad habits" to a post-filter
// config. Swapping the model only changes which row is loaded.
type filterConfig struct {
	NewlineGuard      bool // restore dropped line breaks (Qwen's habit)
	StripExplanations bool // remove model-added "译文：" style labels (GPT)
	Repunct           bool // force half-width delimiters for structured logs
}

var filters = []filterConfig{
	{true, true, true}, // Zhipu · GLM-4-Flash
	{true, true, true}, // SiliconFlow · Qwen2.5-7B-Instruct
	{true, true, true}, // SiliconFlow · Hunyuan-MT-7B
}

var promptNames = []string{
	"English",            // 0
	"Simplified Chinese", // 1
	"Japanese",           // 2
	"Korean",             // 3
	"French",             // 4
	"German",             // 5
	"Spanish",            // 6
}

var targetCodes = []string{"en", "zh", "ja", "ko", "fr", "de", "es"}

var providerNames = []string{"zhipu", "siliconflow", "hunyuan-mt"}

// logTargetName is a safe display name for logging.
func logTargetName(i int) string {
	if i < 0 || i >= len(targetCodes) {
		return "?"
	}
	return targetCodes[i]
}

// logProviderName is a safe display name for logging.
func logProviderName(i int) string {
	if i < 0 || i >= len(providerNames) {
		return "?"
	}
	return providerNames[i]
}

// Options carries per-call configuration.
type Options struct {
	Provider int
	Target   int
	APIKey   string
	Glossary string
	// Log, when non-nil, receives a detailed trace of the run (routing,
	// group requests/replies, per-line verdicts, rescue retries).
	Log *Logger
}

// Result mirrors provider.Result for pipeline callers.
type Result = provider.Result

// Translate runs the full kernel. onDelta, when non-nil, receives the
// running full result (overwrite semantics, valid UTF-8 prefix). The
// debugRoute pointer, when non-nil, receives a one-line diagnostic naming
// the pipeline path — same strings as the C++ kernel so the MCP tooling
// keeps working.
func Translate(ctx context.Context, opts Options, text string, adapter provider.Translator, onDelta func(string), debugRoute *string) Result {
	setRoute := func(r string) {
		if debugRoute != nil {
			*debugRoute = r
		}
		if opts.Log != nil {
			opts.Log.Infof("路由: %s", r)
		}
	}
	key := strings.Trim(opts.APIKey, " \t\r\n")
	if key == "" {
		return Result{Error: "请填写 API Key"}
	}
	src := strings.Trim(text, " \t\r\n")
	if src == "" {
		return Result{Error: "请输入要翻译的文本"}
	}
	if opts.Log != nil {
		opts.Log.Infof("开始翻译: %d 字节, provider=%s, target=%s",
			len(src), logProviderName(opts.Provider), logTargetName(opts.Target))
		opts.Log.Debugf("源文本: %q", src)
	}
	glossaryBlock := BuildGlossary(opts.Glossary, src)
	if opts.Log != nil {
		if glossaryBlock != "" {
			opts.Log.Infof("术语词典注入 %d 条（按上下文过滤）",
				strings.Count(glossaryBlock, "\n")-1)
			opts.Log.Debugf("词典块: %q", glossaryBlock)
		} else if strings.TrimSpace(opts.Glossary) != "" {
			opts.Log.Infof("术语词典无命中条目，未注入")
		}
	}

	// In-process cache: identical input returns the identical, deterministic
	// result immediately, and never lets the stochastic model produce a
	// different translation for the same text across runs.
	ckey := CacheKey(opts.Provider, opts.Target, src, opts.Glossary)
	if cached, ok := resultCacheDefault.get(ckey); ok {
		setRoute("cache")
		return Result{OK: true, Text: cached}
	}

	pIdx := opts.Provider
	if pIdx < 0 || pIdx >= len(filters) {
		pIdx = 0
	}
	tIdx := opts.Target
	if tIdx < 0 || tIdx >= len(promptNames) {
		tIdx = 0
	}

	// Strengthen the prompt against the two known failure modes: echoing
	// the source unchanged (short words/phrases), and emitting Traditional
	// Chinese for a Simplified-Chinese target.
	//
	// MT-style providers (Hunyuan-MT) take NO system prompt per their
	// docs: the instruction is the official user-message template
	// "把下面的文本翻译成<lang>，不要额外解释。" and all pipeline
	// requirements ride along after it. The pipeline keeps its own
	// placeholder/format constraints there too.
	mtStyle := provider.PromptStyle(provider.Provider(opts.Provider)) == "mt"
	var targetName, constraints string
	if tIdx == Chinese {
		targetName = "Mainland China Simplified Chinese " +
			"(中国大陆简体字，禁止使用繁体字)"
		constraints = "Use only Mainland China standard simplified " +
			"characters. Do NOT use traditional Chinese characters."
	} else {
		targetName = promptNames[tIdx]
	}
	if mtStyle {
		targetName = provider.MTTargetName(provider.Target(tIdx))
		constraints = "" // 集成进中文模板本身
	}
	mtPreamble := ""
	if mtStyle {
		mtPreamble = "把下面的文本翻译成" + targetName + "，不要额外解释。"
	}

	filter := filters[pIdx]

	emitProse := func(raw string) Result {
		res := strings.Trim(raw, " \t\r\n")
		if filter.StripExplanations {
			res = StripExplanations(res)
		}
		r := Result{OK: true, Text: normalizeIfChinese(tIdx, res)}
		if r.OK {
			resultCacheDefault.put(ckey, r.Text)
		}
		return r
	}

	wrapResult := func(r Result) Result {
		if r.OK {
			resultCacheDefault.put(ckey, r.Text)
		}
		return r
	}

	// ---- Routing ----
	hasTech := AnalyzeTechnical(src)
	if opts.Log != nil {
		if hasTech {
			opts.Log.Infof("检测到技术 token，走 splice 管线")
		} else {
			opts.Log.Infof("纯文本，直译（prose 路径）")
		}
	}
	if !hasTech {
		// MT style: one user-message request in the official template;
		// the pipeline's must-keep-token rules follow. System prompt stays
		// empty (the model is trained without one).
		var proseSystem, proseUser string
		if mtStyle {
			proseSystem = ""
			proseUser = "把下面的文本翻译成" + targetName + "，不要额外解释。" +
				"逐字保留数字、百分号、括号、文件路径、代码标识符、URL 和命令名，" +
				"保持行结构完全一致，不要遗漏任何一行。" + glossaryBlock + "\n\n" + src
		} else {
			proseSystem = "You are a professional translator. Translate the user's text " +
				"exactly into the requested target language, in natural, fluent " +
				"phrasing. Always provide a translation; never echo or return the " +
				"original text unchanged. " +
				"Preserve verbatim every non-language token exactly as written: " +
				"numbers, percent signs, brackets, file paths, code identifiers, " +
				"URLs, and command names. Never reword, reorder, drop, or recompute " +
				"them. Keep the exact line structure and translate the text " +
				"completely without truncating or omitting any line. Output only the " +
				"translated text, with no explanations, notes, examples, or " +
				"quotation marks. " + constraints + glossaryBlock
			proseUser = "Translate the following text into " + targetName +
				". Provide only the translation:\n\n"
		}
		// Chunk over-long prose at line / sentence boundaries so no single
		// request crosses the context window; translate sequentially and
		// rejoin with the original separators (deltas stream the running
		// result).
		const proseChunkBudget = 1500
		if len(src) > proseChunkBudget {
			pieces := SplitPieces(src, proseChunkBudget)
			setRoute("prose-chunked(" + itoa(len(pieces)) + " pieces)")
			if opts.Log != nil {
				opts.Log.Infof("文本超过 %d 字节预算，切为 %d 段顺序翻译", proseChunkBudget, len(pieces))
			}
			var result strings.Builder
			for i, p := range pieces {
				req := provider.Request{
					APIKey:       key,
					SystemPrompt: proseSystem,
					UserPrompt:   proseUser + p.Text,
				}
				if opts.Log != nil {
					opts.Log.Debugf("段 %d/%d: %q", i+1, len(pieces), p.Text)
				}
				rr := translateWithRetryLog(ctx, adapter, req, nil, opts.Log)
				if !rr.OK {
					return wrapResult(rr)
				}
				result.WriteString(rr.Text)
				if i+1 < len(pieces) {
					result.WriteString(p.Sep)
				}
				if onDelta != nil {
					onDelta(result.String()) // running full result so far
				}
			}
			if result.Len() > 0 {
				return emitProse(result.String())
			}
			return Result{OK: true, Text: ""}
		}
		setRoute("prose")
		req := provider.Request{
			APIKey:       key,
			SystemPrompt: proseSystem,
			UserPrompt:   proseUser + src,
		}
		rr := translateWithRetryLog(ctx, adapter, req, func(delta string) {
			if onDelta != nil {
				res := strings.Trim(delta, " \t\r\n")
				if filter.StripExplanations {
					res = StripExplanations(res)
				}
				onDelta(normalizeIfChinese(tIdx, res))
			}
		}, opts.Log)
		if rr.OK {
			res := strings.Trim(rr.Text, " \t\r\n")
			if filter.StripExplanations {
				res = StripExplanations(res)
			}
			rr = Result{OK: true, Text: normalizeIfChinese(tIdx, res)}
		}
		return wrapResult(rr)
	}

	// ---- Splice mode v2.1 (technical tokens present) ----
	return translateSplice(spliceParams{
		ctx:         ctx,
		adapter:     adapter,
		key:         key,
		src:         src,
		target:      tIdx,
		targetName:  targetName,
		constraints: constraints,
		glossary:    opts.Glossary,
		filter:      filter,
		setRoute:    setRoute,
		onDelta:     onDelta,
		log:         opts.Log,
		mtStyle:     mtStyle,
		mtPreamble:  mtPreamble,
	})
}

// spliceParams bundles everything the splice route needs.
type spliceParams struct {
	ctx         context.Context
	adapter     provider.Translator
	key         string
	src         string
	target      int
	targetName  string
	constraints string
	glossary    string
	filter      filterConfig
	setRoute    func(string)
	onDelta     func(string)
	log         *Logger
	mtStyle     bool
	mtPreamble  string
}

// lineInfo mirrors LineInfo (translator.cpp:113).
type lineInfo struct {
	text     string
	hasProse bool
}

// splitSourceLines mirrors the C++ line splitting (translator.cpp:910).
func splitSourceLines(src string) []lineInfo {
	var vlines []lineInfo
	pos := 0
	for {
		nl := strings.IndexByte(src[pos:], '\n')
		var line string
		if nl < 0 {
			line = src[pos:]
		} else {
			line = src[pos : pos+nl]
		}
		line = strings.TrimSuffix(line, "\r")
		vlines = append(vlines, lineInfo{text: line, hasProse: lineHasProse(line)})
		if nl < 0 {
			break
		}
		pos += nl + 1
	}
	return vlines
}

// spliceResult assembles the final answer after splice translation.
func spliceResult(sp spliceParams, vlines []lineInfo, uniqTranslations []string, lineUnits [][]int, translatedCount, uniqCount int) Result {
	if translatedCount < uniqCount {
		sp.setRoute("splice(incomplete translated " +
			itoa(translatedCount) + "/" + itoa(uniqCount) + ")")
		return Result{Error: "翻译失败：有 " +
			itoa(uniqCount-translatedCount) +
			" 行多次重试后仍未完成翻译，请重试"}
	}
	sp.setRoute("splice(translated " + itoa(translatedCount) + "/" + itoa(uniqCount) + ")")
	// Reassemble: technical-only lines verbatim, prose lines from their unit
	// translations.
	var out strings.Builder
	for li, vl := range vlines {
		if !vl.hasProse {
			out.WriteString(vl.text)
		} else {
			for j, u := range lineUnits[li] {
				if j > 0 {
					out.WriteString(" ")
				}
				out.WriteString(uniqTranslations[u])
			}
		}
		if li+1 < len(vlines) {
			out.WriteByte('\n')
		}
	}
	result := out.String()
	if sp.filter.Repunct {
		result = Repunct(result, sp.src)
	}
	// Safety net: every technical token must still be present. The per-line
	// checks already enforce this; a whole-text miss is a pipeline bug, and
	// returning untranslated source would hide it. MT style applies the
	// same loose matching as its per-line check (case drift, plural
	// stripping, slash-chain respelling).
	wholeTextOK := ProtectedTokensPreserved(sp.src, result)
	if !wholeTextOK && sp.mtStyle {
		wholeTextOK = ProtectedTokensPreservedLoose(sp.src, result)
	}
	if !wholeTextOK {
		sp.setRoute("splice(token-miss, translated " +
			itoa(translatedCount) + "/" + itoa(uniqCount) + ")")
		return Result{Error: "翻译失败：译文丢失了源文本中的技术标记，请重试"}
	}
	return Result{OK: true, Text: normalizeIfChinese(sp.target, strings.Trim(result, " \t\r\n"))}
}

// maxConcurrent bounds parallel model requests (group phase and rescue
// alike). A simultaneous burst invites provider rate limits, which would
// then need retries and stall the whole batch; 4 keeps 8 groups at 2 waves.
const maxConcurrent = 4

// runGroupWorkers fans the group requests out with bounded parallelism and
// returns each group's (result, reply-lines).
func runGroupWorkers(sp spliceParams, systemPrompt string, uniq []string, groups int, groupSize int) ([]Result, [][]string) {
	groupResults := make([]Result, groups)
	groupLines := make([][]string, groups)
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrent)
	for gi := 0; gi < groups; gi++ {
		wg.Add(1)
		go func(gi int) {
			defer wg.Done()
			g := gi * groupSize
			gEnd := g + groupSize
			if gEnd > len(uniq) {
				gEnd = len(uniq)
			}
			var block strings.Builder
			for k := g; k < gEnd; k++ {
				block.WriteString(uniq[k])
				block.WriteByte('\n')
			}
			glossary := BuildGlossary(sp.glossary, block.String())
			var req provider.Request
			if sp.mtStyle {
				req = provider.Request{
					APIKey: sp.key,
					UserPrompt: sp.mtPreamble + "以下是 " + itoa(gEnd-g) +
						" 行文本，逐行翻译，输出行数与输入一致。" + glossary +
						"\n\n" + block.String(),
				}
			} else {
				req = provider.Request{
					APIKey:       sp.key,
					SystemPrompt: systemPrompt + glossary,
					UserPrompt: "Translate each of the following " +
						itoa(gEnd-g) + " lines into " + sp.targetName +
						":\n\n" + block.String(),
				}
			}
			if sp.log != nil {
				sp.log.Infof("组 %d/%d 发起请求（%d 行）", gi+1, groups, gEnd-g)
				sp.log.Debugf("组 %d 请求体:\n%s", gi+1, block.String())
			}
			sem <- struct{}{}
			rr := translateWithRetryLog(sp.ctx, sp.adapter, req, nil, sp.log)
			<-sem
			groupResults[gi] = rr
			if rr.OK {
				groupLines[gi] = SplitLines(rr.Text)
				if sp.log != nil {
					sp.log.Debugf("组 %d 回复:\n%s", gi+1, rr.Text)
				}
			} else if sp.log != nil {
				sp.log.Errorf("组 %d 请求失败: %s", gi+1, rr.Error)
			}
		}(gi)
	}
	wg.Wait()
	return groupResults, groupLines
}
