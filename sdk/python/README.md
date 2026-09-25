# calcside Python SDK

Client and LangChain middleware for the [calcside](../../README.md) Starlark sandbox.

## Install

```bash
uv add "calcside[langchain]"        # client + LangChain middleware
uv add calcside                    # client only
# or from a checkout: pip install -e "sdk/python[langchain]"
```

## Client

```python
from calcside import Client

c = Client()  # CALCSIDE_SERVER / CALCSIDE_API_KEY env vars, or base_url=/api_key=
inst = c.create_instance({"capabilities": {"fs": {}}, "ttl_seconds": 900})
r = c.exec(inst["id"], 'fs.write("a.txt", "hi")\nprint(fs.read("a.txt"))')
print(r.output)  # "hi\n"
print(c.files(inst["id"], "/work/a.txt")["content"])
c.delete_instance(inst["id"])
```

`AsyncClient` mirrors the same methods. Non-2xx responses raise
`CalcsideError(status, code, message)` parsed from the error envelope.
Dev-mode servers accept no credentials; the client always sends the
`X-Requested-With: calcside` CSRF header.

## LangChain middleware

```python
from langchain.agents import create_agent
from calcside.langchain import CalcsideMiddleware

# per-run: a fresh instance is created for each agent run and deleted after
mw = CalcsideMiddleware(spec={"capabilities": {"fs": {}}, "ttl_seconds": 900})
agent = create_agent(model, tools=[], middleware=[mw])
agent.invoke({"messages": [{"role": "user", "content": "compute fib(20) and save it"}]})

# reuse: keep one instance across runs (state persists); middleware never deletes it
mw = CalcsideMiddleware(instance_id="ins_...")
```

The middleware injects a generated system prompt describing the granted
capabilities, Starlark-vs-Python differences, secrets/env usage, and
limits, and registers `calcside_exec`, `calcside_list_files`, and
`calcside_read_file` tools.

### Resolution precedence

`context={"calcside_instance_id": "..."}` in `agent.invoke(context=...)`
(configure `create_agent(..., context_schema=dict)`) overrides the ctor
`instance_id`, which overrides per-run creation from `spec`.

### Prompt customization

`system_prompt="extra text"` appends to the generated prompt;
`system_prompt=lambda info: ...` (a `SandboxInfo` is passed) replaces it.

### Lifecycle

Per-run instances are deleted in `after_agent` (disable via
`delete_on_finish=False`). If a run crashes permanently, the server-side
TTL reaps the instance. Checkpointed runs resumed after an interrupt keep
the same instance (state is tracked, not ephemeral).

## Development

```bash
uv sync --extra langchain
uv run --extra langchain pytest -q   # builds + boots a dev-mode server
uv run ruff check . && uv run ruff format --check .
```

`examples/langchain_agent.py` is a real-model example (needs
`OPENAI_API_KEY` or `ANTHROPIC_API_KEY`).
