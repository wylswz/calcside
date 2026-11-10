# EP-0022: Instance notebooks — explicit sections and empty-process restoration

- **Status:** Draft
- **Created:** 2026-11-10
- **Area:** store, sandbox service, API, MCP, agent prompt, console. No engine change; one optional worker-contract field.
- **Related:** [EP-0001](0001-record-replay.md) (record/replay; names lost-instance recovery as a follow-up), [EP-0008](0008-mcp-and-progressive-disclosure.md) (MCP tools, progressive disclosure), [EP-0014](0014-persistent-instances.md) (applications: fresh state per invoke), [docs/execution-routing.md](../execution-routing.md)

## Summary

An instance already behaves like a Jupyter kernel: globals survive between
`exec` calls. But the code that produced those globals is not a first-class
object. It is spread over the execution history, mixed with failed attempts
and one-off probes, so when the instance process is gone (worker restart,
OOM kill, crash) the state cannot be rebuilt and the instance is `lost` for
good.

This proposal makes the state's source explicit:

1. **Notebook.** Each instance owns one ordered list of **sections**, stored
   durably on the API tier. A section is named code plus metadata
   (description, statically derived `defines`/`uses`, observed effects, run
   state).
2. **Outline first.** An agent reads a compact outline of all sections
   (no code), then reads or runs any one of them by name.
3. **Restoration.** An instance whose process is gone is restored instead of
   staying `lost`: a fresh, empty process under the same instance id and the
   same stored spec. Restoration **executes no code and rebuilds no memory
   state**. Only the code is kept; the agent (or user) is told that the
   instance is empty and is responsible for re-running the sections.

The notebook's meaning is deliberately simple: **the state of the instance
is what you get by running the sections top to bottom in a fresh process.**
Everything else in the design is there to keep that sentence true, or to
tell the agent precisely where it is not.

## Problem

- **Hidden state.** `exec` mutates globals silently. Neither the agent (after
  context compaction or in a new run) nor a human supervisor can tell which
  code the current bindings came from.
- **No recovery.** `MarkRunningAsLostForNode` ends every instance on a
  restarted worker. Starlark values cannot be serialized
  (`docs/execution-routing.md`), so there is nothing to restore from, and
  EP-0001's replay-based recovery needs recording to be enabled and is still
  a draft.
- **History is not a program.** The `executions` table holds full code, but
  replaying it would re-run every failed attempt, probe and duplicate.
- **Agents pay for rediscovery.** An agent resuming work on an instance must
  either re-send code it wrote earlier or guess what is defined.

## Research: what makes a notebook agent friendly

Findings that shape the design (sources at the end):

| Finding | Consequence here |
|---|---|
| Classic notebooks let cells run in any order; state becomes a function of click history. Studies of GitHub notebooks report roughly a quarter run top to bottom and about 4% reproduce their outputs. | Order is semantics. Run state is tracked per section; out-of-order and edited sections are reported as `stale`, never silently trusted. |
| "Restart and run all succeeds" is the only notebook property an agent can actually verify. | After restoration the instance is empty and every section is `not_run`; `run("all")` is that check, and its per-section report is the evidence. |
| marimo derives a dataflow graph statically (names each cell defines and reads) and separates durable cells from an ephemeral scratchpad. | Server-derived `defines`/`uses` per section; plain `exec` stays as the scratchpad; scratch mutations are counted as drift. |
| marimo validates durable changes as a batch and rejects with an error naming the violated invariant and the cells involved. | Section writes are validated (syntax, name, size) before anything is stored or run; errors name the section and line and say what to do next. |
| MCP servers for Jupyter with ~15–20 cell tools were consolidated (Block went from 11 tools to 4); `.ipynb` JSON with embedded outputs is a poor editing format for LLMs. | Few tools, addressed by stable name instead of shifting index, code as plain text, outputs kept out of the outline. |
| Tool results are prompts. Agents act on what the result says. | Every result is compact text that states the state and the next sensible call. |

