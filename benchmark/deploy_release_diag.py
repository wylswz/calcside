#!/usr/bin/env python3
"""Sanitized diagnostics for a (possibly partial) release-validation deploy.

Imported by deploy-release.py; collects status, events, container image IDs,
and logs with every known credential redacted. Never writes Secret objects,
helm values, docker inspect environment, compose config, test.env, or
secrets.env content.
"""

import base64
import datetime
import json
import os
import pathlib
import platform
import shlex
import shutil
import subprocess
import sys
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
NAMESPACE = "calcside"


def _run(argv, timeout=20, stderr=True):
    try:
        result = subprocess.run(argv, capture_output=True, timeout=timeout, check=False)
        return result.stdout + (result.stderr if stderr else b"")
    except (OSError, subprocess.TimeoutExpired) as error:
        return f"<unavailable: {error}>\n".encode()


def _env_file_values(path):
    values = []
    try:
        for line in pathlib.Path(path).read_text().splitlines():
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            if line.startswith("export "):
                line = line[len("export "):]
            if "=" not in line:
                continue
            try:
                values.append(shlex.split(line, posix=True)[0].split("=", 1)[1])
            except (ValueError, IndexError):
                values.append(line.split("=", 1)[1])
    except OSError:
        pass
    return values


def _redact_terms(backend, state):
    terms = []

    def add(value):
        if isinstance(value, str) and len(value) >= 8:
            terms.append(value.encode())

    creds = state / "credentials.json"
    if creds.exists():
        try:
            for value in json.loads(creds.read_text()).values():
                add(value)
        except (OSError, ValueError):
            pass
    connection = state / "connection.json"
    if connection.exists():
        try:
            add(json.loads(connection.read_text()).get("api_key", ""))
        except (OSError, ValueError):
            pass
    for name in ("secrets.env", "test.env"):
        for value in _env_file_values(state / name):
            if not value.startswith("http"):
                add(value)
    kubeconfig = state / "kubeconfig"
    if backend == "kind" and kubeconfig.exists():
        raw = _run(["kubectl", "--kubeconfig", str(kubeconfig),
                    "--request-timeout=15s", "get", "secret", "-n", NAMESPACE,
                    "calcside-secrets", "postgres-test", "-o", "json"], stderr=False)
        try:
            for item in json.loads(raw).get("items", []):
                for encoded in item.get("data", {}).values():
                    try:
                        add(base64.b64decode(encoded).decode("utf-8", "ignore"))
                    except Exception:
                        pass
        except ValueError:
            pass
    return terms


def _redact(data, terms):
    for term in terms:
        data = data.replace(term, b"<redacted>")
    return data


def _write(out, name, data, terms):
    if isinstance(data, str):
        data = data.encode()
    (out / name).write_bytes(_redact(data, terms))


def _env_var_file(path, key):
    path = pathlib.Path(path)
    if not path.exists():
        return ""
    for value_line in path.read_text().splitlines():
        if value_line.startswith(f"{key}=") or value_line.startswith(f"export {key}="):
            return value_line.split("=", 1)[1]
    return ""


def _base_url(state):
    connection = state / "connection.json"
    if connection.exists():
        try:
            return json.loads(connection.read_text()).get("base_url", "")
        except (OSError, ValueError):
            pass
    return _env_var_file(state / "backend.env", "BASE_URL")


def _environment(backend, image_tag):
    lines = [
        f"backend: {backend}",
        f"collected_utc: {datetime.datetime.now(datetime.timezone.utc):%Y-%m-%dT%H:%M:%SZ}",
        f"uname: {platform.platform()}",
        f"git_revision: {_run(['git', '-C', str(ROOT), 'rev-parse', 'HEAD']).decode(errors='ignore').strip()}",
        f"image_tag: {image_tag}",
    ]
    version_cmds = {
        "docker": ["docker", "version", "--format",
                   "client {{.Client.Version}} server {{.Server.Version}}"],
        "kubectl": ["kubectl", "version", "--client"],
        "kind": ["kind", "--version"],
        "helm": ["helm", "version", "--short"],
        "k6": ["k6", "version"],
        "python3": ["python3", "--version"],
    }
    for tool, argv in version_cmds.items():
        if shutil.which(tool) is None:
            lines.append(f"{tool}: missing")
            continue
        output = _run(argv).decode(errors="ignore").strip().splitlines()
        lines.append(f"{tool}: {output[0] if output else 'unknown'}")
    return "\n".join(lines) + "\n"


def _collect_health(state, out, terms):
    base_url = _base_url(state)
    if not base_url:
        _write(out, "health.txt", "no recorded base_url (partial deploy)\n", terms)
        return
    try:
        with urllib.request.urlopen(f"{base_url}/healthz", timeout=5) as response:
            status = f"{response.status}"
    except urllib.error.HTTPError as error:
        status = f"{error.code}"
    except (urllib.error.URLError, OSError) as error:
        status = f"unreachable ({error})"
    _write(out, "health.txt", f"base_url: {base_url}\nhealthz: {status}\n", terms)


