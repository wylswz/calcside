"""LangChain middleware that gives an agent a calcside Starlark sandbox.

Lifecycle mirrors ``ShellToolMiddleware``: the sandbox instance is
resolved in ``before_agent`` (reused from state on resumed runs, reused
via ctor/context instance id, or created per-run from a spec) and
optionally deleted in ``after_agent``. The generated system prompt is
injected via ``wrap_model_call`` so the model learns the Starlark /
capability model automatically.
"""

from __future__ import annotations

from collections.abc import Awaitable, Callable, Mapping
from typing import TYPE_CHECKING, Annotated, Any, ClassVar, cast

from langchain.agents.middleware.types import (
    AgentMiddleware,
    AgentState,
    ContextT,
    ModelRequest,
    ModelResponse,
    PrivateStateAttr,
    ResponseT,
)
from langchain.tools import ToolRuntime
from langchain_core.messages import AIMessage, SystemMessage, ToolMessage
from langchain_core.tools import StructuredTool
from typing_extensions import NotRequired, override

from calcside.client import AsyncClient, CalcsideError, Client, ExecResult

if TYPE_CHECKING:
    from langgraph.runtime import Runtime

DEFAULT_SPEC: dict[str, Any] = {"capabilities": {"fs": {}}, "ttl_seconds": 900}
DEFAULT_CONTEXT_KEY = "calcside_instance_id"


class CalcsideState(AgentState[ResponseT]):
    """Agent state extension tracking the sandbox instance.

    Tracked (not ``UntrackedValue``) so a checkpointed run resumed after
    an interrupt keeps the same instance.
    """

    calcside_instance_id: NotRequired[Annotated[str | None, PrivateStateAttr]]
    calcside_owned: NotRequired[Annotated[bool, PrivateStateAttr]]
    calcside_prompt: NotRequired[Annotated[str | None, PrivateStateAttr]]
    calcside_capabilities: NotRequired[Annotated[list[str], PrivateStateAttr]]


