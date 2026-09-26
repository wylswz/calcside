# LangChain agent example

A LangChain agent that runs code in a calcside Starlark sandbox, with the
`tavily` extension capability for web search. Served locally by
`langgraph dev` — you chat with it in Studio, so the prompt is whatever
you type. Depends on the local SDK checkout via a uv path source
(`../../sdk/python`), so SDK changes are picked up without publishing.

## Setup

```bash
# from the repo root: start a dev-mode server on :8787
# (enables the secrets vault and --ext-local-roots examples/capabilities)
make serve-dev

# create a vault secret named TAVILY_API_KEY in the web console
# (Secrets page), with allowed domain api.tavily.com — vault secrets are
# session-only, so this must be done by hand, not via API key

# in this directory
uv sync
cp .env.example .env   # set OPENAI_API_KEY or ANTHROPIC_API_KEY
uv run langgraph dev   # serves the graph + Studio UI
```

`CALCSIDE_SERVER` defaults to `http://127.0.0.1:8787` in `agent.py`; set it
in `.env` to override.

`langgraph dev` prints a Studio URL (the graph `calcside_agent` is defined
in `langgraph.json`). Type a task there — e.g. "use tavily search to find
the latest stable Python release and write the results to
/work/results.json". Each run gets a fresh sandbox instance, deleted when
the run finishes (a continuing thread resumes the same instance).

## What it does

`CalcsideMiddleware` creates a sandbox instance per run with `fs`, `net`
(allow_hosts `api.tavily.com`), and the `tavily` extension loaded from
`examples/capabilities/tavily`. The instance spec references vault secret
`TAVILY_API_KEY`; the extension's `api_key` config is the
`{{secrets.TAVILY_API_KEY}}` placeholder, so the key is injected by the
`net` capability at send time — the script and the model only ever see the
placeholder, and the key can only leave via `api.tavily.com`.

The middleware fetches the server-generated system prompt describing the
granted capabilities (including `ext.tavily.search`) and registers
`calcside_exec`, `calcside_list_files`, and `calcside_read_file` tools.
See `sdk/python/README.md` for the full middleware reference (reuse mode,
context overrides, prompt customization).
