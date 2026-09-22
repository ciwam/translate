#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""translate-mcp — MCP stdio server exposing the translate kernel to AI clients.

Architecture:
    WorkBuddy AI ⇄ (MCP stdio, line JSON-RPC) ⇄ this server ⇄ (JSON stdin/stdout)
                   ⇄ translate-cli (C++, links translate_core)

Protocol (hand-written, zero deps; mirrors MCP stdio spec):
    initialize / notifications/initialized / tools/list / tools/call / ping
    (+ empty resources/list, prompts/list for read-only clients)

Rules honored:
    - stdout carries ONLY protocol JSON; logs go to stderr
    - tool failures use isError:true content (model-readable), not -32xxx
    - one call answers the whole question: translation + pipeline route + timing

API key resolution (never persisted by this server):
    1. per-call `api_key` argument
    2. env TRANSLATE_API_KEY_ZHIPU / TRANSLATE_API_KEY_SILICONFLOW (provider-specific)
    3. env TRANSLATE_API_KEY (generic fallback)
"""

import json
import os
import subprocess
import sys
import threading

PROTOCOL_VERSION = "2024-11-05"
SERVER_NAME = "translate-debug"
SERVER_VERSION = "1.0.0"
CLI_TIMEOUT_SECONDS = 180

TARGETS = ["en", "zh", "ja", "ko", "fr", "de", "es"]
PROVIDERS = ["zhipu", "siliconflow"]

ROUTE_DOC = (
    "route 字段说明翻译内核走的管线："
    "cache=命中本地缓存；prose=纯文本直译；prose-chunked(N)=长文本按句切 N 段顺序翻译；"
    "splice(translated a/b)=技术行拼接管线，b 行全部被模型翻译且通过校验（a 恒等于 b，"
    "否则整体报错）；splice(incomplete translated a/b)=部分行多次重试仍失败，"
    "整体报错不返回混排结果；splice-verbatim=纯技术内容无需翻译。"
)

TRANSLATE_TOOL = {
    "name": "translate",
    "description": (
        "调用 translate 项目的 C++ 翻译内核翻译文本（等同 macOS App 实际使用的管线）。"
        "返回译文、route（内部路由诊断）与耗时，用于排查翻译质量问题。"
        + ROUTE_DOC
    ),
    "inputSchema": {
        "type": "object",
        "properties": {
            "text": {"type": "string", "description": "要翻译的原文（必填）"},
            "target": {
                "type": "string",
                "enum": TARGETS,
                "description": "目标语言代码，默认 zh（简体中文）",
            },
            "provider": {
                "type": "string",
                "enum": PROVIDERS,
                "description": "模型供应商：zhipu=GLM-4-Flash，siliconflow=Qwen2.5-7B-Instruct，默认 zhipu",
            },
            "glossary": {
                "type": "string",
                "description": "可选术语表，每行一条 'term = translation'",
            },
            "api_key": {
                "type": "string",
                "description": "可选；缺省时读 TRANSLATE_API_KEY_ZHIPU / TRANSLATE_API_KEY_SILICONFLOW 环境变量",
            },
        },
        "required": ["text"],
    },
}

COMPARE_TOOL = {
    "name": "translate_compare",
    "description": (
        "同一段文本分别用 zhipu(GLM-4-Flash) 与 siliconflow(Qwen2.5-7B-Instruct) 并行翻译，"
        "一次返回两路结果与 route，便于 A/B 对比定位译文质量问题。" + ROUTE_DOC
    ),
    "inputSchema": {
        "type": "object",
        "properties": {
            "text": {"type": "string", "description": "要翻译的原文（必填）"},
            "target": {
                "type": "string",
                "enum": TARGETS,
                "description": "目标语言代码，默认 zh（简体中文）",
            },
            "glossary": {
                "type": "string",
                "description": "可选术语表，每行一条 'term = translation'",
            },
            "api_key": {
                "type": "string",
                "description": "可选；缺省时按供应商读对应环境变量",
            },
        },
        "required": ["text"],
    },
}


def log(msg):
    sys.stderr.write("[translate-mcp] %s\n" % msg)
    sys.stderr.flush()


def cli_path():
    """Locate the C++ debug CLI (installed by mcp/build.sh)."""
    env = os.environ.get("TRANSLATE_CLI")
    candidates = [
        env,
        os.path.expanduser("~/.workbuddy/bin/translate-cli"),
        os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                     ".build-cli", "core", "translate_cli"),
    ]
    for c in candidates:
        if c and os.path.isfile(c) and os.access(c, os.X_OK):
            return c
    return None


def resolve_api_key(provider, args):
    if isinstance(args.get("api_key"), str) and args["api_key"].strip():
        return args["api_key"].strip()
    specific = "TRANSLATE_API_KEY_" + provider.upper()
    for name in (specific, "TRANSLATE_API_KEY"):
        v = os.environ.get(name)
        if v and v.strip():
            return v.strip()
    return None


def run_cli(payload):
    """Run translate-cli with a JSON request; return (dict, None) or (None, error).

    TRANSLATE_DEBUG=1 passes the CLI's stderr through (core splice trace)."""
    exe = cli_path()
    if not exe:
        return None, ("找不到 translate-cli，请先在项目 mcp/ 目录执行 ./build.sh 安装"
                      "（或设置 TRANSLATE_CLI 环境变量指向二进制）")
    debug = bool(os.environ.get("TRANSLATE_DEBUG"))
    try:
        proc = subprocess.run(
            [exe],
            input=json.dumps(payload, ensure_ascii=False).encode("utf-8"),
            stdout=subprocess.PIPE,
            stderr=None if debug else subprocess.PIPE,
            timeout=CLI_TIMEOUT_SECONDS,
        )
    except subprocess.TimeoutExpired:
        return None, ("翻译超时（>%ds）：模型服务未在期限内返回，请检查网络或缩短文本"
                      % CLI_TIMEOUT_SECONDS)
    except OSError as e:
        return None, "无法启动 translate-cli：%s" % e

    out = proc.stdout.decode("utf-8", errors="replace").strip()
    if not out:
        stderr_tail = ("" if debug else
                       proc.stderr.decode("utf-8", errors="replace").strip()[-300:])
        return None, "translate-cli 无输出（exit=%d）%s" % (
            proc.returncode, ("；stderr: " + stderr_tail) if stderr_tail else "")
    # take the last non-empty line (defensive against any future banner)
    line = out.splitlines()[-1]
    try:
        result = json.loads(line)
    except ValueError:
        return None, "translate-cli 输出不是 JSON：%s" % out[:300]
    return result, None


