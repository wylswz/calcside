#!/usr/bin/env python3
"""Offline validation and frozen cross-configuration descriptive statistics."""

import hashlib
import json
import re
from pathlib import Path

from evaluation_v2 import distribution
from evaluation_v3 import SERIES, TASKS_B, summarize, validate
from run_v2 import write_json


HERE = Path(__file__).resolve().parent
DIRECTORY = HERE / "results/openai-sol-agentic"
BASELINE = HERE / "results/v3-agentic"
CONDITIONS = ("calcside", "calcside_mcp")


def read(path):
    return json.loads(path.read_text())


def normalized(value):
    return re.sub(r"\bins_[A-Za-z0-9]+\b", "<INSTANCE>", value)


def full_series_reads(row):
    """Exact native read_file observable, not a general exposure detector."""
    if row["backend"] != "calcside_mcp":
        return None
    count = 0
    for event in row["events"]:
        if event["kind"] != "tool" or event["name"] != "read_file":
            continue
        for block in event.get("observation", {}).get("content", []):
            if block.get("type") != "text":
                continue
            try:
                payload = json.loads(block["text"])
            except (ValueError, TypeError):
                continue
            if isinstance(payload, dict) and payload.get("Content") == SERIES:
                count += 1
    return count


def optional_counter(events, category, name):
    values = []
    for event in events:
        usages = event.get("usage_metadata")
        if not usages:
            return None
        for usage in usages:
            value = usage.get(category, {}).get(name)
            if type(value) is not int or value < 0:
                return None
            values.append(value)
    return sum(values) if values else None


