# EP-0023: Notebook DAG execution and graph view

- **Status:** Draft
- **Created:** 2026-11-12
- **Area:** `internal/notebook` static analysis, `service/sandbox`, API spec, MCP tools, console notebook view. No engine change; the worker protocol is unchanged for the first-version planner (a node run is the existing section exec); a store migration only for the persisted notebook mode (paired Atlas files, both dialects).
- **Related:** [EP-0022](0022-instance-notebooks.md) (instance notebooks; this supersedes its page-order run semantics for opt-in `dag` mode only), [docs/execution-routing.md](../execution-routing.md), [docs/interaction-stability.md](../interaction-stability.md)

## Summary

EP-0022 gave a notebook one honest meaning: its state is what you get
by running the sections top to bottom. That is a serialization, not
the program. The program is a dataflow graph — sections define names
and read names — and page order is only one possible order of it.

This proposal adds an opt-in **`dag` mode** that schedules a notebook
by that graph instead of by position, plus a graph view in the
console. Two limits are deliberate: **nothing is reactive** — no node
runs because another ran; every execution is an explicit server plan
reviewed by the caller and executed once, with staleness reported,
never auto-healed — and **nothing is parallel** — nodes run serially
in stable topo order; parallel shared-memory execution is unsafe and
out of scope. Sections, restoration, drift and the outline carry over
from EP-0022; what changes is how run order, freshness and `once` are
decided — over the graph, not the page.

## Problem

- **Order is not dependency.** `run("all")` re-executes clean upstream
  work; nothing shows what feeds a section.
- **Provenance is not a schedule.** `depends_on` is save-time history
  for ordered-mode staleness — it cannot express a provider positioned
  *after* its consumer without flagging it, cannot see the current
  graph, and says nothing about cycles.
- **The shape is invisible.** The system has no graph validation:
  duplicates, cycles and missing providers surface as runtime errors
  or ordered mode's unbound-use warnings — never as structure;
  partial reruns mean reconstructing dependencies by hand.

## Design

A **node** is a section as a graph vertex; an **edge** is
`provider → consumer` for one shared name, labeled with that name.
The **graph** — nodes, edges, diagnostics — is rebuilt from current
saved code on every read. A **plan** is the server's per-node action
list for one intent; a **plan token** is the opaque, single-use,
expiring handle to its snapshot.

### Mode

- `ordered` stays the default for existing and new notebooks; every
  legacy endpoint behaves exactly as EP-0022 specifies.
- `dag` is an explicit per-notebook switch, CAS-guarded by
  `notebook_rev` like section writes, returning the graph diagnostics
  computed at switch time. Switching runs nothing; an invalid graph
  may be entered and only execution is gated.
- The mode persists as a notebook-level field (proposed
  `instances.notebook_mode`); if stored it needs paired Atlas
  migrations per dialect plus the store interface, GORM impl and
  `storetest` updates. Specs are immutable after create, so a spec
  field is not an option.
- In `dag` mode there is no direct section-execution path. `exec` with a
  section still saves — the code, metadata and staleness update
  normally — but its run half does not execute; the result is an
  actionable plan-required outcome, and every other legacy run
  entrypoint likewise routes through the DAG plan rather than
  silently applying page-order semantics. All dag execution goes
  through plan → review → execute with exact `once` approvals.
  Plain scratch exec remains available outside the graph; ordered
  mode keeps every legacy behavior.

### Graph

Built by a pure function in `internal/notebook` from the **current
saved code** of every section, reusing `Analyze` — the same parser
dialect as execution. One node per section carries name, description,
position, `rerun`, state and observed `effects`.

