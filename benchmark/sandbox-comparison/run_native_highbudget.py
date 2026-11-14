#!/usr/bin/env python3
"""Repeat the unchanged native MCP trial with explicit higher runtime budgets.

The original harness/results stay immutable. Runtime dependency bindings below
change only call limits, the controller's budget sentence and graph recursion.
The manifest records these overrides and hashes both the base and this runner.
"""

import argparse
import hashlib
import importlib.metadata
import os
import platform
import random
import time
from datetime import datetime, timezone
from pathlib import Path

import run_native_mcp as native
import run_v2 as base
from evaluation_v2 import TASKS, summarize


MAX_MODELS = 64
MAX_TOOLS = 96
GRAPH_LIMIT = 512
CONTROLLER = native.CONTROLLER.replace(
    "at most eight model calls and twelve tool calls",
    "at most 64 model calls and 96 tool calls",
)
assert CONTROLLER != native.CONTROLLER
ORIGINAL_CREATE_AGENT = native.create_agent


class HighBudgetRecorder(native.NativeRecorder):
    def wrap_tool_call(self, request, handler):
        if self.tool_calls >= MAX_TOOLS:
            raise base.BudgetExceeded("tool call budget exhausted")
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
            raw = response.artifact
            if not isinstance(raw, dict) or "content" not in raw:
                raise ValueError("missing native MCP result artifact")
            failed = bool(raw.get("isError"))
            event.update(ok=not failed, observation=raw,
                         error_kind="mcp_isError" if failed else None)
            return response
        except Exception as exc:
            event.update(ok=False, error_kind="tool_protocol",
                         error="%s: %s" % (type(exc).__name__, str(exc)))
            raise
        finally:
            event["duration_s"] = time.perf_counter() - started
            self.events.append(event)


def create_highbudget_agent(*args, **kwargs):
    graph = ORIGINAL_CREATE_AGENT(*args, **kwargs)

    class BudgetGraph:
        def invoke(self, state, config=None):
            return graph.invoke(
                state, config={**(config or {}), "recursion_limit": GRAPH_LIMIT,
                               "max_concurrency": 1})
    return BudgetGraph()


def configure():
    # The inherited model recorder reads this module setting at call time.
    base.MAX_MODEL_CALLS = MAX_MODELS
    native.NativeRecorder = HighBudgetRecorder
    native.CONTROLLER = CONTROLLER
    native.create_agent = create_highbudget_agent


def main():
    os.environ["LANGSMITH_TRACING"] = "false"
    os.environ["LANGCHAIN_TRACING_V2"] = "false"
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    configure()
    args.output.mkdir(parents=True, exist_ok=False)
    order = [(task, rep) for task in TASKS for rep in range(3)]
    random.Random(20261114).shuffle(order)
    here = Path(__file__).resolve().parent
    manifest = {
        "track": "native-mcp-official-prompt-highbudget",
        "started_at": datetime.now(timezone.utc).isoformat(),
        "order": order, "model": "deepseek-flash", "temperature": 0,
        "max_model_calls": MAX_MODELS, "max_tool_calls": MAX_TOOLS,
        "graph_recursion_limit": GRAPH_LIMIT, "max_concurrency": 1,
        "max_output_tokens": 1200, "controller_prompt": CONTROLLER,
        "base_trial": "run_native_mcp.trial; runtime bindings recorded by this runner",
        "baseline_dataset": "results/v2-native-mcp",
        "platform": platform.platform(),
        "dependencies": {p: importlib.metadata.version(p) for p in (
            "langchain", "langchain-core", "langchain-openai")},
        "source_sha256": {
            name: hashlib.sha256((here / name).read_bytes()).hexdigest()
            for name in ("run_native_highbudget.py", "run_native_mcp.py", "run_v2.py",
                         "mcp_transport_v2.py", "evaluation_v2.py", "backends_v2.py",
                         "PROTOCOL_NATIVE_HIGHBUDGET.md")
        },
    }
    base.write_json(args.output / "manifest.json", manifest)
    rows = []
    for index, (task, repetition) in enumerate(order):
        row = native.trial(task, repetition)
        rows.append(row)
        base.write_json(args.output / ("%03d-%s-%d.json" % (index, task, repetition)), row)
        base.write_json(args.output / "trials.json", rows)
        base.write_json(args.output / "summary.json", summarize(rows))
        print("%d/12 highbudget %s r%d passed=%s agent_error=%s" % (
            index + 1, task, repetition, row["passed"], row["agent_error"]), flush=True)
    manifest["finished_at"] = datetime.now(timezone.utc).isoformat()
    base.write_json(args.output / "manifest.json", manifest)


if __name__ == "__main__":
    main()
