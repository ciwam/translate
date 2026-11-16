// translate-cases — 翻译质量用例集 CLI。
//
// 与桌面应用走同一条管线代码（internal/pipeline）：打这个包测试的效果
// 与打包出来的桌面应用一致。用例表内置（含括号短语回归、构建/运维日志、
// 中英互译等 10 个场景），逐个调用管线并报告通过/失败。
//
// 用法:
//
//	translate-cases                                   # 全部用例，hunyuan-mt
//	translate-cases -provider siliconflow             # 指定模型
//	translate-cases -case paren1,log1                 # 只跑指定用例
//	translate-cases -verbose                          # 打印每次翻译的完整日志
//
// Key 经环境变量传递：TRANSLATE_API_KEY_<PROVIDER>（如
// TRANSLATE_API_KEY_HUNYUAN_MT）或通用 TRANSLATE_API_KEY。
// 退出码：0 全部通过；1 有用例失败（构建门禁用）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"ciwang/translate/internal/pipeline"
	"ciwang/translate/internal/provider"
)

// caseSet 是面向开发/运维/测试/调试人员的典型翻译场景。
type testCase struct {
	id     string
	desc   string
	target int
	text   string
}

var cases = []testCase{
	{
		id:     "paren1",
		desc:   "括号短语（词边缘括号曾误掩码，回归用例）",
		target: int(provider.Chinese),
		text:   "Name is too long (maximum is 40 characters)",
	},
	{
		id:     "prose1",
		desc:   "英文短文（纯散文，技术博客风）",
		target: int(provider.Chinese),
		text: "Distributed tracing helps engineers follow a single request as it " +
			"travels across services. When latency spikes, the trace reveals which " +
			"hop added the delay, turning guesswork into evidence. Most teams adopt " +
			"it only after a painful outage that a dashboard alone could not explain.",
	},
	{
		id:     "log1",
		desc:   "构建日志（cmake/make，路径+目标名+错误码）",
		target: int(provider.Chinese),
		text: "[2026-09-16 10:02:11] ERROR: /Users/ciwang/proj/main.cpp:42: " +
			"undefined reference to `vtable for Widget' in Debug/libgui.a\n" +
			"make[2]: *** [CMakeFiles/app.dir/build.make:113: app] Error 1\n" +
			"make[1]: *** [CMakeFiles/Makefile2:88: CMakeFiles/app.dir/all] Error 2\n" +
			"Warning: deprecated conversion from string constant to 'char*'",
	},
	{
		id:     "log2",
		desc:   "运维日志（nginx upstream 超时，键值对混排）",
		target: int(provider.Chinese),
		text: "Sep 16 02:14:03 web-1 nginx[812]: upstream timed out (110: Connection " +
			"timed out) while reading response header from upstream, client: " +
			"10.0.3.17, server: api.internal:443, request: \"GET /v2/health " +
			"HTTP/1.1\", upstream: \"http://127.0.0.1:8080/v2/health\"",
	},
	{
		id:     "err1",
		desc:   "调试报错（运行时异常堆栈 + 编译器提示）",
		target: int(provider.Chinese),
		text: "TypeError: Cannot read properties of undefined (reading 'map')\n" +
			"    at UserList (src/components/UserList.tsx:23:19)\n" +
			"Expected 3 arguments, but got 2. Did you forget to pass the callback?",
	},
	{
		id:     "short1",
		desc:   "短句（回显风险）+ 含 sudo 命令",
		target: int(provider.Chinese),
		text: "Build succeeded. 0 warnings, 3 tests failed.\n" +
			"Permission denied. Try 'sudo chown -R $(whoami) ./node_modules'",
	},
	{
		id:     "readme1",
		desc:   "README 混排（散文行 + 命令行 + 配置说明）",
		target: int(provider.Chinese),
		text: "Install the dependencies first, then start the dev server on port 8080.\n" +
			"npm install && npm run dev\n" +
			"The config file ~/.config/app/settings.json accepts a `log_level` " +
			"field (debug | info | warn).",
	},
	{
		id:     "log3",
		desc:   "Flutter 构建警告日志（KGP 迁移警告，长 URL/插件列表/相似行/缩进块）",
		target: int(provider.Chinese),
		text: "WARNING: Your Android app project: app located at: " +
			"/Users/wangs/dev/code/phos/android/app/build.gradle.kts\n" +
			"applies the Kotlin Gradle Plugin, which will cause build failures " +
			"in future versions of Flutter.\n" +
			"Please migrate your app to Built-in Kotlin using this guide: " +
			"https://docs.flutter.dev/release/breaking-changes/" +
			"migrate-to-built-in-kotlin/for-app-developers\n\n" +
			"WARNING: Your app uses the following plugins that apply Kotlin " +
			"Gradle Plugin (KGP): dynamic_color, file_picker, " +
			"flutter_image_compress_common, package_info_plus, photo_manager, " +
			"wakelock_plus\n" +
			"Future versions of Flutter will fail to build if your app uses " +
			"plugins that apply KGP.\n\n" +
			"Please check the changelogs of these plugins and upgrade to a " +
			"version that supports Built-in Kotlin.\n" +
			"If no such version exists, report the issue to the plugin. If " +
			"necessary, here is a guide on filing\n" +
			"an issue against a plugin: https://docs.flutter.dev/release/" +
			"breaking-changes/migrate-to-built-in-kotlin/for-app-developers" +
			"#report-incompatible-kotlin-gradle-plugin-usage-to-plugin-authors\n\n" +
			"If you are a plugin author, please migrate your plugin to " +
			"Built-in Kotlin using this guide: https://docs.flutter.dev/" +
			"release/breaking-changes/migrate-to-built-in-kotlin/" +
			"for-plugin-authors\n" +
			"Font asset \"CupertinoIcons.ttf\" was tree-shaken, reducing it " +
			"from 257628 to 1420 bytes (99.4% reduction). Tree-shaking can be " +
			"disabled by providing the --no-tree-shake-icons flag when " +
			"building your app.\n" +
			"Font asset \"MaterialIcons-Regular.otf\" was tree-shaken, " +
			"reducing it from 1645184 to 9392 bytes (99.4% reduction). " +
			"Tree-shaking can be disabled by providing the " +
			"--no-tree-shake-icons flag when building your app.\n" +
			"Warning: The generated ELF library contains unobfuscated DWARF " +
			"debugging information.\n" +
			"         To avoid this, use --strip to remove it.",
	},
	{
		id:     "auto1",
		desc:   "中英互译：英文输入自动译中",
		target: int(provider.AutoBidirectional),
		text:   "The deployment failed because the health check endpoint returned 503.",
	},
	{
		id:     "auto2",
		desc:   "中英互译：中文输入自动译英",
		target: int(provider.AutoBidirectional),
		text:   "部署失败，因为健康检查端点返回了 503。",
	},
}

