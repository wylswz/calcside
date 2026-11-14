# Sandbox comparison

Controlled comparison of code-execution backends under one fixed agent
protocol. The measurement contract is `PROTOCOL.md`; do not edit it or the
harness files (`run_v2.py`, `evaluation_v2.py`, `micro_v2.py`,
`backends_v2.py`, `test_evaluation_v2.py`) — failed trials stay in the
denominators and raw rows are never rewritten.

## Current track

Uniform `execute_code` HTTP adapter + handcrafted language prompt, not
Calcside MCP/official SDK prompt; no Notebook tools.

`backends_v2.BACKENDS` maps five systems to zero-arg factories: `python`
(host `pykernel.py` in a temp dir), `calcside` (local API/worker,
Starlark, gated capabilities), `docker` (`python:3.13-slim`, per-action
`python -c`), `e2b` (hosted sandbox, persistent kernel) and `openshell`
(local gateway, docker driver, `/sandbox` workdir). The agent gets one
`execute_code(code)` tool; `AgentMiddleware.wrap_model_call` /
`wrap_tool_call` record timed events; the controller reads artifacts via
each adapter's control-plane `read_file` and validates contents
independently of the model's answer.

## Native MCP track (separate dataset)

`run_native_mcp.py` is a second, separate track — never merged into the
uniform v2 denominators. Each trial keeps the `CalcsideBackend` for
lifecycle and control-plane artifact reads, then binds a real Calcside
MCP session (`mcp_transport_v2.NativeMCP`, stdlib JSON-RPC over
streamable HTTP to `/mcp/instances/{iid}`) and exposes the server's
seven raw tools via `StructuredTool` (`response_format="content_and_artifact"`).
The system prompt is a fixed controller preamble plus the server's own
`initialize.instructions` verbatim; rows record `mcp_initialize`,
`mcp_tools`, `official_prompt` and `official_prompt_sha256`.

Tool-outcome semantics differ by design: the native track classifies a
tool call by MCP `CallToolResult.isError` (`isError` false = MCP-level
success, recorded as `error_kind="mcp_isError"` on failure), while the
uniform track's `execute_code` success means the adapter returned no
`runtime`/`policy_denied`/`timeout`/`transport` error. Artifact
validation (`evaluation_v2.validate`) is shared by both tracks.

## Expanded agent workflows

`run_v3.py` records task set B: log triage, ledger reconciliation and
5,000-row aggregation. Five uniform backends and a separate native MCP
condition each run three tasks three times (54 trials), with 64 model /
96 tool call caps. `PROTOCOL_V3.md` fixes the contract; `TASKS.md` and
the report appendix disclose every prompt and fixture.

The completed dataset is `results/v3-agentic/`; `audit_v3.py` checks its
source hashes, input bytes, artifacts and timing spans, then freezes
`report-data.json`. `render_agentic.py` reads that snapshot only.
`probe_memory_limits.py` records the separate explicitly capped
Docker/OpenShell test in `results/memory-limits.json`.

After a run, the presentation is rebuilt from the frozen views without
further model calls (`REPORT.md` and the figures in this repository were
produced exactly this way):

    .venv/bin/python audit_v3.py
    .venv/bin/python render_agentic.py
    .venv/bin/python write_report.py

For a paid run, invoke `run_v3.py --output results/<unused-name>`;
never replace a recorded dataset. The supplied audit/report tools target
the published dataset at `results/v3-agentic/`.

## Supplementary model replication — OpenAI `gpt-6.1-sol`

`run_openai_sol.py` is a supplementary replication track, separate from
the DeepSeek denominators: the three set-B tasks
(`evaluation_v3.TASKS_B`) on the two Calcside interfaces only —
uniform `execute_code` (`calcside`) and native MCP (`calcside_mcp`) —
3 repetitions each, 18 planned trials in `results/openai-sol-agentic/`.
The runner reuses the frozen v3 order filtered to those two conditions
and asserts `task-inputs.json` equals `TASKS_B` before starting.

Generation configuration differs from the DeepSeek runs by design
(`PROTOCOL_OPENAI_SOL.md`, `openai_sol_model.py`): `gpt-6.1-sol` over
the OpenAI Responses API with `reasoning_effort="medium"`,
`max_tokens=16384` (output cap including reasoning tokens) and
temperature omitted, versus `deepseek-flash` at temperature 0 with a
1,200-token output cap. The budgets are not matched; this is not a
causal same-budget model A/B comparison.

    .venv/bin/python run_openai_sol.py --output results/<unused-name>

After a run completes, the frozen audit view and figure default to the
published dataset `results/openai-sol-agentic/`:

    .venv/bin/python audit_openai_sol.py    # freezes report-data.json
    .venv/bin/python render_openai_sol.py   # writes figures-openai-sol/

`write_report.py` regenerates `REPORT.md`, including the §4.6
supplementary-model section and updated abstract.

