#!/usr/bin/env python3
"""Execution adapters for the v2 sandbox comparison (see PROTOCOL.md).

BACKENDS maps a backend name to a zero-arg factory. Each instance exposes:

  language    'starlark' or 'python' (selects the prompt dialect)
  setup(files)        fresh namespace plus the given /work files
  execute(code)       -> {"output": str, "error": str|None,
                          "error_kind": 'runtime'|'policy_denied'|'timeout'|
                          'transport'|None, ...backend extras}
  read_file(path)     control-plane read of /work/<path>; raises on failure
  close()             delete only the resources this instance created
  metadata()          sanitized config (image/policy/gateway), no secrets

Python and E2B keep a persistent interpreter; Docker and OpenShell run
`python -c` per action (no globals persistence). Every execute is bounded by a
30-second adapter deadline. Adapters never place credentials in generated code
or subprocess environments.
"""

import hashlib
import json
import os
import re
import select
import subprocess
import sys
import tempfile
import time
import uuid

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import csclient as cs  # noqa: E402

EXEC_TIMEOUT_S = 30
DOCKER_IMAGE = "python:3.13-slim"
OPENSHELL_GATEWAY = "openshell"
OPENSHELL_IMAGE = "python:3.13-slim"
KERNEL = os.path.join(HERE, "pykernel.py")

CALCSIDE_SPEC = {
    "ttl_seconds": 600,
    "policies": ["builtin.block_private_network"],
    "capabilities": {
        "net": {"allow_hosts": ["api.github.com"]},
        "fs": {},
        "io": {},
    },
    "limits": {"exec_timeout_ms": 30000},
}

# up.sh passes this to both calcside processes; it exempts the host's fake-ip
# DNS range (198.18.x.x) from builtin.block_private_network's SSRF check.
# Real private ranges remain blocked.
SERVER_NET_ALLOW_CIDRS = "198.18.0.0/15"


def _result(output, error, error_kind, **extra):
    out = {"output": output or "", "error": error, "error_kind": error_kind}
    out.update(extra)
    return out


def _safe_relpath(path):
    """Workspace-relative path; rejects absolute paths and traversal."""
    raw = str(path)
    if raw.startswith(("/", "\\")) or re.match(r"^[A-Za-z]:", raw):
        raise ValueError("unsafe workspace path: %r" % (path,))
    p = raw.replace("\\", "/")
    parts = [seg for seg in p.split("/") if seg not in ("", ".")]
    if not parts or any(seg == ".." for seg in parts):
        raise ValueError("unsafe workspace path: %r" % (path,))
    return "/".join(parts)


def _starlark_str(s):
    """JSON string literal; a subset of Starlark string syntax."""
    return json.dumps(s, ensure_ascii=True)


def _stderr_tail(text, limit=500):
    text = (text or "").strip()
    return text[-limit:] if text else ""


_DOCKER_CLI_ERR = re.compile(
    r"Cannot connect to the Docker daemon|Error response from daemon|"
    r"docker: Error|is not running|No such container|Cannot connect to the Docker"
)
_OS_CLI_ERR = re.compile(
    r"(?m)^\s*Error:|×|code: '[^']+', message:|gateway|unavailable|"
    r"deadline exceeded|connection refused|transport error|no such sandbox"
)


def _docker_kind(stderr):
    """Heuristic: docker CLI failures are transport, python failures runtime."""
    if "Traceback (most recent call last)" in (stderr or ""):
        return "runtime"
    return "transport" if _DOCKER_CLI_ERR.search(stderr or "") else "runtime"


def _openshell_kind(stderr):
    """Heuristic: CLI/gateway errors are transport, remote command errors runtime."""
    if "Traceback (most recent call last)" in (stderr or ""):
        return "runtime"
    return "transport" if _OS_CLI_ERR.search(stderr or "") else "runtime"


# --------------------------------------------------------------------- python


