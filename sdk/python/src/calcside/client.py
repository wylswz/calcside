"""Sync and async HTTP clients for the calcside sandbox API."""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from typing import Any

import httpx
from typing_extensions import Self

DEFAULT_BASE_URL = "http://127.0.0.1:8080"

# Same anti-CSRF header the web console sends. Required for mutating
# requests that authenticate via cookie or dev-mode anonymous; harmless
# when a bearer key is used.
_CSRF_HEADERS = {"X-Requested-With": "calcside"}


class CalcsideError(Exception):
    """Non-2xx response from the calcside API (error envelope)."""

    def __init__(self, status: int, code: str, message: str) -> None:
        super().__init__(
            f"{status} {code}: {message}" if code else f"{status}: {message}"
        )
        self.status = status
        self.code = code
        self.message = message


@dataclass
class ExecError:
    """Script error reported by an exec call."""

    type: str
    message: str
    backtrace: str = ""


@dataclass
class ExecResult:
    """Result of running Starlark code in an instance."""

    exec_id: str
    output: str
    error: ExecError | None
    duration_ms: int
    steps: int
    raw: dict[str, Any] = field(default_factory=dict)


def _exec_result(data: dict[str, Any]) -> ExecResult:
    err = data.get("error")
    return ExecResult(
        exec_id=data.get("exec_id", ""),
        output=data.get("output", ""),
        error=ExecError(err["type"], err["message"], err.get("backtrace", ""))
        if err
        else None,
        duration_ms=data.get("duration_ms", 0),
        steps=data.get("steps", 0),
        raw=data,
    )


def _raise_for_status(resp: httpx.Response) -> None:
    if resp.status_code < 400:
        return
    code, message = "", resp.text or resp.reason_phrase
    try:
        env = resp.json().get("error") or {}
    except ValueError:  # not a JSON body — keep status text
        env = {}
    code = env.get("code", "")
    message = env.get("message", message)
    raise CalcsideError(resp.status_code, code, message)


def _default_headers(api_key: str | None) -> dict[str, str]:
    headers = dict(_CSRF_HEADERS)
    if api_key:
        headers["Authorization"] = f"Bearer {api_key}"
    return headers


def _resolve(
    base_url: str | None, api_key: str | None, timeout: float
) -> tuple[str, str | None, float]:
    return (
        base_url or os.environ.get("CALCSIDE_SERVER") or DEFAULT_BASE_URL,
        api_key if api_key is not None else os.environ.get("CALCSIDE_API_KEY"),
        timeout,
    )


class Client:
    """Synchronous calcside API client."""

    def __init__(
        self,
        base_url: str | None = None,
        api_key: str | None = None,
        timeout: float = 30.0,
    ) -> None:
        url, key, timeout = _resolve(base_url, api_key, timeout)
        self.base_url = url.rstrip("/")
        self._http = httpx.Client(
            base_url=self.base_url,
            headers=_default_headers(key),
            timeout=timeout,
        )

    def close(self) -> None:
        self._http.close()

    def __enter__(self) -> Self:
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def _req(self, method: str, path: str, **kwargs: Any) -> httpx.Response:
        resp = self._http.request(method, path, **kwargs)
        _raise_for_status(resp)
        return resp

    def capabilities(self) -> list[dict[str, Any]]:
        """The server's capability catalog (ops, params, config fields)."""
        return self._req("GET", "/api/v1/capabilities").json().get("capabilities") or []

    def create_instance(self, spec: dict[str, Any]) -> dict[str, Any]:
        """Create an instance; returns the instance object."""
        return self._req("POST", "/api/v1/instances", json=spec).json()["instance"]

    def get_instance(self, instance_id: str) -> dict[str, Any]:
        return self._req("GET", f"/api/v1/instances/{instance_id}").json()["instance"]

    def delete_instance(self, instance_id: str) -> None:
        self._req("DELETE", f"/api/v1/instances/{instance_id}")

    def keepalive(self, instance_id: str) -> dict[str, Any]:
        return self._req("POST", f"/api/v1/instances/{instance_id}/keepalive").json()[
            "instance"
        ]

    def exec(
        self,
        instance_id: str,
        code: str,
        timeout_ms: int | None = None,
    ) -> ExecResult:
        body: dict[str, Any] = {"code": code}
        if timeout_ms is not None:
            body["timeout_ms"] = timeout_ms
        return _exec_result(
            self._req("POST", f"/api/v1/instances/{instance_id}/exec", json=body).json()
        )

    def files(self, instance_id: str, path: str = "/work") -> dict[str, Any]:
        """Directory listing (``entries``) or file ``content``."""
        return self._req(
            "GET", f"/api/v1/instances/{instance_id}/files", params={"path": path}
        ).json()

    def prompt(
        self, instance_id: str, tool_prefix: str | None = None
    ) -> dict[str, Any]:
        """Server-generated agent prompt (``prompt``, ``capabilities``, ``tools``)."""
        params = {"tool_prefix": tool_prefix} if tool_prefix is not None else None
        return self._req(
            "GET", f"/api/v1/instances/{instance_id}/prompt", params=params
        ).json()

    def list_secrets(self) -> list[dict[str, Any]]:
        """Vault secrets metadata — values are never returned."""
        return self._req("GET", "/api/v1/secrets").json().get("secrets") or []

    def create_secret(
        self,
        name: str,
        value: str,
        allowed_domains: list[str] | None = None,
    ) -> dict[str, Any]:
        """Create a vault secret (e.g. ``TAVILY_API_KEY``); returns metadata."""
        body: dict[str, Any] = {"name": name, "value": value}
        if allowed_domains is not None:
            body["allowed_domains"] = allowed_domains
        return self._req("POST", "/api/v1/secrets", json=body).json()["secret"]

    def update_secret(
        self,
        secret_id: str,
        *,
        value: str | None = None,
        allowed_domains: list[str] | None = None,
    ) -> dict[str, Any]:
        """Update a vault secret's value and/or domain allowlist."""
        body: dict[str, Any] = {}
        if value is not None:
            body["value"] = value
        if allowed_domains is not None:
            body["allowed_domains"] = allowed_domains
        return self._req("PUT", f"/api/v1/secrets/{secret_id}", json=body).json()[
            "secret"
        ]

    def delete_secret(self, secret_id: str) -> None:
        self._req("DELETE", f"/api/v1/secrets/{secret_id}")


