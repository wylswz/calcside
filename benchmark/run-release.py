#!/usr/bin/env python3
"""Run identical API contracts and load gates against kind or Compose."""

import argparse
import datetime
import json
import os
import pathlib
import platform
import subprocess
import sys

from report import render


ROOT = pathlib.Path(__file__).resolve().parent.parent
SCENARIOS = ("lifecycle", "exec", "read")


def run_case(stage, scenario, out, environment):
    env = dict(environment, BENCH_SCENARIO=scenario)
    if stage == "integration":
        env.update(BENCH_VUS="1", BENCH_ITERATIONS="1")
    summary = f"{stage}-{scenario}.json"
    command = [
        "k6", "run", "--no-usage-report",
        f"--summary-export={out / summary}", str(ROOT / "benchmark/api.js"),
    ]
    # Stream k6 output while retaining the exact same log for artifacts.
    with (out / f"{stage}-{scenario}.log").open("w") as log:
        try:
            with subprocess.Popen(
                command, env=env, stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT, text=True,
            ) as process:
                for line in process.stdout:
                    log.write(line)
                    print(line, end="", flush=True)
                code = process.wait()
        except OSError as error:
            log.write(f"Could not launch k6: {error}\n")
            code = 127
    return {"stage": stage, "scenario": scenario, "exit_code": code, "summary": summary}


def run(args):
    env = os.environ.copy()
    if args.state:
        connection = json.loads((args.state / "connection.json").read_text())
        env.update(
            CALCSIDE_BASE_URL=connection["base_url"],
            CALCSIDE_API_KEY=connection["api_key"],
        )
    if not env.get("CALCSIDE_BASE_URL") or not env.get("CALCSIDE_API_KEY"):
        raise ValueError("provide --state or CALCSIDE_BASE_URL and CALCSIDE_API_KEY")
    # Never inherit fixed iterations into the timed performance gate.
    env.pop("BENCH_ITERATIONS", None)
    env.setdefault("BENCH_P95_MS", "2000")
    env.setdefault("BENCH_VUS", "5")
    env.setdefault("BENCH_DURATION", "30s")
    for name in ("BENCH_VUS", "BENCH_P95_MS"):
        if not env[name].isdigit() or int(env[name]) < 1:
            raise ValueError(f"{name} must be a positive integer")
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    metadata = {
        "backend": args.backend,
        "started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "revision": subprocess.check_output(["git", "-C", str(ROOT), "rev-parse", "HEAD"], text=True).strip(),
        "dirty": bool(subprocess.check_output(["git", "-C", str(ROOT), "status", "--porcelain"], text=True).strip()),
        "database": "PostgreSQL 17",
        "routing": "API -> remote worker, process isolation",
        "host": platform.platform(),
        "workload": "closed-loop, no think time; sum squares 0..999; VFS write/read",
        "vus": env["BENCH_VUS"],
        "duration": env["BENCH_DURATION"],
        "p95_limit_ms": env["BENCH_P95_MS"],
        "runs": [],
    }
    manifest = out / "runs.json"
    manifest.write_text(json.dumps(metadata, indent=2) + "\n")
    for stage in ("integration", "performance"):
        for scenario in SCENARIOS:
            metadata["runs"].append(run_case(stage, scenario, out, env))
            manifest.write_text(json.dumps(metadata, indent=2) + "\n")
        if any(case["exit_code"] != 0 for case in metadata["runs"]):
            break
    passed = render(out)
    print(f"Report: {out / 'report.md'}")
    return 0 if passed else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("backend", choices=("kind", "compose"))
    parser.add_argument("output", type=pathlib.Path, help="new output directory")
    parser.add_argument("--state", type=pathlib.Path, help="deployment state with connection.json")
    args = parser.parse_args()
    try:
        return run(args)
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        print(f"release validation: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
