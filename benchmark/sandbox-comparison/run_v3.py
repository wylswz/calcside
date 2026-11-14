#!/usr/bin/env python3
"""High-budget, task-set B extension; preserves every previous source/result."""

import argparse
import hashlib
import importlib.metadata
import os
import platform
import random
from datetime import datetime, timezone
from pathlib import Path

import run_native_highbudget as high
import run_native_mcp as native
import run_v2 as uniform
from backends_v2 import BACKENDS, _safe_relpath
from evaluation_v2 import COMMON_PROMPT, LANGUAGE_PROMPTS
from evaluation_v3 import TASKS_B, summarize, validate


HERE = Path(__file__).resolve().parent
FACTORIES = dict(BACKENDS)
CONDITIONS = sorted([*FACTORIES, "calcside_mcp"])
CONTROLLER = COMMON_PROMPT.replace(
    "at most eight model calls and twelve tool calls",
    "at most 64 model calls and 96 tool calls",
)
assert CONTROLLER != COMMON_PROMPT


class StagedBackend:
    """Stage subdirectories without changing the frozen backend adapters."""

    def __init__(self, factory):
        self.adapter = factory()

    def __getattr__(self, name):
        return getattr(self.adapter, name)

    def setup(self, files):
        self.adapter.setup({})
        for name, content in files.items():
            rel = _safe_relpath(name)
            # Bounded controller writes also fit transports with small argv limits.
            chunks = [content[i:i + 4096] for i in range(0, len(content), 4096)] or [""]
            for index, chunk in enumerate(chunks):
                if self.adapter.language == "starlark":
                    value = repr(chunk) if index == 0 else "fs.read(%r) + %r" % (rel, chunk)
                    code = "fs.write(%r, %s)" % (rel, value)
                else:
                    code = (
                        "__import__('pathlib').Path(%r).parent.mkdir(parents=True, exist_ok=True)\n"
                        "open(%r, %r, encoding='utf-8').write(%r)"
                    ) % (rel, rel, "w" if index == 0 else "a", chunk)
                result = self.adapter.execute(code)
                if result.get("error"):
                    raise RuntimeError("fixture staging failed for %s: %s" % (rel, result["error"]))
            if self.adapter.read_file(rel) != content:
                raise RuntimeError("fixture staging mismatch: %s" % rel)

    def metadata(self):
        return {**self.adapter.metadata(),
                "fixture_staging": "4096-character controller writes + exact read-back"}


def configure():
    high.configure()
    uniform.MAX_TOOL_CALLS = high.MAX_TOOLS
    uniform.COMMON_PROMPT = CONTROLLER
    uniform.TASKS = TASKS_B
    uniform.validate = validate
    uniform.create_agent = high.create_highbudget_agent
    uniform.BACKENDS = {
        name: (lambda factory=factory: StagedBackend(factory))
        for name, factory in FACTORIES.items()
    }
    native.TASKS = TASKS_B
    native.validate = validate
    native.CalcsideBackend = lambda: StagedBackend(FACTORIES["calcside"])


def main():
    os.environ["LANGSMITH_TRACING"] = "false"
    os.environ["LANGCHAIN_TRACING_V2"] = "false"
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    configure()
    order = [(b, t, r) for b in CONDITIONS for t in TASKS_B for r in range(3)]
    random.Random(20261114).shuffle(order)
    manifest = {
        "track": "agentic-tasks-B-highbudget",
        "started_at": datetime.now(timezone.utc).isoformat(),
        "order": order, "model": "deepseek-flash", "temperature": 0,
        "max_model_calls": high.MAX_MODELS, "max_tool_calls": high.MAX_TOOLS,
        "graph_recursion_limit": high.GRAPH_LIMIT, "max_concurrency": 1,
        "max_output_tokens": 1200, "uniform_controller": CONTROLLER,
        "uniform_language_prompts": LANGUAGE_PROMPTS,
        "native_controller": high.CONTROLLER,
        "platform": platform.platform(), "python": platform.python_version(),
        "dependencies": {name: importlib.metadata.version(name) for name in (
            "langchain", "langchain-core", "langchain-openai", "e2b-code-interpreter")},
        "source_sha256": {
            name: hashlib.sha256((HERE / name).read_bytes()).hexdigest()
            for name in (
                "run_v3.py", "evaluation_v3.py", "run_v2.py", "evaluation_v2.py",
                "backends_v2.py", "pykernel.py", "csclient.py", "run_native_mcp.py",
                "run_native_highbudget.py", "mcp_transport_v2.py", "PROTOCOL_V3.md",
            )
        },
        "inputs": {
            task: {
                "prompt": spec["prompt"], "artifact": spec["artifact"],
                "files": {name: {"bytes": len(text.encode()),
                                 "sha256": hashlib.sha256(text.encode()).hexdigest()}
                          for name, text in spec["files"].items()},
            } for task, spec in TASKS_B.items()
        },
    }
    uniform.write_json(args.output / "manifest.json", manifest)
    uniform.write_json(args.output / "task-inputs.json", TASKS_B)
    rows = []
    for index, (backend, task, repetition) in enumerate(order):
        row = (native.trial(task, repetition) if backend == "calcside_mcp"
               else uniform.run_trial(backend, task, repetition, manifest["model"]))
        rows.append(row)
        uniform.write_json(
            args.output / ("%03d-%s-%s-%d.json" % (index, backend, task, repetition)), row)
        uniform.write_json(args.output / "trials.json", rows)
        uniform.write_json(args.output / "summary.json", summarize(rows))
        print("%d/%d %s %s r%d artifact=%s agent_error=%s" % (
            index + 1, len(order), backend, task, repetition, row["passed"], row["agent_error"]),
            flush=True)
    manifest["finished_at"] = datetime.now(timezone.utc).isoformat()
    uniform.write_json(args.output / "manifest.json", manifest)


if __name__ == "__main__":
    main()
