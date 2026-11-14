#!/usr/bin/env python3
"""Supplementary provider/model configuration; original harness stays frozen."""

import argparse
import hashlib
import importlib.metadata
import json
import os
import platform
from datetime import datetime, timezone
from pathlib import Path

from langchain_core.messages import AIMessage

import run_native_highbudget as high
import run_native_mcp as native
import run_v2 as uniform
import run_v3 as extension
from evaluation_v3 import TASKS_B, summarize
from openai_sol_model import BASE_URL, MAX_OUTPUT_TOKENS, MODEL, make_model


HERE = Path(__file__).resolve().parent
BASELINE = HERE / "results/v3-agentic"
CONDITIONS = ("calcside", "calcside_mcp")


class UsageDetailMixin:
    """Keep provider subcounters without changing the original token totals."""

    def wrap_model_call(self, request, handler):
        index = self.model_calls + 1
        usages = []

        def capture(req):
            response = handler(req)
            for message in response.result:
                if isinstance(message, AIMessage) and message.usage_metadata:
                    usages.append(json.loads(json.dumps(message.usage_metadata)))
            return response

        try:
            return super().wrap_model_call(request, capture)
        finally:
            for event in reversed(self.events):
                if event["kind"] == "model" and event["index"] == index:
                    event["usage_metadata"] = usages
                    break


class DetailedRecorder(UsageDetailMixin, uniform.Recorder):
    pass


class DetailedNativeRecorder(UsageDetailMixin, high.HighBudgetRecorder):
    pass


def configure():
    extension.configure()
    uniform.ChatOpenAI = make_model
    native.ChatOpenAI = make_model
    uniform.Recorder = DetailedRecorder
    native.NativeRecorder = DetailedNativeRecorder


def main():
    os.environ["LANGSMITH_TRACING"] = "false"
    os.environ["LANGCHAIN_TRACING_V2"] = "false"
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    assert json.loads((BASELINE / "task-inputs.json").read_text()) == TASKS_B
    baseline_manifest = json.loads((BASELINE / "manifest.json").read_text())
    order = [row for row in baseline_manifest["order"] if row[0] in CONDITIONS]
    assert len(order) == 18
    args.output.mkdir(parents=True, exist_ok=False)
    configure()
    sources = [
        "run_openai_sol.py", "openai_sol_model.py", "PROTOCOL_OPENAI_SOL.md",
        *baseline_manifest["source_sha256"],
    ]
    manifest = {
        "track": "task-B-openai-sol-two-calcside-interfaces",
        "started_at": datetime.now(timezone.utc).isoformat(),
        "order": order, "model": MODEL, "base_url": BASE_URL,
        "provider_api": "responses", "reasoning_effort": "medium",
        "temperature": "omitted (provider default)",
        "max_output_tokens_including_reasoning": MAX_OUTPUT_TOKENS,
        "model_timeout_s": 180, "model_retries": 0,
        "max_model_calls": 64, "max_tool_calls": 96,
        "graph_recursion_limit": 512, "max_concurrency": 1,
        "uniform_controller": extension.CONTROLLER,
        "uniform_language_prompts": baseline_manifest["uniform_language_prompts"],
        "native_controller": high.CONTROLLER,
        "inputs": baseline_manifest["inputs"],
        "platform": platform.platform(), "python": platform.python_version(),
        "dependencies": {name: importlib.metadata.version(name) for name in (
            "langchain", "langchain-core", "langchain-openai", "openai")},
        "source_sha256": {
            name: hashlib.sha256((HERE / name).read_bytes()).hexdigest() for name in sources
        },
        "baseline_sha256": {
            name: hashlib.sha256((BASELINE / name).read_bytes()).hexdigest()
            for name in ("manifest.json", "trials.json", "report-data.json", "task-inputs.json")
        },
        "interpretation": (
            "Independent provider/model configuration replication, not a "
            "matched-temperature/output-budget causal model comparison."
        ),
    }
    uniform.write_json(args.output / "manifest.json", manifest)
    uniform.write_json(args.output / "task-inputs.json", TASKS_B)
    rows = []
    for index, (backend, task, repetition) in enumerate(order):
        row = (native.trial(task, repetition) if backend == "calcside_mcp"
               else uniform.run_trial(backend, task, repetition, MODEL))
        rows.append(row)
        uniform.write_json(
            args.output / ("%03d-%s-%s-%d.json" % (index, backend, task, repetition)), row)
        uniform.write_json(args.output / "trials.json", rows)
        uniform.write_json(args.output / "summary.json", summarize(rows))
        print("%d/18 %s %s r%d artifact=%s agent_error=%s" % (
            index + 1, backend, task, repetition, row["passed"], row["agent_error"]), flush=True)
    manifest["finished_at"] = datetime.now(timezone.utc).isoformat()
    uniform.write_json(args.output / "manifest.json", manifest)


if __name__ == "__main__":
    main()
