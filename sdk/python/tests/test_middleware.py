"""Middleware tests with a scripted chat model against a dev server."""

from __future__ import annotations

import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import Any

import pytest
from langchain.agents import create_agent
from langchain_core.language_models.chat_models import BaseChatModel
from langchain_core.messages import AIMessage, SystemMessage, ToolMessage
from langchain_core.outputs import ChatGeneration, ChatResult
from pydantic import Field

from calcside import CalcsideError, Client
from calcside.langchain import CalcsideMiddleware


class ScriptedChatModel(BaseChatModel):
    """Fake model: pops a scripted AIMessage per call, records system msgs."""

    script: list[AIMessage]
    seen_system: list[str] = Field(default_factory=list)

    @property
    def _llm_type(self) -> str:
        return "scripted-chat"

    def bind_tools(self, tools, *, tool_choice=None, **kwargs):
        # scripted responses already carry tool_calls; no real binding needed
        return self

    def _generate(self, messages, stop=None, run_manager=None, **kwargs) -> ChatResult:
        if messages and isinstance(messages[0], SystemMessage):
            self.seen_system.append(messages[0].text)
        msg = self.script.pop(0)
        return ChatResult(generations=[ChatGeneration(message=msg)])


def _tc(name: str, args: dict[str, Any], i: int = 0) -> AIMessage:
    return AIMessage(
        content="",
        tool_calls=[{"name": name, "args": args, "id": f"call_{i}"}],
    )


def _tool_outputs(result) -> list[str]:
    return [m.content for m in result["messages"] if isinstance(m, ToolMessage)]


def test_per_run_creates_and_deletes(server):
    model = ScriptedChatModel(
        script=[
            _tc("calcside_exec", {"code": 'fs.write("note.txt", "hello")'}),
            _tc("calcside_exec", {"code": 'print(fs.read("note.txt"))'}),
            AIMessage(content="done"),
        ]
    )
    mw = CalcsideMiddleware(base_url=server)
    agent = create_agent(model, tools=[], middleware=[mw], system_prompt="Be helpful.")
    result = agent.invoke({"messages": [{"role": "user", "content": "go"}]})

    outs = _tool_outputs(result)
    assert any("hello" in o for o in outs)

    # injected prompt present and the user's system prompt preserved
    assert model.seen_system, "model never saw a system message"
    sys_msg = model.seen_system[0]
    assert "Be helpful." in sys_msg
    assert "calcside sandbox" in sys_msg
    assert "Starlark" in sys_msg

    # per-run instance was deleted (404 or tombstoned status)
    iid = mw.last_instance_id
    assert iid
    try:
        assert Client(base_url=server).get_instance(iid)["status"] == "deleted"
    except CalcsideError as e:
        assert e.status == 404


def test_reuse_mode_persists_across_runs(server):
    c = Client(base_url=server)
    inst = c.create_instance({"capabilities": {"fs": {}}, "ttl_seconds": 900})
    iid = inst["id"]
    try:
        mw = CalcsideMiddleware(base_url=server, instance_id=iid)

        m1 = ScriptedChatModel(
            script=[_tc("calcside_exec", {"code": "x = 41"}), AIMessage(content="ok")]
        )
        create_agent(m1, tools=[], middleware=[mw]).invoke(
            {"messages": [{"role": "user", "content": "set x"}]}
        )

        m2 = ScriptedChatModel(
            script=[
                _tc("calcside_exec", {"code": "print(x + 1)"}),
                AIMessage(content="ok"),
            ]
        )
        r2 = create_agent(m2, tools=[], middleware=[mw]).invoke(
            {"messages": [{"role": "user", "content": "print x+1"}]}
        )
        assert any("42" in o for o in _tool_outputs(r2))

        # reused instance is still running — middleware never deletes it
        assert c.get_instance(iid)["status"] == "running"
        # injected system message contains the server-rendered prompt
        server_prompt = c.prompt(iid)["prompt"]
        assert server_prompt in m2.seen_system[0]
    finally:
        c.delete_instance(iid)


def test_system_prompt_string_appended(server):
    mw = CalcsideMiddleware(
        base_url=server,
        instance_id=None,
        spec={"capabilities": {"io": {}}, "ttl_seconds": 300},
        system_prompt="EXTRA INSTRUCTION XYZ",
    )
    model = ScriptedChatModel(
        script=[_tc("calcside_exec", {"code": "print(1)"}), AIMessage(content="ok")]
    )
    create_agent(model, tools=[], middleware=[mw]).invoke(
        {"messages": [{"role": "user", "content": "go"}]}
    )
    sys_msg = model.seen_system[0]
    assert "calcside sandbox" in sys_msg  # server prompt present
    assert sys_msg.endswith("EXTRA INSTRUCTION XYZ")


