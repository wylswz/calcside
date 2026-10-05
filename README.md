# calcside

![](./docs/cover.png)

A lightweight code-execution sandbox for AI agents, built on Starlark. There are no containers and no virtual machines: **capabilities are the security boundary**. An agent writes a Starlark script; the only globals it sees are the capabilities it was granted, and every operation those capabilities perform is gated, policy-checked, and audited.

The design follows [citron](https://github.com/mishudark/citron), a Go implementation of the capability-safe agent harness from *[Tracking Capabilities for Safer Agents](https://arxiv.org/abs/2603.00991)* (Odersky et al., CAIS '26). Where the paper enforces safety statically via Scala 3 capture checking and citron via AST analysis, calcside takes the runtime route: a per-instance **Gate** mediates every side effect, Rego policies can veto ops before and after they run, and secrets are injected into outbound requests at send time — scripts can reference them but never read them.

## Quick start

Install Atlas CLI 1.2.2 for local development and database tests. `make dev` and `make serve-dev` apply migrations before starting the API.

```bash
make build                 # web console + bin/calcside + bin/calcside-worker
make dev                   # backend (--dev, anonymous) on :8787 + Vite console on :5173 — no login
```

Use the web console at `http://localhost:5173`, or integrate through the HTTP API or [Python SDK](sdk/python/README.md). With the SDK installed, connect to the dev server from your application:

```python
from calcside import Client

with Client(base_url="http://127.0.0.1:8787", api_key="") as client:
    instance = client.create_instance({"capabilities": {"fs": {}}, "ttl_seconds": 900})
    try:
        result = client.exec(instance["id"], 'fs.write("a.txt", "hi")\nprint(fs.read("a.txt"))')
        print(result.output, end="")
    finally:
        client.delete_instance(instance["id"])
```

Outside dev mode, create an API key in the web console and pass it via `CALCSIDE_API_KEY` or the SDK's `api_key` argument. Manage library policies through the console or `/api/v1/policies`, then select them in the instance spec.

Instance spec (`POST /api/v1/instances`):

```json
{"ttl_seconds": 900, "labels": {"team": "x"},
 "capabilities": {"fs": {"quota_bytes": 67108864}, "net": {"allow_hosts": ["api.github.com"]}, "io": {}},
 "policies": ["builtin.block_private_network", "deny_net_hosts"],
 "limits": {"exec_timeout_ms": 30000, "max_steps": 10000000, "max_output_bytes": 1048576}}
```

For an instance that needs internal HTTP access, pass `"policies": []` (or an explicit list without `builtin.block_private_network`). This does not bypass the instance's host allowlist, secret domain restrictions, or mandatory server policies. No network capability means no network access regardless of policy selection.

## Database migrations

SQLite is the default; PostgreSQL is selected with `--store postgres` and a PostgreSQL DSN. The server only connects to the database: **run migrations before starting it**. Atlas CLI manages migration history, checksums, locks, and transactions; the application binary does not need Atlas installed.

```bash
export ATLAS_DB_URL="sqlite://$(pwd)/calcside.db"
make migrate-apply
make migrate-status
bin/calcside serve --dsn calcside.db
```

For PostgreSQL, set `ATLAS_DB_URL` to the database URL with `search_path=public`, then run `make migrate-apply MIGRATION_ENV=postgres`. Start the server with `--store postgres` and the corresponding `--dsn` (or `CALCSIDE_STORE` / `CALCSIDE_DSN`). Supply real credentials through your deployment's secret configuration, not committed files.

To add a migration:

```bash
make migrate-new name=add_user_avatar
# Hand-write the SQLite and PostgreSQL SQL in the two new files.
make migrate-hash
make migrate-validate
make test-postgres
```

The files live in `internal/store/gormstore/migrations/{sqlite,postgres}` and share a version and name. Keep GORM row structs in sync, but do not generate SQL from them. Never modify an applied migration; add a new one instead. `migrate-validate` checks both directories' checksums. Go database tests apply the migrations to fresh SQLite databases; `make test-postgres` applies them and runs the same store conformance suite on disposable PostgreSQL databases (Docker required).

For an existing **unversioned SQLite database**, back it up and confirm its schema already matches `20261004092922_baseline.sql` before marking the baseline:

```bash
atlas migrate apply --env sqlite --baseline 20261004092922
```

This uses `ATLAS_DB_URL`, records the baseline without executing its CREATE statements, and applies later migrations. It does **not** upgrade an older schema; in particular, a database still carrying `policies.enabled` needs a separately reviewed upgrade first. Do not pass `--baseline` for an empty database.

## Run with Docker

```bash
make docker-env                    # first run: generates docker/.env with a random shared key
docker compose -f docker/docker-compose.yml up --build
```

Compose first runs the pinned Atlas image as a one-shot `migrate` service and starts the API only after it succeeds. The migration service and API share `docker/data`; existing unversioned databases need the explicit baseline step above. For PostgreSQL, set `CALCSIDE_STORE=postgres`, `CALCSIDE_DSN`, and `ATLAS_DB_URL` to the same database/schema.

Compose starts the API (`:8080`) plus one execution worker; the API forwards instance execution to it over an HMAC-authenticated internal protocol. Drop local extensions into `docker/data/ext/` (bind-mounted) — workers resolve them through the API. See `docker/.env.example` for the remaining env knobs.

## LangChain integration

`sdk/python` ships a `calcside` client and `CalcsideMiddleware` for
LangChain v1 agents. It registers `calcside_exec` / `calcside_list_files` /
`calcside_read_file` tools and injects a system prompt describing the
granted capabilities, Starlark-vs-Python differences, and secret/env
usage.

```python
from langchain.agents import create_agent
from calcside.langchain import CalcsideMiddleware

agent = create_agent(model, tools=[], middleware=[
    # fresh sandbox per run, deleted afterwards:
    CalcsideMiddleware(spec={"capabilities": {"fs": {}}, "ttl_seconds": 900}),
    # or reuse one across runs: CalcsideMiddleware(instance_id="ins_...")
])
```

See [sdk/python/README.md](sdk/python/README.md) for install, modes, and
lifecycle details.
