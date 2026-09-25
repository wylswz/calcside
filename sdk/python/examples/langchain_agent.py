"""Example: a LangChain agent with a calcside sandbox.

Prereqs:
  - calcside server running (e.g. `make serve-dev` or `bin/calcside serve --dev`)
  - OPENAI_API_KEY or ANTHROPIC_API_KEY in the environment
  - `uv run --extra langchain python examples/langchain_agent.py`
"""

from __future__ import annotations

import os

from langchain.agents import create_agent

from calcside.langchain import CalcsideMiddleware


def make_model():
    if os.environ.get("OPENAI_API_KEY"):
        from langchain_openai import ChatOpenAI

        return ChatOpenAI(model="gpt-4o-mini")
    if os.environ.get("ANTHROPIC_API_KEY"):
        from langchain_anthropic import ChatAnthropic

        return ChatAnthropic(model="claude-haiku-4-5")
    raise SystemExit("set OPENAI_API_KEY or ANTHROPIC_API_KEY")


def main() -> None:
    # Per-run mode: a fresh instance is created for this run and deleted
    # afterwards (the server-side TTL still reaps it if we crash).
    middleware = CalcsideMiddleware(
        spec={"capabilities": {"fs": {}}, "ttl_seconds": 900},
    )
    agent = create_agent(make_model(), tools=[], middleware=[middleware])
    result = agent.invoke(
        {
            "messages": [
                {
                    "role": "user",
                    "content": "Create /work/fib.py computing fib(20), run it, and tell me the answer.",
                }
            ]
        }
    )
    print(result["messages"][-1].content)

    # Reuse mode: same instance across runs — state persists.
    # from calcside import Client
    # inst = Client().create_instance({"capabilities": {"fs": {}}, "ttl_seconds": 3600})
    # mw = CalcsideMiddleware(instance_id=inst["id"])
    # agent = create_agent(make_model(), tools=[], middleware=[mw])


if __name__ == "__main__":
    main()
