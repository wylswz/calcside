"""LangChain agent with a calcside sandbox (fs + net + tavily extension).

Served by `langgraph dev` — chat with it in Studio; each run gets a fresh
sandbox instance (deleted on finish).

Prereqs:
  - calcside dev server: `make serve-dev` (vault + local ext roots enabled)
  - a vault secret named TAVILY_API_KEY (web console -> Secrets) with
    allowed domain api.tavily.com
  - OPENAI_API_KEY or ANTHROPIC_API_KEY (via env or .env)
"""

from __future__ import annotations

import os
from pathlib import Path

from calcside import CalcsideError, Client
from calcside.langchain import CalcsideMiddleware
from langchain.agents import create_agent

os.environ.setdefault("CALCSIDE_SERVER", "http://127.0.0.1:8787")

REPO_ROOT = Path(__file__).resolve().parents[2]
TAVILY_SOURCE = REPO_ROOT / "contrib" / "tavily"
TAVILY_HOSTS = ["api.tavily.com"]


def make_model():
    if os.environ.get("OPENAI_API_KEY"):
        from langchain_openai import ChatOpenAI

        return ChatOpenAI(model="gpt-4o-mini")
    if os.environ.get("ANTHROPIC_API_KEY"):
        from langchain_anthropic import ChatAnthropic

        return ChatAnthropic(model="claude-haiku-4-5")
    raise SystemExit("set OPENAI_API_KEY or ANTHROPIC_API_KEY (env or .env)")


def check_tavily_secret(client: Client) -> None:
    """Verify the vault has a TAVILY_API_KEY secret; point the user at the
    web console to create it otherwise (secrets are session-only, so an
    API-key client cannot create them)."""
    try:
        names = {s["name"] for s in client.list_secrets()}
    except CalcsideError as e:
        raise SystemExit(
            f"cannot list vault secrets ({e}) — start the server with "
            "--secret-key (make serve-dev sets one)"
        ) from e
    if "TAVILY_API_KEY" not in names:
        raise SystemExit(
            "vault secret TAVILY_API_KEY not found — create it in the web "
            "console (Secrets page) with allowed domain api.tavily.com, "
            "then rerun."
        )


client = Client()
check_tavily_secret(client)

middleware = CalcsideMiddleware(
    client=client,
    spec={
        "capabilities": {
            "fs": {},
            "net": {"allow_hosts": TAVILY_HOSTS, "methods": ["GET", "POST"]},
            "ext": {
                "tavily": {
                    "source": str(TAVILY_SOURCE),
                    "config": {"api_key": "{{secrets.TAVILY_API_KEY}}"},
                }
            },
        },
        "secrets": {
            "TAVILY_API_KEY": {
                "ref": "TAVILY_API_KEY",
                "allowed_domains": TAVILY_HOSTS,
            }
        },
        "ttl_seconds": 900,
    },
)

# Exposed via langgraph.json for `langgraph dev`.
graph = create_agent(make_model(), tools=[], middleware=[middleware])
