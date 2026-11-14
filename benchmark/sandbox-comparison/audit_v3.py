#!/usr/bin/env python3
"""Offline provenance/artifact checks and a frozen report view for task set B."""

import hashlib
import json
from pathlib import Path

from evaluation_v3 import TASKS_B, summarize, validate
from run_v2 import write_json


def main():
    here = Path(__file__).resolve().parent
    directory = here / "results/v3-agentic"
    manifest = json.loads((directory / "manifest.json").read_text())
    assert manifest.get("finished_at"), "experiment not complete"
    rows = json.loads((directory / "trials.json").read_text())
    assert len(rows) == len(manifest["order"]) == 54
    for name, expected in manifest["source_sha256"].items():
        assert hashlib.sha256((here / name).read_bytes()).hexdigest() == expected, name
    assert json.loads((directory / "task-inputs.json").read_text()) == TASKS_B
    for index, (row, order) in enumerate(zip(rows, manifest["order"])):
        assert [row["backend"], row["task"], row["repetition"]] == order
        assert json.loads((directory / ("%03d-%s-%s-%d.json" % (index, *order))).read_text()) == row
        if "artifact" in row:
            assert validate(row["task"], row["artifact"]) == row["verification"]
            assert row["passed"] == row["verification"]["ok"]
        else:
            assert row["passed"] is False
        tools = sorted([e for e in row["events"] if e["kind"] == "tool"],
                       key=lambda e: e["start_s"])
        models = [e for e in row["events"] if e["kind"] == "model"]
        assert len(tools) <= 96 and len(models) <= 64
        for a, b in zip(tools, tools[1:]):
            assert a["start_s"] + a["duration_s"] <= b["start_s"] + 1e-6
        for e in tools:
            if "observation" in e:
                raw = e["observation"]
                expected = (not bool(raw.get("isError")) if row["backend"] == "calcside_mcp"
                            else not bool(raw.get("error")))
                assert e["ok"] == expected
        if row["backend"] == "calcside_mcp" and row["setup_error"] is None:
            assert row["official_prompt"] == row["mcp_initialize"]["instructions"]
            assert hashlib.sha256(row["official_prompt"].encode()).hexdigest() == row["official_prompt_sha256"]
            assert {t["name"] for t in row["mcp_tools"]} == {
                "exec", "notebook", "add_section", "read_section", "run_sections",
                "list_files", "read_file",
            }
        if row["loop_s"] is not None:
            for kind in ("model", "tool"):
                assert abs(row[kind + "_s"] - sum(
                    e["duration_s"] for e in row["events"] if e["kind"] == kind)) < 1e-9
            assert row["residual_s"] >= -1e-6
            assert abs(row["loop_s"] - sum(row[k] for k in (
                "model_s", "tool_s", "residual_s"))) < 1e-9
    summary = json.loads((directory / "summary.json").read_text())
    assert summary == summarize(rows)
    output = {
        "summary": summary, "normal_termination": {}, "tokens_total": {},
        "max_calls": {}, "phase_errors": {},
    }
    for name, stats in summary.items():
        trials = [r for r in rows if r["backend"] == name]
        assert len(trials) == 9
        output["normal_termination"][name] = {
            "successes": sum(r["agent_error"] is None and r["setup_error"] is None for r in trials),
            "attempts": len(trials),
        }
        normal = output["normal_termination"][name]
        normal["rate"] = normal["successes"] / normal["attempts"]
        output["tokens_total"][name] = stats["input_tokens_observed"] + stats["output_tokens_observed"]
        output["max_calls"][name] = {
            kind: max(sum(e["kind"] == kind for e in r["events"]) for r in trials)
            for kind in ("model", "tool")
        }
        output["phase_errors"][name] = {
            kind: sum(r[kind] is not None for r in trials)
            for kind in ("setup_error", "agent_error", "cleanup_error")
        }
        print(name, json.dumps({
            "artifact": stats["task_completion"],
            "by_task": stats["by_task"], "tool": stats["tool_success"],
            "normal": output["normal_termination"][name],
            "model": stats["model_success"], "tokens": output["tokens_total"][name],
            "loop_s": stats["latency_s"]["loop_s"], "errors": stats["tool_error_kinds"],
            "max_calls": output["max_calls"][name], "phase_errors": output["phase_errors"][name],
        }))
    destination = directory / "report-data.json"
    if destination.exists():
        assert json.loads(destination.read_text()) == output
    else:
        write_json(destination, output)
    print("Verified 54 ordered trials, source hashes, input fixtures, prompts, artifacts, spans and summary.")
    for index, row in enumerate(rows):
        if not row["passed"] or row["agent_error"] or row["setup_error"]:
            print("CASE", index, row["backend"], row["task"], row["verification"],
                  row["agent_error"], row["setup_error"])


if __name__ == "__main__":
    main()
