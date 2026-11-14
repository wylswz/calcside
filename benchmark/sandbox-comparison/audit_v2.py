#!/usr/bin/env python3
"""Offline integrity/measurement checks; never starts a sandbox or model."""

import hashlib
import json
from pathlib import Path

from evaluation_v2 import summarize, validate


HERE = Path(__file__).resolve().parent


def main():
    directory = HERE / "results" / "v2-agent"
    manifest = json.loads((directory / "manifest.json").read_text())
    trials = json.loads((directory / "trials.json").read_text())
    summary = json.loads((directory / "summary.json").read_text())
    assert manifest.get("finished_at"), "run incomplete"
    assert len(trials) == len(manifest["order"]) == 60, "unexpected experimental units"
    for name, expected in manifest["source_sha256"].items():
        assert hashlib.sha256((HERE / name).read_bytes()).hexdigest() == expected, name
    for index, (row, order) in enumerate(zip(trials, manifest["order"])):
        assert [row["backend"], row["task"], row["repetition"]] == order
        stored = directory / ("%03d-%s-%s-%d.json" % (index, *order))
        assert json.loads(stored.read_text()) == row, str(stored)
        if "artifact" in row:
            assert validate(row["task"], row["artifact"]) == row["verification"]
            assert row["passed"] == row["verification"]["ok"]
        else:
            assert row["passed"] is False
        events = row["events"]
        tools = [e for e in events if e["kind"] == "tool"]
        models = [e for e in events if e["kind"] == "model"]
        assert len(models) <= 8 and len(tools) <= 12
        assert len({e["tool_call_id"] for e in tools}) == len(tools)
        for event in events:
            assert event["duration_s"] >= 0
        if row["loop_s"] is not None:
            model_s = sum(e["duration_s"] for e in models)
            tool_s = sum(e["duration_s"] for e in tools)
            assert abs(model_s - row["model_s"]) < 1e-9
            assert abs(tool_s - row["tool_s"]) < 1e-9
            assert row["residual_s"] >= -1e-6
            assert abs(row["loop_s"] - model_s - tool_s - row["residual_s"]) < 1e-9
    assert summarize(trials) == summary, "summary does not match raw records"
    print("Verified: 60 ordered trials, source hashes, per-trial files, artifacts, spans, summary.")
    print("backend | task | tool | first | recovery | model | input/output | setup/loop p50 s")
    for name, data in summary.items():
        ratios = [
            "%s/%s" % (data[k]["successes"], data[k]["attempts"])
            for k in ("task_completion", "tool_success", "first_tool_success",
                      "recovery", "model_success")
        ]
        lat = data["latency_s"]
        print("%s | %s | %s/%s | %.3f/%.3f" % (
            name, " | ".join(ratios), data["input_tokens_observed"],
            data["output_tokens_observed"], lat["setup_s"]["p50"], lat["loop_s"]["p50"]))
    print("Errors/recovery trials (if any):")
    for index, row in enumerate(trials):
        errors = [e for e in row["events"] if not e["ok"]]
        if errors or row["agent_error"] or row["setup_error"] or row["cleanup_error"]:
            print(index, row["backend"], row["task"], "passed=", row["passed"],
                  "agent_error=", row["agent_error"], "setup_error=", row["setup_error"],
                  "cleanup_error=", row["cleanup_error"])
            for event in errors:
                print(json.dumps(event, ensure_ascii=True))


if __name__ == "__main__":
    main()
