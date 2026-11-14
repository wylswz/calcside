#!/usr/bin/env python3
"""Task set v3: the four short data tasks (A) plus three agentic tasks (B).

Task A definitions and validators stay in evaluation_v2. Set B adds tasks that
require directory enumeration, multi-file reconciliation and bounded-context
aggregation over a 5,000-row input. Every expectation is deterministic and checked
from persisted artifacts only; validators never read model output.
"""

import json
import math

from evaluation_v2 import TASKS as TASKS_A
from evaluation_v2 import validate as validate_a


# ------------------------------------------------------------------ fixtures


def _log_files():
    files = {}
    services = ["api", "worker", "render"]
    for part in range(10):
        lines = []
        for i in range(20):
            n = part * 20 + i
            level = "error" if n % 7 == 0 and n % 5 != 0 else "info"
            lines.append(json.dumps({
                "ts": "2026-11-01T00:%02d:%02dZ" % (n // 60, n % 60),
                "level": level,
                "service": services[n % 3],
                "request_id": "req-%04d" % n,
            }, separators=(",", ":")))
        files["logs/part-%02d.jsonl" % part] = "\n".join(lines) + "\n"
    files["logs/part-09.jsonl"] += (
        '{"ts":"2026-11-01T00:04:00Z","level":"error","service":"render",'
        '"request_id":"req-tie-z"}\n'
        '{"ts":"2026-11-01T00:04:00Z","level":"error","service":"api",'
        '"request_id":"req-tie-a"}\n'
    )
    return files


LEDGER = (
    "id,amount\n"
    "TX01,100\nTX02,50.50\nTX03,200\nTX04,75\nTX05,10\nTX06,300\n"
)
BANK = (
    "id,amount,status\n"
    "TX01,100,settled\nTX02,50.50,settled\nTX03,210.10,settled\n"
    "TX04,75,pending\nTX06,300,settled\nTX07,40,settled\n"
)


def _series_rows():
    return ["%d,%d" % (i, (i * 37) % 1000) for i in range(5000)]


SERIES = "idx,value\n" + "\n".join(_series_rows()) + "\n"

TASKS_B = {
    "triage": {
        "prompt": (
            "The directory logs/ contains JSONL files. Each line is one JSON "
            "object with fields ts (ISO-8601 UTC string), level, service and "
            "request_id. Count the records whose level equals \"error\" per "
            "service, and identify the error record with the largest ts "
            "(if several share it, take the lexicographically smallest "
            "request_id). Write triage.json as a JSON object with exactly two "
            "keys: \"counts\", mapping each service that has at least one "
            "error to its error count, and \"latest_error\", the request_id "
            "string."
        ),
        "files": _log_files(),
        "artifact": "triage.json",
    },
    "reconcile": {
        "prompt": (
            "ledger.csv has columns id,amount. bank.csv has columns "
            "id,amount,status. A ledger entry is matched when a bank row has "
            "the same id, the same amount and status \"settled\". Every other "
            "id is unmatched on its own side. Write reconcile.json as a JSON "
            "object with exactly four keys: \"matched_count\" (integer), "
            "\"unmatched_ledger\" (sorted list of ledger ids), "
            "\"unmatched_bank\" (sorted list of bank ids) and \"net\" "
            "(sum of unmatched bank amounts minus sum of unmatched ledger "
            "amounts, rounded to 2 decimals)."
        ),
        "files": {"ledger.csv": LEDGER, "bank.csv": BANK},
        "artifact": "reconcile.json",
    },
    "series": {
        "prompt": (
            "series.csv has columns idx,value with 5000 rows. Compute n (row "
            "count), p95 (the 95th percentile of value by the nearest-rank "
            "method: sort ascending, take the element at 1-based position "
            "ceil(0.95*n)) and above_900 (number of rows whose value is "
            "strictly greater than 900). Write series_stats.json as a JSON "
            "object with exactly three integer fields: n, p95, above_900. Do "
            "not print the whole file."
        ),
        "files": {"series.csv": SERIES},
        "artifact": "series_stats.json",
    },
}


TASKS = {**TASKS_A, **TASKS_B}


# Fixed independently checked oracles, not calls to the fixture generators.
EXPECTED = {
    "triage": {
        "counts": {"api": 9, "worker": 8, "render": 8},
        "latest_error": "req-tie-a",
    },
    "reconcile": {
        "matched_count": 3,
        "unmatched_ledger": ["TX03", "TX04", "TX05"],
        "unmatched_bank": ["TX03", "TX04", "TX07"],
        "net": 40.1,
    },
    "series": {"n": 5000, "p95": 949, "above_900": 495},
}


def _is_number(value):
    return type(value) in (int, float) and math.isfinite(value)


def validate(task_id, text):
    if task_id in TASKS_A:
        return validate_a(task_id, text)
    try:
        data = json.loads(text)
    except ValueError as exc:
        return {"ok": False, "reason": "%s: %s" % (type(exc).__name__, str(exc)[:200])}
    if not isinstance(data, dict):
        return {"ok": False, "reason": "artifact is not a JSON object"}
    expected = EXPECTED[task_id]
    if set(data) != set(expected):
        return {"ok": False, "reason": "key set %s != %s"
                % (sorted(data), sorted(expected))}
    if task_id == "triage":
        counts = data["counts"]
        ok = (
            isinstance(counts, dict)
            and all(isinstance(k, str) and type(v) is int for k, v in counts.items())
            and counts == expected["counts"]
            and data["latest_error"] == expected["latest_error"]
        )
    elif task_id == "reconcile":
        ok = (
            type(data["matched_count"]) is int
            and data["matched_count"] == expected["matched_count"]
            and data["unmatched_ledger"] == expected["unmatched_ledger"]
            and data["unmatched_bank"] == expected["unmatched_bank"]
            and _is_number(data["net"]) and data["net"] == expected["net"]
        )
    else:
        ok = (
            type(data["n"]) is int and data["n"] == expected["n"]
            and type(data["p95"]) is int and data["p95"] == expected["p95"]
            and type(data["above_900"]) is int
            and data["above_900"] == expected["above_900"]
        )
    return {"ok": ok, "reason": "matched" if ok else "artifact content mismatch"}


def summarize(rows):
    from evaluation_v2 import rate, summarize as base
    result = base(rows)
    for name, summary in result.items():
        trials = [r for r in rows if r["backend"] == name]
        summary["by_task"] = {
            task: rate(sum(r["passed"] for r in trials if r["task"] == task),
                       sum(r["task"] == task for r in trials))
            for task in TASKS_B
        }
    return result