def test_system_prompt_callable_receives_server_prompt(server):
    captured: dict[str, Any] = {}

    def override(server_prompt: str, resp: dict[str, Any]) -> str:
        captured["prompt"] = server_prompt
        captured["resp"] = resp
        return "CUSTOM PROMPT"

    mw = CalcsideMiddleware(
        base_url=server,
        spec={"capabilities": {"io": {}}, "ttl_seconds": 300},
        system_prompt=override,
    )
    model = ScriptedChatModel(
        script=[_tc("calcside_exec", {"code": "print(1)"}), AIMessage(content="ok")]
    )
    create_agent(model, tools=[], middleware=[mw]).invoke(
        {"messages": [{"role": "user", "content": "go"}]}
    )
    assert model.seen_system[0] == "CUSTOM PROMPT"
    assert "calcside sandbox" in captured["prompt"]
    assert captured["resp"]["tools"]["exec"] == "calcside_exec"
    assert "io" in captured["resp"]["capabilities"]


def test_context_override_wins(server):
    c = Client(base_url=server)
    inst = c.create_instance({"capabilities": {"io": {}}, "ttl_seconds": 900})
    iid = inst["id"]
    try:
        # per-run middleware, but this run's context points at `iid`
        mw = CalcsideMiddleware(base_url=server)
        model = ScriptedChatModel(
            script=[
                _tc("calcside_exec", {"code": 'print("ctx")'}),
                AIMessage(content="ok"),
            ]
        )
        agent = create_agent(model, tools=[], middleware=[mw], context_schema=dict)
        agent.invoke(
            {"messages": [{"role": "user", "content": "go"}]},
            context={"calcside_instance_id": iid},
        )
        # context override won over the ctor's per-run spec
        assert mw.last_instance_id == iid
        # context-override instance must not be deleted
        assert c.get_instance(iid)["status"] == "running"
    finally:
        c.delete_instance(iid)


def test_script_error_surfaces_as_text(server):
    model = ScriptedChatModel(
        script=[
            _tc("calcside_exec", {"code": "def broken(:"}),
            AIMessage(content="saw the error"),
        ]
    )
    mw = CalcsideMiddleware(base_url=server)
    agent = create_agent(model, tools=[], middleware=[mw])
    result = agent.invoke({"messages": [{"role": "user", "content": "go"}]})
    outs = _tool_outputs(result)
    assert any("[error: syntax]" in o for o in outs)
    assert result["messages"][-1].content == "saw the error"


async def test_async_path(server):
    model = ScriptedChatModel(
        script=[
            _tc("calcside_exec", {"code": 'print("async-hi")'}),
            AIMessage(content="ok"),
        ]
    )
    mw = CalcsideMiddleware(base_url=server)
    agent = create_agent(model, tools=[], middleware=[mw])
    result = await agent.ainvoke({"messages": [{"role": "user", "content": "go"}]})
    assert any("async-hi" in o for o in _tool_outputs(result))


def test_checkpointer_per_run_new_instance_each_turn(server):
    from langgraph.checkpoint.memory import InMemorySaver

    c = Client(base_url=server)
    mw = CalcsideMiddleware(base_url=server)
    model = ScriptedChatModel(
        script=[
            _tc("calcside_exec", {"code": 'print("t1")'}),
            AIMessage(content="turn1 done"),
            _tc("calcside_exec", {"code": 'print("t2")'}),
            AIMessage(content="turn2 done"),
        ]
    )
    agent = create_agent(model, tools=[], middleware=[mw], checkpointer=InMemorySaver())
    cfg = {"configurable": {"thread_id": "t1"}}

    r1 = agent.invoke({"messages": [{"role": "user", "content": "go"}]}, config=cfg)
    assert any("t1" in o for o in _tool_outputs(r1))
    id1 = mw.last_instance_id

    r2 = agent.invoke({"messages": [{"role": "user", "content": "again"}]}, config=cfg)
    assert any("t2" in o for o in _tool_outputs(r2))
    id2 = mw.last_instance_id
    assert id1 != id2

    # both per-run instances are deleted (404 or tombstoned)
    for iid in (id1, id2):
        try:
            assert c.get_instance(iid)["status"] == "deleted"
        except CalcsideError as e:
            assert e.status == 404


