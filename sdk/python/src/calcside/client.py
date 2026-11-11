"""Sync and async HTTP clients for the calcside sandbox API."""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from email.message import Message
from typing import Any, Literal
from urllib.parse import quote

import httpx
from typing_extensions import Self, TypedDict

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


class InstanceRestored(CalcsideError):
    """The instance's process was lost and restored EMPTY (HTTP 409,
    code ``instance_restored``).

    The triggering call did NOT execute and nothing was re-run. The
    durable notebook is preserved — inspect it with ``notebook()`` /
    ``read_section()`` and rebuild state explicitly with
    ``run_sections()`` before retrying. Never retried automatically.
    """


class InstanceExpired(CalcsideError):
    """The instance expired (HTTP 410, code ``instance_expired``).

    Expired instances are never restored; reads stay available but
    writes and executions are refused.
    """


@dataclass
class ExecError:
    """Script error reported by an exec call."""

    type: str
    message: str
    backtrace: str = ""


@dataclass
class ExecResult:
    """Result of running Starlark code in an instance.

    ``notebook_revision``/``drift`` are additive notebook facts (0 on
    older servers); ``section``/``section_state`` are set only for
    sectioned execs.
    """

    exec_id: str
    output: str
    error: ExecError | None
    duration_ms: int
    steps: int
    raw: dict[str, Any] = field(default_factory=dict)
    notebook_revision: int = 0
    drift: int = 0
    section: str = ""
    section_state: str = ""


class ExecSection(TypedDict, total=False):
    """Section metadata for ``exec(section=...)``: the named section is
    saved/replaced BEFORE running — failed code is preserved as a
    failed section. ``description`` is required when creating a new
    section (omit to keep an existing one); ``revision`` is the notebook
    CAS and optional."""

    name: str
    description: str
    rerun: Literal["safe", "once"]
    after: str
    revision: int


class NotebookSectionMeta(TypedDict, total=False):
    """One outline row: metadata only, no code or output.
    ``last_incarnation`` identifies which process generation produced
    the last run — it differs from the notebook's ``incarnation`` after
    a restore, marking that output as previous-runtime history."""

    name: str
    position: int
    description: str
    state: str  # not_run | fresh | stale | failed
    rerun: str  # safe | once
    defines: list[str]
    uses: list[str]
    effects: list[str]
    depends_on: list[str]
    last_exec_id: str
    last_incarnation: int
    last_status: str
    last_run_at: str
    once_ran: bool


class NotebookSection(TypedDict, total=False):
    """One section with code; output/error only with include_output."""

    section: NotebookSectionMeta
    code: str
    output: str
    error: str
    output_truncated: bool


class Notebook(TypedDict, total=False):
    """The notebook outline: instance facts, per-section metadata, the
    rendered text outline, and next-step guidance. ``restored_empty``
    is true only while a restored process is verifiably untouched."""

    instance_id: str
    status: str
    incarnation: int
    restored_at: str
    restored_empty: bool
    revision: int
    drift: int
    sections: list[NotebookSectionMeta]
    outline: str
    next: list[str]


class SaveSectionResult(TypedDict, total=False):
    """PUT /sections response: saved section metadata + new revision."""

    section: NotebookSectionMeta
    revision: int


class DeleteSectionResult(TypedDict, total=False):
    affected: list[str]
    revision: int


class RunSectionResult(TypedDict, total=False):
    name: str
    status: str  # ok | error | skipped | not_run
    exec_id: str
    error: str
    skipped_reason: str
    duration_ms: int


class RunReport(TypedDict, total=False):
    """POST /notebook/run report: per-section outcomes in notebook order.

    ``restored``/``stopped_reason`` appear when the runtime was lost
    mid-run: earlier results executed in the previous incarnation and
    the rest are ``not_run`` — do not blindly retry side effects.
    """

    instance_id: str
    incarnation: int
    revision: int
    drift: int
    results: list[RunSectionResult]
    restored: bool
    stopped_reason: str
    warnings: list[str]


@dataclass(frozen=True)
class ArtifactDownload:
    content: bytes
    filename: str
    redacted: bool
    file_count: int