var providerNames = []string{"zhipu", "siliconflow", "hunyuan-mt"}

func main() {
	prov := flag.String("provider", "hunyuan-mt", strings.Join(providerNames, " | "))
	caseFilter := flag.String("case", "", "只跑指定 id 的用例（逗号分隔）")
	verbose := flag.Bool("verbose", false, "打印每次翻译的完整管线日志")
	flag.Parse()

	idx := -1
	for i, name := range providerNames {
		if *prov == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		fmt.Fprintf(os.Stderr, "未知 provider %q（可选 %s）\n", *prov, strings.Join(providerNames, " / "))
		os.Exit(2)
	}
	key := resolveKey(*prov)
	if key == "" {
		fmt.Fprintf(os.Stderr, "缺少环境变量 TRANSLATE_API_KEY_%s 或 TRANSLATE_API_KEY（Key 不落盘）\n",
			strings.ToUpper(strings.ReplaceAll(*prov, "-", "_")))
		os.Exit(2)
	}

	var want map[string]bool
	if *caseFilter != "" {
		want = map[string]bool{}
		for _, id := range strings.Split(*caseFilter, ",") {
			want[strings.TrimSpace(id)] = true
		}
	}

	if err := pipeline.DefaultInit(); err != nil {
		fmt.Fprintf(os.Stderr, "繁简转换初始化失败：%v\n", err)
		os.Exit(2)
	}

	fmt.Printf("# provider=%s cases=%v\n", *prov, len(cases))
	adapter := provider.New(provider.Provider(idx))
	failed := 0
	for _, tc := range cases {
		if want != nil && !want[tc.id] {
			continue
		}
		var log *pipeline.Logger
		if *verbose {
			log = pipeline.NewLogger()
		}
		start := time.Now()
		res := pipeline.Translate(context.Background(), pipeline.Options{
			Provider: idx,
			Target:   tc.target,
			APIKey:   key,
			Log:      log,
		}, tc.text, adapter, nil, nil)
		elapsed := time.Since(start).Milliseconds()

		if res.OK {
			fmt.Printf("== %s | %s\n   译文(%dms): %s\n", tc.id, tc.desc, elapsed, firstLine(res.Text))
		} else {
			failed++
			fmt.Printf("== %s | %s  [失败 %dms]\n   错误: %s\n", tc.id, tc.desc, elapsed, res.Error)
		}
		if log != nil {
			fmt.Println(log.Render())
		}
	}

	total := len(cases)
	if want != nil {
		total = countFiltered(want)
	}
	fmt.Printf("\n# %d/%d 通过\n", total-failed, total)
	if failed > 0 {
		os.Exit(1)
	}
}

func countFiltered(want map[string]bool) int {
	n := 0
	for _, tc := range cases {
		if want[tc.id] {
			n++
		}
	}
	return n
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// resolveKey mirrors translate-cli: per-provider env first, then generic.
func resolveKey(prov string) string {
	envName := "TRANSLATE_API_KEY_" + strings.ToUpper(strings.ReplaceAll(prov, "-", "_"))
	if k := os.Getenv(envName); k != "" {
		return k
	}
	return os.Getenv("TRANSLATE_API_KEY")
}