def test_checkpointer_context_override_switches(server):
    from langgraph.checkpoint.memory import InMemorySaver

    c = Client(base_url=server)
    ext = c.create_instance({"capabilities": {"io": {}}, "ttl_seconds": 900})
    try:
        mw = CalcsideMiddleware(base_url=server)
        model = ScriptedChatModel(
            script=[
                _tc("calcside_exec", {"code": 'print("a")'}),
                AIMessage(content="t1"),
                _tc("calcside_exec", {"code": 'print("b")'}),
                AIMessage(content="t2"),
            ]
        )
        agent = create_agent(
            model,
            tools=[],
            middleware=[mw],
            checkpointer=InMemorySaver(),
            context_schema=dict,
        )
        cfg = {"configurable": {"thread_id": "t2"}}
        agent.invoke({"messages": [{"role": "user", "content": "go"}]}, config=cfg)
        id1 = mw.last_instance_id

        agent.invoke(
            {"messages": [{"role": "user", "content": "again"}]},
            config=cfg,
            context={"calcside_instance_id": ext["id"]},
        )
        assert mw.last_instance_id == ext["id"]
        # the override target is still running; the abandoned per-run
        # instance was cleaned up
        assert c.get_instance(ext["id"])["status"] == "running"
        try:
            assert c.get_instance(id1)["status"] == "deleted"
        except CalcsideError as e:
            assert e.status == 404
    finally:
        c.delete_instance(ext["id"])


def test_decision_helpers():
    plan = CalcsideMiddleware._plan
    resume = CalcsideMiddleware._resume_or_replace

    # no stored, no desired → create
    assert plan(None, None) == ("create", None)
    # stored only → resume
    assert plan("ins_a", None) == ("resume", "ins_a")
    # desired differs from stored → switch
    assert plan("ins_a", "ins_b") == ("switch", "ins_b")
    # desired, nothing stored → adopt
    assert plan(None, "ins_b") == ("adopt", "ins_b")
    # desired == stored → resume
    assert plan("ins_a", "ins_a") == ("resume", "ins_a")

    # stored running → keep
    assert resume("ins_a", "running", owned=True) == "keep"
    assert resume("ins_a", "running", owned=False) == "keep"
    # stored gone, owned → create fresh
    assert resume("ins_a", None, owned=True) == "create"
    assert resume("ins_a", "deleted", owned=True) == "create"
    # stored gone, reused → clear error
    with pytest.raises(RuntimeError, match="not running"):
        resume("ins_a", "deleted", owned=False)
    with pytest.raises(RuntimeError, match="not running"):
        resume("ins_a", None, owned=False)


def test_empty_output_hint(server):
    model = ScriptedChatModel(
        script=[
            _tc("calcside_exec", {"code": "x = 1"}),
            AIMessage(content="ok"),
        ]
    )
    mw = CalcsideMiddleware(base_url=server)
    agent = create_agent(model, tools=[], middleware=[mw])
    result = agent.invoke({"messages": [{"role": "user", "content": "go"}]})
    assert any("use print()" in o for o in _tool_outputs(result))


class _Echo(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"tok=" + self.headers.get("X-Token", "").encode())
        self.wfile.write(b" body=" + body)

    def do_GET(self):
        self.do_POST()

    def log_message(self, *a):
        pass


def test_secret_placeholder_and_redaction(server):
    echo = HTTPServer(("127.0.0.1", 0), _Echo)
    threading.Thread(target=echo.serve_forever, daemon=True).start()
    port = echo.server_address[1]
    try:
        mw = CalcsideMiddleware(
            base_url=server,
            spec={
                "capabilities": {"net": {"allow_hosts": [f"127.0.0.1:{port}"]}},
                "policies": [],
                "secrets": {
                    "T": {"value": "s3cr3t", "allowed_domains": [f"127.0.0.1:{port}"]}
                },
                "ttl_seconds": 600,
            },
        )
        model = ScriptedChatModel(
            script=[
                _tc(
                    "calcside_exec",
                    {
                        "code": 'print(net.get("http://127.0.0.1:PORT/", headers={"X-Token": "{{secrets.T}}"})["body"])'.replace(
                            "PORT", str(port)
                        )
                    },
                ),
                AIMessage(content="ok"),
            ]
        )
        agent = create_agent(model, tools=[], middleware=[mw])
        result = agent.invoke({"messages": [{"role": "user", "content": "go"}]})
        outs = _tool_outputs(result)
        assert any("[REDACTED:T]" in o for o in outs)
        assert not any("s3cr3t" in o for o in outs)
        # prompt lists the secret name but never the value
        assert "secrets.T" in model.seen_system[0] or "T`" in model.seen_system[0]
        assert "s3cr3t" not in model.seen_system[0]
    finally:
        echo.shutdown()