def tool_translate(args):
    text = args.get("text")
    if not isinstance(text, str) or not text.strip():
        return err_content("缺少必填参数 text（要翻译的文本）")
    target = args.get("target") if args.get("target") in TARGETS else "zh"
    provider = args.get("provider") if args.get("provider") in PROVIDERS else "zhipu"

    api_key = resolve_api_key(provider, args)
    if not api_key:
        return err_content(
            "缺少 API Key：请在调用参数传入 api_key，或为连接器配置环境变量 "
            "TRANSLATE_API_KEY_ZHIPU / TRANSLATE_API_KEY_SILICONFLOW"
            "（连接器管理页编辑该 MCP 的 env 字段）"
        )

    payload = {"provider": provider, "target": target, "text": text,
               "api_key": api_key}
    if isinstance(args.get("glossary"), str) and args["glossary"].strip():
        payload["glossary"] = args["glossary"]

    result, error = run_cli(payload)
    if error:
        return err_content(error)

    if result.get("ok"):
        reply = {
            "translation": result.get("text", ""),
            "provider": result.get("provider", provider),
            "target": result.get("target", target),
            "route": result.get("route", ""),
            "elapsed_ms": result.get("elapsed_ms"),
            "source_chars": len(text),
        }
        reply["_note"] = ROUTE_DOC
        return ok_content(json.dumps(reply, ensure_ascii=False, indent=2))
    # Kernel-level failure (API error, fallback …): route info still helps triage.
    reply = {
        "ok": False,
        "error": result.get("error", "未知错误"),
        "provider": result.get("provider", provider),
        "target": result.get("target", target),
        "route": result.get("route", ""),
        "elapsed_ms": result.get("elapsed_ms"),
    }
    return err_content(json.dumps(reply, ensure_ascii=False, indent=2))


