#!/usr/bin/env python3
"""Deploy an isolated release-validation stack (kind or compose), wait for its
health endpoint, mint a short-lived API key over a browser session, and write
STATE_DIR/test.env plus STATE_DIR/connection.json for the release runner.
With `diagnostics`, collect sanitized, non-secret status for a (possibly
partial) deployment.

Usage:
  deploy-release.py kind|compose STATE_DIR
  deploy-release.py kind|compose STATE_DIR diagnostics OUTPUT_DIR

Environment:
  TEST_IMAGE_TAG        image tag for both stacks (default: test)
  TEST_KIND_CLUSTER     kind cluster name (default: calcside-release-test)
  TEST_KIND_PORT        host port for the kind port-forward (default: 18080)
  TEST_COMPOSE_PROJECT  compose project name (default: calcside-release-test)

STATE_DIR must be new; generated credentials live there privately (mode 600),
never on stdout or in process arguments. Deploys leave the stack and the
port-forward running; nothing here deletes clusters, projects, or data.
"""

import argparse
import base64
import http.cookiejar
import json
import os
import pathlib
import secrets
import shlex
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
NAMESPACE = "calcside"


def die(message):
    print(f"deploy-release: {message}", file=sys.stderr)
    sys.exit(1)


def need(tool):
    if shutil.which(tool) is None:
        die(f"required tool not found: {tool}")


def mask(value):
    if os.environ.get("GITHUB_ACTIONS") == "true" and value and len(value) >= 8:
        print(f"::add-mask::{value}")


def run(argv, env=None):
    result = subprocess.run(argv, env=env, check=False)
    if result.returncode != 0:
        die(f"command failed ({result.returncode}): {' '.join(str(a) for a in argv)}")
    return result


def wait_healthy(url, budget=120):
    deadline = time.monotonic() + budget
    while True:
        try:
            with urllib.request.urlopen(url, timeout=3) as response:
                if response.status == 200:
                    return
        except (urllib.error.URLError, OSError):
            pass
        if time.monotonic() >= deadline:
            die(f"timed out ({budget}s) waiting for {url}")
        time.sleep(2)


def port_free(port):
    sock = socket.socket()
    try:
        sock.bind(("127.0.0.1", port))
    except OSError:
        return False
    finally:
        sock.close()
    return True


def write_private(path, text):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    os.fchmod(fd, 0o600)  # cover preexisting files too, not only creation
    with os.fdopen(fd, "w") as handle:
        handle.write(text)


def write_json_private(path, obj):
    write_private(path, json.dumps(obj, indent=2) + "\n")


# One login attempt and one key creation; abort on any unexpected status or
# payload so auth problems surface instead of retrying against rate limits.
def issue_api_key(base_url, user, password):
    token = base64.b64encode(f"{user}:{password}".encode()).decode()
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))

    def call(request, want):
        try:
            response = opener.open(request, timeout=20)
        except urllib.error.HTTPError as error:
            die(f"unexpected status {error.code} from {request.full_url}")
        if response.status != want:
            die(f"unexpected status {response.status} from {request.full_url}")
        return response

    login = urllib.request.Request(
        f"{base_url}/auth/basic/login", data=b"", method="POST",
        headers={"X-Requested-With": "calcside",
                 "Authorization": f"Basic {token}"})
    call(login, 200)
    if not any(cookie.name == "cs_session" for cookie in jar):
        die("login did not return a cs_session cookie")

    body = json.dumps({"name": "release-validation",
                       "expires_in_seconds": 3600}).encode()
    create = urllib.request.Request(
        f"{base_url}/api/v1/keys", data=body, method="POST",
        headers={"X-Requested-With": "calcside",
                 "Content-Type": "application/json"})
    try:
        payload = json.loads(call(create, 201).read())
    except ValueError:
        die("key creation did not return JSON")
    secret = payload.get("secret")
    if not isinstance(secret, str) or not secret:
        die("key creation response missing .secret")
    return secret


def deploy_test_script():
    candidate = ROOT / "charts" / "deploy-test.py"
    if not candidate.exists():
        die("charts/deploy-test.py not found")
    return [sys.executable, str(candidate)]


def sanitize(text, values):
    for value in values:
        if isinstance(value, str) and len(value) >= 8:
            text = text.replace(value, "<redacted>")
    return text


