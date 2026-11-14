#!/usr/bin/env python3
"""Derive scalability measurements from captured k6 summaries, never live APIs."""

import argparse
import collections
import datetime
import json
import pathlib
import re
import statistics


def values(summary, name):
    return summary["metrics"].get(name, {}).get("values", {})


def count(summary, name):
    return values(summary, name).get("count", 0)


def parse_case(case, summary):
    elapsed = summary["state"]["testRunDurationMs"] / 1000
    if elapsed <= 0:
        raise ValueError(f"Invalid measured duration: {case['name']}")
    attempts = count(summary, "scalability_attempts")
    completed = count(summary, "scalability_completed")
    transport = count(summary, "scalability_transport_errors")
    http = count(summary, "scalability_http_errors")
    runtime = count(summary, "scalability_runtime_errors")
    if not 0 <= completed <= attempts or attempts == 0:
        raise ValueError(f"Invalid invocation counts: {case['name']}")
    residual = attempts - completed - transport - http - runtime
    if residual < 0:
        raise ValueError(f"Overlapping error categories: {case['name']}")
    api = [count(summary, f"scalability_api_{i}") for i in range(2)]
    api_unknown = count(summary, "scalability_api_unknown")
    if sum(api) + api_unknown != attempts:
        raise ValueError(f"API attribution missing requests: {case['name']}")
    workers = [count(summary, f"scalability_worker_{i}") for i in range(3)]
    worker_unknown = count(summary, "scalability_worker_unknown")
    # CPU fixtures contain no worker-identity request. I/O ones must account
    # for every successfully validated result; invalid results can also return
    # a worker, so attributed counts may exceed completed on a faulty fixture.
    if case["workload"] == "io" and sum(workers) + worker_unknown < completed:
        raise ValueError(f"Worker attribution missing good results: {case['name']}")
    return {
        **case, "measured_s": elapsed, "attempts": attempts, "completed": completed,
        "success_rate": completed / attempts,
        "completed_rps": completed / elapsed,
        "attempted_rps": attempts / elapsed,
        "latency_all_ms": values(summary, "scalability_latency_ms"),
        "latency_success_ms": values(summary, "scalability_success_latency_ms"),
        "errors": {"transport": transport, "http": http, "runtime": runtime,
                   "invalid_output": residual},
        "api_counts": api, "api_unknown": api_unknown,
        "worker_counts": workers if case["workload"] == "io" else None,
        "worker_unknown": worker_unknown if case["workload"] == "io" else None,
    }


def aggregate(rows):
    groups = collections.defaultdict(list)
    for row in rows:
        m = re.fullmatch(r"steady-w(\d+)-(\w+)-v(\d+)-r(\d+)", row["name"])
        if m:
            groups[(int(m[1]), m[2], int(m[3]))].append(row)
    result = []
    for (workers, workload, vus), runs in sorted(groups.items()):
        result.append({
            "workers": workers, "workload": workload, "vus": vus,
            "runs": [r["name"] for r in runs], "replicates": len(runs),
            "attempts": sum(r["attempts"] for r in runs),
            "completed": sum(r["completed"] for r in runs),
            "errors": sum(r["attempts"] - r["completed"] for r in runs),
            "rps_median": statistics.median(r["completed_rps"] for r in runs),
            "rps_min": min(r["completed_rps"] for r in runs),
            "rps_max": max(r["completed_rps"] for r in runs),
            # These are a RANGE of per-run percentiles, not a pooled percentile.
            "p95_min_ms": min(r["latency_success_ms"]["p(95)"] for r in runs),
            "p95_max_ms": max(r["latency_success_ms"]["p(95)"] for r in runs),
            "api_counts": [sum(r["api_counts"][i] for r in runs) for i in range(2)],
            "worker_counts": None if workload == "cpu" else [
                sum(r["worker_counts"][i] for r in runs) for i in range(3)],
        })
    for group in result:
        base = next(g for g in result if g["workers"] == 1 and
                    (g["workload"], g["vus"]) == (group["workload"], group["vus"]))
        group["speedup_vs_1"] = group["rps_median"] / base["rps_median"]
        group["efficiency_vs_1"] = group["speedup_vs_1"] / group["workers"]
    return result


