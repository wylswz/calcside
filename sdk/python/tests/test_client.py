"""Client tests against a real dev-mode server."""

from __future__ import annotations

from pathlib import Path

import pytest
import yaml

from calcside import CalcsideError, Client

OPENAPI = Path(__file__).resolve().parents[3] / "api" / "openapi.yaml"


def test_end_to_end(server):
    c = Client(base_url=server)
    inst = c.create_instance({"capabilities": {"fs": {}}, "ttl_seconds": 600})
    iid = inst["id"]
    try:
        r = c.exec(iid, 'fs.write("a.txt", "hi")')
        assert r.error is None
        r = c.exec(iid, 'print(fs.read("a.txt"))\nprint(6*7)')
        assert r.output == "hi\n42\n"
        assert r.steps > 0 and r.duration_ms >= 0

        listing = c.files(iid, "/work")
        assert any(e["path"].endswith("a.txt") for e in listing["entries"])
        content = c.files(iid, "/work/a.txt")
        assert content["content"] == "hi"

        caps = c.capabilities()
        assert {cap["name"] for cap in caps} == {"fs", "net", "io"}

        got = c.get_instance(iid)
        assert got["status"] == "running"
    finally:
        c.delete_instance(iid)
    # the API either 404s or reports the tombstoned status
    try:
        assert c.get_instance(iid)["status"] == "deleted"
    except CalcsideError as e:
        assert e.status == 404


def test_error_envelope(server):
    c = Client(base_url=server)
    with pytest.raises(CalcsideError) as ei:
        c.exec("ins_nonexistent", "print(1)")
    assert ei.value.status == 404
    assert ei.value.code == "not_found"


def test_exec_error(server):
    c = Client(base_url=server)
    inst = c.create_instance({"capabilities": {"io": {}}, "ttl_seconds": 300})
    try:
        r = c.exec(inst["id"], "def broken(:")
        assert r.error is not None
        assert r.error.type == "syntax"
    finally:
        c.delete_instance(inst["id"])


def test_client_paths_in_openapi(server):
    """Every (method, path) the client uses must exist in the contract."""
    spec = yaml.safe_load(OPENAPI.read_text())
    paths = spec["paths"]
    used = [
        ("get", "/api/v1/capabilities"),
        ("post", "/api/v1/instances"),
        ("get", "/api/v1/instances/{id}"),
        ("delete", "/api/v1/instances/{id}"),
        ("post", "/api/v1/instances/{id}/keepalive"),
        ("post", "/api/v1/instances/{id}/exec"),
        ("get", "/api/v1/instances/{id}/files"),
    ]
    for method, path in used:
        assert path in paths, f"missing path {path}"
        assert method in paths[path], f"missing {method.upper()} {path}"
