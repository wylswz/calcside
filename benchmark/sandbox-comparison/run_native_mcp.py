#!/usr/bin/env python3
"""Separate native Calcside MCP/prompt track; never merge into uniform v2."""

import argparse
import hashlib
import json
import os
import random
import time
from datetime import datetime, timezone
from pathlib import Path

from langchain.agents import create_agent
from langchain_core.tools import StructuredTool
from langchain_openai import ChatOpenAI

import csclient as cs
from backends_v2 import CalcsideBackend
from evaluation_v2 import TASKS, summarize, validate
from mcp_transport_v2 import NativeMCP
from run_v2 import BudgetExceeded, Recorder, write_json


CONTROLLER = (
    "Complete the user's task in the provided workspace. Create the requested "
    "artifact before your final answer. Do not access credentials, install "
    "packages or access files outside the workspace. You have at most eight "
    "model calls and twelve tool calls. The following are the execution "
    "server's instructions, supplied verbatim:\n\n"
)


class NativeRecorder(Recorder):
    """Count native MCP outcomes by CallToolResult.isError, not JSON stdout."""

    def wrap_tool_call(self, request, handler):
        if self.tool_calls >= 12:
            raise BudgetExceeded("tool call budget exhausted")
        self.tool_calls += 1
        started = time.perf_counter()
        event = {
            "kind": "tool", "index": self.tool_calls,
            "start_s": started - self.origin,
            "tool_call_id": request.tool_call["id"],
            "name": request.tool_call["name"], "args": request.tool_call["args"],
        }
        try:
            response = handler(request)
            raw = response.artifact
            if not isinstance(raw, dict) or "content" not in raw:
                raise ValueError("missing native MCP result artifact")
            failed = bool(raw.get("isError"))
            event.update(ok=not failed, observation=raw,
                         error_kind="mcp_isError" if failed else None)
            return response
        except Exception as exc:
            event.update(ok=False, error_kind="tool_protocol",
                         error="%s: %s" % (type(exc).__name__, str(exc)))
            raise
        finally:
            event["duration_s"] = time.perf_counter() - started
            self.events.append(event)


def native_tools(client, schemas):
    def bind(name):
        def call(**arguments):
            raw = client.call_tool(name, arguments)
            return raw["content"], raw
        return call
    return [
        StructuredTool.from_function(
            name=item["name"], description=item.get("description", ""),
            args_schema=item["inputSchema"], func=bind(item["name"]),
            response_format="content_and_artifact",
        )
        for item in schemas
    ]