Two things the Jupyter and marimo designs do not have to deal with, and this
one does: sections have **external side effects** (re-running a section
re-sends its `net.post`), and the consumer is an agent with a **token
budget**. Hence the server never executes a section on its own, neither
reactively nor on restoration, and the outline costs one line per section.

## Design

### Words

- **Notebook** — the ordered sections of one instance. One per instance,
  created implicitly; no separate resource id.
- **Section** — one named unit of code with metadata.
- **Scratch exec** — today's `exec` without a section. Unchanged.
- **Incarnation** — one life of the instance's process. Starts at 1,
  increments on every restoration. Run state is always relative to the
  current incarnation.
- **Restoration** — replacing a dead process with a fresh empty one for the
  same instance. Not a replay.

### Section

| Field | Source | Notes |
|---|---|---|
| `name` | author | Stable address, unique per notebook, `[a-z][a-z0-9_]{0,47}`. Agents address sections by name; positions shift, opaque ids cost tokens. |
| `description` | author | Required, one line, ≤ 200 chars. This is what the outline shows. |
| `code` | author | ≤ `MaxCodeBytes` (256 KB), same limit as exec. |
| `rerun` | author | `safe` (default) or `once`. See *Side effects*. |
| `defines`, `uses` | derived | Top-level names bound, and free names read, from a static parse (`go.starlark.net/syntax`; parsing only, the API tier still executes nothing). Recomputed on every code change. Capability and builtin names are excluded from `uses`. |
| `effects` | derived | Distinct `capability.op` pairs seen in the audit batch of the last run, e.g. `net.request`, `fs.write`. Metadata only, never arguments or bodies. |
| `state` | derived | `fresh`, `stale`, `failed`, `not_run`. See *Run state*. |
| last run | derived | `exec_id`, status, duration, output size. Output itself stays on the execution record. |

Author-declared metadata is limited to what cannot be derived. Anything the
server can compute (names, effects, state) is computed, so the outline
cannot drift from the code.

### Two ways to execute

1. **Scratch**: `exec(code)` as today. For probing, `help(...)`, printing a
   variable. It is not part of the notebook, so it cannot be re-run after a
   restoration.
   If it binds or rebinds a top-level name (static check on the code), the
   instance's **drift** counter increments and the result says so.
2. **Section**: `exec(code, section={name, description})` creates or
   replaces the section with that name and runs it. This is the main
   authoring path: one call, like Jupyter's insert-and-execute. The section
   is stored **whether or not the run succeeds**: a failed run is kept with
   state `failed` and its error, so the intention (name, description, code)
   is preserved and the agent fixes it by sending corrected code under the
   same name. This includes code that does not parse; such a section has
   empty `defines`/`uses`. Because the attempt is recorded in the notebook,
   a failed section run is not drift: whatever it bound before failing is
   attributable to a visible section. Rule the agent can rely on: *a
   section's code is the last code run under that name, and its state says
   whether that run worked.*

A successful scratch exec can also be promoted afterwards
(`save_section(from_exec=exe_…)`), so exploration does not have to be
re-sent.

New sections append to the end by default (`after: name` places them
elsewhere). Existing sections keep their position.

### Run state

All derived from stored facts: section code hash, the incarnation, and a
per-instance run sequence number stamped on each section run.

- `fresh` — ran successfully in this incarnation with its current code, and
  no earlier section whose `defines` intersect its `uses` has run since.
- `stale` — ran in this incarnation, but its code was edited without
  running, or a section it reads from ran later.
- `failed` — last run in this incarnation errored. The outline shows the
  error type next to it; `read_section` returns the message and backtrace.
  Top-level statements before the failing one may have taken effect, so
  sections that read its `defines` are flagged in the `next:` line. A
  failed section stops `run("all")` at that point until it is fixed or
  deleted.
- `not_run` — not executed in this incarnation. Every section is `not_run`
  right after a restoration.