def tool_compare(args):
    text = args.get("text")
    if not isinstance(text, str) or not text.strip():
        return err_content("缺少必填参数 text（要翻译的文本）")
    target = args.get("target") if args.get("target") in TARGETS else "zh"

    keys = {p: resolve_api_key(p, args) for p in PROVIDERS}
    missing = [p for p in PROVIDERS if not keys[p]]
    if missing:
        return err_content(
            "缺少 API Key（%s）：请传入 api_key 或配置对应环境变量" % "/".join(missing)
        )

    glossary = args.get("glossary") if isinstance(args.get("glossary"), str) else None
    results = {}

    def worker(provider):
        payload = {"provider": provider, "target": target, "text": text,
                   "api_key": keys[provider]}
        if glossary and glossary.strip():
            payload["glossary"] = glossary
        results[provider] = run_cli(payload)

    threads = [threading.Thread(target=worker, args=(p,)) for p in PROVIDERS]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    report = {"target": target, "source_chars": len(text), "providers": {}}
    for p in PROVIDERS:
        res, error = results[p]
        if error:
            report["providers"][p] = {"ok": False, "error": error}
        elif res.get("ok"):
            report["providers"][p] = {
                "ok": True,
                "translation": res.get("text", ""),
                "route": res.get("route", ""),
                "elapsed_ms": res.get("elapsed_ms"),
            }
        else:
            report["providers"][p] = {
                "ok": False,
                "error": res.get("error", "未知错误"),
                "route": res.get("route", ""),
                "elapsed_ms": res.get("elapsed_ms"),
            }
    report["_note"] = ROUTE_DOC + " 对比时重点关注：两路 route 是否一致、译文风格/术语差异、何者保留了源文本结构。"
    return ok_content(json.dumps(report, ensure_ascii=False, indent=2))


def ok_content(text):
    return {"content": [{"type": "text", "text": text}]}


def err_content(text):
    return {"content": [{"type": "text", "text": text}], "isError": True}


TOOLS = {t["name"]: t for t in (TRANSLATE_TOOL, COMPARE_TOOL)}
HANDLERS = {"translate": tool_translate, "translate_compare": tool_compare}


def handle(req):
    method = req.get("method", "")
    params = req.get("params") or {}
    if method == "initialize":
        ver = params.get("protocolVersion")
        return {
            "protocolVersion": ver if isinstance(ver, str) else PROTOCOL_VERSION,
            "capabilities": {"tools": {}},
            "serverInfo": {"name": SERVER_NAME, "version": SERVER_VERSION},
        }
    if method == "ping":
        return {}
    if method == "tools/list":
        return {"tools": list(TOOLS.values())}
    if method == "resources/list":
        return {"resources": []}
    if method == "prompts/list":
        return {"prompts": []}
    if method == "tools/call":
        name = params.get("name")
        args = params.get("arguments") or {}
        if name not in HANDLERS:
            return err_content("未知工具：%s（可用：%s）" % (name, ", ".join(TOOLS)))
        try:
            return HANDLERS[name](args)
        except Exception as e:  # tool boundary: report, never crash the server
            log("tools/call %s 异常: %r" % (name, e))
            return err_content("工具内部错误：%s" % e)
    return None  # unknown method → caller maps to -32601


def main():
    log("started (pid=%d, cli=%s)" % (os.getpid(), cli_path() or "未找到"))
    for raw in sys.stdin:
        line = raw.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except ValueError:
            write_resp({"jsonrpc": "2.0", "id": None,
                        "error": {"code": -32700, "message": "Parse error"}})
            continue
        req_id = req.get("id")
        method = req.get("method", "")
        if req_id is None:
            # notification (e.g. notifications/initialized): no response ever
            log("notification: %s" % method)
            continue
        if not isinstance(method, str) or (
            method not in ("initialize", "ping", "tools/list", "tools/call",
                           "resources/list", "prompts/list")
        ):
            write_resp({"jsonrpc": "2.0", "id": req_id,
                        "error": {"code": -32601, "message": "Method not found"}})
            continue
        resp = handle(req)
        write_resp({"jsonrpc": "2.0", "id": req_id, "result": resp})
    log("stdin closed, exiting")


def write_resp(obj):
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()


if __name__ == "__main__":
    try:
        main()
    except (KeyboardInterrupt, BrokenPipeError):
        pass