class AsyncClient:
    """Async calcside API client (same surface as ``Client``)."""

    def __init__(
        self,
        base_url: str | None = None,
        api_key: str | None = None,
        timeout: float = 30.0,
    ) -> None:
        url, key, timeout = _resolve(base_url, api_key, timeout)
        self.base_url = url.rstrip("/")
        self._http = httpx.AsyncClient(
            base_url=self.base_url,
            headers=_default_headers(key),
            timeout=timeout,
        )

    async def aclose(self) -> None:
        await self._http.aclose()

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *exc: object) -> None:
        await self.aclose()

    async def _req(self, method: str, path: str, **kwargs: Any) -> httpx.Response:
        resp = await self._http.request(method, path, **kwargs)
        _raise_for_status(resp)
        return resp

    async def capabilities(self) -> list[dict[str, Any]]:
        return (await self._req("GET", "/api/v1/capabilities")).json().get(
            "capabilities"
        ) or []

    async def create_instance(self, spec: dict[str, Any]) -> dict[str, Any]:
        return (await self._req("POST", "/api/v1/instances", json=spec)).json()[
            "instance"
        ]

    async def get_instance(self, instance_id: str) -> dict[str, Any]:
        return (await self._req("GET", f"/api/v1/instances/{instance_id}")).json()[
            "instance"
        ]

    async def delete_instance(self, instance_id: str) -> None:
        await self._req("DELETE", f"/api/v1/instances/{instance_id}")

    async def keepalive(self, instance_id: str) -> dict[str, Any]:
        return (
            await self._req("POST", f"/api/v1/instances/{instance_id}/keepalive")
        ).json()["instance"]

    async def exec(
        self,
        instance_id: str,
        code: str,
        timeout_ms: int | None = None,
    ) -> ExecResult:
        body: dict[str, Any] = {"code": code}
        if timeout_ms is not None:
            body["timeout_ms"] = timeout_ms
        return _exec_result(
            (
                await self._req(
                    "POST", f"/api/v1/instances/{instance_id}/exec", json=body
                )
            ).json()
        )

    async def files(self, instance_id: str, path: str = "/work") -> dict[str, Any]:
        return (
            await self._req(
                "GET", f"/api/v1/instances/{instance_id}/files", params={"path": path}
            )
        ).json()

    async def prompt(
        self, instance_id: str, tool_prefix: str | None = None
    ) -> dict[str, Any]:
        params = {"tool_prefix": tool_prefix} if tool_prefix is not None else None
        return (
            await self._req(
                "GET", f"/api/v1/instances/{instance_id}/prompt", params=params
            )
        ).json()

    async def list_secrets(self) -> list[dict[str, Any]]:
        return (await self._req("GET", "/api/v1/secrets")).json().get("secrets") or []

    async def create_secret(
        self,
        name: str,
        value: str,
        allowed_domains: list[str] | None = None,
    ) -> dict[str, Any]:
        body: dict[str, Any] = {"name": name, "value": value}
        if allowed_domains is not None:
            body["allowed_domains"] = allowed_domains
        return (await self._req("POST", "/api/v1/secrets", json=body)).json()["secret"]

    async def update_secret(
        self,
        secret_id: str,
        *,
        value: str | None = None,
        allowed_domains: list[str] | None = None,
    ) -> dict[str, Any]:
        body: dict[str, Any] = {}
        if value is not None:
            body["value"] = value
        if allowed_domains is not None:
            body["allowed_domains"] = allowed_domains
        return (
            await self._req("PUT", f"/api/v1/secrets/{secret_id}", json=body)
        ).json()["secret"]

    async def delete_secret(self, secret_id: str) -> None:
        await self._req("DELETE", f"/api/v1/secrets/{secret_id}")
