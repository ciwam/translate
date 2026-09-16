#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""debug_cases.py — 翻译质量调试用例集（MCP 端到端驱动）。

直接以 MCP stdio 协议驱动 mcp/translate_mcp.py（与 WorkBuddy 连接器同一条
协议路径），跑一批面向计算机从业者（开发/运维/测试/调试）的典型场景。

用法（Key 只经环境变量传递，不写入任何文件）:
    TRANSLATE_API_KEY_ZHIPU=<key> python3 mcp/debug_cases.py                # 全部用例
    TRANSLATE_API_KEY_ZHIPU=<key> python3 mcp/debug_cases.py --case log1    # 单用例
    TRANSLATE_API_KEY_SILICONFLOW=<key> python3 mcp/debug_cases.py --provider siliconflow

结果原始 JSON 追加式写入 .workbuddy/debug-runs/<ts>.json（不覆盖历史）。
"""

import argparse
import json
import os
import subprocess
import sys
import threading
import time

HERE = os.path.dirname(os.path.abspath(__file__))
SERVER = os.path.join(HERE, "translate_mcp.py")
RUNS_DIR = os.path.join(os.path.dirname(HERE), ".workbuddy", "debug-runs")

# 面向开发/运维/测试/调试人员的典型翻译场景，target 默认简体中文。
CASES = [
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
]


class McpClient:
    """Minimal line-JSON MCP client over stdin/stdout of the server process."""

    def __init__(self, env_extra=None):
        env = os.environ.copy()
        if env_extra:
            env.update(env_extra)
        self.proc = subprocess.Popen(
            [sys.executable, SERVER],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            text=True, encoding="utf-8", env=env, bufsize=1,
        )
        self._lock = threading.Lock()
        self._next_id = 0

    def request(self, method, params=None, expect_reply=True):
        with self._lock:
            self._next_id += 1
            rid = self._next_id
            frame = {"jsonrpc": "2.0", "id": rid, "method": method}
            if params is not None:
                frame["params"] = params
            self.proc.stdin.write(json.dumps(frame, ensure_ascii=False) + "\n")
            self.proc.stdin.flush()
            if not expect_reply:
                return None
            while True:
                line = self.proc.stdout.readline()
                if not line:
                    raise RuntimeError("server closed stream (method=%s)" % method)
                resp = json.loads(line)
                if resp.get("id") == rid:
                    return resp

    def notify(self, method, params=None):
        self.request(method, params, expect_reply=False)

    def close(self):
        try:
            self.proc.stdin.close()
        except Exception:
            pass
        self.proc.wait(timeout=5)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--case", help="只跑指定 id 的用例（逗号分隔）")
    ap.add_argument("--provider", default="zhipu", choices=["zhipu", "siliconflow"])
    ap.add_argument("--target", default="zh")
    args = ap.parse_args()

    want = set(args.case.split(",")) if args.case else None
    cases = [c for c in CASES if not want or c["id"] in want]
    if not cases:
        print("没有匹配的用例", file=sys.stderr)
        return 2

    key_env = "TRANSLATE_API_KEY_" + args.provider.upper()
    if not os.environ.get(key_env):
        print("缺少环境变量 %s（Key 不落盘，只经环境变量传递）" % key_env, file=sys.stderr)
        return 2

    cli = McpClient()
    init = cli.request("initialize", {"protocolVersion": "2024-11-05"})
    server_info = init["result"]["serverInfo"]
    cli.notify("notifications/initialized")
    print("# server: %s %s | provider=%s target=%s cases=%s" % (
        server_info["name"], server_info["version"], args.provider, args.target,
        ",".join(c["id"] for c in cases)), flush=True)

    results = []
    for c in cases:
        call_args = dict(c["args"])
        call_args.setdefault("target", args.target)
        call_args["provider"] = args.provider
        t0 = time.time()
        resp = cli.request("tools/call", {"name": "translate", "arguments": call_args})
        wall_ms = int((time.time() - t0) * 1000)
        payload = resp["result"]
        is_err = payload.get("isError", False)
        text = payload["content"][0]["text"]
        try:
            detail = json.loads(text)
        except ValueError:
            detail = {"raw": text}
        row = {"id": c["id"], "desc": c["desc"], "isError": is_err,
               "wall_ms": wall_ms, "detail": detail}
        results.append(row)
        print("\n== %s | %s%s" % (c["id"], c["desc"], "  [isError]" if is_err else ""))
        print(json.dumps(detail, ensure_ascii=False, indent=2), flush=True)

    cli.close()

    os.makedirs(RUNS_DIR, exist_ok=True)
    out = os.path.join(RUNS_DIR, "%s.json" % time.strftime("%Y%m%d-%H%M%S"))
    with open(out, "w", encoding="utf-8") as f:
        json.dump({"provider": args.provider, "target": args.target,
                   "cases": results}, f, ensure_ascii=False, indent=2)
    print("\n# 结果已追加保存: %s" % out)
    return 0


if __name__ == "__main__":
    sys.exit(main())
