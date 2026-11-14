#!/usr/bin/env python3
"""Freeze additional lifecycle statistics for the report; offline only."""

import hashlib
import json
from pathlib import Path

from evaluation_v2 import distribution
from run_v2 import write_json


def main():
    here = Path(__file__).resolve().parent
    manifest_path = here / "results/v2-agent/manifest.json"
    manifest = json.loads(manifest_path.read_text())
    assert manifest.get("finished_at"), "uniform experiment is incomplete"
    trials_path = here / "results/v2-agent/trials.json"
    trials = json.loads(trials_path.read_text())
    assert len(trials) == 60
    summary_path = here / "results/v2-agent/summary.json"
    summary = json.loads(summary_path.read_text())
    micro_path = here / "results/v2-micro/micro.json"
    micro = json.loads(micro_path.read_text())
    output = {
        "sources_sha256": {
            str(p.relative_to(here)): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in (manifest_path, trials_path, summary_path, micro_path)
        },
        "uniform": summary,
        "micro": micro,
        "lifecycle": {},
    }
    for backend in summary:
        rows = [r for r in trials if r["backend"] == backend]
        completed = [r for r in rows if all(
            r.get(k) is not None for k in ("setup_s", "loop_s", "verification_s", "cleanup_s"))]
        output["lifecycle"][backend] = {
            "total_s": distribution([
                sum(r[k] for k in ("setup_s", "loop_s", "verification_s", "cleanup_s"))
                for r in completed
            ]),
            "missing_phase_trials": len(rows) - len(completed),
            "local_artifacts": {
                "passed": sum(r["passed"] for r in rows if r["task"] != "fetch"),
                "attempts": sum(r["task"] != "fetch" for r in rows),
            },
            "agent_errors": sum(r["agent_error"] is not None for r in rows),
            "setup_errors": sum(r["setup_error"] is not None for r in rows),
        }
    destination = here / "results/report-data-v2.json"
    assert not destination.exists(), "do not overwrite the frozen report snapshot"
    write_json(destination, output)
    for backend, data in summary.items():
        lat = data["latency_s"]
        total = output["lifecycle"][backend]["total_s"]
        print(backend, json.dumps({
            "wilson": data["task_completion"]["wilson95"],
            "tool_rate": data["tool_success"]["rate"],
            "loop_p50": lat["loop_s"]["p50"], "loop_p95": lat["loop_s"]["p95"],
            "model_mean": lat["model_s"]["mean"], "tool_mean": lat["tool_s"]["mean"],
            "residual_mean": lat["residual_s"]["mean"],
            "total_p50": total["p50"], "total_p95": total["p95"],
            "errors": data["tool_error_kinds"],
            "missing_usage": data["model_events_missing_usage"],
        }))
    for row in micro:
        assert len(row["samples"]) == 30
        assert row["success"]["successes"] == sum(s["ok"] for s in row["samples"])
        assert row["all_attempt_latency_s"] == distribution([s["duration_s"] for s in row["samples"]])
        print("warm", row["backend"], row["success"], row["all_attempt_latency_s"])


if __name__ == "__main__":
    main()
