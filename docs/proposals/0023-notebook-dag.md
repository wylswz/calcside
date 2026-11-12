# EP-0023: Dependency-driven notebook execution and graph view

- **Status:** Implemented
- **Created:** 2026-11-12
- **Area:** `internal/notebook` static analysis and planner,
  `service/sandbox` run/exec orchestration, OpenAPI spec, MCP tools,
  agent prompt, console notebook view. No engine change; the worker
  protocol, in-memory globals and store schema are unchanged.
- **Related:** [EP-0022](0022-instance-notebooks.md) (instance
  notebooks; this supersedes its page-order run semantics),
  [docs/execution-routing.md](../execution-routing.md),
  [docs/interaction-stability.md](../interaction-stability.md)

## Summary

The notebook's dependency graph — built from each section's saved
code — is now the execution model for every notebook. There is no
mode to enable: running a section first runs every upstream section
that is not fresh, in dependency order; staleness propagates over the
same edges; blocking diagnostics refuse the runs they touch; and
`dry_run` previews the plan without executing. Agents see the graph
through the outline (`reads x ← provider`, `blocked:` lines), the MCP
surface (exec requires a section), and the console's Code/Graph view.

## Problem

- **Page order is not dependency order.** `run("all")` used to
  re-execute sections in stored position even when a provider sat
  after its consumer, and a consumer could run against globals its
  provider never refreshed.
- **`depends_on` is provenance, not the program.** It recorded what a
  section read at save time — it cannot see the current code, a
  provider positioned later, or a cycle.
- **Agents cannot see inputs.** Scratch execs hid state outside the
  notebook; unbound-use warnings approximated missing providers after
  the fact instead of showing structure up front.

## Design — graph & diagnostics

`internal/notebook.BuildGraph` is a pure function over per-section
static facts (`notebook.Analyze` of the saved code: defines, uses,
parse status). One **edge** `provider → consumer` exists per shared
name; **diagnostics** are `duplicate_define`, `missing_provider`,
`cycle`, `parse_error`. The graph is rebuilt from saved code on every
notebook read — stored defines/uses/`depends_on` are provenance only.

- A name defined by two or more sections is ambiguous **only when
  something reads it** — unread duplicates (loop variables,
  temporaries) are harmless and emit no diagnostic and no edges. The
  diagnostic lists definers in notebook order, then readers not
  already listed.
- A use with no definer — or whose only definer is the reader itself
  (read before bind) — is `missing_provider`, one diagnostic per
  section with sorted names.
- `!ParseOK` earns `parse_error` and contributes no defines/uses.
- Cycles report one concrete path per strongly connected component.
- `Order` is a deterministic Kahn topological order (ready set by
  notebook position), nil when a cycle exists.

## States over edges

Section state stays derived, never stored: `not_run` without a run
this incarnation, `failed` on a current-code error, `stale` on a code
change, `fresh` otherwise. Then a fixed point propagates over direct
provider edges: a `fresh` section becomes `stale` when any provider is
not fresh — stale, failed or not_run — or ran after it (higher run
seq). Position no longer matters; `depends_on` metadata is the graph's
direct providers for display.

## Run rules

`Graph.Plan` (`internal/notebook/plan.go`) is the single scheduler for
`RunSections` and exec-with-section.

- **Targets and closure.** A run resolves `"all"` (every section),
  `"stale"` (every non-fresh: stale, failed, not_run), or explicit
  names (always run, `Bulk=false`). The closure is the targets plus
  all transitive providers; execution walks it in topological order.
- **Actions.** A target runs (`target`); a non-target runs when not
  fresh (`upstream: not fresh`) or when a direct provider runs in the
  same plan (`upstream <name> runs first`); otherwise it is `reuse`
  (`fresh`).
- **`once`.** Bulk selections skip `once` sections that already
  succeeded — unless they re-enter the closure as an upstream, in
  which case the normal freshness rules apply. Explicit targets and
  exec-with-section ignore the flag for the target itself.
- **Blocking.** `duplicate_define`, `missing_provider` and `cycle`
  diagnostics intersecting the closure refuse the whole run —
  `graph_blocked` (400 — the code is not in `statusByCode`), message
  `run blocked — <diagnostics>` — and
  nothing executes; an unrelated broken section does not block.
  `parse_error` never blocks: the section runs and fails with the real
  syntax error.
- **Fail-fast.** Execution is serial in plan order, each section its
  own exec (lease refresh, run seq, stamps, audit, restoration
  handling unchanged); the first script failure stops the run and
  unreached nodes report `not_run`. `RunSectionResult` carries
  `status` (`ok`/`error`/`reused`/`skipped`/`not_run`, plus `planned`
  in dry-run) and the planner `reason`.
- **`dry_run`.** `RunSectionsRequest.dry_run` returns the report the
  run would produce — `planned`/`reused`/`skipped` — without
  executing, claiming the op lease, touching the heartbeat, or
  restoring a lost instance (states are computed as-is; everything on
  a lost instance plans `not_run` → `planned`).