class PythonBackend:
    """Long-lived pykernel subprocess in a private TemporaryDirectory."""

    language = "python"

    def __init__(self):
        self._tmp = None
        self._workdir = None
        self._proc = None
        self._seq = 0

    def _spawn(self):
        self._proc = subprocess.Popen(
            [sys.executable, KERNEL],
            cwd=self._workdir,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            bufsize=1,
            env={
                "PATH": os.pathsep.join(
                    [os.path.dirname(sys.executable), "/usr/local/bin", "/usr/bin", "/bin"]
                ),
                "HOME": self._workdir,
                "TMPDIR": self._workdir,
                "LANG": "C.UTF-8",
                "LC_ALL": "C.UTF-8",
                "PYTHONIOENCODING": "utf-8",
                "PYTHONDONTWRITEBYTECODE": "1",
            },
        )

    def _kill_kernel(self):
        proc, self._proc = self._proc, None
        if proc is None:
            return
        try:
            proc.kill()
        except OSError:
            pass
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pass

    def setup(self, files):
        self._tmp = tempfile.TemporaryDirectory(prefix="benchv2-py-")
        self._workdir = self._tmp.name
        for name, content in files.items():
            path = os.path.join(self._workdir, _safe_relpath(name))
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "w", encoding="utf-8") as f:
                f.write(content)
        self._spawn()

    def execute(self, code):
        try:
            if self._proc is None or self._proc.poll() is not None:
                self._spawn()  # fresh namespace; files persist on disk
            self._seq += 1
            rid = self._seq
            self._proc.stdin.write(json.dumps({"id": rid, "code": code}) + "\n")
            self._proc.stdin.flush()
            ready, _, _ = select.select([self._proc.stdout], [], [], EXEC_TIMEOUT_S)
            if not ready:
                stderr = self._proc.stderr.read() if self._proc.poll() is not None else ""
                self._kill_kernel()
                return _result("", "execution exceeded %ds%s" % (
                    EXEC_TIMEOUT_S, ("; kernel stderr: " + _stderr_tail(stderr)) if stderr else ""),
                    "timeout")
            line = self._proc.stdout.readline()
            if not line:
                stderr = self._proc.stderr.read() if self._proc.poll() is not None else ""
                self._kill_kernel()
                return _result("", "kernel exited%s" % (
                    ("; stderr: " + _stderr_tail(stderr)) if stderr else ""), "transport")
            resp = json.loads(line)
            if resp.get("id") != rid:
                return _result("", "kernel protocol desync", "transport")
            err = resp.get("error")
            return _result(resp.get("out", ""), err, "runtime" if err else None)
        except (BrokenPipeError, OSError, json.JSONDecodeError) as exc:
            self._kill_kernel()
            return _result("", "%s: %s" % (type(exc).__name__, str(exc)[:300]), "transport")
        except Exception as exc:
            return _result("", "%s: %s" % (type(exc).__name__, str(exc)[:300]), "transport")

    def read_file(self, path):
        full = os.path.join(self._workdir, _safe_relpath(path))
        with open(full, encoding="utf-8") as f:
            return f.read()

    def close(self):
        self._kill_kernel()
        if self._tmp is not None:
            self._tmp.cleanup()
            self._tmp = None

    def metadata(self):
        return {
            "backend": "python",
            "kernel": "pykernel.py",
            "workspace": "tempfile.TemporaryDirectory",
            "exec_timeout_s": EXEC_TIMEOUT_S,
            "env": "minimal (PATH/HOME/TMPDIR/LANG only)",
        }


# --------------------------------------------------------------------- docker


