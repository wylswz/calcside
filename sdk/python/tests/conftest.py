"""Session fixture: build the Go server once and run it in dev mode."""

from __future__ import annotations

import base64
import os
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
    subprocess.run(
        ["go", "build", "-o", str(binary), "./cmd/calcside"],
        cwd=REPO_ROOT,
        check=True,
    )
    subprocess.run(
        ["atlas", "migrate", "apply", "--env", "sqlite"],
        cwd=REPO_ROOT,
        env={**os.environ, "ATLAS_DB_URL": f"sqlite://{tmp / 't.db'}"},
        check=True,
    )
    port = _free_port()
    proc = subprocess.Popen(
        [
            str(binary),
            "serve",
            "--dev",
            "--addr",
            f"127.0.0.1:{port}",
            "--dsn",
            str(tmp / "t.db"),
            "--secret-key",
            SECRET_KEY,
            "--secrets-allow-http",
        ],
        cwd=REPO_ROOT,
    )
    url = f"http://127.0.0.1:{port}"
    deadline = time.time() + 30
    while time.time() < deadline:
        if proc.poll() is not None:
            raise RuntimeError(f"calcside exited with {proc.returncode}")
        try:
            urllib.request.urlopen(url + "/healthz", timeout=1)
            break
        except OSError:
            time.sleep(0.2)
    else:
        proc.kill()
        raise RuntimeError("calcside did not become healthy")
    yield url
    proc.terminate()
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()