def write_credentials(state, image_tag, creds):
    write_private(state / "secrets.env", "".join(
        f"{key}={value}\n" for key, value in [
            ("TEST_IMAGE_TAG", image_tag),
            ("TEST_PG_PASSWORD", creds["pg_password"]),
            ("TEST_ADMIN_PASSWORD", creds["admin_password"]),
            ("TEST_WORKER_KEY", creds["worker_key"]),
            ("TEST_VAULT_KEY", creds["vault_key"]),
        ]))
    write_json_private(state / "credentials.json", {
        "admin_username": "admin",
        "admin_password": creds["admin_password"],
        "pg_password": creds["pg_password"],
        "worker_key": creds["worker_key"],
        "vault_key": creds["vault_key"],
    })


def generate_credentials(state, image_tag):
    creds = {
        "pg_password": secrets.token_hex(24),
        "admin_password": secrets.token_hex(24),
        "worker_key": secrets.token_hex(32),
        "vault_key": base64.b64encode(secrets.token_bytes(32)).decode(),
    }
    # Mask before any subprocess runs so failure output can never echo them.
    for value in creds.values():
        mask(value)
    write_credentials(state, image_tag, creds)
    return creds


# Health gate, session login -> API key, then the public test.env and
# connection.json contracts.
def finish_deploy(state, backend, base_url, creds):
    wait_healthy(f"{base_url}/healthz")
    api_key = issue_api_key(base_url, "admin", creds["admin_password"])
    write_private(state / "test.env",
                  f"export CALCSIDE_BASE_URL={shlex.quote(base_url)}\n"
                  f"export CALCSIDE_API_KEY={shlex.quote(api_key)}\n")
    write_json_private(state / "connection.json",
                       {"base_url": base_url, "api_key": api_key})
    mask(api_key)
    print(f"deploy-release: {backend} stack ready at {base_url}")
    print(f"deploy-release: credentials, test.env and connection.json in {state}")


def deploy_kind(state, image_tag):
    for tool in ("kind", "kubectl", "helm"):
        need(tool)
    cluster = os.environ.get("TEST_KIND_CLUSTER", "calcside-release-test")
    port = int(os.environ.get("TEST_KIND_PORT", "18080"))
    base_url = f"http://127.0.0.1:{port}"
    clusters = subprocess.run(["kind", "get", "clusters"],
                              capture_output=True, text=True, check=False).stdout.split()
    if cluster in clusters:
        die(f"kind cluster already exists: {cluster} (refusing to reuse a dirty cluster)")
    if not port_free(port):
        die(f"port {port} is already in use (set TEST_KIND_PORT)")

    # Record target metadata before creating anything so partial-failure
    # diagnostics aim at the right cluster.
    write_private(state / "backend.env",
                  f"BACKEND=kind\nBASE_URL={base_url}\nKIND_CLUSTER={cluster}\n"
                  f"KIND_NAMESPACE={NAMESPACE}\nKIND_PORT={port}\n")

    kubeconfig = state / "kubeconfig"
    env = dict(os.environ, KUBECONFIG=str(kubeconfig))
    creds = generate_credentials(state, image_tag)
    run(["kind", "create", "cluster", "--name", cluster,
         "--kubeconfig", str(kubeconfig), "--wait", "2m"], env=env)
    run(["kind", "load", "docker-image",
         f"ghcr.io/wylswz/calcside:{image_tag}",
         f"ghcr.io/wylswz/calcside-migrate:{image_tag}",
         "--name", cluster], env=env)

    deploy_env = dict(
        env,
        TEST_IMAGE_TAG=image_tag,
        TEST_SERVICE_TYPE="ClusterIP",
        TEST_PERSISTENCE="false",
        TEST_CONSOLE_ORIGIN=base_url,
        TEST_ADMIN_USERNAME="admin",
        TEST_ADMIN_PASSWORD=creds["admin_password"],
        TEST_PG_PASSWORD=creds["pg_password"],
        TEST_WORKER_KEY=creds["worker_key"],
        TEST_VAULT_KEY=creds["vault_key"],
    )
    with open(state / "deploy.log", "wb") as log:
        result = subprocess.run(deploy_test_script(), env=deploy_env,
                                stdout=log, stderr=subprocess.STDOUT, check=False)
    if result.returncode != 0:
        tail = (state / "deploy.log").read_text(errors="ignore").splitlines()[-20:]
        print(sanitize("\n".join(tail), creds.values()), file=sys.stderr)
        die(f"charts/deploy-test failed; see {state}/deploy.log")

    log = open(state / "port-forward.log", "wb")
    proc = subprocess.Popen(
        ["kubectl", "port-forward", "-n", NAMESPACE, "--address", "127.0.0.1",
         "svc/calcside", f"{port}:8080"],
        env=env, stdin=subprocess.DEVNULL, stdout=log,
        stderr=subprocess.STDOUT, start_new_session=True)
    log.close()
    (state / "port-forward.pid").write_text(f"{proc.pid}\n")

    finish_deploy(state, "kind", base_url, creds)


