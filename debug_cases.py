#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""debug_cases.py — 翻译质量调试用例集（驱动 Go 版 translate-cli）。

直接调用 cmd/translate-cli（JSON stdin/stdout，与 MCP 包装同一条契约），
跑一批面向计算机从业者（开发/运维/测试/调试）的典型场景。

用法（Key 只经环境变量传递，不写入任何文件）:
    TRANSLATE_API_KEY_SILICONFLOW=<key> python3 debug_cases.py                 # 全部用例（默认 zhipu 需对应 Key）
    TRANSLATE_API_KEY_SILICONFLOW=<key> python3 debug_cases.py --case paren1   # 单用例
    TRANSLATE_API_KEY_SILICONFLOW=<key> python3 debug_cases.py --provider hunyuan-mt

结果原始 JSON 追加式写入 .workbuddy/debug-runs/<ts>.json（不覆盖历史）。
"""

import argparse
import json
import os
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
CLI = os.path.join(HERE, "cmd", "translate-cli")
RUNS_DIR = os.path.join(os.path.dirname(HERE), ".workbuddy", "debug-runs")

PROVIDERS = ["zhipu", "siliconflow", "hunyuan-mt"]

# 面向开发/运维/测试/调试人员的典型翻译场景，target 默认简体中文。
CASES = [
    {
        "id": "paren1",
        "desc": "括号短语（词边缘括号曾误掩码，回归用例）",
        "args": {
            "target": "zh",
            "text": "Name is too long (maximum is 40 characters)",
        },
    },
    {
        "id": "prose1",
        "desc": "英文短文（纯散文，技术博客风）",
        "args": {
            "target": "zh",
            "text": (
                "Distributed tracing helps engineers follow a single request as it "
                "travels across services. When latency spikes, the trace reveals which "
                "hop added the delay, turning guesswork into evidence. Most teams adopt "
                "it only after a painful outage that a dashboard alone could not explain."
            ),
        },
    },
    {
        "id": "log1",
        "desc": "构建日志（cmake/make，路径+目标名+错误码）",
        "args": {
            "target": "zh",
            "text": (
                "[2026-09-16 10:02:11] ERROR: /Users/ciwang/proj/main.cpp:42: "
                "undefined reference to `vtable for Widget' in Debug/libgui.a\n"
                "make[2]: *** [CMakeFiles/app.dir/build.make:113: app] Error 1\n"
                "make[1]: *** [CMakeFiles/Makefile2:88: CMakeFiles/app.dir/all] Error 2\n"
                "Warning: deprecated conversion from string constant to 'char*'"
            ),
        },
    },
    {
        "id": "log2",
        "desc": "运维日志（nginx upstream 超时，键值对混排）",
        "args": {
            "target": "zh",
            "text": (
                "Sep 16 02:14:03 web-1 nginx[812]: upstream timed out (110: Connection "
                "timed out) while reading response header from upstream, client: "
                "10.0.3.17, server: api.internal:443, request: \"GET /v2/health "
                "HTTP/1.1\", upstream: \"http://127.0.0.1:8080/v2/health\""
            ),
        },
    },
    {
        "id": "err1",
        "desc": "调试报错（运行时异常堆栈 + 编译器提示）",
        "args": {
            "target": "zh",
            "text": (
                "TypeError: Cannot read properties of undefined (reading 'map')\n"
                "    at UserList (src/components/UserList.tsx:23:19)\n"
                "Expected 3 arguments, but got 2. Did you forget to pass the callback?"
            ),
        },
    },
    {
        "id": "short1",
        "desc": "短句（回显风险）+ 含 sudo 命令",
        "args": {
            "target": "zh",
            "text": (
                "Build succeeded. 0 warnings, 3 tests failed.\n"
                "Permission denied. Try 'sudo chown -R $(whoami) ./node_modules'"
            ),
        },
    },
    {
        "id": "readme1",
        "desc": "README 混排（散文行 + 命令行 + 配置说明）",
        "args": {
            "target": "zh",
            "text": (
                "Install the dependencies first, then start the dev server on port 8080.\n"
                "npm install && npm run dev\n"
                "The config file ~/.config/app/settings.json accepts a `log_level` "
                "field (debug | info | warn)."
            ),
        },
    },
    {
        "id": "log3",
        "desc": "Flutter 构建警告日志（KGP 迁移警告，长 URL/插件列表/相似行/缩进块）",
        "args": {
            "target": "zh",
            "text": (
                "WARNING: Your Android app project: app located at: "
                "/Users/wangs/dev/code/phos/android/app/build.gradle.kts\n"
                "applies the Kotlin Gradle Plugin, which will cause build failures "
                "in future versions of Flutter.\n"
                "Please migrate your app to Built-in Kotlin using this guide: "
                "https://docs.flutter.dev/release/breaking-changes/"
                "migrate-to-built-in-kotlin/for-app-developers\n"
                "\n"
                "WARNING: Your app uses the following plugins that apply Kotlin "
                "Gradle Plugin (KGP): dynamic_color, file_picker, "
                "flutter_image_compress_common, package_info_plus, photo_manager, "
                "wakelock_plus\n"
                "Future versions of Flutter will fail to build if your app uses "
                "plugins that apply KGP.\n"
                "\n"
                "Please check the changelogs of these plugins and upgrade to a "
                "version that supports Built-in Kotlin.\n"
                "If no such version exists, report the issue to the plugin. If "
                "necessary, here is a guide on filing\n"
                "an issue against a plugin: https://docs.flutter.dev/release/"
                "breaking-changes/migrate-to-built-in-kotlin/for-app-developers"
                "#report-incompatible-kotlin-gradle-plugin-usage-to-plugin-authors\n"
                "\n"
                "If you are a plugin author, please migrate your plugin to "
                "Built-in Kotlin using this guide: https://docs.flutter.dev/"
                "release/breaking-changes/migrate-to-built-in-kotlin/"
                "for-plugin-authors\n"
                "Font asset \"CupertinoIcons.ttf\" was tree-shaken, reducing it "
                "from 257628 to 1420 bytes (99.4% reduction). Tree-shaking can be "
                "disabled by providing the --no-tree-shake-icons flag when "
                "building your app.\n"
                "Font asset \"MaterialIcons-Regular.otf\" was tree-shaken, "
                "reducing it from 1645184 to 9392 bytes (99.4% reduction). "
                "Tree-shaking can be disabled by providing the "
                "--no-tree-shake-icons flag when building your app.\n"
                "Warning: The generated ELF library contains unobfuscated DWARF "
                "debugging information.\n"
                "         To avoid this, use --strip to remove it."
            ),
        },
    },
    {
        "id": "auto1",
        "desc": "中英互译：英文输入自动译中",
        "args": {
            "target": -1,
            "text": "The deployment failed because the health check endpoint returned 503.",
        },
    },
    {
        "id": "auto2",
        "desc": "中英互译：中文输入自动译英",
        "args": {
            "target": -1,
            "text": "部署失败，因为健康检查端点返回了 503。",
        },
    },
]


def run_case(provider, case, key):
    req = dict(case["args"])
    req["provider"] = provider
    env = os.environ.copy()
    env["TRANSLATE_API_KEY_" + provider.upper().replace("-", "_")] = key
    env["TRANSLATE_API_KEY"] = key  # generic fallback
    t0 = time.time()
    proc = subprocess.run(
        ["go", "run", "./cmd/translate-cli"],
        input=json.dumps(req, ensure_ascii=False).encode("utf-8"),
        capture_output=True, cwd=HERE, env=env, timeout=300,
    )
    wall_ms = int((time.time() - t0) * 1000)
    try:
        detail = json.loads(proc.stdout.decode("utf-8"))
    except ValueError:
        detail = {"raw": proc.stdout.decode("utf-8", "replace")}
    return {"id": case["id"], "desc": case["desc"],
            "exit": proc.returncode, "wall_ms": wall_ms, "detail": detail}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--case", help="只跑指定 id 的用例（逗号分隔）")
    ap.add_argument("--provider", default="hunyuan-mt", choices=PROVIDERS)
    args = ap.parse_args()

    want = set(args.case.split(",")) if args.case else None
    cases = [c for c in CASES if not want or c["id"] in want]
    if not cases:
        print("没有匹配的用例", file=sys.stderr)
        return 2

    # Key resolution mirrors translate-cli: per-provider env, then generic.
    key = (os.environ.get("TRANSLATE_API_KEY_" + args.provider.upper().replace("-", "_"))
           or os.environ.get("TRANSLATE_API_KEY"))
    if not key:
        print("缺少环境变量 TRANSLATE_API_KEY_%s 或 TRANSLATE_API_KEY（Key 不落盘）"
              % args.provider.upper().replace("-", "_"), file=sys.stderr)
        return 2

    print("# provider=%s cases=%s" % (args.provider,
          ",".join(c["id"] for c in cases)), flush=True)

    results = []
    failed = 0
    for c in cases:
        row = run_case(args.provider, c, key)
        results.append(row)
        ok = row["detail"].get("ok", False)
        if not ok:
            failed += 1
        print("\n== %s | %s%s" % (c["id"], c["desc"],
              "" if ok else "  [失败]"), flush=True)
        print(json.dumps(row["detail"], ensure_ascii=False, indent=2), flush=True)

    os.makedirs(RUNS_DIR, exist_ok=True)
    out = os.path.join(RUNS_DIR, "%s.json" % time.strftime("%Y%m%d-%H%M%S"))
    with open(out, "w", encoding="utf-8") as f:
        json.dump({"provider": args.provider, "cases": results},
                  ensure_ascii=False, indent=2, fp=f)
    print("\n# %d/%d 通过 | 结果已保存: %s" % (len(results) - failed, len(results), out))
    if failed:
        print("# %d 个用例失败" % failed, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