class CalcsideMiddleware(AgentMiddleware[CalcsideState, ContextT]):
    """Provides ``<prefix>exec``/``<prefix>list_files``/``<prefix>read_file``
    tools backed by a calcside instance, plus a generated system prompt.

    Resolution precedence for the instance: ``context_key`` in the run
    context > ctor ``instance_id`` > create from ``spec`` (per-run).
    """

    state_schema = CalcsideState  # type: ignore[assignment]

    def __init__(
        self,
        *,
        client: Client | None = None,
        async_client: AsyncClient | None = None,
        base_url: str | None = None,
        api_key: str | None = None,
        instance_id: str | None = None,
        spec: dict[str, Any] | None = None,
        delete_on_finish: bool = True,
        context_key: str = DEFAULT_CONTEXT_KEY,
        tool_prefix: str = "calcside_",
        max_tool_output_chars: int = 16000,
        exec_timeout_ms: int | None = None,
        system_prompt: str | Callable[[str, dict[str, Any]], str] | None = None,
    ) -> None:
        """Initialize the middleware.

        Args:
            client: Prebuilt sync ``Client`` (else built from
                ``base_url``/``api_key``/env).
            async_client: Prebuilt ``AsyncClient`` for the async path.
            base_url: calcside server URL (default env ``CALCSIDE_SERVER``
                or http://127.0.0.1:8080).
            api_key: ``cs_...`` API key (optional — dev-mode servers need
                no credentials).
            instance_id: Reuse an existing instance across runs; the
                middleware never deletes it.
            spec: Instance spec for per-run mode (used when no instance
                id resolves). Defaults to ``{"capabilities": {"fs": {}},
                "ttl_seconds": 900}``.
            delete_on_finish: Delete the per-run instance after the run
                (a crashed run is still reaped by the server-side TTL).
            context_key: Key looked up in ``runtime.context`` (mapping or
                attribute) that overrides ``instance_id`` per run.
            tool_prefix: Tool name prefix.
            max_tool_output_chars: Tool output truncation limit.
            exec_timeout_ms: Per-call exec timeout passed to the server.
            system_prompt: ``str`` is appended after the server-generated
                prompt; a callable receives ``(server_prompt,
                prompt_response_dict)`` and returns the final prompt.
        """
        super().__init__()
        self._client = client or Client(base_url=base_url, api_key=api_key)
        self._aclient = async_client or AsyncClient(base_url=base_url, api_key=api_key)
        self._instance_id = instance_id
        self._spec = spec if spec is not None else dict(DEFAULT_SPEC)
        self._delete_on_finish = delete_on_finish
        self._context_key = context_key
        self._tool_prefix = tool_prefix
        self._max_chars = max_tool_output_chars
        self._exec_timeout_ms = exec_timeout_ms
        self._system_prompt = system_prompt
        self.tools = self._make_tools()
        # Best-effort diagnostic only: the instance id resolved for the
        # most recent run (private-state fields are omitted from the
        # invoke result). Shared mutable state — under concurrent runs on
        # one middleware instance it simply records the last resolution.
        self.last_instance_id: str | None = None

    # -- instance resolution ------------------------------------------------

    def _context_instance_id(self, runtime: Runtime[ContextT]) -> str | None:
        ctx = getattr(runtime, "context", None)
        if ctx is None:
            return None
        if isinstance(ctx, Mapping):
            v = ctx.get(self._context_key)
        else:
            v = getattr(ctx, self._context_key, None)
        return str(v) if v else None

    def _prompt_for(self, resp: dict[str, Any]) -> tuple[str, list[str]]:
        """Apply ``system_prompt`` to the server-rendered prompt.

        Returns ``(final_prompt, capabilities)``. The callable form
        receives ``(server_prompt, prompt_response_dict)``.
        """
        server_prompt = resp.get("prompt") or ""
        capabilities = [str(c) for c in resp.get("capabilities") or []]
        sp = self._system_prompt
        if callable(sp):
            return sp(server_prompt, resp), capabilities
        if isinstance(sp, str) and sp:
            return server_prompt + "\n\n" + sp, capabilities
        return server_prompt, capabilities

    # -- lifecycle ----------------------------------------------------------

    @staticmethod
    def _plan(stored_id: str | None, desired_id: str | None) -> tuple[str, str | None]:
        """Decide which instance to bind before any fetch.

        Returns ``(kind, id)``: ``switch``/``adopt`` bind ``desired_id``
        (validate it is running), ``resume`` re-checks ``stored_id``, and
        ``create`` starts a fresh per-run instance from the spec.
        """
        if desired_id and desired_id != stored_id:
            return ("switch" if stored_id else "adopt", desired_id)
        if stored_id:
            return ("resume", stored_id)
        return ("create", None)

    @staticmethod
    def _resume_or_replace(stored_id: str, status: str | None, owned: bool) -> str:
        """After fetching a stored instance: ``keep`` it, ``create`` a
        replacement (it was ours and is gone), or raise for a reused
        instance that is not running."""
        if status == "running":
            return "keep"
        if owned:
            return "create"
        raise RuntimeError(
            f"calcside instance {stored_id} is not running (status={status or 'missing'})"
        )

    _CLEARED: ClassVar[dict[str, Any]] = {
        "calcside_instance_id": None,
        "calcside_owned": False,
        "calcside_prompt": None,
        "calcside_capabilities": [],
    }

    def _resolved_update(self, inst: dict[str, Any], owned: bool) -> dict[str, Any]:
        iid = inst["id"]
        self.last_instance_id = iid
        resp = self._client.prompt(iid, tool_prefix=self._tool_prefix)
        prompt, capabilities = self._prompt_for(resp)
        return {
            "calcside_instance_id": iid,
            "calcside_owned": owned,
            "calcside_prompt": prompt,
            "calcside_capabilities": capabilities,
        }

    async def _resolved_update_async(
        self, inst: dict[str, Any], owned: bool
    ) -> dict[str, Any]:
        iid = inst["id"]
        self.last_instance_id = iid
        resp = await self._aclient.prompt(iid, tool_prefix=self._tool_prefix)
        prompt, capabilities = self._prompt_for(resp)
        return {
            "calcside_instance_id": iid,
            "calcside_owned": owned,
            "calcside_prompt": prompt,
            "calcside_capabilities": capabilities,
        }

    def _get_optional(self, iid: str) -> dict[str, Any] | None:
        try:
            return self._client.get_instance(iid)
        except CalcsideError as e:
            if e.status == 404:
                return None
            raise

    async def _aget_optional(self, iid: str) -> dict[str, Any] | None:
        try:
            return await self._aclient.get_instance(iid)
        except CalcsideError as e:
            if e.status == 404:
                return None
            raise

    @override
    def before_agent(
        self, state: CalcsideState[ResponseT], runtime: Runtime[ContextT]
    ) -> dict[str, Any] | None:
        stored_id = state.get("calcside_instance_id")
        owned = bool(state.get("calcside_owned"))
        desired = self._context_instance_id(runtime) or self._instance_id
        kind, iid = self._plan(stored_id, desired)

        if kind in ("switch", "adopt"):
            if kind == "switch" and owned:
                # abandon the owned sandbox — best-effort cleanup; the
                # server TTL reaps it if this fails
                try:
                    self._client.delete_instance(stored_id)  # type: ignore[arg-type]
                except CalcsideError:
                    pass
            inst = self._get_optional(iid)  # type: ignore[arg-type]
            if inst is None or inst.get("status") != "running":
                raise RuntimeError(
                    f"calcside instance {iid} is not running"
                    f" (status={(inst or {}).get('status') or 'missing'})"
                )
            return self._resolved_update(inst, owned=False)

        if kind == "resume":
            inst = self._get_optional(iid)  # type: ignore[arg-type]
            status = (inst or {}).get("status")
            if self._resume_or_replace(iid, status, owned) == "keep":
                # type: ignore[arg-type]
                if state.get("calcside_prompt"):
                    self.last_instance_id = iid
                    return None  # nothing changed — resumed run
                return self._resolved_update(inst, owned)  # type: ignore[arg-type]
            # owned but gone → fall through to create

        inst = self._client.create_instance(self._spec)
        return self._resolved_update(inst, owned=True)

    @override
    async def abefore_agent(
        self, state: CalcsideState[ResponseT], runtime: Runtime[ContextT]
    ) -> dict[str, Any] | None:
        stored_id = state.get("calcside_instance_id")
        owned = bool(state.get("calcside_owned"))
        desired = self._context_instance_id(runtime) or self._instance_id
        kind, iid = self._plan(stored_id, desired)

        if kind in ("switch", "adopt"):
            if kind == "switch" and owned:
                try:
                    await self._aclient.delete_instance(stored_id)  # type: ignore[arg-type]
                except CalcsideError:
                    pass
            inst = await self._aget_optional(iid)  # type: ignore[arg-type]
            if inst is None or inst.get("status") != "running":
                raise RuntimeError(
                    f"calcside instance {iid} is not running"
                    f" (status={(inst or {}).get('status') or 'missing'})"
                )
            return await self._resolved_update_async(inst, owned=False)

        if kind == "resume":
            inst = await self._aget_optional(iid)  # type: ignore[arg-type]
            status = (inst or {}).get("status")
            if self._resume_or_replace(iid, status, owned) == "keep":
                if state.get("calcside_prompt"):
                    self.last_instance_id = iid
                    return None
                return await self._resolved_update_async(inst, owned)  # type: ignore[arg-type]

        inst = await self._aclient.create_instance(self._spec)
        return await self._resolved_update_async(inst, owned=True)

    @override
    def after_agent(
        self, state: CalcsideState[ResponseT], runtime: Runtime[ContextT]
    ) -> dict[str, Any] | None:
        if state.get("calcside_owned") and self._delete_on_finish:
            iid = state.get("calcside_instance_id")
            if iid:
                try:
                    self._client.delete_instance(iid)
                except CalcsideError as e:
                    if e.status != 404:
                        raise
                return dict(self._CLEARED)
        return None

    @override
    async def aafter_agent(
        self, state: CalcsideState[ResponseT], runtime: Runtime[ContextT]
    ) -> dict[str, Any] | None:
        if state.get("calcside_owned") and self._delete_on_finish:
            iid = state.get("calcside_instance_id")
            if iid:
                try:
                    await self._aclient.delete_instance(iid)
                except CalcsideError as e:
                    if e.status != 404:
                        raise
                return dict(self._CLEARED)
        return None

    # -- prompt injection ---------------------------------------------------

    def _inject_prompt(self, request: ModelRequest[ContextT]) -> ModelRequest[ContextT]:
        prompt = request.state.get("calcside_prompt")
        if not prompt:
            return request
        if request.system_message is not None:
            content = [
                *request.system_message.content_blocks,
                {"type": "text", "text": f"\n\n{prompt}"},
            ]
        else:
            content = [{"type": "text", "text": prompt}]
        return request.override(
            system_message=SystemMessage(
                content=cast("list[str | dict[str, str]]", content)
            )
        )

    @override
    def wrap_model_call(
        self,
        request: ModelRequest[ContextT],
        handler: Callable[[ModelRequest[ContextT]], ModelResponse[ResponseT]],
    ) -> ModelResponse[ResponseT] | AIMessage:
        return handler(self._inject_prompt(request))

    @override
    async def awrap_model_call(
        self,
        request: ModelRequest[ContextT],
        handler: Callable[
            [ModelRequest[ContextT]], Awaitable[ModelResponse[ResponseT]]
        ],
    ) -> ModelResponse[ResponseT] | AIMessage:
        return await handler(self._inject_prompt(request))

    # -- tools ---------------------------------------------------------------

    def _exec_text(self, r: ExecResult) -> str:
        text = r.output
        if not r.error and not text:
            text = "(no output — use print() to show results)"
        if r.error:
            text += f"\n[error: {r.error.type}] {r.error.message}"
            if r.error.backtrace:
                text += f"\n{r.error.backtrace}"
        if len(text) > self._max_chars:
            cut = len(text) - self._max_chars
            text = text[: self._max_chars] + f"\n…[truncated {cut} chars]"
        return text

    def _instance_id_of(self, runtime: ToolRuntime) -> str | None:
        return runtime.state.get("calcside_instance_id")

    def _gone_or_error(self, e: CalcsideError) -> str:
        if e.status in (404, 409):
            return (
                f"sandbox instance is gone ({e.message}); it may have expired or been"
                " deleted — tell the user instead of retrying."
            )
        return f"calcside error ({e.status} {e.code}): {e.message}"

    def _make_tools(self) -> list[StructuredTool]:
        prefix = self._tool_prefix
        self_ref = self

        def exec_fn(code: str, runtime: ToolRuntime) -> ToolMessage | str:
            iid = self_ref._instance_id_of(runtime)
            if not iid:
                return "no calcside sandbox instance is active for this run."
            try:
                r = self_ref._client.exec(
                    iid, code, timeout_ms=self_ref._exec_timeout_ms
                )
            except CalcsideError as e:
                return self_ref._gone_or_error(e)
            return ToolMessage(
                content=self_ref._exec_text(r),
                tool_call_id=runtime.tool_call_id,
                artifact=r.raw,
            )

        async def aexec_fn(code: str, runtime: ToolRuntime) -> ToolMessage | str:
            iid = self_ref._instance_id_of(runtime)
            if not iid:
                return "no calcside sandbox instance is active for this run."
            try:
                r = await self_ref._aclient.exec(
                    iid, code, timeout_ms=self_ref._exec_timeout_ms
                )
            except CalcsideError as e:
                return self_ref._gone_or_error(e)
            return ToolMessage(
                content=self_ref._exec_text(r),
                tool_call_id=runtime.tool_call_id,
                artifact=r.raw,
            )

        def list_fn(runtime: ToolRuntime, path: str = "/work") -> str:
            iid = self_ref._instance_id_of(runtime)
            if not iid:
                return "no calcside sandbox instance is active for this run."
            if "fs" not in (runtime.state.get("calcside_capabilities") or []):
                return "the 'fs' capability is not granted to this sandbox; use exec() for non-filesystem work."
            try:
                res = self_ref._client.files(iid, path)
            except CalcsideError as e:
                return self_ref._gone_or_error(e)
            entries = res.get("entries")
            if entries is None:
                return res.get("content", "")
            return (
                "\n".join(
                    f"{'d' if e.get('is_dir') else '-'} {e.get('size', 0):>10} {e.get('path')}"
                    for e in entries
                )
                or "(empty directory)"
            )

        async def alist_fn(runtime: ToolRuntime, path: str = "/work") -> str:
            iid = self_ref._instance_id_of(runtime)
            if not iid:
                return "no calcside sandbox instance is active for this run."
            if "fs" not in (runtime.state.get("calcside_capabilities") or []):
                return "the 'fs' capability is not granted to this sandbox; use exec() for non-filesystem work."
            try:
                res = await self_ref._aclient.files(iid, path)
            except CalcsideError as e:
                return self_ref._gone_or_error(e)
            entries = res.get("entries")
            if entries is None:
                return res.get("content", "")
            return (
                "\n".join(
                    f"{'d' if e.get('is_dir') else '-'} {e.get('size', 0):>10} {e.get('path')}"
                    for e in entries
                )
                or "(empty directory)"
            )

        def read_fn(path: str, runtime: ToolRuntime) -> str:
            iid = self_ref._instance_id_of(runtime)
            if not iid:
                return "no calcside sandbox instance is active for this run."
            if "fs" not in (runtime.state.get("calcside_capabilities") or []):
                return "the 'fs' capability is not granted to this sandbox; use exec() for non-filesystem work."
            try:
                res = self_ref._client.files(iid, path)
            except CalcsideError as e:
                return self_ref._gone_or_error(e)
            content = res.get("content", "")
            if len(content) > self_ref._max_chars:
                cut = len(content) - self_ref._max_chars
                content = content[: self_ref._max_chars] + f"\n…[truncated {cut} chars]"
            return content

        async def aread_fn(path: str, runtime: ToolRuntime) -> str:
            iid = self_ref._instance_id_of(runtime)
            if not iid:
                return "no calcside sandbox instance is active for this run."
            if "fs" not in (runtime.state.get("calcside_capabilities") or []):
                return "the 'fs' capability is not granted to this sandbox; use exec() for non-filesystem work."
            try:
                res = await self_ref._aclient.files(iid, path)
            except CalcsideError as e:
                return self_ref._gone_or_error(e)
            content = res.get("content", "")
            if len(content) > self_ref._max_chars:
                cut = len(content) - self_ref._max_chars
                content = content[: self_ref._max_chars] + f"\n…[truncated {cut} chars]"
            return content

        return [
            StructuredTool.from_function(
                func=exec_fn,
                coroutine=aexec_fn,
                name=f"{prefix}exec",
                description=(
                    "Run Starlark code in the persistent calcside sandbox and return "
                    "printed output (use print(); the last expression's value is NOT "
                    "shown). Top-level variables persist across calls. Starlark is not "
                    "Python: no import/classes/try/with/f-strings/yield; json and math "
                    "modules exist; while/recursion/sets work."
                ),
            ),
            StructuredTool.from_function(
                func=list_fn,
                coroutine=alist_fn,
                name=f"{prefix}list_files",
                description=(
                    "List a directory in the sandbox filesystem (requires the 'fs' "
                    "capability). Paths are rooted at /work."
                ),
            ),
            StructuredTool.from_function(
                func=read_fn,
                coroutine=aread_fn,
                name=f"{prefix}read_file",
                description=(
                    "Read a file's content from the sandbox filesystem (requires the "
                    "'fs' capability). Paths are rooted at /work."
                ),
            ),
        ]


__all__ = ["CalcsideMiddleware", "CalcsideState"]