- **Exec-with-section.** Saving then planning under the same op lease:
  non-fresh upstream sections execute first as their own stamped execs
  (reported in `ExecResult.upstream`), fresh upstream is reused without
  a new record. A blocking diagnostic still saves — the save stands —
  and returns `graph_blocked` prefixed `section saved; `. An upstream
  script failure returns `upstream_failed` (400) `section saved;
  upstream <name> failed: <error>`; the target never runs.
- **Sectionless exec.** Plain REST `exec` without a section is
  unchanged — still a scratch exec for the SDK, benchmarks and module
  debug, still counted as drift.

## Agent surface

- The outline shows `reads x ← provider` (`?` for missing or
  ambiguous), `depends_on` as direct providers, `blocked:` diagnostic
  lines first in `next`, and `run_sections(sections="stale") runs
  <names> in dependency order` guidance; unbound-use warnings are
  gone.
- The prompt describes the graph model: page position is display
  only, every name read by another section must be defined by exactly
  one section, blocked diagnostics must be fixed first, and there are
  no scratch execs — `exec(code, section=…)` always.
- MCP `exec` requires `section` (refused otherwise — every execution
  belongs to a section); `run_sections` prints `name: status (reason)`
  and accepts `dry_run`; the `upstream:` line reports what ran or was
  reused before the section.

## Console

- The Code/Graph view renders the dependency graph with React Flow +
  dagre (`notebookGraph.ts` stays the pure layout module): it follows
  polls live — a changed structure re-lays out and tweens node
  positions (~250ms, edges attached), new entries fade in and removed
  ones fade out, while the viewport only moves on first reveal,
  selection pans or the Controls fit button; state/once/description
  repaint in place and an unchanged structure moves nothing. Edges
  carry variable labels, diagnostics sit in the aside with a textual
  "blocks runs" tag on the blocking kinds, selection highlighting is
  `?section=`-driven and paint-only. A "Run section" action in the
  aside drives the same run path as the cell gutter.
- Every run first asks the server for a `dry_run` plan, validates the
  displayed-vs-saved fingerprint of every `planned` section, then posts
  the real run with the same revision; `graph_blocked` surfaces the
  server diagnostic verbatim in the status line's `title`. The status
  line counts `reused` sections and each batch row's `status` +
  `reason`.
- The scratch cell, its directory chip and the "Save as section…"
  promote flow are removed — every console execution belongs to a
  section.

## Limits

- Analysis is static: an in-place mutation of another section's value
  (`xs.append(...)`, dict writes) is not tracked as an edge.
- Residue in the shared globals — names left by deleted or edited
  sections, or set by scratch execs — is not tracked; only recorded
  section runs define freshness.
- Plain REST `exec` without a section remains for SDK, benchmark and
  module-debug callers; it bypasses the notebook and is counted as
  drift.
- Filesystem and network effects are not tracked or isolated — the
  graph orders code, it does not sandbox side effects.

## Alternatives rejected

- **Opt-in mode.** A per-notebook switch would leave two semantics to
  teach and let agents pick the unsafe one; the graph is correct for
  every notebook, so it is simply the model.
- **Reactive automatic rerun.** Nothing runs because another section
  ran or was edited — dependents only become stale. Upstream sections
  run only as part of an explicit run that needs them, once, in
  dependency order.
- **Plan token.** A reviewed plan→execute handshake duplicates what
  `dry_run` + the deterministic planner already give: the plan is a
  pure function of saved code and derived states, so preview and
  execute cannot diverge.
- **Engine-level binding isolation/freezing.** Considered, not now:
  this design relies on static analysis only, so in-place mutation
  and residue in the shared globals remain stated limits. Isolation
  or freezing may be revisited later if those limits prove costly.

## Tests

- `internal/notebook`: graph construction (reversed order, diamond,
  duplicates read/unread, missing, parse, cycles) and the planner
  table (upstream order, reuse, once rules, closure-scoped blocking,
  fail-fast shape, unknown targets).
- `service/sandbox`: provider-after-consumer run order, fresh-upstream
  reuse without a new exec record, edit→stale→rerun propagation,
  `upstream_failed`, `graph_blocked` (missing provider, read
  duplicate, `all`), unrelated-breakage and unread-duplicate
  non-blocking, `stale` covering not_run/failed, once bulk rules,
  `dry_run` purity, exec-with-section upstream reporting, outline
  `reads`/`blocked:` lines.
- `api`/`mcp`: selector and dry-run wire shapes, `upstream` in exec
  responses, MCP section-required refusal, goldens for the prompt.

## References

- [EP-0022](0022-instance-notebooks.md) — notebooks, run states,
  `once`, restoration, drift.
- [docs/execution-routing.md](../execution-routing.md) — why globals
  cannot be serialized and the worker-child execution model.
- [docs/interaction-stability.md](../interaction-stability.md) —
  invariants the graph view must keep.
- marimo, "Running cells"
  (<https://docs.marimo.io/guides/reactivity/>) — static
  defines/references forming a DAG, execution order from variables not
  page position, mutations untracked, globally unique names.
- marimo, "Cycles"
  (<https://docs.marimo.io/guides/understanding_errors/cycles/>) —
  cycle diagnostics naming the cells and variables involved.
