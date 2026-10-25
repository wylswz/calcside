"""Render captured k6 summaries without re-running or re-deriving the workload."""

import json
import math
import pathlib
import sys


def metric(summary, name):
    value = summary.get("metrics", {}).get(name, {})
    return value.get("values", value)


def number(value):
    return "-" if value is None else f"{value:.2f}"


OPERATIONS = {
    "lifecycle": ("createInstance", "exec", "deleteInstance"),
    "exec": ("exec",),
    "read": ("me", "capabilities", "listInstances", "getInstance", "keepalive",
             "listFiles", "readFile", "inspect", "prompt", "listExecutions",
             "getExecution", "listAudit"),
}


def rate(values):
    # Legacy --summary-export calls Rate samples "value"; newer output uses "rate".
    return values.get("rate", values.get("value"))


def valid_summary(summary, run, p95_limit):
    iterations = metric(summary, "benchmark_iterations").get("count", 0)
    if iterations <= 0 or (run["stage"] == "integration" and iterations != 1):
        return False
    checks = metric(summary, "checks")
    failures = rate(metric(summary, "http_req_failed{phase:benchmark}"))
    if (rate(metric(summary, "benchmark_success")) != 1 or checks.get("fails") != 0
            or checks.get("passes", 0) <= 0 or failures is None or failures >= 0.01
            or metric(summary, "http_reqs{phase:benchmark}").get("count", 0) <= 0):
        return False
    for operation in OPERATIONS[run["scenario"]]:
        name = f"http_req_duration{{phase:benchmark,name:{operation}}}"
        alternate = f"http_req_duration{{name:{operation},phase:benchmark}}"
        values = metric(summary, name) or metric(summary, alternate)
        p95 = values.get("p(95)")
        if not isinstance(p95, (int, float)) or not math.isfinite(p95) or p95 >= p95_limit:
            return False
    return True


def render(directory):
    directory = pathlib.Path(directory)
    data = json.loads((directory / "runs.json").read_text())
    expected = {(stage, case) for stage in ("integration", "performance")
                for case in ("lifecycle", "exec", "read")}
    observed = {(run["stage"], run["scenario"]) for run in data["runs"]}
    passed = observed == expected and len(data["runs"]) == 6
    passed = passed and all(run["exit_code"] == 0 for run in data["runs"])
    lines = [
        f"# Release Validation: {data['backend']}",
        "",
        f"Gate: **{'PASS' if passed else 'FAIL / INCOMPLETE'}**",
        f"- Started (UTC): {data['started_at']}",
        f"- Revision: `{data['revision']}` (dirty worktree: {data['dirty']})",
        f"- Host: {data.get('host', 'not recorded')}",
        f"- Database: {data['database']}; {data['routing']}",
        f"- Workload: {data['workload']}",
        f"- Performance: {data['vus']} VUs, {data['duration']} per scenario",
        f"- Gate: all checks pass, iteration success 100%, measured HTTP failures <1%, "
        f"each operation p95 < {data['p95_limit_ms']} ms",
        "",
        "| Stage | Scenario | Exit | Completed iterations | Success | Measured requests | Request rate* |",
        "| --- | --- | ---: | ---: | ---: | ---: | ---: |",
    ]
    details = []
    for run in data["runs"]:
        path = directory / run["summary"]
        if not path.exists():
            passed = False
            lines.append(f"| {run['stage']} | {run['scenario']} | {run['exit_code']} | missing summary | - | - | - |")
            continue
        summary = json.loads(path.read_text())
        passed = valid_summary(summary, run, float(data["p95_limit_ms"])) and passed
        iterations = metric(summary, "benchmark_iterations").get("count")
        success = rate(metric(summary, "benchmark_success"))
        requests = metric(summary, "http_reqs{phase:benchmark}")
        lines.append(
            f"| {run['stage']} | {run['scenario']} | {run['exit_code']} | "
            f"{number(iterations)} | {number(success)} | "
            f"{number(requests.get('count'))} | {number(requests.get('rate'))} |"
        )
        if run["stage"] != "performance":
            continue
        details.extend([
            "", f"## {run['scenario']} Latency", "",
            "| Operation | Avg ms | p50 ms | p95 ms | p99 ms | Max ms |",
            "| --- | ---: | ---: | ---: | ---: | ---: |",
        ])
        for name in sorted(summary.get("metrics", {})):
            if not name.startswith("http_req_duration{") or "name:" not in name or "phase:benchmark" not in name:
                continue
            tags = dict(part.split(":", 1) for part in name.split("{", 1)[1].rstrip("}").split(","))
            values = metric(summary, name)
            cells = [number(values.get(key)) for key in ("avg", "med", "p(95)", "p(99)", "max")]
            details.append(f"| {tags['name']} | " + " | ".join(cells) + " |")
    # Missing summaries must never appear as a passing report.
    if not passed:
        lines[2] = "Gate: **FAIL / INCOMPLETE**"
    lines += details + [
        "",
        "## Interpretation",
        "",
        "- Integration uses one VU and one iteration per scenario; load uses the configured VUs/duration.",
        "- lifecycle measures create + execute + delete; exec uses warmed instances; read includes file listing/content and other read APIs.",
        "- Latencies and request counts filter phase:benchmark; setup/teardown are excluded.",
        "- *k6 summary request rates use total run wall time, including setup/teardown; they are not steady-state RPS.",
        "- This is a short release sanity gate, not a saturation/capacity or regression-baseline claim.",
        "- Local kind/Compose share Docker and the load-generator host. Run separately; host contention affects comparisons.",
        "- Raw summaries/logs and runs.json are the source artifacts. No credentials are included in metadata.",
        "",
    ]
    (directory / "report.md").write_text("\n".join(lines))
    return passed


if __name__ == "__main__":
    sys.exit(0 if render(sys.argv[1]) else 1)