def traces(directory, cases):
    result = {}
    for case in cases:
        if not case["name"].startswith(("expand-", "pause-")):
            continue
        rows = []
        for line in (directory / f"{case['name']}.log").read_text().splitlines():
            marker = "SCALABILITY_EVENT "
            if marker not in line:
                continue
            # k6 text logs quote/escape console.log payloads. JSONDecodeError is
            # fatal rather than silently dropping malformed telemetry.
            match = re.search(r'msg=("(?:\\.|[^"\\])*")', line)
            if match:
                message = json.loads(match[1])
                row = json.loads(message.split(marker, 1)[1])
            else:
                row = json.loads(line.split(marker, 1)[1])
            rows.append(row)
        if not rows:
            raise ValueError(f"Missing request trace: {case['name']}")
        bins = collections.defaultdict(list)
        first = min(r["started_ms"] for r in rows)
        for row in rows:
            bins[int((row["started_ms"] - first) // 5000) * 5].append(row)
        result[case["name"]] = {
            "requests": len(rows), "started_ms": first,
            "bins_5s": [{
                "start_s": start, "attempts": len(rs),
                "completed": sum(r["ok"] for r in rs),
                "status_counts": dict(collections.Counter(str(r["status"]) for r in rs)),
                "worker_counts": dict(collections.Counter(r["worker"] for r in rs if r["ok"])),
            } for start, rs in sorted(bins.items())],
        }
        if len(rows) != case["attempts"]:
            raise ValueError(f"Trace/summary mismatch: {case['name']}")
    return result


def resources(directory):
    previous = {}
    series = collections.defaultdict(list)
    memory = collections.defaultdict(list)
    errors = 0
    samples = 0
    for line in (directory / "samples.jsonl").read_text().splitlines():
        sample = json.loads(line)
        samples += 1
        errors += int("error" in sample)
        for pod in sample.get("pods", []):
            name = pod["podRef"]["name"]
            for container in pod.get("containers") or []:
                key = name + "/" + container["name"]
                cpu = container.get("cpu", {})
                nano = cpu.get("usageCoreNanoSeconds")
                stamp = cpu.get("time")
                if nano is not None and stamp:
                    t = datetime.datetime.fromisoformat(stamp.replace("Z", "+00:00")).timestamp()
                    if key in previous:
                        old_t, old_nano = previous[key]
                        if t > old_t and nano >= old_nano:
                            series[key].append((nano - old_nano) / 1e9 / (t - old_t))
                    previous[key] = (t, nano)
                used = container.get("memory", {}).get("workingSetBytes")
                if used is not None:
                    memory[key].append(used)
    return {
        "samples": samples, "sample_errors": errors,
        "containers": {
            key: {
                "interval_cpu_cores_median": statistics.median(series[key]) if series[key] else None,
                "interval_cpu_cores_max": max(series[key]) if series[key] else None,
                "working_set_bytes_max": max(memory[key]) if memory[key] else None,
            } for key in sorted(series.keys() | memory.keys())
        },
    }


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("directory", type=pathlib.Path)
    p.add_argument("--output", type=pathlib.Path, required=True)
    args = p.parse_args()
    cases = json.loads((args.directory / "cases.json").read_text())
    rows = [parse_case(c, json.loads((args.directory / c["summary"]).read_text())) for c in cases]
    result = {
        "schema": 1, "cases": rows, "steady": aggregate(rows),
        "traces": traces(args.directory, rows), "resources": resources(args.directory),
    }
    args.output.write_text(json.dumps(result, indent=2) + "\n")
    for g in result["steady"]:
        print(f"w{g['workers']} {g['workload']:3s} v{g['vus']:2d} "
              f"{g['rps_median']:.2f}/s [{g['rps_min']:.2f},{g['rps_max']:.2f}] "
              f"p95 [{g['p95_min_ms']:.1f},{g['p95_max_ms']:.1f}]ms "
              f"errors={g['errors']} speedup={g['speedup_vs_1']:.2f}")


if __name__ == "__main__":
    main()