## Datasets

Runs write datasets under `results/`; they are **not** committed. The
repository carries the harness, the report and the nine figures it
embeds, so a clone documents the method and the published numbers
without shipping megabytes of per-trial JSON. Everything below is
produced by the commands in this file.

- `results/v2-agent/` — official run: 4 tasks × 3 repetitions × 5
  backends = 60 planned trials, `deepseek-flash`, temperature 0,
  seed 20261114. `manifest.json` records commit, dirty-worktree status,
  dependency versions, platform, order and source hashes; controller
  interruptions are appended to `manifest.interruptions` (completed
  trials preserved, in-flight work marked unknown) and the run resumes
  via `resume_v2.py` without overwriting saved rows.
- `results/v2-micro/` — warm round trips: 30 samples per backend plus
  one excluded warmup, single session. Latency observation only, not an
  isolation or cold-start benchmark.
- `results/v2-native-mcp/` — official native MCP run: same 4 tasks ×
  3 repetitions on backend `calcside_mcp` (12 planned trials),
  `deepseek-flash`, temperature 0. Separate dataset; see the native MCP
  track section for the `isError` vs `execute_code` distinction.
- `results/figures-v2/` — presentation SVGs rendered from the frozen
  summaries above by `render_v2.py`.
- `results/openai-sol-agentic/` — supplementary `gpt-6.1-sol` run:
  3 set-B tasks × 3 repetitions × 2 Calcside conditions (`calcside`,
  `calcside_mcp`) = 18 planned trials; see the supplementary model
  section for the configuration differences.
- `results/figures-openai-sol/` — outcome and cost figures rendered by
  `render_openai_sol.py` from the frozen `report-data.json` only.

`.gitignore` keeps `results/` out of version control except the three
figure directories, so preflights, provider smokes, aborted attempts,
run logs, pid files and service state never reach a commit either. They
are setup evidence only and never enter a denominator.

Pilot-only files — `agent.py`, `probes.py`, `bench_local.py`,
`bench_e2b.py`, `bench_openshell.py`, `report_figures.py`,
`PILOT_REPORT.md` and the older pilot `results/` JSON/figures — were
produced under a different protocol. They are unsafe for comparison
claims, unsupported, and must not be rerun or cited as v2 evidence.
The v1 pilot set was moved (reversibly, nothing deleted) to the
external archive `/Users/wy/Desktop/workspace/calcside-sandbox-v1-archive-20261114/`
with `manifest.json` recording every path and SHA-256; security probe
observations were extracted to `results/security-observations.json`
before the move.

## Environment

Python env: existing `benchmark/sandbox-comparison/.venv`
(Python 3.14). Direct pins live in `requirements-langchain.txt`; the
frozen resolution is `requirements-langchain.lock.txt` (every version
published ≥7 days before capture; resolver cutoff
`--exclude-newer 2026-11-07T04:26:51Z`).

    uv venv .venv --python 3.14
    uv pip install --python .venv/bin/python \
      --exclude-newer 2026-11-07T04:26:51Z -r requirements-langchain.lock.txt

`requirements-langchain.txt` documents the three direct pins and the
resolver cutoff; `requirements-langchain.lock.txt` is the frozen
resolution of all 65 distributions — install from the lock for exact
reproduction.

Required environment variables: `DEEPSEEK_API_KEY` (model calls) and
`E2B_API_KEY` (e2b backend). Keys exist only in the controller
environment: generated agent code and backend subprocess environments
never receive them (`run_v2.redact` also strips env secrets from saved
rows).

## Calcside API/worker

Fixed loopback endpoints only — API `http://127.0.0.1:18787`, worker
`http://127.0.0.1:18788`; `csclient.BASE` is hard-coded, never point at a
production server. Always use a fresh state dir; `up.sh` requires an
empty `BENCH_STATE_DIR` (default: new `mktemp -d` under `results/`),
refuses occupied ports and non-empty dirs, builds the dev binaries,
migrates a fresh SQLite DB and records `api.pid` / `worker.pid` /
`connection.json` inside the state dir:

    BENCH_STATE_DIR=results/state-<tag> ./up.sh    # or ./up.sh (mktemp)

Stop exactly what a state dir recorded — pid command lines are verified
before killing; nothing else (including the shared OpenShell gateway) is
touched, and state files are kept:

    ./down.sh results/state-<tag>

Do not run any flow that deletes a pre-existing `bench.db`; old
databases and results are preserved. Both processes run with
`--net-allow-cidrs=198.18.0.0/15`, which exempts this host's fake-IP DNS
range (198.18.x.x) from `builtin.block_private_network`; real private
ranges remain blocked. This exemption is recorded in `connection.json`
and PROTOCOL.md.

## Official commands

High-budget native replication (separate dataset; old results stay intact):

    LANGSMITH_TRACING=false LANGCHAIN_TRACING_V2=false \
      .venv/bin/python run_native_highbudget.py --output results/v2-native-mcp-highbudget

