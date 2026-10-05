"""Client tests against a real dev-mode server."""

from __future__ import annotations

from io import BytesIO
from pathlib import Path
from zipfile import ZipFile

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
        assert {cap["name"] for cap in caps} == {"fs", "net", "io", "ext"}

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


def test_prompt(server):
    c = Client(base_url=server)
    inst = c.create_instance({"capabilities": {"fs": {}, "io": {}}, "ttl_seconds": 600})
    try:
        r = c.prompt(inst["id"])
        assert r["instance_id"] == inst["id"]
        assert "calcside sandbox" in r["prompt"]
        assert r["tools"]["exec"] == "calcside_exec"
        assert set(r["capabilities"]) == {"fs", "io"}
        r2 = c.prompt(inst["id"], tool_prefix="sb_")
        assert r2["tools"]["exec"] == "sb_exec"
        assert "sb_exec" in r2["prompt"]
    finally:
        c.delete_instance(inst["id"])
    with pytest.raises(CalcsideError):
        c.prompt(inst["id"])


def test_exec_error(server):
    c = Client(base_url=server)
    inst = c.create_instance({"capabilities": {"io": {}}, "ttl_seconds": 300})
    try:
        r = c.exec(inst["id"], "def broken(:")
        assert r.error is not None
        assert r.error.type == "syntax"
    finally:
        c.delete_instance(inst["id"])


def test_secrets(server):
    c = Client(base_url=server)
    sec = c.create_secret(
        "SDK_TEST_TOKEN", "hunter2", allowed_domains=["api.example.com"]
    )
    try:
        assert sec["name"] == "SDK_TEST_TOKEN"
        assert "value" not in sec  # write-only
        assert sec["allowed_domains"] == ["api.example.com"]
        assert any(s["id"] == sec["id"] for s in c.list_secrets())

        with pytest.raises(CalcsideError) as ei:
            c.create_secret("SDK_TEST_TOKEN", "x")
        assert ei.value.status == 409

        upd = c.update_secret(sec["id"], allowed_domains=["*.example.com"])
        assert upd["allowed_domains"] == ["*.example.com"]

        inst = c.create_instance(
            {
                "capabilities": {
                    "net": {"allow_hosts": ["api.example.com"], "methods": ["GET"]}
                },
                "secrets": {"SDK_TEST_TOKEN": {"ref": "SDK_TEST_TOKEN"}},
                "ttl_seconds": 300,
            }
        )
        try:
            assert inst["spec"]["secrets"]["SDK_TEST_TOKEN"]["source"] == "vault"
        finally:
            c.delete_instance(inst["id"])
    finally:
        c.delete_secret(sec["id"])
    assert all(s["id"] != sec["id"] for s in c.list_secrets())


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
        ("get", "/api/v1/instances/{id}/prompt"),
        ("get", "/api/v1/secrets"),
        ("post", "/api/v1/secrets"),
        ("put", "/api/v1/secrets/{id}"),
        ("delete", "/api/v1/secrets/{id}"),
    ]
    for method, path in used:
        assert path in paths, f"missing path {path}"
        assert method in paths[path], f"missing {method.upper()} {path}"


@pytest.mark.parametrize("selection", [None, [], ["builtin.block_private_network"]])
def test_network_policy_selection(server, selection):
    with Client(base_url=server) as c:
        spec = {"capabilities": {"net": {}}}
        if selection is not None:
            spec["policies"] = selection
        inst = c.create_instance(spec)
        try:
            expected = (
                ["builtin.block_private_network"] if selection is None else selection
            )
            assert inst["spec"]["policies"] == expected
            r = c.exec(inst["id"], f'print(net.get("{server}/healthz")["status"])')
            if expected:
                assert r.error is not None
                assert r.error.type == "policy_denied"
                assert "builtin.block_private_network" in r.error.message
            else:
                assert r.error is None
                assert r.output == "200\n"
        finally:
            c.delete_instance(inst["id"])


def test_artifacts(server):
    with Client(base_url=server) as c:
        instance = c.create_instance({"capabilities": {"fs": {}}})
        iid = instance["id"]
        try:
            result = c.exec(
                iid, 'fs.write("reports/结果.csv", "id,value\\r\\nA,001\\r\\n")'
            )
            assert result.error is None
            preview = c.preview_artifact(iid, "reports/结果.csv")
            assert preview["kind"] == "csv"
            assert preview["csv_rows"][1] == ["A", "001"]
            download = c.download_file(iid, "reports/结果.csv")
            assert download.filename == "结果.csv"
            assert download.content == b"id,value\r\nA,001\r\n"
            assert download.file_count == 1
            assert download.redacted is False
            archive = c.export_files(iid, ["reports"])
            with ZipFile(BytesIO(archive.content)) as zipped:
                assert zipped.read("reports/结果.csv") == download.content
            with pytest.raises(CalcsideError):
                c.export_files(iid, ["reports", "missing.txt"])
        finally:
            c.delete_instance(iid)
        with pytest.raises(CalcsideError):
            c.download_file(iid, "reports/结果.csv")


def test_async_artifacts(server):
    import asyncio

    from calcside import AsyncClient

    async def scenario():
        async with AsyncClient(base_url=server) as c:
            instance = await c.create_instance({"capabilities": {"fs": {}}})
            iid = instance["id"]
            try:
                result = await c.exec(iid, 'fs.write("report.txt", "hello")')
                assert result.error is None
                assert (await c.preview_artifact(iid, "report.txt"))[
                    "source"
                ] == "hello"
                assert (await c.download_file(iid, "report.txt")).content == b"hello"
                archive = await c.export_files(iid, ["report.txt"])
                with ZipFile(BytesIO(archive.content)) as zipped:
                    assert zipped.read("report.txt") == b"hello"
            finally:
                await c.delete_instance(iid)

    asyncio.run(scenario())