def trial(task_id, repetition):
    adapter = CalcsideBackend()
    client = None
    recorder = NativeRecorder()
    row = {
        "backend": "calcside_mcp", "task": task_id, "repetition": repetition,
        "passed": False, "events": recorder.events, "setup_s": None,
        "loop_s": None, "verification_s": None, "cleanup_error": None,
        "setup_error": None, "agent_error": None,
    }
    setup_ok = False
    started = time.perf_counter()
    try:
        adapter.setup(TASKS[task_id]["files"])
        # The controller owns lifecycle; agent gets the real bound-instance MCP surface.
        client = NativeMCP(adapter._iid)
        initialized = client.initialize()
        schemas = client.list_tools()
        instructions = initialized["instructions"]
        if not instructions:
            raise ValueError("MCP server supplied no instructions")
        row["official_prompt"] = instructions
        row["official_prompt_sha256"] = hashlib.sha256(instructions.encode()).hexdigest()
        row["mcp_initialize"] = initialized
        row["mcp_tools"] = schemas
        row["adapter"] = adapter.metadata()
        setup_ok = True
    except Exception as exc:
        row["setup_error"] = "%s: %s" % (type(exc).__name__, str(exc))
    row["setup_s"] = time.perf_counter() - started
    try:
        if setup_ok:
            model = ChatOpenAI(
                model="deepseek-flash", base_url="https://api.deepseek.com",
                api_key=os.environ["DEEPSEEK_API_KEY"], temperature=0,
                max_tokens=1200, timeout=90, max_retries=0,
                use_responses_api=False, model_kwargs={"parallel_tool_calls": False},
            )
            agent = create_agent(
                model=model, tools=native_tools(client, schemas), middleware=[recorder],
                system_prompt=CONTROLLER + instructions,
            )
            started = time.perf_counter()
            try:
                result = agent.invoke(
                    {"messages": [{"role": "user", "content": TASKS[task_id]["prompt"]}]},
                    config={"recursion_limit": 40, "max_concurrency": 1})
                row["final_answer"] = result["messages"][-1].content
            except Exception as exc:
                row["agent_error"] = "%s: %s" % (type(exc).__name__, str(exc))
            row["loop_s"] = time.perf_counter() - started
            started = time.perf_counter()
            try:
                row["artifact"] = adapter.read_file(TASKS[task_id]["artifact"])
                row["verification"] = validate(task_id, row["artifact"])
                row["passed"] = row["verification"]["ok"]
            except Exception as exc:
                row["verification"] = {
                    "ok": False, "reason": "%s: %s" % (type(exc).__name__, str(exc))}
            row["verification_s"] = time.perf_counter() - started
            status, notebook = cs.notebook(adapter._iid)
            row["notebook_after"] = notebook if status == 200 else {"status": status}
    except Exception as exc:
        row["agent_error"] = "%s: %s" % (type(exc).__name__, str(exc))
    finally:
        started = time.perf_counter()
        errors = []
        for close in ([client.close] if client is not None else []) + [adapter.close]:
            try:
                close()
            except Exception as exc:
                errors.append("%s: %s" % (type(exc).__name__, str(exc)))
        row["cleanup_error"] = "; ".join(errors) if errors else None
        row["cleanup_s"] = time.perf_counter() - started
    if row["loop_s"] is not None:
        row["model_s"] = sum(e["duration_s"] for e in recorder.events if e["kind"] == "model")
        row["tool_s"] = sum(e["duration_s"] for e in recorder.events if e["kind"] == "tool")
        row["residual_s"] = row["loop_s"] - row["model_s"] - row["tool_s"]
    return row


def main():
    os.environ["LANGSMITH_TRACING"] = "false"
    os.environ["LANGCHAIN_TRACING_V2"] = "false"
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    order = [(task, rep) for task in TASKS for rep in range(3)]
    random.Random(20261114).shuffle(order)
    manifest = {
        "track": "native-mcp-official-prompt", "started_at": datetime.now(timezone.utc).isoformat(),
        "order": order, "model": "deepseek-flash", "temperature": 0,
        "max_model_calls": 8, "max_tool_calls": 12, "max_output_tokens": 1200,
        "controller_prompt": CONTROLLER,
        "tool_success_definition": "CallToolResult.isError is not true",
        "source_sha256": {
            name: hashlib.sha256((Path(__file__).parent / name).read_bytes()).hexdigest()
            for name in ("run_native_mcp.py", "mcp_transport_v2.py", "run_v2.py",
                         "evaluation_v2.py", "backends_v2.py")
        },
    }
    write_json(args.output / "manifest.json", manifest)
    rows = []
    for index, (task, repetition) in enumerate(order):
        row = trial(task, repetition)
        rows.append(row)
        write_json(args.output / ("%03d-%s-%d.json" % (index, task, repetition)), row)
        write_json(args.output / "trials.json", rows)
        write_json(args.output / "summary.json", summarize(rows))
        print("%d/12 native-mcp %s r%d passed=%s" %
              (index + 1, task, repetition, row["passed"]), flush=True)
    manifest["finished_at"] = datetime.now(timezone.utc).isoformat()
    write_json(args.output / "manifest.json", manifest)


if __name__ == "__main__":
    main()
