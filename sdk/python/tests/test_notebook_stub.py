"""Notebook client tests against a stub HTTP transport — no server.

Covers sync+async request shapes, the typed error subclasses
(instance_restored / instance_expired, never retried), and the
ExecResult notebook fields.
"""

from __future__ import annotations

import asyncio
import json

import httpx
import pytest

from calcside import (
    AsyncClient,
    CalcsideError,
    Client,
    ExecSection,
    InstanceExpired,
    InstanceRestored,
)


def _envelope(status: int, body: dict) -> httpx.Response:
    return httpx.Response(status, json=body)


def _err(status: int, code: str, message: str) -> httpx.Response:
    return _envelope(status, {"error": {"code": code, "message": message}})


def _router(calls: list[tuple[str, str, dict]]) -> httpx.MockTransport:
    def handler(request: httpx.Request) -> httpx.Response:
        body = json.loads(request.content) if request.content else {}
        calls.append((request.method, request.url.path, body))
        path = request.url.path
        if path.endswith("/notebook") and request.method == "GET":
            return _envelope(
                200,
                {
                    "notebook": {
                        "instance_id": "ins_1",
                        "status": "running",
                        "incarnation": 2,
                        "restored_at": "2026-01-01T00:00:00Z",
                        "restored_empty": True,
                        "revision": 7,
                        "drift": 0,
                        "sections": [
                            {
                                "name": "setup",
                                "position": 0,
                                "description": "init",
                                "state": "not_run",
                                "rerun": "safe",
                                "defines": ["x"],
                                "uses": [],
                                "effects": [],
                                "depends_on": [],
                                "last_incarnation": 1,
                                "once_ran": False,
                            }
                        ],
                        "outline": "outline text",
                        "next": ["not_run: setup"],
                    }
                },
            )
        if "/sections/" in path:
            if request.method == "GET":
                return _envelope(
                    200,
                    {
                        "section": {
                            "section": {"name": "setup", "state": "fresh"},
                            "code": "x = 1",
                        }
                    },
                )
            if request.method == "PUT":
                return _envelope(
                    200,
                    {
                        "section": {"name": "setup", "state": "not_run"},
                        "revision": 8,
                    },
                )
            if request.method == "DELETE":
                return _envelope(200, {"affected": ["b"], "revision": 9})
        if path.endswith("/notebook/run"):
            return _envelope(
                200,
                {
                    "report": {
                        "instance_id": "ins_1",
                        "incarnation": 2,
                        "revision": 8,
                        "drift": 0,
                        "results": [
                            {"name": "setup", "status": "ok", "exec_id": "ex_1"}
                        ],
                        "warnings": ["setup: reads y"],
                    }
                },
            )
        if path.endswith("/exec"):
            if body.get("section"):
                return _envelope(
                    200,
                    {
                        "exec_id": "ex_2",
                        "output": "42\n",
                        "error": None,
                        "duration_ms": 3,
                        "steps": 5,
                        "notebook_revision": 8,
                        "drift": 0,
                        "section": "setup",
                        "section_state": "fresh",
                    },
                )
            return _err(
                409,
                "instance_restored",
                "instance was restored at 2026-01-01T00:00:00Z",
            )
        return _err(404, "not_found", f"no route {request.method} {path}")

    return httpx.MockTransport(handler)


def test_sync_notebook_shapes() -> None:
    calls: list[tuple[str, str, dict]] = []
    with Client(base_url="http://x", transport=_router(calls)) as c:
        nb = c.notebook("ins_1")
        assert nb["revision"] == 7
        assert nb["restored_empty"] is True
        assert nb["sections"][0]["last_incarnation"] == 1

        sec = c.read_section("ins_1", "setup", include_output=True)
        assert sec["code"] == "x = 1"
        assert "/sections/setup" in calls[-1][1]

        saved = c.save_section(
            "ins_1",
            "setup",
            revision=7,
            code="x = 2",
            create_only=True,
        )
        assert saved["revision"] == 8
        put = calls[-1][2]
        assert put["create_only"] is True and put["code"] == "x = 2"

        deleted = c.delete_section("ins_1", "setup", 8)
        assert deleted["affected"] == ["b"]
        assert calls[-1][1].endswith("/sections/setup")

        report = c.run_sections("ins_1", ["setup", "b"], revision=8)
        assert report["results"][0]["status"] == "ok"
        assert report["warnings"] == ["setup: reads y"]
        run_body = calls[-1][2]
        assert run_body["sections"] == ["setup", "b"]
        assert run_body["revision"] == 8


def test_sync_exec_section_and_errors() -> None:
    calls: list[tuple[str, str, dict]] = []
    with Client(base_url="http://x", transport=_router(calls)) as c:
        section: ExecSection = {
            "name": "setup",
            "description": "init",
            "rerun": "safe",
        }
        r = c.exec("ins_1", "x = 42\nprint(x)", section=section)
        assert r.output == "42\n"
        assert r.section == "setup"
        assert r.section_state == "fresh"
        assert r.notebook_revision == 8
        body = calls[-1][2]
        assert body["section"]["name"] == "setup"
        assert body["section"]["rerun"] == "safe"

        # scratch exec hitting instance_restored raises the typed
        # subclass exactly once — no transparent retry.
        with pytest.raises(InstanceRestored) as ei:
            c.exec("ins_1", "print(1)")
        assert ei.value.code == "instance_restored"
        assert ei.value.status == 409
        exec_calls = [c2 for c2 in calls if c2[1].endswith("/exec")]
        assert len(exec_calls) == 2  # one sectioned, one scratch — no retry


def test_typed_expired_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return _err(410, "instance_expired", "instance expired at 2026-01-01")

    with Client(base_url="http://x", transport=httpx.MockTransport(handler)) as c:
        with pytest.raises(InstanceExpired) as ei:
            c.notebook("ins_1")
        assert ei.value.status == 410
        assert ei.value.code == "instance_expired"
        # CalcsideError base still catches it.
        assert isinstance(ei.value, CalcsideError)


def test_async_notebook_shapes() -> None:
    calls: list[tuple[str, str, dict]] = []

    async def scenario() -> None:
        async with AsyncClient(base_url="http://x", transport=_router(calls)) as c:
            nb = await c.notebook("ins_1")
            assert nb["restored_empty"] is True
            sec = await c.read_section("ins_1", "setup")
            assert sec["section"]["name"] == "setup"
            saved = await c.save_section(
                "ins_1", "setup", revision=7, code="x = 2", create_only=True
            )
            assert saved["section"]["state"] == "not_run"
            report = await c.run_sections("ins_1", "all")
            assert report["results"][0]["exec_id"] == "ex_1"
            section: ExecSection = {"name": "setup", "description": "init"}
            r = await c.exec("ins_1", "print(1)", section=section)
            assert r.section_state == "fresh"

    asyncio.run(scenario())


def test_async_restored_no_retry() -> None:
    calls: list[tuple[str, str, dict]] = []

    async def scenario() -> None:
        async with AsyncClient(base_url="http://x", transport=_router(calls)) as c:
            with pytest.raises(InstanceRestored):
                await c.exec("ins_1", "print(1)")

    asyncio.run(scenario())
    assert sum(1 for _, p, _ in calls if p.endswith("/exec")) == 1
