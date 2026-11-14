#!/usr/bin/env python3
"""Single-session warm round trips; not an isolation implementation benchmark."""

import argparse
import json
import time
from pathlib import Path

from backends_v2 import BACKENDS
from evaluation_v2 import distribution, rate
from run_v2 import write_json


def measure(name):
    adapter = BACKENDS[name]()
    result = {"backend": name, "samples": [], "setup_error": None, "cleanup_error": None}
    started = time.perf_counter()
    try:
        adapter.setup({})
        result["setup_s"] = time.perf_counter() - started
        result["adapter"] = adapter.metadata()
        result["warmup"] = adapter.execute('print("ok")')
        for _ in range(30):
            started = time.perf_counter()
            try:
                response = adapter.execute('print("ok")')
                ok = not response.get("error") and response["output"].strip() == "ok"
            except Exception as exc:
                response = {"error": "%s: %s" % (type(exc).__name__, str(exc))}
                ok = False
            result["samples"].append({
                "duration_s": time.perf_counter() - started, "ok": ok,
                "response": response,
            })
    except Exception as exc:
        result["setup_error"] = "%s: %s" % (type(exc).__name__, str(exc))
    finally:
        try:
            adapter.close()
        except Exception as exc:
            result["cleanup_error"] = "%s: %s" % (type(exc).__name__, str(exc))
    samples = result["samples"]
    result["success"] = rate(sum(x["ok"] for x in samples), len(samples))
    result["all_attempt_latency_s"] = distribution([x["duration_s"] for x in samples])
    result["successful_latency_s"] = distribution([x["duration_s"] for x in samples if x["ok"]])
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    rows = []
    for name in sorted(BACKENDS):
        row = measure(name)
        rows.append(row)
        write_json(args.output / ("%s.json" % name), row)
        write_json(args.output / "micro.json", rows)
        print(json.dumps({
            "backend": name, "success": row["success"],
            "latency": row["all_attempt_latency_s"], "setup_error": row["setup_error"],
        }), flush=True)


if __name__ == "__main__":
    main()