def main():
    manifest = read(DIRECTORY / "manifest.json")
    assert manifest.get("finished_at")
    assert manifest["model"] == "gpt-6.1-sol"
    for folder, key in ((HERE, "source_sha256"), (BASELINE, "baseline_sha256")):
        for name, expected in manifest[key].items():
            assert hashlib.sha256((folder / name).read_bytes()).hexdigest() == expected, name
    assert read(DIRECTORY / "task-inputs.json") == TASKS_B
    rows = read(DIRECTORY / "trials.json")
    assert len(rows) == len(manifest["order"]) == 18
    baseline_rows = [r for r in read(BASELINE / "trials.json") if r["backend"] in CONDITIONS]
    assert len(baseline_rows) == 18
    baseline_prompts = {
        normalized(r["official_prompt"]) for r in baseline_rows if r["backend"] == "calcside_mcp"
    }
    baseline_schemas = {
        normalized(json.dumps(r["mcp_tools"], sort_keys=True))
        for r in baseline_rows if r["backend"] == "calcside_mcp"
    }
    for index, (row, order) in enumerate(zip(rows, manifest["order"])):
        assert [row["backend"], row["task"], row["repetition"]] == order
        assert read(DIRECTORY / ("%03d-%s-%s-%d.json" % (index, *order))) == row
        if "artifact" in row:
            assert validate(row["task"], row["artifact"]) == row["verification"]
            assert row["passed"] == row["verification"]["ok"]
        else:
            assert row["passed"] is False
        models = [e for e in row["events"] if e["kind"] == "model"]
        tools = sorted([e for e in row["events"] if e["kind"] == "tool"],
                       key=lambda e: e["start_s"])
        assert len(models) <= 64 and len(tools) <= 96
        for a, b in zip(tools, tools[1:]):
            assert a["start_s"] + a["duration_s"] <= b["start_s"] + 1e-6
        for event in models:
            if event["ok"]:
                assert event["usage_metadata"]
                for name in ("input_tokens", "output_tokens"):
                    assert sum(u[name] for u in event["usage_metadata"]) == event[name]
                assert all(r["response_metadata"].get("model_name") == manifest["model"]
                           for r in event["responses"])
                assert all(0 <= u.get("output_token_details", {}).get("reasoning", 0)
                           <= u["output_tokens"] for u in event["usage_metadata"])
        for event in tools:
            if "observation" in event:
                raw = event["observation"]
                assert event["ok"] == (not bool(raw.get(
                    "isError" if row["backend"] == "calcside_mcp" else "error")))
        if row["backend"] == "calcside_mcp" and not row["setup_error"]:
            assert row["official_prompt"] == row["mcp_initialize"]["instructions"]
            assert normalized(row["official_prompt"]) in baseline_prompts
            assert normalized(json.dumps(row["mcp_tools"], sort_keys=True)) in baseline_schemas
        if row["loop_s"] is not None:
            for kind in ("model", "tool"):
                assert abs(row[kind + "_s"] - sum(
                    e["duration_s"] for e in row["events"] if e["kind"] == kind)) < 1e-9
            assert row["residual_s"] >= -1e-6
    summary = read(DIRECTORY / "summary.json")
    assert summary == summarize(rows)
    baseline_summary = read(BASELINE / "report-data.json")["summary"]
    result = {
        "groups": [], "comparisons": {},
        "usage_note": "Input includes cached context; output already includes reasoning.",
        "native_file_read_note": "Exact Content == SERIES in native read_file results only.",
    }
    for label, dataset, stats in (
        ("deepseek-flash", baseline_rows, baseline_summary),
        ("gpt-6.1-sol", rows, summary),
    ):
        for backend in CONDITIONS:
            trials = [r for r in dataset if r["backend"] == backend]
            assert len(trials) == 9
            models = [e for r in trials for e in r["events"] if e["kind"] == "model"]
            known = [e for e in models if e.get("input_tokens") is not None]
            first = [next((e.get("input_tokens") for e in r["events"] if e["kind"] == "model"), None)
                     for r in trials]
            counts = [full_series_reads(r) for r in trials if r["task"] == "series"]
            st = stats[backend]
            group = {
                "model": label, "backend": backend, "trials": len(trials),
                "artifact_successes": st["task_completion"]["successes"],
                "normal_successes": sum(not r["setup_error"] and not r["agent_error"] for r in trials),
                "tool_successes": st["tool_success"]["successes"],
                "tool_attempts": st["tool_success"]["attempts"],
                "model_calls": len(models), "usage_events": len(known),
                "input_tokens": st["input_tokens_observed"],
                "output_tokens": st["output_tokens_observed"],
                "tokens_total": st["input_tokens_observed"] + st["output_tokens_observed"],
                "cache_read_tokens": optional_counter(models, "input_token_details", "cache_read"),
                "reasoning_tokens": optional_counter(models, "output_token_details", "reasoning"),
                "first_input_tokens": distribution([v for v in first if v is not None]),
                "mean_input_tokens_per_request_with_usage": (
                    st["input_tokens_observed"] / len(known) if known else None),
                "loop_latency_s": st["latency_s"]["loop_s"],
                "by_task": st["by_task"],
                "task_usage": {
                    task: {
                        "model_calls": sum(
                            e["kind"] == "model" for r in trials if r["task"] == task
                            for e in r["events"]),
                        "input_tokens": sum(
                            e.get("input_tokens") or 0 for r in trials if r["task"] == task
                            for e in r["events"] if e["kind"] == "model"),
                        "output_tokens": sum(
                            e.get("output_tokens") or 0 for r in trials if r["task"] == task
                            for e in r["events"] if e["kind"] == "model"),
                    } for task in TASKS_B
                },
                "native_series_full_read_calls": sum(counts) if backend == "calcside_mcp" else None,
                "native_series_trials_with_full_read": (
                    sum(c > 0 for c in counts) if backend == "calcside_mcp" else None),
                "series_trials": len(counts), "tool_error_kinds": st["tool_error_kinds"],
                "phase_errors": {
                    phase: sum(r[phase] is not None for r in trials)
                    for phase in ("setup_error", "agent_error", "cleanup_error")
                },
            }
            if group["cache_read_tokens"] is not None:
                assert group["cache_read_tokens"] <= group["input_tokens"]
            result["groups"].append(group)
            print(json.dumps(group))
        a, b = result["groups"][-2:]
        result["comparisons"][label] = {
            "native_over_uniform_tokens": b["tokens_total"] / a["tokens_total"],
            "native_over_uniform_input": b["input_tokens"] / a["input_tokens"],
            "native_over_uniform_model_calls": b["model_calls"] / a["model_calls"],
        }
    destination = DIRECTORY / "report-data.json"
    if destination.exists():
        assert read(destination) == result
    else:
        write_json(destination, result)
    print("Verified 18 Sol trials and frozen baseline; prompt/schema parity holds after instance-id normalization.")
    print("Ratios:", result["comparisons"])


if __name__ == "__main__":
    main()