def _artifact_download(resp: httpx.Response) -> ArtifactDownload:
    header = Message()
    header["Content-Disposition"] = resp.headers.get("Content-Disposition", "")
    return ArtifactDownload(
        content=resp.content,
        filename=header.get_filename() or "artifact",
        redacted=resp.headers.get("X-Calcside-Redacted") == "true",
        file_count=int(resp.headers.get("X-Calcside-File-Count", "0")),
    )


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
        notebook_revision=data.get("notebook_revision", 0),
        drift=data.get("drift", 0),
        section=data.get("section", ""),
        section_state=data.get("section_state", ""),
    )


# Reads are bounded at the server's 1 MiB upload ceiling plus one byte
# so a caller-streamed file fails client-side instead of being
# truncated into a silently different file.
_MAX_UPLOAD = 1 << 20


def _file_bytes(content: bytes | bytearray | str | Any) -> bytes:
    if isinstance(content, str):
        return content.encode("utf-8")
    if isinstance(content, (bytes, bytearray)):
        return bytes(content)
    if not hasattr(content, "read"):
        raise TypeError("content must be bytes, str, or a readable file-like object")
    data = content.read(_MAX_UPLOAD + 1)
    if isinstance(data, str):
        data = data.encode("utf-8")
    return bytes(data)


