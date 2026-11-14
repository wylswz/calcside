#!/usr/bin/env python3
"""LangChain benchmark runner. See PROTOCOL.md before interpreting results."""

import argparse
import hashlib
import importlib.metadata
import json
import os
import platform
import random
import subprocess
import time
from datetime import datetime, timezone
from pathlib import Path

from langchain.agents import create_agent
from langchain.agents.middleware import AgentMiddleware
from langchain_core.messages import AIMessage
from langchain_core.tools import tool
from langchain_openai import ChatOpenAI

from backends_v2 import BACKENDS
from evaluation_v2 import COMMON_PROMPT, LANGUAGE_PROMPTS, TASKS, summarize, validate


HERE = Path(__file__).resolve().parent
MAX_MODEL_CALLS = 8
MAX_TOOL_CALLS = 12


class BudgetExceeded(RuntimeError):
    pass


class Recorder(AgentMiddleware):
    """Observe model/tool boundaries without changing successful observations."""

    def __init__(self):
        self.events = []
        self.model_calls = 0
        self.tool_calls = 0
        self.origin = time.perf_counter()

    def wrap_model_call(self, request, handler):
        if self.model_calls >= MAX_MODEL_CALLS:
            raise BudgetExceeded("model call budget exhausted")
        self.model_calls += 1
        started = time.perf_counter()
        event = {"kind": "model", "index": self.model_calls,
                 "start_s": started - self.origin}
        try:
            response = handler(request)
            messages = [m for m in response.result if isinstance(m, AIMessage)]
            usages = [m.usage_metadata for m in messages if m.usage_metadata]
            event.update({
                "ok": True,
                "input_tokens": sum(u["input_tokens"] for u in usages) if usages else None,
                "output_tokens": sum(u["output_tokens"] for u in usages) if usages else None,
                "responses": [
                    {"content": m.content, "tool_calls": m.tool_calls,
                     "response_metadata": {
                         k: m.response_metadata[k]
                         for k in ("model_name", "finish_reason")
                         if k in m.response_metadata
                     }}
                    for m in messages
                ],
            })
            return response
        except Exception as exc:
            event.update(ok=False, error="%s: %s" % (type(exc).__name__, str(exc)))
            raise
        finally:
            event["duration_s"] = time.perf_counter() - started
            self.events.append(event)

    def wrap_tool_call(self, request, handler):
        if self.tool_calls >= MAX_TOOL_CALLS:
            raise BudgetExceeded("tool call budget exhausted")
        self.tool_calls += 1
        started = time.perf_counter()
        event = {
            "kind": "tool", "index": self.tool_calls,
            "start_s": started - self.origin,
            "tool_call_id": request.tool_call["id"],
            "name": request.tool_call["name"], "args": request.tool_call["args"],
        }
        try:
            response = handler(request)
            payload = json.loads(response.content)
            event.update(
                ok=not bool(payload.get("error")), observation=payload,
                error_kind=payload.get("error_kind") or (
                    "runtime" if payload.get("error") else None),
            )
            return response
        except Exception as exc:
            event.update(ok=False, error_kind="tool_protocol",
                         error="%s: %s" % (type(exc).__name__, str(exc)))
            raise
        finally:
            event["duration_s"] = time.perf_counter() - started
            self.events.append(event)


def run_trial(backend_name, task_id, repetition, model_name):
    backend = BACKENDS[backend_name]()
    recorder = Recorder()
    row = {
        "backend": backend_name, "task": task_id, "repetition": repetition,
        "passed": False, "events": recorder.events, "setup_s": None,
        "loop_s": None, "verification_s": None, "cleanup_error": None,
        "agent_error": None, "setup_error": None,
    }
    started = time.perf_counter()
    setup_ok = False
    try:
        backend.setup(TASKS[task_id]["files"])
        setup_ok = True
    except Exception as exc:
        row["setup_error"] = "%s: %s" % (type(exc).__name__, str(exc))
    row["setup_s"] = time.perf_counter() - started
    try:
        if setup_ok:
            row["adapter"] = backend.metadata()

            @tool
            def execute_code(code: str) -> str:
                """Execute code in the workspace; return output and structured errors."""
                try:
                    return json.dumps(backend.execute(code))
                except Exception as exc:
                    return json.dumps({
                        "output": "", "error": "%s: %s" % (type(exc).__name__, str(exc)),
                        "error_kind": "transport",
                    })

            model = ChatOpenAI(
                model=model_name, base_url="https://api.deepseek.com",
                api_key=os.environ["DEEPSEEK_API_KEY"],
                temperature=0, max_tokens=1200, timeout=90, max_retries=0,
                use_responses_api=False,
                model_kwargs={"parallel_tool_calls": False},
            )
            agent = create_agent(
                model=model, tools=[execute_code], middleware=[recorder],
                system_prompt=COMMON_PROMPT + LANGUAGE_PROMPTS[backend.language],
            )
            started = time.perf_counter()
            try:
                result = agent.invoke(
                    {"messages": [{"role": "user", "content": TASKS[task_id]["prompt"]}]},
                    config={"recursion_limit": 40},
                )
                row["final_answer"] = result["messages"][-1].content
            except Exception as exc:
                row["agent_error"] = "%s: %s" % (type(exc).__name__, str(exc))
            row["loop_s"] = time.perf_counter() - started
            started = time.perf_counter()
            try:
                artifact = backend.read_file(TASKS[task_id]["artifact"])
                row["artifact"] = artifact
                row["verification"] = validate(task_id, artifact)
                row["passed"] = row["verification"]["ok"]
            except Exception as exc:
                row["verification"] = {
                    "ok": False, "reason": "%s: %s" % (type(exc).__name__, str(exc))}
            row["verification_s"] = time.perf_counter() - started
    except Exception as exc:
        row["agent_error"] = "%s: %s" % (type(exc).__name__, str(exc))
    finally:
        started = time.perf_counter()
        try:
            backend.close()
        except Exception as exc:
            row["cleanup_error"] = "%s: %s" % (type(exc).__name__, str(exc))
        row["cleanup_s"] = time.perf_counter() - started
    if row["loop_s"] is not None:
        row["model_s"] = sum(e["duration_s"] for e in recorder.events if e["kind"] == "model")
        row["tool_s"] = sum(e["duration_s"] for e in recorder.events if e["kind"] == "tool")
        row["residual_s"] = row["loop_s"] - row["model_s"] - row["tool_s"]
    return row