This uses 64 model calls / 96 tools per trial and recursion limit 512.
See `PROTOCOL_NATIVE_HIGHBUDGET.md`. The original eight-call limit was a
local evaluation cap, not an account-credit error. Use a fresh output path
and a fresh dedicated stack; never overwrite a previous dataset.

    LANGSMITH_TRACING=false LANGCHAIN_TRACING_V2=false \
      .venv/bin/python run_v2.py --output results/v2-agent --repetitions 3
    LANGSMITH_TRACING=false LANGCHAIN_TRACING_V2=false \
      .venv/bin/python micro_v2.py --output results/v2-micro
    LANGSMITH_TRACING=false LANGCHAIN_TRACING_V2=false \
      .venv/bin/python run_native_mcp.py --output results/v2-native-mcp

Render figures only from the frozen summaries (never from raw rows):

    .venv/bin/python render_v2.py   # writes results/figures-v2/*.svg

## Operations

### Environment troubleshooting

- `up.sh` refuses occupied ports and non-empty state dirs by design —
  that is the guard, not a bug. Pick a fresh `BENCH_STATE_DIR`; never
  reuse or delete an old one.
- This host's DNS returns fake-IP addresses in `198.18.0.0/15`, which
  `builtin.block_private_network` would deny; both dedicated processes
  run with `--net-allow-cidrs=198.18.0.0/15` (recorded in
  `connection.json`). Real private ranges stay blocked.
- OpenShell sandbox user is uid 1000 with a read-only root: the workdir
  is `/sandbox` (the policy's include-workdir), not `/work`.
- OpenShell default policy denies all public egress. Each `bench-v2-*`
  sandbox gets exactly one per-sandbox rule — `api.github.com:443`
  `GET /repos/**` — applied via `policy set` after `policy get --full`
  (the `policy update --add-allow` form requires a pre-existing L7
  endpoint, and `--add-endpoint` cannot be combined with `--add-allow`
  in one call; access presets expand to broad GET/HEAD/OPTIONS, so the
  adapter substitutes only `network_policies`). Effective policy +
  sha256 are stored in adapter `metadata()`.
- `openshell sandbox create --help` documents a 32 KiB limit per
  command argument, and `sandbox exec` enforces the same limit: the
  43,350-byte `series.csv` staging payload exceeds it. `run_v3`
  stages files in 4,096-character chunks with exact `read_file`
  verification; the interrupted pre-chunk attempt is kept locally at
  `results/preflight-v3-staging/` (git-ignored, not committed).
- Fixture staging (`run_v3.StagedBackend`) executes each input file
  through the backend and verifies an exact `read_file` round-trip;
  failures are recorded as `setup_error` in the trial row and are
  never silently retried.

### Run interruption

- Official runs write `manifest.json` up front (commit, source hashes,
  order, limits) and append per-trial rows incrementally; every output
  dir is created with `exist_ok=False` — never reuse an output path.
- A controller interruption appends to `manifest.interruptions`
  (completed trials preserved, in-flight work marked unknown) and the
  run resumes via `resume_v2.py` from the manifest order without
  overwriting saved rows. Failed trials stay in denominators.
- Aborted or superseded attempts are preserved, never deleted, and are
  excluded from official datasets: the interrupted v3 staging attempt
  (21 rows, killed at the OpenShell 32 KiB exec-argument limit before
  the chunked-staging fix) lives at `results/preflight-v3-staging/`;
  the first memory-probe source/result pair lives at
  `results/memory-probe-preflight/`.

### Account and budget

- `DEEPSEEK_API_KEY` / `E2B_API_KEY` exist only in the controller
  environment; generated code and backend subprocesses never receive
  them, and `run_v2.redact` strips env secrets from saved rows.
- `BudgetExceeded` is a local harness cap (8/12 calls in v2, 64/96 in
  native/v3 tracks), not an account-credit error. Provider retries are
  disabled on the native track; graph recursion is capped at 512 with
  `max_concurrency=1`.

### Cleanup

- Trial adapters delete only the resources they created (Docker
  container, E2B sandbox, `bench-v2-*` OpenShell sandbox); cleanup
  errors are retained in the trial row.
- Dedicated Calcside servers stop via `./down.sh <state-dir>` — pid
  command lines are verified before killing and state files (db, logs,
  `connection.json`) are kept. The shared OpenShell gateway and the
  pre-existing `dify-*` sandboxes are never touched.
- Runtime state (`.venv`, logs, pid files, `results/state-*/`,
  `results/bench-state-*/`, `results/*.db*`) is excluded by the
  directory `.gitignore`; raw JSON results and figures stay tracked.
- Old databases, pilot outputs and prior datasets are preserved —
  never delete `bench.db` or rerun/overwrite saved trials.
