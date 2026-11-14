#!/usr/bin/env python3
"""Check native MCP records and freeze its additional report statistics."""

import hashlib
import json
from pathlib import Path

from evaluation_v2 import distribution, rate, summarize, validate
from run_v2 import write_json


def main():
    here = Path(__file__).resolve().parent
    directory = here / "results/v2-native-mcp"
    manifest = json.loads((directory / "manifest.json").read_text())
    assert manifest.get("finished_at"), "native run incomplete"
    rows = json.loads((directory / "trials.json").read_text())
    assert len(rows) == len(manifest["order"]) == 12
    for name, expected in manifest["source_sha256"].items():
        assert hashlib.sha256((here / name).read_bytes()).hexdigest() == expected, name
    for index, (row, order) in enumerate(zip(rows, manifest["order"])):
        assert [row["task"], row["repetition"]] == order
        assert json.loads((directory / ("%03d-%s-%d.json" % (index, *order))).read_text()) == row
        if row["setup_error"] is None:
            assert row["official_prompt"] == row["mcp_initialize"]["instructions"]
            assert hashlib.sha256(row["official_prompt"].encode()).hexdigest() == row["official_prompt_sha256"]
            assert {t["name"] for t in row["mcp_tools"]} == {
                "exec", "notebook", "add_section", "read_section", "run_sections",
                "list_files", "read_file",
            }
        if "artifact" in row:
            assert validate(row["task"], row["artifact"]) == row["verification"]
            assert row["passed"] == row["verification"]["ok"]
        else:
            assert row["passed"] is False
        tools = sorted([e for e in row["events"] if e["kind"] == "tool"], key=lambda e: e["start_s"])
        for a, b in zip(tools, tools[1:]):
            assert a["start_s"] + a["duration_s"] <= b["start_s"] + 1e-6, "tools overlapped"
        for e in tools:
            if "observation" in e:
                assert e["ok"] == (not bool(e["observation"].get("isError")))
        if row["loop_s"] is not None:
            assert row["residual_s"] >= -1e-6
            for kind in ("model", "tool"):
                assert abs(row[kind + "_s"] - sum(
                    e["duration_s"] for e in row["events"] if e["kind"] == kind)) < 1e-9
            assert abs(row["loop_s"] - row["model_s"] - row["tool_s"] - row["residual_s"]) < 1e-9
    summary = json.loads((directory / "summary.json").read_text())
    assert summarize(rows) == summary
    tools = [e for r in rows for e in r["events"] if e["kind"] == "tool"]
    totals = [
        sum(r[k] for k in ("setup_s", "loop_s", "verification_s", "cleanup_s"))
        for r in rows if all(r.get(k) is not None for k in (
            "setup_s", "loop_s", "verification_s", "cleanup_s"))
    ]
    output = {
        "summary": summary,
        "total_s": distribution(totals),
        "by_tool": {
            name: rate(sum(e["ok"] for e in tools if e["name"] == name),
                       sum(e["name"] == name for e in tools))
            for name in sorted({e["name"] for e in tools})
        },
        "agent_errors": sum(r["agent_error"] is not None for r in rows),
        "by_task_errors": {
            task: [r["agent_error"] or r["setup_error"] or r.get("verification", {}).get("reason")
                   for r in rows if r["task"] == task and not r["passed"]]
            for task in sorted({r["task"] for r in rows})
        },
    }
    destination = here / "results/native-report-data-v2.json"
    assert not destination.exists(), "do not overwrite a frozen report snapshot"
    write_json(destination, output)
    print("Verified native prompt/schema provenance, 12 ordered artifacts, sequential spans, summary.")
    print(json.dumps(output, indent=2))
    for index, row in enumerate(rows):
        errors = [e for e in row["events"] if not e["ok"]]
        if errors or not row["passed"] or row["agent_error"] or row["cleanup_error"]:
            print("CASE", index, row["task"], "passed=", row["passed"],
                  "agent_error=", row["agent_error"], "cleanup_error=", row["cleanup_error"])
            for event in errors:
                print(json.dumps(event, ensure_ascii=True))


if __name__ == "__main__":
    main()