class DockerBackend:
    """One python:3.13-slim container per instance; `python -c` per action."""

    language = "python"

    def __init__(self):
        self._name = "bench-v2-" + uuid.uuid4().hex[:12]
        self._cid = None

    def _exec(self, args, timeout=60, **kw):
        return subprocess.run(
            ["docker", "exec", "-w", "/work", self._cid] + args,
            capture_output=True, text=True, timeout=timeout, **kw,
        )

    def setup(self, files):
        p = subprocess.run(
            ["docker", "run", "-d", "--rm", "--name", self._name,
             DOCKER_IMAGE, "sleep", "infinity"],
            capture_output=True, text=True, timeout=120,
        )
        if p.returncode != 0:
            raise RuntimeError("docker run failed: %s" % _stderr_tail(p.stderr))
        self._cid = p.stdout.strip()
        p = subprocess.run(
            ["docker", "exec", self._cid, "mkdir", "-p", "/work"],
            capture_output=True, text=True, timeout=30,
        )
        if p.returncode != 0:
            raise RuntimeError("docker mkdir failed: %s" % _stderr_tail(p.stderr))
        for name, content in files.items():
            rel = _safe_relpath(name)
            code = "import sys\nopen('/work/'+sys.argv[1],'w').write(%r)" % content
            p = self._exec(["python", "-u", "-c", code, rel], timeout=30)
            if p.returncode != 0:
                raise RuntimeError("docker setup write %s failed: %s" % (
                    rel, _stderr_tail(p.stderr)))

    def execute(self, code):
        try:
            p = self._exec(
                ["timeout", "-s", "KILL", str(EXEC_TIMEOUT_S),
                 "python", "-u", "-c", code],
                timeout=EXEC_TIMEOUT_S + 15,
            )
        except subprocess.TimeoutExpired:
            return _result("", "docker exec exceeded deadline", "timeout")
        except OSError as exc:
            return _result("", "%s: %s" % (type(exc).__name__, str(exc)[:300]), "transport")
        if p.returncode == 0:
            return _result(p.stdout, None, None)
        if p.returncode in (124, 137, 143):
            # `timeout -s KILL` surfaces as 137; 124/143 cover TERM variants.
            return _result(p.stdout, "execution exceeded %ds" % EXEC_TIMEOUT_S, "timeout")
        err = _stderr_tail(p.stderr)
        return _result(p.stdout, err or "exit %d" % p.returncode,
                       _docker_kind(p.stderr))

    def read_file(self, path):
        rel = _safe_relpath(path)
        p = self._exec(
            ["python", "-u", "-c",
             "import sys\nsys.stdout.write(open(sys.argv[1],encoding='utf-8').read())",
             rel],
            timeout=30,
        )
        if p.returncode != 0:
            raise RuntimeError("docker read %s failed: %s" % (rel, _stderr_tail(p.stderr)))
        return p.stdout

    def close(self):
        if self._cid is None:
            return
        p = subprocess.run(["docker", "rm", "-f", self._cid],
                           capture_output=True, text=True, timeout=60)
        if p.returncode != 0 and "No such container" not in (p.stderr or ""):
            raise RuntimeError("docker rm failed: %s" % _stderr_tail(p.stderr))
        self._cid = None

    def metadata(self):
        return {
            "backend": "docker",
            "image": DOCKER_IMAGE,
            "container": self._name,
            "network": "default bridge",
            "privileged": False,
            "mounts": False,
            "env": "none",
            "workdir": "/work",
            "exec": "python -u -c per action (no globals persistence)",
            "exec_timeout_s": EXEC_TIMEOUT_S,
        }


# ------------------------------------------------------------------------ e2b


class E2BBackend:
    """Fresh E2B sandbox per instance; persistent code-interpreter kernel."""

    language = "python"

    def __init__(self):
        self._sbx = None

    def setup(self, files):
        from e2b_code_interpreter import Sandbox
        self._sbx = Sandbox.create(timeout=600)
        r = self._sbx.run_code(
            "import os\nos.makedirs('/work', exist_ok=True)\nos.chdir('/work')",
            timeout=EXEC_TIMEOUT_S,
        )
        if r.error:
            raise RuntimeError("e2b init failed: %s: %s" % (r.error.name, r.error.value))
        for name, content in files.items():
            self._sbx.files.write("/work/" + _safe_relpath(name), content)

    def execute(self, code):
        from e2b import TimeoutException
        try:
            r = self._sbx.run_code(code, timeout=EXEC_TIMEOUT_S)
        except TimeoutException:
            return _result("", "execution exceeded %ds" % EXEC_TIMEOUT_S, "timeout")
        except Exception as exc:
            return _result("", "%s: %s" % (type(exc).__name__, str(exc)[:300]), "transport")
        out = "".join(r.logs.stdout)
        if r.error:
            return _result(out, "%s: %s" % (r.error.name, r.error.value), "runtime")
        return _result(out, None, None)

    def read_file(self, path):
        data = self._sbx.files.read("/work/" + _safe_relpath(path))
        if isinstance(data, (bytes, bytearray)):
            return bytes(data).decode("utf-8")
        return data

    def close(self):
        if self._sbx is None:
            return
        try:
            self._sbx.kill()
        except Exception as exc:
            if "not found" not in str(exc).lower() and "404" not in str(exc):
                raise
        self._sbx = None

    def metadata(self):
        return {
            "backend": "e2b",
            "template": "default",
            "sandbox_timeout_s": 600,
            "exec_timeout_s": EXEC_TIMEOUT_S,
            "workdir": "/work",
        }