Staleness is advisory and static. It does not see mutation through aliases
(`cfg["k"] = 1` in a later section), the same limit marimo documents. There
is no automatic re-execution of stale sections: with real side effects that
must be the agent's decision. `run(sections="stale")` makes it one call.

### Outline

The entry point for any agent arriving at an instance. Text, one line per
section, no code, no output:

```
instance ins_8fk2 running · incarnation 3 · 5 sections · drift: 2 scratch execs changed globals
 1 config        fresh    defines API, HEADERS                    — endpoints and auth headers
 2 fetch_orders  fresh    uses API, HEADERS; defines orders       — pull last 30 days of orders [net.request] 2.1s
 3 clean         stale    uses orders; defines rows, bad          — drop test accounts, normalize currency
 4 notify        fresh    uses rows; defines ticket_id  (once)    — open the finance ticket [net.request]
 5 report        not_run  uses rows, ticket_id                    — write summary to /work/report.md [fs.write]
next: run(sections="stale") re-runs clean; report has not run.
```

Right after a restoration the same notebook reads:

```
instance ins_8fk2 running · incarnation 4 · restored 2026-11-10T14:05Z, memory and files are EMPTY · 5 sections
 1 config        not_run  defines API, HEADERS                    — endpoints and auth headers
 2 fetch_orders  not_run  uses API, HEADERS; defines orders       — pull last 30 days of orders [net.request]
 3 clean         not_run  uses orders; defines rows, bad          — drop test accounts, normalize currency
 4 notify        not_run  uses rows; defines ticket_id  (once, ran in incarnation 3) — open the finance ticket [net.request]
 5 report        not_run  uses rows, ticket_id                    — write summary to /work/report.md [fs.write]
next: nothing was re-run for you. run(sections="all") re-runs 1-3 and 5; notify is `once` and already ran, run it by name only if repeating it is intended.
```

The trailing `next:` line is computed, not prose: stale and not-run
sections, `once` sections that already ran, sections that read names nobody
has bound in this incarnation, and drift.

### Running

`run(sections)` accepts one name, a list, `"all"` or `"stale"`. Sections
always execute in notebook order, each as its own exec (own `exec_id`,
audit batch, timeout and step limits, exactly as today). Before running a
section, names in its `uses` that no earlier section in the notebook defines
and no successful run has bound are reported as a warning, not an error (the
static view can be wrong). Execution stops at the first failure. The result
is one line per section plus the failing section's full error:

```
config ok 3ms · fetch_orders ok 2.1s · clean FAILED
error[runtime] clean:14: key "currency" not found
not run: notify, report
```

The exec request carries the section name as the chunk's filename so
backtraces read `clean:14` instead of an anonymous chunk (one optional
field on `runtime.ExecRequest`; the contract type changes, not the spec).

### Restoration

Restoration replaces a dead process and nothing more.

**What it does.** For an instance whose process is gone and whose TTL has
not passed, create a new process under the same instance id from the
instance's **stored spec, unchanged**: placement picks a live node, the
lease epoch is bumped, the incarnation increments, drift resets, status is
`running`. The request takes no spec, no overrides and no code. Until
instances can be edited after creation, a restored instance has exactly the
boundary it was created with; `expires_at` is not extended either.

**What it never does.** It executes no section and no other code, and it
does not write to the new process's memory or VFS. Globals are empty, `/work`
is empty, every section is `not_run`. There is no server-side "run all on
restore", no pinned values, no replay.

**Which instances.** Only unexpired ones. An instance past its TTL is
ignored by restoration and ends as `expired`, as does one deleted meanwhile;
both stay terminal, and their notebooks are not reusable by any other
instance. `exec` or `run` on an expired instance, whether it expired while
running or while lost, returns a dedicated `expired` error (new API error
code, HTTP 410) instead of the generic `not_running`, and says what is
left:

