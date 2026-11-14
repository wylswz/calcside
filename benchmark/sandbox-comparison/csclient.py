"""Minimal calcside API client for the sandbox comparison.

Stdlib only, so it runs under any Python here. Every call is one HTTP request;
the harness measures around these, never inside them.
"""

import json
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:18787"


def _req(method, path, body=None, timeout=300):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(
        BASE + path,
        data=data,
        method=method,
        headers={
            "Content-Type": "application/json",
            "X-Requested-With": "calcside",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except json.JSONDecodeError:
            return e.code, raw.decode(errors="replace")


def health():
    return _req("GET", "/healthz", timeout=5)[0] == 200


def create(spec):
    return _req("POST", "/api/v1/instances", spec)


def delete(iid):
    return _req("DELETE", f"/api/v1/instances/{iid}")


def exec_(iid, code, timeout_ms=None, section=None):
    body = {"code": code}
    if timeout_ms:
        body["timeout_ms"] = timeout_ms
    if section:
        body["section"] = section
    return _req("POST", f"/api/v1/instances/{iid}/exec", body)


def audit(exec_id):
    _, body = _req("GET", f"/api/v1/audit?exec_id={exec_id}&limit=200")
    return (body or {}).get("events", [])


def notebook(iid):
    return _req("GET", f"/api/v1/instances/{iid}/notebook")


def run_sections(iid, sections, dry_run=False):
    return _req(
        "POST",
        f"/api/v1/instances/{iid}/notebook/run",
        {"sections": sections, "dry_run": dry_run},
    )


def timed_create(spec, poll=0.02):
    """Create and poll until running; returns (instance, wall seconds)."""
    t0 = time.perf_counter()
    status, body = create(spec)
    if status != 201:
        raise RuntimeError(f"create failed {status}: {body}")
    iid = body["instance"]["id"]
    while time.perf_counter() - t0 < 120:
        status, got = _req("GET", f"/api/v1/instances/{iid}")
        if status == 200 and got["instance"]["status"] == "running":
            return got["instance"], time.perf_counter() - t0
        time.sleep(poll)
    raise RuntimeError("instance never reached running")