# ------------------------------------------------------------------ calcside


class CalcsideBackend:
    """Dedicated local calcside API/worker; Starlark with gated capabilities."""

    language = "starlark"

    def __init__(self):
        self._iid = None

    def setup(self, files):
        inst, _ = cs.timed_create(CALCSIDE_SPEC)
        self._iid = inst["id"]
        for name, content in files.items():
            rel = _safe_relpath(name)
            code = "fs.write(%s, %s)" % (_starlark_str(rel), _starlark_str(content))
            status, body = cs.exec_(self._iid, code, timeout_ms=30000)
            if status != 200:
                raise RuntimeError("calcside setup write %s failed: http %s %s" % (
                    rel, status, str(body)[:300]))
            if isinstance(body, dict) and body.get("error"):
                raise RuntimeError("calcside setup write %s failed: %s" % (
                    rel, body["error"].get("message", body["error"])))

    def execute(self, code):
        try:
            status, body = cs.exec_(self._iid, code, timeout_ms=30000)
        except Exception as exc:
            return _result("", "%s: %s" % (type(exc).__name__, str(exc)[:300]), "transport")
        if status != 200 or not isinstance(body, dict):
            return _result("", "http %s: %s" % (status, str(body)[:300]), "transport")
        extra = {"exec_id": body.get("exec_id")}
        err = body.get("error")
        if not err:
            return _result(body.get("output") or "", None, None, **extra)
        etype = err.get("type") if isinstance(err, dict) else None
        kind = {"policy_denied": "policy_denied", "timeout": "timeout"}.get(
            etype, "runtime")
        message = err.get("message", "") if isinstance(err, dict) else str(err)
        return _result(body.get("output") or "", "%s: %s" % (etype, message),
                       kind, **extra)

    def read_file(self, path):
        rel = _safe_relpath(path)
        code = "print(json.encode(fs.read(%s)))" % _starlark_str(rel)
        status, body = cs.exec_(self._iid, code, timeout_ms=30000)
        if status != 200 or not isinstance(body, dict):
            raise RuntimeError("calcside read %s failed: http %s" % (rel, status))
        if body.get("error"):
            raise RuntimeError("calcside read %s failed: %s" % (
                rel, body["error"].get("message", body["error"])))
        if body.get("output_truncated"):
            raise RuntimeError("calcside read %s failed: output truncated" % rel)
        return json.loads(body.get("output") or "")

    def close(self):
        if self._iid is None:
            return
        status, _ = cs.delete(self._iid)
        if status >= 300 and status not in (404, 410):
            raise RuntimeError("calcside delete failed: http %s" % status)
        self._iid = None

    def metadata(self):
        return {
            "backend": "calcside",
            "base_url": cs.BASE,
            "language_runtime": "starlark",
            "policies": ["builtin.block_private_network"],
            "net_allow_hosts": ["api.github.com"],
            "capabilities": ["fs", "io", "net"],
            "ttl_seconds": 600,
            "exec_timeout_ms": 30000,
            "server_net_allow_cidrs": SERVER_NET_ALLOW_CIDRS,
            "audit": "exec_id returned; not polled inside execute",
        }


# ----------------------------------------------------------------- openshell


