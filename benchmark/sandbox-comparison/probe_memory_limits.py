#!/usr/bin/env python3
"""Bounded allocation probe in newly created, explicitly capped containers."""

import argparse
import hashlib
import json
import subprocess
import time
import uuid
from pathlib import Path

from run_v2 import write_json


CAP = 256 * 1024 * 1024
REQUEST = 768 * 1024 * 1024
IMAGE = "python:3.13-slim"
CGROUP_CODE = """
import json
paths = ['/sys/fs/cgroup/memory.max', '/sys/fs/cgroup/memory.swap.max',
         '/sys/fs/cgroup/memory.events']
out = {}
for p in paths:
    try:
        with open(p) as f:
            out[p] = f.read().strip()
    except OSError as e:
        out[p] = str(e)
print(json.dumps(out))
"""
ALLOCATE_CODE = "x = bytearray(%d); print(len(x), flush=True)" % REQUEST


def command(args, timeout=60):
    p = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    return {"returncode": p.returncode, "stdout": p.stdout, "stderr": p.stderr}


def checked(args, timeout=60):
    r = command(args, timeout)
    if r["returncode"]:
        raise RuntimeError("%s: %s" % (args[0], r["stderr"][-500:]))
    return r["stdout"]


def memory_state(cid):
    raw = checked([
        "docker", "inspect", "--format",
        "{{.HostConfig.Memory}} {{.HostConfig.MemorySwap}} {{.State.OOMKilled}}", cid,
    ]).split()
    cg = json.loads(checked(["docker", "exec", cid, "python", "-c", CGROUP_CODE]))
    return {"memory": int(raw[0]), "memory_swap": int(raw[1]),
            "state_oom_killed": raw[2] == "true", "cgroup": cg}


def oom_count(state):
    text = state["cgroup"]["/sys/fs/cgroup/memory.events"]
    fields = dict(line.split() for line in text.splitlines())
    return int(fields["oom_kill"])


def probe(kind):
    name = "bench-mem-" + uuid.uuid4().hex[:8]
    row = {"system": kind, "requested_memory_limit_bytes": CAP,
           "allocation_request_bytes": REQUEST, "name": name,
           "error": None, "cleanup_error": None}
    created = False
    cid = None
    try:
        if kind == "docker":
            cid = checked([
                "docker", "run", "-d", "--name", name, "--memory", str(CAP),
                "--memory-swap", str(CAP), IMAGE, "sleep", "infinity",
            ]).strip()
            created = True
        else:
            checked([
                "openshell", "-g", "openshell", "sandbox", "create",
                "--name", name, "--from", IMAGE, "--memory", "256Mi",
                "--detach", "--", "sleep", "infinity",
            ], timeout=120)
            created = True
            deadline = time.monotonic() + 180
            while time.monotonic() < deadline:
                doc = json.loads(checked([
                    "openshell", "-g", "openshell", "sandbox", "get",
                    "-o", "json", name,
                ]))
                if doc.get("phase") == "Ready":
                    break
                if doc.get("phase") in ("Failed", "Error"):
                    raise RuntimeError("sandbox phase %s" % doc["phase"])
                time.sleep(1)
            else:
                raise RuntimeError("sandbox readiness deadline")
            candidates = checked([
                "docker", "ps", "--filter", "name=--" + name + "-",
                "--format", "{{.ID}}\t{{.Names}}",
            ]).splitlines()
            workload = [line.split("\t")[0] for line in candidates
                        if not line.split("\t")[1].endswith("-supervisor")]
            if len(workload) != 1:
                raise RuntimeError("expected exactly one own workload container")
            cid = workload[0]
        row["container_id"] = cid
        row["before"] = memory_state(cid)
        # Do not stress an uncapped or unexpectedly configured process.
        if (row["before"]["memory"] != CAP
                or row["before"]["cgroup"]["/sys/fs/cgroup/memory.max"] != str(CAP)):
            raise RuntimeError("requested cgroup cap not confirmed; allocation skipped")
        oom_count(row["before"])  # Check observability before executing the probe.
        args = (["docker", "exec", cid, "python", "-u", "-c", ALLOCATE_CODE]
                if kind == "docker" else [
                    "openshell", "-g", "openshell", "sandbox", "exec", "-n", name,
                    "--timeout", "20", "--no-login-shell", "--",
                    "python", "-u", "-c", ALLOCATE_CODE])
        row["allocation_result"] = command(args, timeout=45)
        row["after"] = memory_state(cid)
        row["oom_kill_delta"] = oom_count(row["after"]) - oom_count(row["before"])
    except Exception as exc:
        row["error"] = "%s: %s" % (type(exc).__name__, str(exc))
    finally:
        if created:
            try:
                checked(["docker", "rm", "-f", cid] if kind == "docker" else [
                    "openshell", "-g", "openshell", "sandbox", "delete", name])
            except Exception as exc:
                row["cleanup_error"] = "%s: %s" % (type(exc).__name__, str(exc))
    return row


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    assert not args.output.exists(), "refusing to overwrite memory evidence"
    output = {
        "configuration": "supplementary explicitly capped probe; not the agent benchmark defaults",
        "source_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
        "allocation_code": ALLOCATE_CODE,
        "observations": [],
    }
    for system in ("docker", "openshell"):
        output["observations"].append(probe(system))
        write_json(args.output, output)
        print(json.dumps(output["observations"][-1]), flush=True)


if __name__ == "__main__":
    main()