def deploy_compose(state, image_tag):
    need("docker")
    project = os.environ.get("TEST_COMPOSE_PROJECT", "calcside-release-test")
    compose_file = ROOT / "docker" / "compose.test.yml"
    if not compose_file.exists():
        die(f"missing compose file: {compose_file}")
    # Fail closed on unexpected docker errors and reject any leftover project
    # artifacts — compose ls omits stopped containers and volumes.
    label = f"com.docker.compose.project={project}"
    listing = subprocess.run(["docker", "compose", "ls", "--format", "json"],
                             capture_output=True, text=True, check=False)
    if listing.returncode != 0:
        die(f"docker compose ls failed: {listing.stderr.strip()}")
    try:
        projects = {p.get("Name") for p in json.loads(listing.stdout)}
    except ValueError:
        die("could not parse docker compose ls output")
    containers = subprocess.run(["docker", "ps", "-aq", "--filter", f"label={label}"],
                                capture_output=True, text=True, check=False)
    volumes = subprocess.run(["docker", "volume", "ls", "-q", "--filter", f"label={label}"],
                             capture_output=True, text=True, check=False)
    if containers.returncode != 0 or volumes.returncode != 0:
        die("docker listing failed; cannot verify project isolation")
    leftovers = containers.stdout.split() + volumes.stdout.split()
    if project in projects or leftovers:
        die(f"compose project artifacts already exist: {project} "
            "(refusing contaminated data)")

    # Record target metadata before creating anything so partial-failure
    # diagnostics aim at the right project.
    write_private(state / "backend.env",
                  f"BACKEND=compose\nBASE_URL=\nCOMPOSE_PROJECT={project}\n"
                  f"COMPOSE_FILE={compose_file}\n")
    creds = generate_credentials(state, image_tag)
    compose = ["docker", "compose", "--env-file", str(state / "secrets.env"),
               "-p", project, "-f", str(compose_file)]
    run(compose + ["up", "-d", "--wait", "--wait-timeout", "300"])
    mapping = subprocess.run(compose + ["port", "api", "8080"],
                             capture_output=True, text=True, check=False).stdout.split()
    if not mapping or ":" not in mapping[0]:
        die(f"could not resolve api port (got: {' '.join(mapping) or 'nothing'})")
    base_url = f"http://127.0.0.1:{mapping[0].rsplit(':', 1)[1]}"

    write_private(state / "backend.env",
                  f"BACKEND=compose\nBASE_URL={base_url}\nCOMPOSE_PROJECT={project}\n"
                  f"COMPOSE_FILE={compose_file}\n")
    finish_deploy(state, "compose", base_url, creds)


def main():
    parser = argparse.ArgumentParser(
        prog="deploy-release.py",
        description="Deploy an isolated kind or compose release-validation "
                    "stack and mint a session API key, or collect sanitized "
                    "diagnostics for an existing state directory.",
        epilog="STATE_DIR must be new for deploys. diagnostics writes "
               "non-secret status/logs to OUTPUT_DIR.")
    parser.add_argument("backend", choices=["kind", "compose"])
    parser.add_argument("state_dir", type=pathlib.Path, metavar="STATE_DIR")
    parser.add_argument("mode", nargs="?", default=None)
    parser.add_argument("output_dir", nargs="?", type=pathlib.Path, default=None,
                        metavar="OUTPUT_DIR")
    args = parser.parse_args()
    if args.mode is not None and args.mode != "diagnostics":
        parser.error("third argument must be 'diagnostics'")
    if args.mode == "diagnostics" and args.output_dir is None:
        parser.error("diagnostics requires OUTPUT_DIR")

    os.umask(0o077)
    image_tag = os.environ.get("TEST_IMAGE_TAG", "test")

    if args.mode == "diagnostics":
        if not args.state_dir.is_dir():
            print(f"deploy-release: warning: state directory missing: {args.state_dir}",
                  file=sys.stderr)
        sys.path.insert(0, str(ROOT / "benchmark"))
        sys.dont_write_bytecode = True  # keep the repo free of __pycache__
        import deploy_release_diag
        args.output_dir.mkdir(parents=True, exist_ok=True)
        deploy_release_diag.collect(args.backend, args.state_dir, args.output_dir.resolve())
        print(f"deploy-release: diagnostics written to {args.output_dir}")
        return

    if args.state_dir.exists():
        die(f"state directory already exists: {args.state_dir}")
    args.state_dir.mkdir(parents=True)
    state = args.state_dir.resolve()
    if args.backend == "kind":
        deploy_kind(state, image_tag)
    else:
        deploy_compose(state, image_tag)


if __name__ == "__main__":
    main()
