#!/usr/bin/env python3
"""Lead-authored task contract and descriptive statistics; stdlib only."""

import csv
import io
import json
import math
import statistics


TASKS = {
    "stats": {
        "prompt": (
            "Read data.csv (header x). Write result.json as a JSON object with "
            "count, mean and max of column x. Use numbers, not strings."
        ),
        "files": {"data.csv": "x\n" + "".join("%d\n" % n for n in range(1, 101))},
        "artifact": "result.json",
    },
    "join": {
        "prompt": (
            "Join employees.csv to departments.csv on dept_id. Sum salary by "
            "department name. Write summary.json as a JSON object mapping each "
            "department name to its numeric total. Do not include extra keys."
        ),
        "files": {
            "employees.csv": "name,dept_id,salary\nAda,1,100\nBob,2,90\nChen,1,110\n",
            "departments.csv": "dept_id,name\n1,Engineering\n2,Sales\n",
        },
        "artifact": "summary.json",
    },
    "file": {
        "prompt": (
            "Create squares.csv with header n,sq and exactly five rows, ordered "
            "n=1 through 5, with sq=n*n. Read it back to check your work."
        ),
        "files": {},
        "artifact": "squares.csv",
    },
    "fetch": {
        "prompt": (
            "Fetch https://api.github.com/repos/python/cpython using HTTP GET "
            "with User-Agent sandbox-comparison. Write repo.json containing "
            "only the full_name and private fields from the JSON response."
        ),
        "files": {},
        "artifact": "repo.json",
    },
}

COMMON_PROMPT = (
    "Complete the user's task using execute_code. Files persist between tool "
    "calls in the working directory. Create the requested artifact before "
    "your final answer. Tool errors are observations: correct the code and "
    "retry when useful. Do not access credentials or files outside the "
    "working directory. Do not install packages. You have at most eight "
    "model calls and twelve tool calls. "
)
LANGUAGE_PROMPTS = {
    "starlark": (
        "Code is Starlark, NOT Python. No imports/classes/f-strings/try/except; "
        "strings are not iterable (use .elems()). Globals persist. "
        "Files are under /work; use fs.read(path), fs.write(path,text). "
        "json.decode(text), json.encode(value), csv.parse(text) -> rows, "
        "csv.parse_dicts(text) -> list of dictionaries, stats.mean(numbers), "
        "float(), int(), max(), len() are available. "
        "net.get(url, headers={...}) returns a dict with status, headers, body; "
        "decode body with json.decode. help('net') or help('csv') prints docs. "
        "Print diagnostics with print()."
    ),
    "python": (
        "Code is Python 3 with the standard library only. The current directory "
        "is the workspace. Use open for files, csv/json for parsing and "
        "urllib.request for HTTP. Variables may not persist between calls; "
        "persist intermediate work in files. Print diagnostics with print()."
    ),
}


def numeric(value, expected):
    return type(value) in (int, float) and math.isfinite(value) and value == expected


def validate(task_id, text):
    """Validate persisted data, not the agent's claims or output substrings."""
    try:
        if task_id == "file":
            rows = list(csv.reader(io.StringIO(text)))
            expected = [["n", "sq"]] + [[str(n), str(n * n)] for n in range(1, 6)]
            ok = rows == expected
        else:
            data = json.loads(text)
            if not isinstance(data, dict):
                return {"ok": False, "reason": "artifact is not a JSON object"}
            if task_id == "stats":
                ok = set(data) == {"count", "mean", "max"} and all(
                    numeric(data[k], v)
                    for k, v in {"count": 100, "mean": 50.5, "max": 100}.items()
                )
            elif task_id == "join":
                ok = set(data) == {"Engineering", "Sales"} and all(
                    numeric(data[k], v) for k, v in {"Engineering": 210, "Sales": 90}.items()
                )
            elif task_id == "fetch":
                ok = (
                    set(data) == {"full_name", "private"}
                    and data["full_name"] == "python/cpython"
                    and data["private"] is False
                )
            else:
                raise ValueError("unknown task %s" % task_id)
        return {"ok": ok, "reason": "matched" if ok else "artifact content mismatch"}
    except (ValueError, TypeError, KeyError, csv.Error) as exc:
        return {"ok": False, "reason": "%s: %s" % (type(exc).__name__, str(exc)[:200])}


def rate(successes, attempts):
    if not attempts:
        return {"successes": successes, "attempts": attempts, "rate": None, "wilson95": None}
    z = 1.959963984540054
    p = successes / attempts
    denominator = 1 + z * z / attempts
    center = (p + z * z / (2 * attempts)) / denominator
    half = z * math.sqrt(p * (1 - p) / attempts + z * z / (4 * attempts**2)) / denominator
    return {
        "successes": successes, "attempts": attempts, "rate": p,
        "wilson95": [max(0, center - half), min(1, center + half)],
    }


def distribution(values):
    if not values:
        return {"n": 0, "p50": None, "p95": None, "mean": None}
    xs = sorted(values)
    return {
        "n": len(xs),
        "p50": xs[math.ceil(0.50 * len(xs)) - 1],
        "p95": xs[math.ceil(0.95 * len(xs)) - 1],
        "mean": statistics.fmean(xs),
    }


def summarize(rows):
    result = {}
    for backend in sorted({r["backend"] for r in rows}):
        trials = [r for r in rows if r["backend"] == backend]
        tools = [e for r in trials for e in r["events"] if e["kind"] == "tool"]
        models = [e for r in trials for e in r["events"] if e["kind"] == "model"]
        first = [next((e for e in r["events"] if e["kind"] == "tool"), None) for r in trials]
        first = [e for e in first if e is not None]
        with_errors = [
            r for r in trials
            if any(e["kind"] == "tool" and not e["ok"] for e in r["events"])
        ]
        token_events = [e for e in models if e.get("input_tokens") is not None]
        result[backend] = {
            "task_completion": rate(sum(r["passed"] for r in trials), len(trials)),
            "by_task": {
                task: rate(sum(r["passed"] for r in trials if r["task"] == task),
                           sum(r["task"] == task for r in trials))
                for task in TASKS
            },
            "tool_success": rate(sum(e["ok"] for e in tools), len(tools)),
            "first_tool_success": rate(sum(e["ok"] for e in first), len(first)),
            "recovery": rate(sum(r["passed"] for r in with_errors), len(with_errors)),
            "model_success": rate(sum(e["ok"] for e in models), len(models)),
            "input_tokens_observed": sum(e["input_tokens"] for e in token_events),
            "output_tokens_observed": sum(e["output_tokens"] for e in token_events),
            "model_events_missing_usage": len(models) - len(token_events),
            "tool_error_kinds": {
                kind: sum(e.get("error_kind") == kind for e in tools if not e["ok"])
                for kind in sorted({e.get("error_kind", "unknown") for e in tools if not e["ok"]})
            },
            "latency_s": {
                name: distribution([r[name] for r in trials if r.get(name) is not None])
                for name in ("setup_s", "loop_s", "model_s", "tool_s", "residual_s",
                             "verification_s", "cleanup_s")
            },
            "tool_latency_s": distribution([e["duration_s"] for e in tools]),
            "cleanup_errors": sum(r.get("cleanup_error") is not None for r in trials),
        }
    return result