```
instance expired at 2026-11-10T15:00Z and cannot be restored or run.
Its notebook (5 sections) is read-only history; create a new instance to continue.
```

**When.** On first use: the first `exec` or `run` that reaches an instance
without a live process restores it. That call is **not executed**. It returns
a `restored` result so that no code ever runs against an empty instance
before the caller has been told:

```
instance was restored (its process ended at 14:02Z). Memory and files are EMPTY; nothing was re-run.
Your call was NOT executed. The notebook has 5 sections, all not_run:
  config, fetch_orders, clean, notify (once, ran in incarnation 3), report
Re-run what you need with run(sections=...), then repeat your call.
```

Restoring on first use covers every way a process dies (worker restart,
child crash, OOM kill) with one mechanism, costs nothing for instances
nobody comes back to, and needs no worker database access. `lost` remains
as the state between process death and the next use; read-only calls
(`notebook`, `read_section`, history) work on a lost instance and do not
restore it.

**Being told.** Three places carry the notice, so an agent that missed one
still sees it: the `restored` result above; the outline header and `next:`
line, for as long as any section is `not_run` in a restored incarnation; and
the agent prompt, which states the rule up front ("if a result says
`restored`, the instance is empty: read `notebook` and re-run the sections
you need; nothing is re-run for you").

**Stored spec.** Only references are stored, as today: the persisted spec is
sanitized and names vault secrets, library policies and modules without
holding their contents. Restoration resolves those references again through
the same path as create, so the spec is identical but a secret rotated or a
policy edited since create takes its current content. A reference that no
longer resolves (deleted secret, policy or module) fails the restoration
with an error naming it; the instance stays `lost` and the notebook stays
readable. An instance created with inline secret values cannot be restored
and reports that instead. No create-time snapshot is persisted.

**Not recoverable, and said so in the outline header when relevant:**
anything done only through scratch execs, and files uploaded through the API
(EP-0005). Files written by sections come back when those sections are run
again.

### Side effects

Re-running is only correct for code that is safe to repeat. Reads,
computation and `fs` writes are. "Send the email" is not. The author marks
such a section `rerun: once`:

- `once` is a guard on bulk runs, nothing else. `run("all")` and
  `run("stale")` leave out a `once` section that has succeeded before, in
  this or any earlier incarnation, and say so in the report. Naming the
  section explicitly always runs it.
- After a restoration its `defines` are therefore unbound until the agent
  acts, and the outline's `next:` line names every later section that reads
  them. The agent decides: bind the value in a small `safe` section
  (`ticket_id = "FIN-1234"`), or run the section again on purpose.

The server cannot infer safety reliably (a `net.request` may be a GET or a
payment), so the default stays `safe` and `effects` is shown in the outline
to make the question visible. When a section created without `once` has
network effects, the exec result adds one line: `this section calls
net.request; mark rerun="once" if repeating it after a restoration would be
harmful`.

### Editing

`save_section(name, code?, description?, rerun?, after?)` changes a section
without running it (state becomes `stale`); `delete_section(name)` removes
it. Deleting does not unbind the names it defined in the live process, so it
counts as drift, and the result lists sections that still `use` those names.
Section writes carry the notebook's revision for optimistic concurrency
between an agent and a human in the console; a conflict returns the current
outline.

### Agent surface (MCP)

| Tool | Arguments | Result |
|---|---|---|
| `exec` | `code`, `section?: {name, description, rerun?, after?}` | output or error; with `section`, plus the one-line section status |
| `notebook` | — | the outline |
| `read_section` | `name`, `output?: bool` | code, metadata, optionally last output |
| `run` | `sections`: name, list, `"all"`, `"stale"` | per-section report |
| `save_section` | `name`, fields or `from_exec` | section status |
| `delete_section` | `name` | dependents, drift |

Five new tools plus one extended; all take `instance_id` on the unbound
endpoint, as today. There is no restore tool: restoration happens on first
use and announces itself. `create_instance` and the bound-instance prompt
gain a short notebook paragraph: keep state-building code in sections, use
bare `exec` for probes, read `notebook` first when resuming, and on a
`restored` result re-run the sections you need because nothing is re-run
for you. `Large values stay in memory` advice stays as is.

The REST API mirrors this under `/api/v1/instances/{id}/notebook` (outline
as JSON with the same fields, plus `incarnation` and `restored_at`),
`/sections/{name}`, `/notebook/run`, and `section` on the exec body. The
`restored` outcome is a distinct error code on exec/run so SDKs can surface
it as a typed condition. `api/openapi.yaml` first, then `make gen`. The
Python SDK follows.

### Storage

- New table `notebook_sections`: `instance_id`, `name`, `position`,
  `description`, `code`, `code_sha256`, `rerun`, `defines`/`uses`/`effects`
  (JSON), `last_exec_id`, `last_incarnation`, `last_run_seq`, `last_status`,
  `succeeded_before` (for `once`), `updated_by`, timestamps. Unique
  `(instance_id, name)`.
- `instances` gains `incarnation`, `restored_at`, `run_seq`, `drift`,
  `notebook_rev`.
- `executions` gains nullable `section_name` and `incarnation`, so history
  shows which runs were notebook runs.
- Store interface + GORM impl + `storetest` conformance; one new paired
  Atlas migration, dialect neutral; the baseline is untouched.
- Limits: 200 sections per notebook; section rows are deleted with the
  instance's other history.

Section code is stored at rest in plain text, like `executions.code` today.
Secrets are referenced by `{{secrets.NAME}}` placeholders, so code does not
carry values.

### Where the logic lives

Entirely in `service/sandbox` (or a sibling `service/notebook` using it).
Running a section is `Exec` with the section's code; the worker, supervisor
and engine do not know sections exist. Restoration is the existing `Create`
with the stored spec plus a compare-and-swap store transition
(`lost → running`, incarnation + 1), so two concurrent first calls restore
once. A child that died on a live worker is detected from the worker's
`not_found` and moved to `lost` first. This keeps the worker protocol unchanged
apart from the optional chunk name, and keeps the rule that the API tier
never executes code.

### Access

Decided in `service/access`, one table entry per new operation
(`TestRouteCoverage`): outline and read need **View**; exec-with-section,
run, save and delete need **Run**. Restoration has no route of its own; it
is a consequence of a Run-level call, so only an actor who may run code on
the instance can bring it back. It re-checks the per-user and per-key
live-instance limits, since a lost instance is not counted as live. Keys
bound to an instance survive `lost` today (only delete revokes them), so a
bound agent keeps working across a restoration. View-only keys can read the
notebook but neither run nor restore it.

### Console

The instance page gets a Notebook view: the outline as a list (state badge,
description, defines/uses), code and last output on selection, run / run
stale actions, a drift notice, and "save as section" on scratch executions
in the history. A restored instance shows a persistent "restored, memory is
empty, sections have not been re-run" notice until its sections are run; a
lost one still shows its notebook read-only. Must keep the `docs/interaction-stability.md` invariants (state badges
paint-only, list order stable under polling).

## Plan

1. **Sections.** Migration, store, static `defines`/`uses`, service, REST,
   exec-with-section, outline, run, drift. MCP tools and prompt text.
2. **Restoration.** Incarnation, restore on first use from the stored spec,
   the `restored` result and outline notice, `once`, limits re-check.
3. **Console** notebook view.
4. **SDK** methods and LangChain tool wrappers.

Each step is usable on its own; step 1 already gives agents the outline and
re-runnable sections on a live instance.

## Tests

- Static analysis: defines/uses for assignments, `def`, `load`, comprehension
  scopes, augmented assignment; builtins and capabilities excluded.
- State machine: fresh/stale/not_run across edit, upstream re-run,
  restoration.
- Exec-with-section stores the section on success and on failure (runtime
  and syntax errors), replacing earlier code under that name; `failed` state
  and error are readable; a failed section stops `run("all")`; drift counts
  scratch execs only.
- Restoration re-resolves references: rotated secret and edited policy take
  current content; a deleted reference fails restoration and leaves the
  instance `lost`.
- Run order, stop-at-first-failure report, section name in backtraces.
- Restoration: after worker restart and after a child crash; the triggering
  call is not executed and produces zero audit events and zero execution
  records; empty globals and VFS; all sections `not_run`; spec byte-identical
  before and after; `expires_at` unchanged; epoch fencing against the old
  process; concurrent first calls restore once; expired and deleted
  instances are not restored; exec/run on an expired instance (expired
  while running, and while lost) returns the `expired` error and creates no
  process; read-only calls do not restore; inline-secret
  refusal; limit re-check.
- `once`: left out of `"all"`/`"stale"` after it succeeded, including across
  incarnations; runs when named; dependents reported.
- Access table coverage; bound, restricted and view-only keys.
- Store conformance on both dialects; MCP round trip
  (exec-with-section → notebook → process loss → `restored` → run report).

## Alternatives considered

- **Replay the execution history.** No new concept, but replays failures,
  probes and superseded definitions, and repeats every side effect.
- **Snapshot globals.** Not possible in general for Starlark values
  (closures, capability bindings, aliasing); rejected in
  `docs/execution-routing.md`.
- **Reactive execution like marimo.** Automatic downstream re-runs are right
  for pure dataflow and wrong when sections call external systems. Staleness
  is reported instead.
- **Re-running the notebook on restoration.** Convenient, but it repeats
  side effects without anyone asking, hides from the agent that state was
  rebuilt rather than kept, and ties worker recovery cost to notebook size.
  Rejected: restoration executes nothing; re-running is the caller's
  explicit act.
- **Restoring state without running code** (pinned values, tape replay from
  EP-0001). Rejected for the same reason: restoration does not mutate memory
  state. One rule, no exceptions for the agent to remember.
- **Eager restoration when the worker re-registers.** Same end state, but
  spawns a process for every instance, including ones nobody returns to
  before their TTL, and does not cover a single crashed child.
- **An explicit restore call.** One more tool and one more round trip for
  the same result; first-use restoration already refuses the triggering
  call, so the agent is told either way.
- **Changing the spec on restoration.** Not until instances are editable
  after creation; restoration must not become a side door for it.
- **Reusing a notebook from an expired instance.** Rejected: expired is
  terminal and its notebook is history, not a template.
- **Sections as VFS files.** The VFS dies with the process, which is the
  problem being solved.

## Decisions

- Restoration only replaces the process. It never executes code and never
  writes memory or files; the caller re-runs sections and is told to.
- The instance spec is unchanged by restoration.
- Expired instances are not restored and their notebooks are not reusable.
- Only references are stored. Restoration resolves them again; no
  create-time snapshot of policy, module or secret content is kept.
- Failed section runs are stored with state `failed`, so the intention is
  preserved.

- Restoration happens on first use; there is no restore call and no eager
  restoration on worker registration.
- Executing on an expired instance returns a dedicated expiration error.
- Notebooks belong to the code-execution sandbox only. They are not a
  serving concept and there is no path from a notebook to an EP-0014
  application (no export, no publish).

## Sources

- marimo, "Python notebooks as dataflow graphs" and "Lessons learned reinventing the Python notebook" (static dataflow graph, hidden state, reproducibility figures).
- "Beyond the Shell: Extending Agents with Reactive Python Notebooks" (marimo pair: scratchpad vs durable cells, transactional mutation, invariant-naming errors).
- "Notebooks Are Hostile Territory for Coding Agents" (restart-and-run-all as the only verifiable property).
- Jupyter MCP servers: datalayer/jupyter-mcp-server, Try3D/JupyterMCP (`notebook_as_script`), block/mcp-jupyter (tool consolidation from 11 to 4).