_ERROR_TYPES: dict[str, type[CalcsideError]] = {
    "instance_restored": InstanceRestored,
    "instance_expired": InstanceExpired,
}


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
    raise _ERROR_TYPES.get(code, CalcsideError)(resp.status_code, code, message)


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
        transport: httpx.BaseTransport | None = None,
    ) -> None:
        url, key, timeout = _resolve(base_url, api_key, timeout)
        self.base_url = url.rstrip("/")
        self._http = httpx.Client(
            base_url=self.base_url,
            headers=_default_headers(key),
            timeout=timeout,
            transport=transport,
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
        section: ExecSection | None = None,
    ) -> ExecResult:
        """Run Starlark code; returns printed output or the error.

        ``section`` saves/replaces the named notebook section BEFORE
        running it — failed code is preserved too. Omit for a scratch
        exec, which never becomes a section.
        """
        body: dict[str, Any] = {"code": code}
        if timeout_ms is not None:
            body["timeout_ms"] = timeout_ms
        if section is not None:
            body["section"] = dict(section)
        return _exec_result(
            self._req("POST", f"/api/v1/instances/{instance_id}/exec", json=body).json()
        )

    # -- notebook ------------------------------------------------------------

    def notebook(self, instance_id: str) -> Notebook:
        """Notebook outline: per-section state/defines/uses/effects plus
        guidance. Read-only — never restores a lost instance."""
        return self._req("GET", f"/api/v1/instances/{instance_id}/notebook").json()[
            "notebook"
        ]

    def read_section(
        self, instance_id: str, name: str, include_output: bool = False
    ) -> NotebookSection:
        """One section: metadata plus its code; ``include_output`` adds
        the last run's output/error."""
        params = {"include_output": "true"} if include_output else None
        return self._req(
            "GET",
            f"/api/v1/instances/{instance_id}/sections/{quote(name, safe='')}",
            params=params,
        ).json()["section"]

    def save_section(
        self,
        instance_id: str,
        name: str,
        *,
        revision: int,
        code: str | None = None,
        description: str | None = None,
        rerun: str | None = None,
        after: str | None = None,
        from_exec: str | None = None,
        create_only: bool = False,
    ) -> SaveSectionResult:
        """Edit a section WITHOUT running it. ``revision`` is the
        notebook CAS from ``notebook()``/``read_section()`` — required.
        ``from_exec`` promotes a successful scratch exec's code.
        ``create_only=True`` rejects an existing name with a conflict —
        the agent-facing add semantics."""
        body: dict[str, Any] = {"revision": revision}
        if code is not None:
            body["code"] = code
        if description is not None:
            body["description"] = description
        if rerun is not None:
            body["rerun"] = rerun
        if after is not None:
            body["after"] = after
        if from_exec is not None:
            body["from_exec"] = from_exec
        if create_only:
            body["create_only"] = True
        return self._req(
            "PUT",
            f"/api/v1/instances/{instance_id}/sections/{quote(name, safe='')}",
            json=body,
        ).json()

    def delete_section(
        self, instance_id: str, name: str, revision: int
    ) -> DeleteSectionResult:
        """Delete a section under notebook CAS; returns
        ``{affected, revision}`` (affected = sections still reading its
        defines)."""
        return self._req(
            "DELETE",
            f"/api/v1/instances/{instance_id}/sections/{quote(name, safe='')}",
            params={"revision": revision},
        ).json()

    def run_sections(
        self,
        instance_id: str,
        sections: str | list[str],
        revision: int | None = None,
    ) -> RunReport:
        """Run notebook sections in order: ``"all"``, ``"stale"``, one
        name, or a list of names. Bulk runs skip once-sections that
        already succeeded; execution stops at the first failure. On a
        lost instance the first run restores an EMPTY process and raises
        ``InstanceRestored`` without executing — call again afterwards."""
        body: dict[str, Any] = {"sections": sections}
        if revision is not None:
            body["revision"] = revision
        return self._req(
            "POST", f"/api/v1/instances/{instance_id}/notebook/run", json=body
        ).json()["report"]

    def files(self, instance_id: str, path: str = "/work") -> dict[str, Any]:
        """Directory listing (``entries``) or file ``content``."""
        return self._req(
            "GET", f"/api/v1/instances/{instance_id}/files", params={"path": path}
        ).json()

    def upload_file(
        self,
        instance_id: str,
        path: str,
        content: bytes | bytearray | str | Any,
        overwrite: bool = False,
    ) -> dict[str, Any]:
        """Upload one file into the instance filesystem (max 1 MiB).

        ``content`` accepts ``bytes``/``bytearray``/``str`` (encoded as
        UTF-8) or a readable file-like object opened by the caller — a
        local path is never sent to the server. Existing files raise
        ``CalcsideError(409, "conflict")`` unless ``overwrite`` is true.
        Returns the write receipt ``{path, size, mtime, replaced}``.
        """
        body = _file_bytes(content)
        return self._req(
            "PUT",
            f"/api/v1/instances/{instance_id}/files",
            params={"path": path, "overwrite": "true" if overwrite else "false"},
            content=body,
            headers={"Content-Type": "application/octet-stream"},
        ).json()

    def preview_artifact(
        self,
        instance_id: str,
        path: str,
        mode: Literal["source", "static", "interactive"] = "source",
    ) -> dict[str, Any]:
        return self._req(
            "POST",
            f"/api/v1/instances/{instance_id}/artifacts/preview",
            json={"path": path, "mode": mode},
        ).json()

    def download_file(self, instance_id: str, path: str) -> ArtifactDownload:
        return _artifact_download(
            self._req(
                "POST",
                f"/api/v1/instances/{instance_id}/artifacts/export",
                json={"paths": [path], "format": "file"},
            )
        )

    def export_files(self, instance_id: str, paths: list[str]) -> ArtifactDownload:
        return _artifact_download(
            self._req(
                "POST",
                f"/api/v1/instances/{instance_id}/artifacts/export",
                json={"paths": paths, "format": "zip"},
            )
        )

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
        transport: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        url, key, timeout = _resolve(base_url, api_key, timeout)
        self.base_url = url.rstrip("/")
        self._http = httpx.AsyncClient(
            base_url=self.base_url,
            headers=_default_headers(key),
            timeout=timeout,
            transport=transport,
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
        section: ExecSection | None = None,
    ) -> ExecResult:
        """Async counterpart of :meth:`Client.exec`."""
        body: dict[str, Any] = {"code": code}
        if timeout_ms is not None:
            body["timeout_ms"] = timeout_ms
        if section is not None:
            body["section"] = dict(section)
        return _exec_result(
            (
                await self._req(
                    "POST", f"/api/v1/instances/{instance_id}/exec", json=body
                )
            ).json()
        )

    # -- notebook ------------------------------------------------------------

    async def notebook(self, instance_id: str) -> Notebook:
        """Async counterpart of :meth:`Client.notebook`."""
        return (
            await self._req("GET", f"/api/v1/instances/{instance_id}/notebook")
        ).json()["notebook"]

    async def read_section(
        self, instance_id: str, name: str, include_output: bool = False
    ) -> NotebookSection:
        """Async counterpart of :meth:`Client.read_section`."""
        params = {"include_output": "true"} if include_output else None
        return (
            await self._req(
                "GET",
                f"/api/v1/instances/{instance_id}/sections/{quote(name, safe='')}",
                params=params,
            )
        ).json()["section"]

    async def save_section(
        self,
        instance_id: str,
        name: str,
        *,
        revision: int,
        code: str | None = None,
        description: str | None = None,
        rerun: str | None = None,
        after: str | None = None,
        from_exec: str | None = None,
        create_only: bool = False,
    ) -> SaveSectionResult:
        """Async counterpart of :meth:`Client.save_section`."""
        body: dict[str, Any] = {"revision": revision}
        if code is not None:
            body["code"] = code
        if description is not None:
            body["description"] = description
        if rerun is not None:
            body["rerun"] = rerun
        if after is not None:
            body["after"] = after
        if from_exec is not None:
            body["from_exec"] = from_exec
        if create_only:
            body["create_only"] = True
        return (
            await self._req(
                "PUT",
                f"/api/v1/instances/{instance_id}/sections/{quote(name, safe='')}",
                json=body,
            )
        ).json()

    async def delete_section(
        self, instance_id: str, name: str, revision: int
    ) -> DeleteSectionResult:
        """Async counterpart of :meth:`Client.delete_section`."""
        return (
            await self._req(
                "DELETE",
                f"/api/v1/instances/{instance_id}/sections/{quote(name, safe='')}",
                params={"revision": revision},
            )
        ).json()

    async def run_sections(
        self,
        instance_id: str,
        sections: str | list[str],
        revision: int | None = None,
    ) -> RunReport:
        """Async counterpart of :meth:`Client.run_sections`."""
        body: dict[str, Any] = {"sections": sections}
        if revision is not None:
            body["revision"] = revision
        return (
            await self._req(
                "POST", f"/api/v1/instances/{instance_id}/notebook/run", json=body
            )
        ).json()["report"]

    async def files(self, instance_id: str, path: str = "/work") -> dict[str, Any]:
        return (
            await self._req(
                "GET", f"/api/v1/instances/{instance_id}/files", params={"path": path}
            )
        ).json()

    async def upload_file(
        self,
        instance_id: str,
        path: str,
        content: bytes | bytearray | str | Any,
        overwrite: bool = False,
    ) -> dict[str, Any]:
        """Async counterpart of :meth:`Client.upload_file`."""
        body = _file_bytes(content)
        return (
            await self._req(
                "PUT",
                f"/api/v1/instances/{instance_id}/files",
                params={"path": path, "overwrite": "true" if overwrite else "false"},
                content=body,
                headers={"Content-Type": "application/octet-stream"},
            )
        ).json()

    async def preview_artifact(
        self,
        instance_id: str,
        path: str,
        mode: Literal["source", "static", "interactive"] = "source",
    ) -> dict[str, Any]:
        return (
            await self._req(
                "POST",
                f"/api/v1/instances/{instance_id}/artifacts/preview",
                json={"path": path, "mode": mode},
            )
        ).json()

    async def download_file(self, instance_id: str, path: str) -> ArtifactDownload:
        return _artifact_download(
            await self._req(
                "POST",
                f"/api/v1/instances/{instance_id}/artifacts/export",
                json={"paths": [path], "format": "file"},
            )
        )

    async def export_files(
        self, instance_id: str, paths: list[str]
    ) -> ArtifactDownload:
        return _artifact_download(
            await self._req(
                "POST",
                f"/api/v1/instances/{instance_id}/artifacts/export",
                json={"paths": paths, "format": "zip"},
            )
        )

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
