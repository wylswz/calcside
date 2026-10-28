"""Run a disposable API and process-isolated worker for SDK tests."""

from __future__ import annotations

import base64
import os
import secrets
import socket
import subprocess
import time
import urllib.request
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).resolve().parents[3]
SECRET_KEY = base64.b64encode(bytes(range(32))).decode()


def _free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


@pytest.fixture(scope="session")
def server(tmp_path_factory):
    tmp = tmp_path_factory.mktemp("calcside-server")
    binary = tmp / "calcside"
    worker_binary = tmp / "calcside-worker"
    env = {
        key: value
        for key, value in os.environ.items()
        if not key.startswith(("CALCSIDE_", "ATLAS_"))
    }
    for target, package in [
        (binary, "./cmd/calcside"),
        (worker_binary, "./cmd/calcside-worker"),
    ]:
        subprocess.run(
            ["go", "build", "-o", str(target), package],
            cwd=REPO_ROOT,
            env=env,
            check=True,
        )
    subprocess.run(
        ["atlas", "migrate", "apply", "--env", "sqlite"],
        cwd=REPO_ROOT,
        env={**env, "ATLAS_DB_URL": f"sqlite://{tmp / 't.db'}"},
        check=True,
    )
    port = _free_port()
    worker_port = _free_port()
    while worker_port == port:
        worker_port = _free_port()
    shared_key = secrets.token_hex(32)
    url = f"http://127.0.0.1:{port}"
    processes: list[subprocess.Popen] = []
    try:
        processes.append(
            subprocess.Popen(
                [
                    str(binary), "serve", "--dev",
                    "--addr", f"127.0.0.1:{port}",
                    "--dsn", str(tmp / "t.db"),
                    "--secret-key", SECRET_KEY, "--secrets-allow-http",
                    "--workers", f"sdk-worker=127.0.0.1:{worker_port}",
                    "--worker-key", shared_key,
                ],
                cwd=REPO_ROOT,
                env=env,
            )
        )
        processes.append(
            subprocess.Popen(
                [
                    str(worker_binary), "serve",
                    "--listen", f"127.0.0.1:{worker_port}",
                    "--api-addr", url, "--shared-key", shared_key,
                    "--node-id", "sdk-worker",
                    "--ext-cache-dir", str(tmp / "worker-cache"),
                    "--instance-memory-max", "0", "--secrets-allow-http",
                ],
                cwd=REPO_ROOT,
                env=env,
            )
        )
        for endpoint in [url, f"http://127.0.0.1:{worker_port}"]:
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                if any(proc.poll() is not None for proc in processes):
                    raise RuntimeError("API or worker exited during startup")
                try:
                    with urllib.request.urlopen(endpoint + "/healthz", timeout=1):
                        break
                except OSError:
                    time.sleep(0.2)
            else:
                raise RuntimeError("API or worker did not become healthy")
        yield url
    finally:
        for proc in processes:
            if proc.poll() is None:
                proc.terminate()
        for proc in processes:
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait(timeout=5)