- Each name in a section's `uses` resolves to every section whose
  `defines` contain it; each provider→consumer pair is an edge labeled
  with the name. **Providers must be unique** — a name defined by two
  or more sections is a `duplicate define` diagnostic naming the
  sections (marimo's global-uniqueness rule); edges into it are
  unresolved.
- **Cycles are diagnostics, not crashes.** The report walks the cycle
  labeling each hop with the variable (`a → b via rows; b → a via
  orders`), as in marimo's cycle error; nodes and edges still render.
- **Missing provider**: a `use` name no section defines — `Analyze`
  already excludes builtins, capabilities and `env`/`secrets`/`print`/
  `help`/`interrupt` — is a diagnostic; with no fallback to scratch
  execs or stale globals the notebook is not self-contained.
- **Parse failure**: the section keeps empty `defines`/`uses` plus a
  parse diagnostic and unknowable exports — why v1 rejects execution
  of a notebook while *any* graph-invalid diagnostic exists rather
  than approximating per node.
- **Position is display and tie-break only**: it orders the section
  list and, among topo-ready nodes, orders by `(position, name)`; it
  never creates or removes an edge.
- **`depends_on` stays provenance.** It keeps driving ordered-mode
  staleness; the DAG is recomputed from current code and is the
  scheduling authority in `dag` mode. The graph answers "what does
  this code read"; run provenance answers "what did this run bind".

The graph view renders invalid graphs — cycles included — so the
author can see the problem; only execution is gated.

### Staleness in dag mode

The four EP-0022 states (`fresh`, `stale`, `failed`, `not_run`) are
kept, but edges — not page order — decide propagation: `fresh` means
succeeded this incarnation with current code and no DAG provider has
run since or gone `stale`/`failed`; `stale` means code edited without
running, or a DAG provider re-ran, was edited, failed, or was deleted.
Position never taints — a provider placed after its consumer, or a
neighbour sharing no edge, changes nothing.

- **Reuse needs valid provenance.** A `fresh` node is reusable only
  while incarnation, its code hash and its providers' run provenance
  hold; any graph edit — dropped export, deleted section, rewired
  edge — invalidates historical consumers even when the live process
  still holds the old binding.
- **Failures poison descendants.** A `failed` node may have bound
  names before erroring; every DAG descendant is untrusted and never
  reused on the failed run's say-so, and missing values are never
  recovered by fallback.
- **The limits are shared, not solved.** Static edges cannot see
  mutation (`rows.append(x)`, `cfg["k"] = 1`), attribute writes,
  closures over mutated names, or external-world drift. A deleted
  section's exports linger as residue; dag mode marks consumers stale
  rather than pretending the names vanished. Clean-runtime and binding
  ownership are future work — dag mode does not claim to fix hidden
  state.
- **External effects are not inferred.** `effects` are observed audit
  facts, not predictions: fs/net ordering between independent nodes
  is not derived from code, and the serial tie order is a scheduling
  convenience, not an effect-ordering guarantee.
- **Scratch drift gates plans.** `drift` counts only scratch execs
  that bind top-level names — mutations like `x.append` or `d[k]=v`
  are invisible to it. A dag plan surfaces drift and, by default,
  requires explicit acceptance at execute — drifted globals make
  results *unverified*, not proven unreproducible, and acceptance is
  not a clean-state guarantee. Plan *validation* is broader than
  `drift`: the runtime-state generation moves on every exec, so a
  plan cannot outlive any scratch execution. There is no automatic
  reset or serialization.

### Plan → execute

DAG execution is always **plan, review, execute** — never a bare "run
these nodes" call, and never in the background.

**Intents.** The caller picks an intent and targets; the server
computes the node set — clients cannot override it. "Always execute"
means the node runs even when fresh-valid; reuse applies only outside
that set — except under `all`, where freshness does not spare a
normal node at all.

| Intent | Always execute | Reuse | Also execute |
|---|---|---|---|
| `target` | the named nodes | fresh-valid transitive providers | nonfresh transitive providers |
| `affected` | named nodes + all transitive descendants | providers outside that set when fresh-valid | nonfresh transitive providers |
| `all` | every node except trusted `once` successes | trusted `once` successes | — |
| `stale` selector | every `stale`/`failed`/`not_run` node | their fresh-valid providers | nonfresh transitive providers |

`stale` is target-selection sugar, not a fourth intent: those nodes
become `target` semantics' targets, then prerequisites join; nodes
reachable by several paths are listed once. In dag mode "stale" means
"these nodes plus what they need", and a re-run provider's fresh
descendants flip stale afterwards, as in ordered mode. `all` is the
verify-everything intent: it schedules every node — fresh-valid
included — sparing only a `once` node whose prior success is still
trusted (`succeeded_before` with valid provenance). Freshness does
not reduce an `all` plan.

**In-batch invalidation.** Actions are computed as a fixed point over
the candidate set, not in one pass: a node scheduled `execute`
invalidates every `reuse` candidate that transitively depends on it —
its new output means the consumer's prior result is stale before the
batch starts — so those candidates flip to `execute` (reason
`upstream re-runs in this plan`) and the check repeats until stable.
Topo order then guarantees each selected downstream sees its
providers' new inputs, and a shared upstream still executes once.
Nodes outside the candidate set are never pulled in; they are merely
marked stale afterwards, per the usual rules. Under `all` the same
propagation can revoke a trusted `once` reuse — its provider re-runs,
so it lands in `needs_once_confirm` instead.

**Plan contents.** Every candidate node gets exactly one action with a
reason:

- `execute` — `target`, `descendant of target`, `stale: code edited`,
  `provider re-ran`, `not_run`, `previous run failed`, `nonfresh
  prerequisite`, `upstream re-runs in this plan`.
- `reuse` — `fresh: last ok run still valid`, `once: succeeded before,
  values live`.
- `needs_once_confirm` — a `once` node that must execute and has
  `succeeded_before` in any incarnation: re-running repeats a side
  effect, so it needs approval bound to the exact node name + code
  hash. A first-time `once` node executes under the plan's authority.
- `blocked` — a graph-invalid diagnostic touches it; in v1 any
  graph-invalid diagnostic rejects the whole plan with the diagnostic
  list instead.

Each entry also carries the observed `effects` advisory — last-run
`capability.op` pairs. **Unobserved is not pure**: empty effects means
nothing was seen, not that the node is side-effect free.

**Binding and validation.** The plan binds `notebook_rev`,
`incarnation`, a runtime-state generation, and lifecycle state
(status/expiry). The generation moves on every state-changing op —
node runs, *every* exec including scratch (a mutation need not bind
a top-level name — exactly the drift counter's blind spot), notebook
writes, mode switches, restoration. It can be a dedicated `runtime_gen`
bumped in the same store writes or composed from existing counters;
the op lease is not part of the snapshot. The server holds the plan —
node list, per-node code hashes, snapshot — behind an opaque
single-use token with a short TTL. `execute` claims the op lease as
usual (`claimOp`), then — holding it — re-validates the *exact*
snapshot against the instance's current runtime fields immediately
before the first node runs; the lease acquisition is the caller's own
lock, not an external mutation, and never invalidates the token. Any
drift returns `plan_stale` and nothing executes. No stale plan ever
runs; authorizations are re-checked at execute — access can change
between plan and execute.

**Execution.** Nodes run serially in stable topo order — Kahn's
algorithm, ready-set ordered by `(position, name)` — one exec per node
with its own `exec_id`, audit batch, timeout, step limits and chunk
filename, exactly as today. **Fail-fast**: the first failure stops the
batch, including independent nodes not yet run — no skipping ahead, no
automatic retry, no rollback. The API tier plans; each node executes
in the worker child through the existing Gate, audit and lease path —
serialization unchanged.

**`once` in dag mode.** A `once` node whose prior success is trusted —
`succeeded_before` with still-valid provenance — is `reuse` wherever
it is not scheduled to execute, including under `all`. One that must
execute and has ever succeeded lands in `needs_once_confirm` —
re-running repeats a side effect, so it needs approval bound to the
exact node name + code hash; that includes after restoration, where
`succeeded_before` survives but the values do not. A first-time `once`
node executes under the plan's authority. Approval is per node +
exact code hash — approving `notify@abc` says nothing about
`notify@def`. `execute` validates *every* required approval — and
`accept_drift` when the plan flagged drift — before the first node
runs: a missing or mismatched approval rejects the whole plan and
nothing executes. Once execution starts, only runtime errors
fail-fast; there is no partial plan for unmet approvals.

**Outcomes and partial runs.** The report distinguishes `executed`,
`reused`, `executed_after_once_confirm`, `skipped`, `not_run`,
`failed` and `blocked` per node, each with a reason. Partial reports
arise only from runtime fail-fast or restoration — never from unmet
approvals, which reject the plan before anything runs. A mid-batch
failure or restoration keeps executed results, marks the rest
`not_run` with a `stopped_reason`, and never replays blindly.

**Restoration.** Unchanged: a lost instance restores EMPTY on first
use, incarnation + 1, and the generation moves — every outstanding
plan token is invalidated without auto-running anything.

### API and MCP (proposed — not in the spec)

`api/openapi.yaml` first, then `make gen`; nothing below exists yet:

- `GET .../notebook` gains `mode` and a `graph` summary (nodes, edges,
  diagnostics), or a sibling `GET .../notebook/graph`.
- `PUT .../notebook/mode` `{mode, revision}` → diagnostics.
- `POST .../notebook/dag/plan` `{intent, targets?}` →
  `{plan_token, expires_at, drift, nodes: [{name, action, reason,
  code_sha256, effects}], diagnostics}`.
- `POST .../notebook/dag/execute` `{plan_token, once_approvals?
  [{name, code_sha256}], accept_drift?}` → the per-node report.

Access goes through the existing checker: graph/outline reads need
View; plan and execute need Run like section runs, re-checked at
execute; the mode switch takes the same permission as other notebook
writes — the exact access-table entry is settled with
`TestRouteCoverage` at implementation. Bound keys behave as today.
MCP keeps the same semantics: `run_sections` gains dag intents
(`affected` included) and plan/execute tools carry the token flow;
`notebook` gains a mode + diagnostics line. Graph metadata stays
compact — names, edges, states — never code, outputs or secrets.

### Console

Inside the notebook view, a Code/Graph control swaps the card list for
a graph; both panels stay mounted so editors, drafts and scroll survive
switches and polls (C2/C3). A node shows name, description, state
badge, `once` tag and observed effects; edges are variable-labeled,
and selecting a node highlights its ancestors and descendants.
Diagnostics — including cycle paths — render on the graph. A plan
preview lists every node's execute/reuse/confirm/blocked action with
reasons before anything runs; nodes the plan pulls in beyond the
selection are identified with their code and require review. Each node
has an explicit jump-to-code action; there is no editable wiring — the
graph is derived, never authored.

Stability obligations (`docs/interaction-stability.md`): node state
is paint-only (S1); polling never moves nodes — structural changes
wait for an explicit refresh keeping selection, pan and zoom, with no
auto-fit (S5); view state survives refetches (C3); drafts and the plan
preview survive view switches (C2); pending, failed and reused states
are honest (F3); the `once` confirmation names the node and its code
snapshot through `Confirm` (F2). A keyboard-accessible dependency list
and a mobile fallback cover dense graphs. Coverage is planned, not
claimed — the status table is unchanged.

The run path's validation extends: `Notebook.tsx`'s `flushSave` plus
displayed-vs-saved fingerprint check must cover **every** node the
plan will execute — including ancestors and descendants the plan
added — and a node still held behind "Show N new" or not loaded blocks
execution, its code unverifiable. The preview names the added code;
execute binds `notebook_rev`.

### Where the logic lives

- `internal/notebook`: pure graph construction and diagnostics — no
  I/O, no store, no clock.
- `cmd/calcside/internal/service/sandbox`: mode, states, plan build /
  validate / execute, report. Orchestrates `Exec` per node.
- API: `api/openapi.yaml` → `make gen`; the worker protocol is
  unchanged — a node run is the existing section exec.
- Store: only `notebook_mode` (plus `runtime_gen` if the dedicated
  counter is chosen) — paired Atlas migrations, store interface, GORM
  impl, `storetest`.

## Plan

1. **Graph + diagnostics + read-only UI.** Graph construction and
   diagnostics in `internal/notebook` and a read-only graph view in
   the console — no mode storage, no activation, no plan preview, no
   execution-path changes; ordered semantics untouched.
2. **DAG planning/execution and activation.** `dag` mode storage and
   switch, intents, plan token + generation validation, serial topo
   executor, `once` confirmation, drift acceptance, the plan-preview
   UI, REST + MCP + agent prompt.
3. **UI polish.** Selection highlighting, jump-to-code, mobile and
   keyboard fallbacks, e2e coverage.

## Tests

Future coverage, not existing — listed so implementation can be
checked against intent:

- Ordering: reverse visual ordering still executes in topo order; a
  diamond with a shared `once` ancestor confirms once and reuses
  correctly; independent nodes keep stable `(position, name)` order.
- Diagnostics: duplicate define, cycle (variable-labeled path),
  missing provider and parse failure each surface on graph and plan;
  invalid drafts save; execution is rejected with the diagnostic list.
- Staleness: provider edit, delete and dropped export invalidate
  consumers; page-order neighbours without edges do not; a provider
  positioned after its consumer works.
- `once`: a trusted success (`succeeded_before` + valid provenance)
  is reused even under `all`; stale and post-restore required `once`
  nodes need approval; first-time `once` is allowed; a missing once
  approval or drift acceptance rejects the whole plan before any node
  runs — no partial run for approvals, and an unapproved `once` never
  yields false success downstream.
- In-batch invalidation: a `reuse` candidate downstream of a node
  scheduled `execute` flips to `execute` (fixed point); under
  `affected`/`all` a fresh downstream is invalidated by its scheduled
  upstream — including revoking a trusted `once` reuse into
  `needs_once_confirm`; a node outside the candidate set is only
  marked stale.
- Drift: a plan requires `accept_drift` at execute when the binding
  drift counter is nonzero; acceptance is recorded, not a clean-state
  claim. Non-binding scratch executions invalidate outstanding plans
  through the runtime generation but remain a drift-detection blind spot.
- UI validation: the saved-code fingerprint covers every planned node
  including added ancestors/descendants; unloaded or held nodes block.
- Plan integrity: a concurrent code edit, node run, *any* scratch
  exec, restoration or incarnation change invalidates the token;
  expired and reused tokens fail; authorizations are re-checked at
  execute; the execute's own lease claim does not invalidate it.
- Execution: a failure stops the batch including independent nodes;
  no auto retry or rollback; a partial report keeps executed results.
- UI: polling never moves nodes; drafts and selection survive
  refreshes; `once` confirmation uses `Confirm`.

## Alternatives considered

- **Reactive re-execution (marimo's default).** Rejected: sections
  have external side effects; automatic downstream re-runs would repeat
  `net`/`fs` effects without anyone asking. Staleness is reported
  instead — marimo's own lazy "mark stale" mode is the closer analogue.
- **Parallel DAG execution.** Rejected: nodes share one globals
  namespace; parallel shared-memory execution is unsafe and out of
  scope.
- **Reusing `depends_on` as the graph.** Rejected: it is save-time
  provenance — conservative, possibly stale, silent about current code.
- **Page-order fallback for invalid graphs.** Rejected: silently
  running a dag notebook in page order hides exactly the bugs dag mode
  exists to expose.
- **Editable wiring.** Rejected: edges are derived from code; drawn
  edges would be a second source of truth that can disagree.
- **Plan-free direct execute.** Rejected: without a bound snapshot,
  "run the stale ones" can execute against state the caller never saw.
- **Auto-reset before DAG runs.** Rejected for v1: globals cannot be
  serialized; a clean runtime is future work.

## Decisions

- `dag` is opt-in per notebook; `ordered` stays the default and every
  legacy behavior is unchanged.
- The graph is derived from current saved code; `depends_on` remains
  ordered-mode provenance, never the schedule.
- v1 rejects execution of graph-invalid dag notebooks; the graph view
  still renders them.
- Execution is serial topo, fail-fast, plan → review → execute, with a
  single-use expiring token and a server-authoritative node list.
- `all` schedules every node — fresh-valid included — sparing only a
  `once` node whose prior success is still trusted.
- In dag mode there is no direct section-execution path: `exec` with a
  section saves but never runs; every dag execution goes through the
  plan with exact `once` approvals.
- Re-executing a `once` node that succeeded before always needs
  explicit approval bound to node + code hash, validated for the
  whole plan before the first node runs.
- No fallback to scratch or stale globals for missing providers; no
  automatic reset; no claim that dag mode solves hidden state.

## References

- [EP-0022](0022-instance-notebooks.md) — notebooks, run states,
  `once`, restoration, drift; its research section (R1–R9) covers the
  notebook-agent literature.
- [docs/execution-routing.md](../execution-routing.md) — why globals
  cannot be serialized and the worker-child execution model.
- [docs/interaction-stability.md](../interaction-stability.md) —
  invariants the graph view must keep.
- marimo, "Running cells"
  (<https://docs.marimo.io/guides/reactivity/>) — static
  defines/references forming a DAG, execution order from variables not
  page position, mutations untracked, globally unique names, lazy
  "mark stale" runtime mode.
- marimo, "Cycles"
  (<https://docs.marimo.io/guides/understanding_errors/cycles/>) —
  cycle diagnostics naming the cells and variables involved.
