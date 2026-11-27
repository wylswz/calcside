#!/usr/bin/env python3
"""Measure a dedicated kind deployment; never point this at shared/user data.

Requires the chart overlay scalability-values.yaml, two directly forwarded API
pods and a forwarded nginx proxy that round-robins those pods, plus the delay
fixture documented in docs/scalability.md. Credentials stay in --state.
The runner scales workers and briefly SIGSTOP/SIGCONTs one test worker.
It never deletes notebooks, applications, databases, clusters or user files.
"""

import argparse
import base64
import collections
import datetime
import hashlib
import http.cookiejar
import json
import os
import pathlib
import platform
import re
import subprocess
import threading
import time
import urllib.error
import urllib.request


ROOT = pathlib.Path(__file__).resolve().parent.parent
CPU_CODE = '''def main(input):
    fresh = not fs.exists("marker.txt")
    fs.write("marker.txt", "local")
    total = 0
    for i in range(50000):
        total += i * i
    return {"answer": total, "fresh": fresh}
'''
IO_CODE = '''def main(input):
    fresh = not fs.exists("marker.txt")
    fs.write("marker.txt", "local")
    r = net.get(input["url"])
    return {"answer": r["status"], "fresh": fresh, "worker": r["body"]}
'''

# These local-test queries intentionally expose only routing/fencing metadata.
# The pool predicate matches serving eligibility, not the legacy active pointer.
POOL_QUERY = """
SELECT COALESCE(json_agg(q ORDER BY q.instance_id,q.node_id),'[]'::json)
FROM (
 SELECT r.id,r.instance_id,r.node_id,r.boot_id,r.node_incarnation,
        r.generation,r.epoch,r.lease_until,n.heartbeat_until,n.addr
 FROM serving_replicas r
 JOIN persistent_instances i ON i.id=r.instance_id
 JOIN serving_nodes n ON n.node_id=r.node_id
 WHERE r.instance_id IN ({ids}) AND i.enabled
   AND r.workspace_id=i.workspace_id AND r.release_id=i.release_id
   AND r.generation=i.generation AND r.epoch>0 AND r.epoch<=i.last_epoch
   AND r.state='ready' AND r.lease_until>CURRENT_TIMESTAMP
   AND r.boot_id=n.boot_id AND r.node_incarnation=n.incarnation
   AND n.heartbeat_until>CURRENT_TIMESTAMP
) q;
"""
BINDING_QUERY = """
SELECT row_to_json(q) FROM (
 SELECT id,node_id,lease_epoch FROM instances WHERE id='{id}'
) q;
"""


def utc():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