def redact(value):
    secret_values = [
        v for k, v in os.environ.items()
        if any(marker in k.upper() for marker in ("KEY", "TOKEN", "SECRET", "PASSWORD"))
        and len(v) >= 8
    ]

    def visit(item):
        if isinstance(item, str):
            for secret in secret_values:
                item = item.replace(secret, "[REDACTED]")
            return item
        if isinstance(item, list):
            return [visit(x) for x in item]
        if isinstance(item, dict):
            return {k: visit(v) for k, v in item.items()}
        return item
    return visit(value)


def write_json(path, value):
    path.write_text(json.dumps(redact(value), indent=2, ensure_ascii=True) + "\n")


def main():
    os.environ["LANGSMITH_TRACING"] = "false"
    os.environ["LANGCHAIN_TRACING_V2"] = "false"
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--backends", nargs="+", choices=sorted(BACKENDS), default=sorted(BACKENDS))
    parser.add_argument("--repetitions", type=int, default=3)
    parser.add_argument("--seed", type=int, default=20261114)
    parser.add_argument("--model", default="deepseek-flash")
    args = parser.parse_args()
    if args.repetitions < 1 or len(set(args.backends)) != len(args.backends):
        parser.error("positive repetitions and unique backends required")
    if not os.environ.get("DEEPSEEK_API_KEY"):
        parser.error("DEEPSEEK_API_KEY must be set")
    args.output.mkdir(parents=True, exist_ok=False)
    order = [(b, t, r) for b in args.backends for t in TASKS for r in range(args.repetitions)]
    random.Random(args.seed).shuffle(order)
    manifest = {
        "schema": "sandbox-comparison-v2",
        "started_at": datetime.now(timezone.utc).isoformat(),
        "platform": platform.platform(), "python": platform.python_version(),
        "commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=HERE, text=True).strip(),
        "git_status": subprocess.check_output(["git", "status", "--short"], cwd=HERE, text=True),
        "model": args.model, "temperature": 0, "max_output_tokens": 1200,
        "max_model_calls": MAX_MODEL_CALLS, "max_tool_calls": MAX_TOOL_CALLS,
        "seed": args.seed, "order": order, "repetitions": args.repetitions,
        "dependencies": {
            p: importlib.metadata.version(p)
            for p in ("langchain", "langchain-core", "langchain-openai", "e2b-code-interpreter")
        },
        "source_sha256": {
            name: hashlib.sha256((HERE / name).read_bytes()).hexdigest()
            for name in ("run_v2.py", "evaluation_v2.py", "backends_v2.py", "PROTOCOL.md",
                         "csclient.py", "pykernel.py", "requirements-langchain.lock.txt")
        },
    }
    write_json(args.output / "manifest.json", manifest)
    rows = []
    for index, (backend, task, repetition) in enumerate(order):
        row = run_trial(backend, task, repetition, args.model)
        rows.append(row)
        write_json(args.output / ("%03d-%s-%s-%d.json" % (index, backend, task, repetition)), row)
        write_json(args.output / "trials.json", rows)
        write_json(args.output / "summary.json", summarize(rows))
        print("%d/%d %s %s r%d passed=%s" %
              (index + 1, len(order), backend, task, repetition, row["passed"]), flush=True)
    manifest["finished_at"] = datetime.now(timezone.utc).isoformat()
    write_json(args.output / "manifest.json", manifest)


if __name__ == "__main__":
    main()