class OpenShellBackend:
    """Unique bench-v2-* sandbox on the local 'openshell' gateway (docker driver).

    The built-in default policy denies all egress. The incremental
    `policy update --add-allow` form cannot express the grant: it requires a
    pre-existing L7 endpoint, and `--add-endpoint` access presets expand to
    broader path sets (read-only -> GET/HEAD/OPTIONS on /**). Setup therefore
    uses `policy set` with the sandbox's current effective policy plus one
    network rule granting exactly GET api.github.com:443 /repos/** — verified
    live: in-scope path returns 200, out-of-scope paths return 403. No global
    policy is touched. `python -c` per action; the writable workspace is
    /sandbox — the directory the default filesystem policy designates via
    include_workdir (/work at the filesystem root is not creatable by the
    unprivileged sandbox user).
    """

    language = "python"
    WORKDIR = "/sandbox"

    def __init__(self):
        # gateway caps sandbox names at 19 chars
        self._name = "bench-v2-" + uuid.uuid4().hex[:8]
        self._created = False
        self._policy_sha256 = None
        self._effective_policy = None

    def _capture_policy(self):
        """Poll `policy get --full` until benchmark_github is effective, then
        store the sha256 of the effective policy document for metadata."""
        deadline = time.time() + 60
        doc = ""
        while time.time() < deadline:
            g = self._cli(self._gargs(
                "policy", "get", self._name, "--full", "-o", "json"), timeout=30)
            if g.returncode != 0:
                raise RuntimeError("openshell policy get failed: %s"
                                   % _stderr_tail(g.stderr))
            doc = g.stdout
            if "benchmark_github" in doc:
                break
            time.sleep(2)
        if "benchmark_github" not in doc:
            raise RuntimeError("openshell policy rule benchmark_github "
                               "not effective after update")
        self._policy_sha256 = hashlib.sha256(doc.encode("utf-8")).hexdigest()
        self._effective_policy = json.loads(doc)["policy"]

    def _apply_network_policy(self):
        """Grant exactly GET api.github.com:443 /repos/** via `policy set`.

        Fetches the sandbox's current effective policy, replaces
        network_policies with the single benchmark_github rule, and sets it
        back (JSON is valid YAML for --policy). Raises on any schema or CLI
        failure — the caller must not proceed with a broader fallback.
        """
        g = self._cli(self._gargs(
            "policy", "get", self._name, "--full", "-o", "json"), timeout=30)
        if g.returncode != 0:
            raise RuntimeError("openshell policy get failed: %s"
                               % _stderr_tail(g.stderr))
        try:
            policy = json.loads(g.stdout)["policy"]
            if not isinstance(policy, dict):
                raise TypeError("policy is %s" % type(policy).__name__)
        except (json.JSONDecodeError, KeyError, TypeError) as exc:
            raise RuntimeError("openshell policy get returned unexpected "
                               "document: %s" % exc)
        policy["network_policies"] = {
            "benchmark_github": {
                "name": "benchmark_github",
                "binaries": [{"path": "/**"}],
                "endpoints": [{
                    "host": "api.github.com",
                    "port": 443,
                    "protocol": "rest",
                    "enforcement": "enforce",
                    "rules": [{"allow": {"method": "GET", "path": "/repos/**"}}],
                }],
            }
        }
        fd, tmp = tempfile.mkstemp(prefix="openshell-policy-", suffix=".yaml")
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as f:
                json.dump(policy, f)
            p = self._cli(self._gargs(
                "policy", "set", self._name, "--policy", tmp, "--wait"),
                timeout=120)
        finally:
            try:
                os.unlink(tmp)
            except OSError:
                pass
        if p.returncode != 0:
            raise RuntimeError("openshell policy set failed: %s"
                               % _stderr_tail(p.stderr))

    def _cli(self, args, timeout=90, **kw):
        return subprocess.run(
            ["openshell"] + args, capture_output=True, text=True,
            timeout=timeout, **kw,
        )

    def _gargs(self, *args):
        return ["-g", OPENSHELL_GATEWAY] + list(args)

    def _exec_raw(self, args, timeout=45, workdir=None, **kw):
        flags = ["-n", self._name, "--timeout", str(EXEC_TIMEOUT_S),
                 "--no-login-shell"]
        if workdir is None:
            workdir = self.WORKDIR
        if workdir:
            flags += ["--workdir", workdir]
        return self._cli(
            self._gargs("sandbox", "exec") + flags + ["--"] + list(args),
            timeout=timeout + 15, **kw,
        )

    def setup(self, files):
        p = self._cli(self._gargs(
            "sandbox", "create", "--name", self._name, "--from", OPENSHELL_IMAGE,
            "--detach", "--", "sleep", "infinity"), timeout=120)
        if p.returncode != 0:
            raise RuntimeError("openshell create failed: %s" % _stderr_tail(p.stderr))
        self._created = True
        deadline = time.time() + 240
        phase = ""
        while time.time() < deadline:
            g = self._cli(self._gargs(
                "sandbox", "get", "-o", "json", self._name), timeout=30)
            if g.returncode == 0:
                try:
                    phase = json.loads(g.stdout).get("phase", "")
                except json.JSONDecodeError:
                    phase = ""
            if phase == "Ready":
                break
            if phase in ("Failed", "Error"):
                raise RuntimeError("openshell sandbox %s phase=%s" % (self._name, phase))
            time.sleep(2)
        if phase != "Ready":
            raise RuntimeError("openshell sandbox %s not Ready (phase=%r)" % (
                self._name, phase))
        self._apply_network_policy()
        self._capture_policy()
        p = self._exec_raw(["mkdir", "-p", self.WORKDIR], timeout=30, workdir=None)
        if p.returncode != 0:
            raise RuntimeError("openshell mkdir failed: %s" % _stderr_tail(p.stderr))
        for name, content in files.items():
            rel = _safe_relpath(name)
            code = ("import sys\nopen(%r+'/'+sys.argv[1],'w').write(%r)"
                    % (self.WORKDIR, content))
            p = self._exec_raw(["python", "-u", "-c", code, rel], timeout=30)
            if p.returncode != 0:
                raise RuntimeError("openshell setup write %s failed: %s" % (
                    rel, _stderr_tail(p.stderr)))

    def execute(self, code):
        wrapped = "import os\nos.chdir(%r)\n" % self.WORKDIR + code
        try:
            p = self._exec_raw(["python", "-u", "-c", wrapped])
        except subprocess.TimeoutExpired:
            return _result("", "openshell exec exceeded deadline", "timeout")
        except OSError as exc:
            return _result("", "%s: %s" % (type(exc).__name__, str(exc)[:300]), "transport")
        if p.returncode == 0:
            return _result(p.stdout, None, None)
        err = _stderr_tail(p.stderr) or "exit %d" % p.returncode
        return _result(p.stdout, err, _openshell_kind(p.stderr))

    def read_file(self, path):
        rel = self.WORKDIR + "/" + _safe_relpath(path)
        p = self._exec_raw(
            ["python", "-u", "-c",
             "import sys\nsys.stdout.write(open(sys.argv[1],encoding='utf-8').read())",
             rel],
            timeout=30,
        )
        if p.returncode != 0:
            raise RuntimeError("openshell read %s failed: %s" % (rel, _stderr_tail(p.stderr)))
        return p.stdout

    def close(self):
        if not self._created:
            return
        p = self._cli(self._gargs("sandbox", "delete", self._name), timeout=90)
        if p.returncode != 0 and not re.search(
                r"not found|no such|does not exist", p.stderr or "", re.I):
            raise RuntimeError("openshell delete failed: %s" % _stderr_tail(p.stderr))
        self._created = False

    def metadata(self):
        return {
            "backend": "openshell",
            "gateway": OPENSHELL_GATEWAY,
            "gateway_endpoint": "https://localhost:17670 (mTLS, local)",
            "driver": "docker",
            "image": OPENSHELL_IMAGE,
            "sandbox": self._name,
            "policy": "default fs/landlock + benchmark_github rule "
                      "(api.github.com:443 GET /repos/** only)",
            "policy_sha256": (self._policy_sha256 or "")[:16] or None,
            "effective_policy": self._effective_policy,
            "workdir": self.WORKDIR,
            "exec": "python -u -c per action with chdir(workdir)",
            "exec_timeout_s": EXEC_TIMEOUT_S,
        }


BACKENDS = {
    "calcside": CalcsideBackend,
    "python": PythonBackend,
    "docker": DockerBackend,
    "e2b": E2BBackend,
    "openshell": OpenShellBackend,
}
