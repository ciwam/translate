// Debug CLI front-end for the Go translation kernel. Reads one JSON
// request from stdin and writes one JSON response line to stdout. Built
// for the MCP debug channel (mcp/translate_mcp.py) and manual quality
// triage — the GUI is not involved. The request/response contract matches
// core/tools' C++ translate_cli exactly, so the Python server needs no
// change beyond pointing at this binary.
//
// Request:
//
//	{
//	  "provider": 0 | "zhipu" | "siliconflow" | 1,   // default: 0 (Zhipu)
//	  "target":   0..6 | "en"|"zh"|"ja"|"ko"|"fr"|"de"|"es",  // default: 1 (zh)
//	  "text":     "source text",                     // required
//	  "glossary": "term = translation\n...",         // optional
//	  "api_key":  "sk-..."                           // optional; falls back to
//	}                                                // TRANSLATE_API_KEY_ZHIPU /
//	                                                 // TRANSLATE_API_KEY_SILICONFLOW /
//	                                                 // TRANSLATE_API_KEY
//
// Response (single line, compact):
//
//	{"ok":true, "text":"...", "route":"splice(translated 8/9)",
//	 "provider":"siliconflow", "target":"zh", "elapsed_ms":812}
//	{"ok":false, "error":"...", "provider":"...", "target":"...", "elapsed_ms":N}
//
// Exit code: 0 on success, 1 on failure (the MCP wrapper reads the JSON
// either way).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"ciwang/translate/internal/pipeline"
	"ciwang/translate/internal/provider"
)

var targetCodes = []string{"en", "zh", "ja", "ko", "fr", "de", "es"}
var providerNames = []string{"zhipu", "siliconflow", "hunyuan-mt"}

type request struct {
	Provider any    `json:"provider"`
	Target   any    `json:"target"`
	Text     string `json:"text"`
	Glossary string `json:"glossary"`
	APIKey   string `json:"api_key"`
}

type response struct {
	OK        bool   `json:"ok"`
	Text      string `json:"text,omitempty"`
	Error     string `json:"error,omitempty"`
	Route     string `json:"route,omitempty"`
	Provider  string `json:"provider"`
	Target    string `json:"target"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

func main() {
	if err := pipeline.DefaultInit(); err != nil {
		fatal("繁简转换初始化失败：" + err.Error())
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fatal("读取 stdin 失败：" + err.Error())
	}
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		fatal("请求 JSON 解析失败：" + err.Error())
	}
	if strings.TrimSpace(req.Text) == "" {
		fatal("缺少 text 字段")
	}

	prov, perr := parseProvider(req.Provider)
	tgt, terr := parseTarget(req.Target)
	if perr != nil || terr != nil {
		if perr != nil {
			fatal(perr.Error())
		}
		fatal(terr.Error())
	}

	apiKey := req.APIKey
	if strings.TrimSpace(apiKey) == "" {
		apiKey = os.Getenv("TRANSLATE_API_KEY_" + strings.ToUpper(providerNames[prov]))
	}
	if strings.TrimSpace(apiKey) == "" {
		apiKey = os.Getenv("TRANSLATE_API_KEY")
	}

	start := time.Now()
	var route string
	var log *pipeline.Logger
	// TRANSLATE_VERBOSE=1 (or --verbose) dumps the full pipeline trace to
	// stderr; stdout stays protocol-clean for the MCP wrapper.
	verbose := false
	for _, arg := range os.Args[1:] {
		if arg == "--verbose" || arg == "-v" {
			verbose = true
		}
	}
	if verbose || os.Getenv("TRANSLATE_VERBOSE") == "1" {
		log = pipeline.NewLogger()
	}
	adapter := provider.New(provider.Provider(prov))
	res := pipeline.Translate(context.Background(), pipeline.Options{
		Provider: prov,
		Target:   tgt,
		APIKey:   apiKey,
		Glossary: req.Glossary,
		Log:      log,
	}, req.Text, adapter, nil, &route)
	elapsed := time.Since(start).Milliseconds()
	respTarget := "auto"
	if tgt >= 0 {
		respTarget = targetCodes[tgt]
	}

	resp := response{
		OK:        res.OK,
		Text:      res.Text,
		Error:     res.Error,
		Route:     route,
		Provider:  providerNames[prov],
		Target:    respTarget,
		ElapsedMS: elapsed,
	}
	out, _ := json.Marshal(resp)
	fmt.Println(string(out))
	if verbose || os.Getenv("TRANSLATE_VERBOSE") == "1" {
		fmt.Fprint(os.Stderr, log.Render())
	}
	if !res.OK {
		os.Exit(1)
	}
}

func parseProvider(v any) (int, error) {
	switch t := v.(type) {
	case nil:
		return 0, nil
	case float64:
		i := int(t)
		if i < 0 || i >= len(providerNames) {
			return 0, fmt.Errorf("provider 越界（0=Zhipu, 1=SiliconFlow, 2=HunyuanMT）")
		}
		return i, nil
	case string:
		for i, name := range providerNames {
			if t == name {
				return i, nil
			}
		}
		return 0, fmt.Errorf("未知 provider %q（可选 zhipu / siliconflow）", t)
	}
	return 0, fmt.Errorf("provider 类型非法")
}

func parseTarget(v any) (int, error) {
	switch t := v.(type) {
	case nil:
		return 1, nil
	case float64:
		i := int(t)
		if i == int(provider.AutoBidirectional) {
			return i, nil
		}
		if i < 0 || i >= len(targetCodes) {
			return 0, fmt.Errorf("target 越界（-1=中英互译, 0..6）")
		}
		return i, nil
	case string:
		for i, code := range targetCodes {
			if t == code {
				return i, nil
			}
		}
		return 0, fmt.Errorf("未知 target %q（可选 en/zh/ja/ko/fr/de/es）", t)
	}
	return 0, fmt.Errorf("target 类型非法")
}

func fatal(msg string) {
	b, _ := json.Marshal(response{Error: msg})
	fmt.Println(string(b))
	os.Exit(1)
}