def _kind(state, out, terms):
    kubeconfig = state / "kubeconfig"
    if not kubeconfig.exists():
        _write(out, "cluster.txt", "no kubeconfig in state directory (partial deploy)\n", terms)
        return
    kube = ["kubectl", "--kubeconfig", str(kubeconfig), "--request-timeout=15s"]
    context = _run(kube + ["config", "current-context"]).decode(errors="ignore").strip()
    nodes = _run(kube + ["get", "nodes", "-o", "wide"]).decode(errors="ignore")
    _write(out, "cluster.txt", f"## context: {context}\n\n{nodes}", terms)
    _write(out, "pods.txt", _run(kube + ["get", "pods", "-n", NAMESPACE, "-o", "wide"]), terms)
    _write(out, "resources.txt",
           _run(kube + ["get", "deploy,statefulset,svc,pvc", "-n", NAMESPACE]), terms)
    _write(out, "events.txt",
           _run(kube + ["get", "events", "-n", NAMESPACE, "--sort-by=.lastTimestamp"]), terms)
    try:
        pods = json.loads(_run(kube + ["get", "pods", "-n", NAMESPACE, "-o", "json"],
                               stderr=False))
    except ValueError:
        pods = {}
    images = []
    for pod in pods.get("items", []):
        name = pod["metadata"]["name"]
        status = pod.get("status", {})
        for cs in status.get("initContainerStatuses", []) + status.get("containerStatuses", []):
            images.append(f"{name}/{cs.get('name')} image={cs.get('image')} id={cs.get('imageID')}")
    _write(out, "images.txt", "\n".join(images) + "\n", terms)
    _write(out, "helm.txt",
           _run(["helm", "--kubeconfig", str(kubeconfig), "list", "-n", NAMESPACE]), terms)
    names = _run(kube + ["get", "pods", "-n", NAMESPACE, "-o", "name"])
    for line in names.decode(errors="ignore").split():
        pod = line.removeprefix("pod/")
        containers = _run(kube + ["get", "pod", "-n", NAMESPACE, pod, "-o",
                                  "jsonpath={.spec.initContainers[*].name} {.spec.containers[*].name}"])
        for container in containers.decode(errors="ignore").split():
            _write(out, f"logs-{pod}-{container}.txt",
                   _run(kube + ["logs", "-n", NAMESPACE, pod, "-c", container, "--tail=400"]), terms)
            _write(out, f"logs-{pod}-{container}-previous.txt",
                   _run(kube + ["logs", "-n", NAMESPACE, pod, "-c", container,
                                "--previous", "--tail=200"]), terms)
    for extra in ("deploy.log", "port-forward.log"):
        source = state / extra
        if source.exists():
            _write(out, extra, source.read_bytes(), terms)


def _compose(state, out, terms):
    project = os.environ.get("TEST_COMPOSE_PROJECT", "calcside-release-test")
    backend_env = state / "backend.env"
    if backend_env.exists():
        project = _env_var_file(backend_env, "COMPOSE_PROJECT") or project
    compose_file = _env_var_file(backend_env, "COMPOSE_FILE") or str(ROOT / "docker/compose.test.yml")
    compose = ["docker", "compose"]
    if (state / "secrets.env").exists():
        compose += ["--env-file", str(state / "secrets.env")]
    compose += ["-p", project, "-f", compose_file]
    ps = _run(compose + ["ps", "-a"], timeout=30).decode(errors="ignore")
    port = _run(compose + ["port", "api", "8080"], timeout=30).decode(errors="ignore")
    _write(out, "compose-ps.txt", f"## docker compose ps\n{ps}\n## api port\n{port}", terms)
    _write(out, "compose-logs.txt", _run(compose + ["logs", "--tail=400"], timeout=60), terms)
    ids = _run(["docker", "ps", "-aq", "--filter",
                f"label=com.docker.compose.project={project}"])
    images = []
    for cid in ids.decode(errors="ignore").split():
        line = _run(["docker", "inspect", "--format",
                     "{{.Name}} image={{.Config.Image}} id={{.Image}}", cid])
        images.append(line.decode(errors="ignore").strip())
    _write(out, "images.txt", "\n".join(images) + "\n", terms)


def collect(backend, state, out):
    state = pathlib.Path(state)
    out = pathlib.Path(out)
    out.mkdir(parents=True, exist_ok=True)
    terms = _redact_terms(backend, state)
    image_tag = os.environ.get("TEST_IMAGE_TAG", "test")
    _write(out, "environment.txt", _environment(backend, image_tag), terms)
    _collect_health(state, out, terms)
    if backend == "kind":
        _kind(state, out, terms)
    else:
        _compose(state, out, terms)


if __name__ == "__main__":
    if len(sys.argv) != 4 or sys.argv[1] not in ("kind", "compose"):
        sys.exit(f"usage: {sys.argv[0]} kind|compose STATE_DIR OUTPUT_DIR")
    collect(sys.argv[1], pathlib.Path(sys.argv[2]), pathlib.Path(sys.argv[3]))
