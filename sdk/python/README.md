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

Instance policies use the same spec for `Client`, `AsyncClient`, and the middleware:

```python
spec = {
    "capabilities": {"net": {"allow_hosts": ["internal.example.com"]}},
    "policies": [],
}
inst = c.create_instance(spec)
```

An omitted (or `None`) `policies` field selects `builtin.block_private_network` by default. An explicit list replaces defaults; `[]` opts out of the built-in private/reserved-IP restriction. To use a library policy while keeping network protection, pass `"policies": ["builtin.block_private_network", "my_policy"]`. Server policies and host/secret allowlists still apply.

## Data utilities

All utility modules are available through `exec` without extra capability grants;
file operations still require `fs`:

```python
inst = c.create_instance({"capabilities": {"fs": {}}})
result = c.exec(
    inst["id"],
    """
rows = csv.parse_dicts("id,amount_minor\\nA,1200\\nB,300\\n")
fs.write("output/selected.csv", csv.format_dicts(
    [r for r in rows if int(r["amount_minor"]) > 1000],
    columns=["id", "amount_minor"],
))
print(json.encode({"path": "/work/output/selected.csv", "rows": len(rows)}))
""",
)
```

URL, CSV, Base64, SHA-256, RE2 regex, and UTC date conversion share the same
bounded implementation in scripts and extensions. `datetime` timestamps are
Unix milliseconds; `base64.decode` returns bytes. Inspect the server prompt
for parameter defaults and bounds rather than assuming Python library APIs.

## Artifact preview and downloads

Both `Client` and `AsyncClient` provide `preview_artifact(instance_id, path,
mode="source")`, `download_file(instance_id, path)`, and
`export_files(instance_id, paths)` (ZIP). Downloads return `ArtifactDownload`
with `content: bytes`, `filename`, `redacted`, and `file_count`; saving to local
storage is an explicit caller action.

```python
from pathlib import Path

preview = client.preview_artifact(instance_id, "/work/output/differences.csv")
print(preview.get("csv_rows", []))
download = client.download_file(instance_id, "/work/output/differences.csv")
Path("differences.csv").write_bytes(download.content)
archive = client.export_files(instance_id, ["/work/output"])
Path("output.zip").write_bytes(archive.content)
```

Preview tables/source may be visibly truncated; successful downloads are complete
within server limits and are never just the preview prefix. Redaction may alter
text, CSV structure, or HTML. ZIP paths are relative to `/work`; a denied or
unsupported member fails the entire request. HTTP transfer errors propagate rather
than returning a partial successful download. Files must be downloaded while the
instance is live; the SDK does not automatically keep it alive or retain artifacts.

HTML modes `static` and `interactive` require the deployment's isolated preview
domain. Local JS/CSS references inside the entry directory are automatically
embedded in the preview; the SDK needs no extra resource options. Source reads and
downloads remain the original redacted files; use ZIP for a multi-file report.
Interactive mode also requires operator opt-in. Treat `preview_url` as a
short-lived bearer credential: do not log it or attach API credentials when opening
it. Browser scripts are outside Starlark resource limits, and CSP is not a complete
network firewall. See the repository README for deployment and security boundaries.

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

The middleware fetches a server-generated system prompt
(`GET /api/v1/instances/{id}/prompt`) describing the granted
capabilities, Starlark-vs-Python differences, secrets/env usage, and
limits — clients never assemble it themselves — and registers
`calcside_exec`, `calcside_list_files`, and `calcside_read_file` tools.

### Resolution precedence

`context={"calcside_instance_id": "..."}` in `agent.invoke(context=...)`
(configure `create_agent(..., context_schema=dict)`) overrides the ctor
`instance_id`, which overrides per-run creation from `spec`.

### Prompt customization

`system_prompt="extra text"` appends to the server-generated prompt;
`system_prompt=lambda prompt, resp: ...` replaces it — the callable
receives the server prompt and the full `/prompt` response dict.

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