class Experiment:
    def __init__(self, args):
        self.args = args
        self.out = args.output.resolve()
        self.out.mkdir(parents=True, exist_ok=False)
        self.connection = json.loads((args.state / "connection.json").read_text())
        self.env = dict(os.environ, KUBECONFIG=str(args.state / "kubeconfig"))
        self.base = self.connection["base_url"]
        self.apis = self.connection["api_urls"]
        self.key = self.connection["api_key"]
        self.delay_url = self.connection["delay_url"]
        # Management is session-only (API keys intentionally cap at Run).
        # Timed invocations still use the release helper's API key.
        credentials = json.loads((args.state / "credentials.json").read_text())
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        token = base64.b64encode(
            f"{credentials['admin_username']}:{credentials['admin_password']}".encode()).decode()
        login = urllib.request.Request(
            self.base + "/auth/basic/login", data=b"", method="POST",
            headers={"Authorization": "Basic " + token, "X-Requested-With": "calcside"})
        with self.opener.open(login, timeout=30) as response:
            if response.status != 200:
                raise RuntimeError("Fixture management login failed")
        self.pins = []
        self.fixtures = {}
        self.events = []
        self.cases = []
        self.failures = []
        self.stop_monitor = threading.Event()
        self.pg_pod = self.kube(
            "get", "pods", "-l", "app.kubernetes.io/name=postgres-test",
            "-o", "jsonpath={.items[0].metadata.name}").strip()

    def save(self, name, data):
        (self.out / name).write_text(json.dumps(data, indent=2) + "\n")

    def kube(self, *args):
        p = subprocess.run(
            ["kubectl", "-n", "calcside", *args], env=self.env,
            check=True, capture_output=True, text=True, timeout=45,
        )
        return p.stdout

    def sql(self, query):
        raw = self.kube("exec", self.pg_pod, "--", "psql", "-X", "-U", "calcside",
                        "-d", "calcside", "-At", "-v", "ON_ERROR_STOP=1", "-c", query)
        return json.loads(raw)

    def request(self, method, path, payload=None, base=None):
        req = urllib.request.Request(
            (base or self.base) + path,
            data=None if payload is None else json.dumps(payload).encode(),
            headers={
                "Content-Type": "application/json",
                "X-Requested-With": "calcside",
            }, method=method,
        )
        try:
            with self.opener.open(req, timeout=30) as response:
                return json.load(response)
        except urllib.error.HTTPError as exc:
            detail = exc.read(1024).decode(errors="replace")
            raise RuntimeError(f"{method} {path}: HTTP {exc.code}: {detail}") from None

    def pods(self):
        return json.loads(self.kube("get", "pods", "-o", "json"))["items"]

    def api_identity(self):
        return sorted(
            (p["metadata"]["name"], p["metadata"]["uid"])
            for p in self.pods()
            if p["metadata"].get("labels", {}).get("app.kubernetes.io/component") == "api"
        )

    def pool(self):
        if not self.pins:
            return []
        if not all(re.fullmatch(r"pin_[A-Za-z0-9_-]+", p) for p in self.pins):
            raise ValueError("Unexpected persistent instance ID")
        ids = ",".join("'" + p + "'" for p in self.pins)
        return self.sql(POOL_QUERY.format(ids=ids))

    def event(self, name, **details):
        e = {"at": utc(), "unix_ms": time.time_ns() // 1000000, "name": name, **details}
        self.events.append(e)
        self.save("events.json", self.events)
        print(f"{e['at']} {name}", flush=True)

    def wait_pool(self, count, timeout=150):
        start = time.monotonic()
        while time.monotonic() - start < timeout:
            rows = self.pool()
            groups = collections.Counter(r["instance_id"] for r in rows)
            if all(groups[p] == count for p in self.pins):
                self.event("pool_ready", workers=count, elapsed_s=time.monotonic() - start,
                           replicas=rows)
                return rows
            time.sleep(1)
        raise RuntimeError(f"Pool did not reach {count} replicas per application")

    def create_fixtures(self):
        for workload, code, spec, answer in [
            ("cpu", CPU_CODE, {"capabilities": {"fs": {}}}, 41665416675000),
            ("io", IO_CODE, {"capabilities": {"fs": {}, "net": {
                "allow_hosts": ["scalability-delay:8080"], "timeout_ms": 10000,
            }}}, 200),
        ]:
            app = self.request("POST", "/api/v1/applications", {
                "name": f"scalability-{workload}",
                "sections": [{"id": "main", "code": code}], "spec": spec,
            })["application"]
            path = f"/api/v1/applications/{app['id']}"
            preview = self.request("POST", path + "/publish/preview", {"version": 1})
            self.request("POST", path + "/publish", {
                "version": 1, "expected_base": "", "candidate": preview["candidate"]["digest"],
            })
            app = self.request("GET", path)["application"]
            self.pins.append(app["persistent_instance_id"])
            self.fixtures[workload] = {
                "app_id": app["id"], "pin_id": app["persistent_instance_id"], "answer": answer,
                "input": {} if workload == "cpu" else {"url": self.delay_url},
                "spec": spec,
            }
        notebook = self.request("POST", "/api/v1/instances", {
            "ttl_seconds": 7200, "capabilities": {"fs": {}},
        }, base=self.apis[0])["instance"]
        self.notebook = notebook["id"]
        self.request("POST", f"/api/v1/instances/{self.notebook}/exec", {
            "code": 'x = 4242\nfs.write("marker.txt", "stable-notebook")',
        }, base=self.apis[0])
        self.binding = self.notebook_binding()
        self.save("fixtures.json", self.fixtures)
        self.event("notebook_seeded", binding=self.binding)
        self.check_notebook("initial", cold_api=False)

    def notebook_binding(self):
        if not re.fullmatch(r"ins_[A-Za-z0-9_-]+", self.notebook):
            raise ValueError("Unexpected notebook ID")
        return self.sql(BINDING_QUERY.format(id=self.notebook))

    def check_notebook(self, phase, cold_api=True):
        output = []
        for base in self.apis if cold_api else self.apis[:1]:
            r = self.request("POST", f"/api/v1/instances/{self.notebook}/exec", {
                "code": 'print(x)\nprint(fs.read("marker.txt"))',
            }, base=base)
            if r.get("error") or r.get("output") != "4242\nstable-notebook\n":
                raise RuntimeError(f"Notebook state changed during {phase}")
            output.append({"base": base, "output": r["output"]})
        current = self.notebook_binding()
        if current != self.binding:
            raise RuntimeError(f"Notebook binding changed during {phase}")
        self.event("notebook_verified", phase=phase, binding=current, results=output)

    def monitor(self):
        node = self.kube("get", "nodes", "-o", "jsonpath={.items[0].metadata.name}").strip()
        with (self.out / "samples.jsonl").open("w") as f:
            while not self.stop_monitor.is_set():
                sample = {"at": utc(), "unix_ms": time.time_ns() // 1000000}
                try:
                    summary = json.loads(self.kube(
                        "get", "--raw", f"/api/v1/nodes/{node}/proxy/stats/summary"))
                    sample["pods"] = [
                        {key: p.get(key) for key in ("podRef", "cpu", "memory", "containers")}
                        for p in summary.get("pods", [])
                        if p["podRef"]["namespace"] == "calcside"
                    ]
                    sample["ready_pool"] = self.pool()
                except Exception as exc:
                    sample["error"] = type(exc).__name__
                f.write(json.dumps(sample) + "\n")
                f.flush()
                self.stop_monitor.wait(5)

    def run_case(self, name, workload, vus, duration, allow_failures=False, trace=False, actions=()):
        fixture = self.fixtures[workload]
        pods = self.pods()
        worker_ips = {
            p["status"]["podIP"]: int(p["metadata"]["name"].rsplit("-", 1)[1])
            for p in pods
            if p["metadata"].get("labels", {}).get("app.kubernetes.io/component") == "worker"
            and p["status"].get("podIP")
        }
        api_pods = sorted(
            (p for p in pods if p["metadata"].get("labels", {}).get(
                "app.kubernetes.io/component") == "api"), key=lambda p: p["metadata"]["name"])
        api_ips = {p["status"]["podIP"] + ":8080": i for i, p in enumerate(api_pods)}
        summary = self.out / f"{name}.json"
        env = dict(self.env, CALCSIDE_BASE_URL=self.base, CALCSIDE_API_KEY=self.key,
                   SCALABILITY_APP_ID=fixture["app_id"],
                   SCALABILITY_ANSWER=str(fixture["answer"]),
                   SCALABILITY_INPUT=json.dumps(fixture["input"]),
                   SCALABILITY_VUS=str(vus), SCALABILITY_DURATION=f"{duration}s",
                   SCALABILITY_SUMMARY=str(summary),
                   SCALABILITY_WORKER_IPS=json.dumps(worker_ips),
                   SCALABILITY_API_IPS=json.dumps(api_ips),
                   SCALABILITY_ALLOW_FAILURES="1" if allow_failures else "0",
                   SCALABILITY_TRACE="1" if trace else "0")
        self.event("case_start", case=name, workload=workload, vus=vus, duration_s=duration)
        started = time.monotonic()
        pending = list(actions)
        with (self.out / f"{name}.log").open("w") as log:
            proc = subprocess.Popen(
                ["k6", "run", "--no-usage-report", str(ROOT / "benchmark/persistent.js")],
                env=env, stdout=log, stderr=subprocess.STDOUT)
            try:
                while proc.poll() is None:
                    if pending and time.monotonic() - started >= pending[0][0]:
                        _, action = pending.pop(0)
                        action()
                    time.sleep(0.2)
                code = proc.wait()
            finally:
                if proc.poll() is None:
                    proc.terminate()
                    proc.wait(timeout=25)
        case = {
            "name": name, "workload": workload, "vus": vus, "duration_s": duration,
            "wall_s": time.monotonic() - started, "exit_code": code,
            "allow_failures": allow_failures, "summary": summary.name,
            "api_ips": api_ips, "worker_ips": worker_ips,
        }
        if not summary.exists():
            raise RuntimeError(f"Missing k6 summary for {name}; inspect its log")
        self.cases.append(case)
        self.save("cases.json", self.cases)
        self.event("case_end", case=name, exit_code=code)
        if code != 0 and not allow_failures:
            self.failures.append(name)
        return case

    def scale(self, count):
        self.event("scale_requested", workers=count)
        self.kube("scale", "statefulset/calcside-worker", f"--replicas={count}")

    def pause(self):
        self.event("worker_pause_requested", worker="calcside-worker-2")
        # A PID-namespace init can ignore SIGSTOP from its own namespace.
        # Signal its node-namespace PID instead, and verify the actual state.
        pod = json.loads(self.kube("get", "pod", "calcside-worker-2", "-o", "json"))
        container = next(c for c in pod["status"]["containerStatuses"] if c["name"] == "calcside-worker")
        cid = container["containerID"].split("://", 1)[1]
        node = pod["spec"]["nodeName"]
        raw = subprocess.check_output(
            ["docker", "exec", node, "crictl", "inspect", cid], text=True, timeout=15)
        pid = int(json.loads(raw)["info"]["pid"])
        self.paused_process = (node, pid)
        subprocess.run(["docker", "exec", node, "kill", "-STOP", str(pid)],
                       check=True, capture_output=True, timeout=15)
        state = subprocess.check_output(
            ["docker", "exec", node, "cat", f"/proc/{pid}/status"], text=True, timeout=15)
        state_line = next(line for line in state.splitlines() if line.startswith("State:"))
        if "T" not in state_line.split(":", 1)[1].split()[0]:
            raise RuntimeError(f"Worker was not stopped: {state_line}")
        self.event("worker_paused", worker="calcside-worker-2", node=node, pid=pid,
                   process_state=state_line)

    def resume(self):
        if not hasattr(self, "paused_process"):
            return
        node, pid = self.paused_process
        subprocess.run(["docker", "exec", node, "kill", "-CONT", str(pid)],
                       check=True, capture_output=True, timeout=15)
        state = subprocess.check_output(
            ["docker", "exec", node, "cat", f"/proc/{pid}/status"], text=True, timeout=15)
        state_line = next(line for line in state.splitlines() if line.startswith("State:"))
        if state_line.split(":", 1)[1].split()[0] in ("T", "t"):
            raise RuntimeError(f"Worker is still stopped: {state_line}")
        self.event("worker_resumed", worker="calcside-worker-2")

    def run(self):
        files = subprocess.check_output(
            ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
            cwd=ROOT).decode().split("\0")
        source_hashes = {
            name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest()
            for name in sorted(set(files)) if name and (ROOT / name).is_file()
        }
        self.save("environment.json", {
            "started_at": utc(), "host": platform.platform(),
            "head": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
            "dirty": bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT)),
            "source_diff_sha256": hashlib.sha256(subprocess.check_output(
                ["git", "diff", "--binary"], cwd=ROOT)).hexdigest(),
            "source_files_sha256": hashlib.sha256(json.dumps(
                source_hashes, sort_keys=True).encode()).hexdigest(),
            "matrix_workers": [1, 2, 3], "matrix_vus": [1, 8, 24],
            "repeats": self.args.repeats, "duration_s": self.args.duration,
            "warmup_s": 5, "io_delay_ms": 100, "cpu_loop_n": 50000,
            "mode": "fault-only" if self.args.fault_only_from else "full",
            "api_pods": self.api_identity(),
        })
        self.save("source-hashes.json", source_hashes)
        self.save("pods-before.json", self.pods())
        self.api_before = self.api_identity()
        if self.args.fault_only_from:
            previous = self.args.fault_only_from
            self.fixtures = json.loads((previous / "fixtures.json").read_text())
            self.pins = [f["pin_id"] for f in self.fixtures.values()]
            prior_events = json.loads((previous / "events.json").read_text())
            self.binding = next(e["binding"] for e in prior_events if e["name"] == "notebook_seeded")
            self.notebook = self.binding["id"]
            self.save("fixtures.json", self.fixtures)
            self.event("fixtures_reused", source=str(previous), binding=self.binding)
        else:
            self.create_fixtures()
        monitor = threading.Thread(target=self.monitor, daemon=True)
        monitor.start()
        try:
            if self.args.fault_only_from:
                self.wait_pool(3)
                self.run_case(
                    "pause-w3-io-v12-verified", "io", 12, 120, allow_failures=True, trace=True,
                    actions=((20, self.pause), (80, self.resume)))
                self.wait_pool(3)
                self.run_case("recovery-w3-io-v12-verified", "io", 12, 30)
                self.check_notebook("after-verified-worker-pause")
                if self.api_identity() != self.api_before:
                    raise RuntimeError("API pods changed during fault experiment")
                self.event("experiment_complete", steady_failures=self.failures)
                return 1 if self.failures else 0
            for count in (1, 2, 3):
                self.wait_pool(count)
                self.check_notebook(f"workers-{count}", cold_api=count > 1)
                for repeat in range(1, self.args.repeats + 1):
                    # Reverse workload/VU order on even repeats to expose order effects.
                    workloads = ("cpu", "io") if repeat % 2 else ("io", "cpu")
                    levels = (1, 8, 24) if repeat % 2 else (24, 8, 1)
                    for workload in workloads:
                        self.run_case(f"warm-w{count}-{workload}-r{repeat}",
                                      workload, 8, 5)
                        for vus in levels:
                            self.run_case(f"steady-w{count}-{workload}-v{vus}-r{repeat}",
                                          workload, vus, self.args.duration)
                if count < 3:
                    self.run_case(
                        f"expand-w{count}-to-{count+1}", "io", 12, 80, trace=True,
                        actions=((20, lambda n=count + 1: self.scale(n)),))
                    self.wait_pool(count + 1)
                    if self.api_identity() != self.api_before:
                        raise RuntimeError("API pods changed while scaling workers")
            # Bounded queue overload is descriptive, not a zero-error acceptance gate.
            self.run_case("overload-w3-io-v120", "io", 120, 30, allow_failures=True)
            self.run_case(
                "pause-w3-io-v12", "io", 12, 120, allow_failures=True, trace=True,
                actions=((20, self.pause), (80, self.resume)))
            self.wait_pool(3)
            self.run_case("recovery-w3-io-v12", "io", 12, 30)
            self.check_notebook("after-worker-pause")
            if self.api_identity() != self.api_before:
                raise RuntimeError("API pods changed during experiment")
        finally:
            # SIGCONT is idempotent and prevents an aborted run leaving the test pod paused.
            try:
                self.resume()
            except Exception:
                self.event("resume_cleanup_failed")
            self.stop_monitor.set()
            monitor.join(timeout=50)
            self.save("pods-after.json", self.pods())
        self.event("experiment_complete", steady_failures=self.failures)
        return 1 if self.failures else 0


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--state", type=pathlib.Path, required=True)
    p.add_argument("--output", type=pathlib.Path, required=True)
    p.add_argument("--duration", type=int, default=30)
    p.add_argument("--repeats", type=int, default=2)
    p.add_argument("--fault-only-from", type=pathlib.Path,
                   help="reuse captured fixtures in an existing dedicated deployment; run only the verified pause")
    p.add_argument("--confirm-isolated", action="store_true", required=True,
                   help="acknowledge worker scaling and SIGSTOP of a disposable test pod")
    args = p.parse_args()
    if args.duration < 1 or args.repeats < 1:
        p.error("duration/repeats must be positive")
    raise SystemExit(Experiment(args).run())


if __name__ == "__main__":
    main()
