# EP-0015: Copy-isolated parallel `map` and `amap` for Starlark

- **Status:** Draft
- **Created:** 2026-10-27
- **Area:** Starlark engine, capability gate, resource limits, audit
- **Related:** [EP-0002](0002-standard-library-utilities.md), [EP-0003](0003-community-extensions.md)

## Summary

Add two engine-level combinators that run a Starlark callable over list
elements on bounded worker threads:

- `map(items, fn, *, concurrency=8) -> list`
- `amap(items, fns, *, concurrency=8) -> list`

`map` applies one unary function per element; `amap` zips N values with N
unary functions (elementwise pairing, not cartesian, not async/futures).
Both return a plain list in input order and run inside the existing
instance process under `inproc` and `process` isolation; no extra process
or worker-protocol change is needed to map.

Isolation is by **copy**, not shared access or freezing: each task calls a
fresh clone of the function plus its captured state (defaults, cells,
module globals, transitive callables) taken at call entry; the parent sees
only returned values. This is a proposal; nothing here is implemented.

## Motivation

Agent scripts fan out over collections constantly: fetch N pages, parse N
records, score N candidates. The only execution model today is the
serialized exec loop (`Engine.Exec` holds `Session.ExecMu`, runs
`starlark.ExecREPLChunk`, blocks until done), so independent I/O-bound
work stays sequential.

Parallel user Starlark is unsafe by default, and captures are the hard
part: a `def`/`lambda` closes over cells, defaults, its defining module's
globals and every callable reachable through them. Sharing that graph
exposes the heap to concurrent mutation and gives tasks a covert channel;
freezing changes observable semantics and cannot be undone per task.

## Proposed surface

Both builtins are engine globals, not capability ops; they grant no new I/O.

- `items` (and `fns`) must be exact lists, not arbitrary iterables, so
  preflight can inspect the full input before dispatch.
- `fn` must be a Starlark `def`/`lambda` or an explicitly registered
  copy-safe builtin. Bound methods are supported only by cloning the
  receiver and rebinding the method.
- `amap` requires `len(items) == len(fns)` (`fns[i]` applied to `items[i]`);
  mismatches fail argument checks before any task runs. `map([], fn)` and
  `amap([], [])` return `[]` after those checks.
- `concurrency` is a positive int (booleans are rejected) bounded by the
  operator hard cap (see Budget). It controls scheduling only:
  `concurrency=1` still copies per task; the returned list always matches
  input order and execution order is never guaranteed.

```python
doubled = map([1, 2, 3], lambda x: x * 2)          # [2, 4, 6]

results = amap(["a", "bb", "ccc"],
               [len, lambda s: s.upper(), lambda s: s + "!"])  # [1, "BB", "ccc!"]

# captures are per-task copies: config mutation stays isolated
cfg = {"retries": 3, "seen": []}
def worker(item):
    cfg["retries"] = 0          # mutates only the task's clone
    cfg["seen"].append(item)
    return item * cfg["retries"]
map([1, 2], worker)             # [0, 0]; parent cfg is unchanged
```

## Execution model

Structured fork/join inside `Engine.Exec`:

- The calling exec stays paused holding `Session.ExecMu`; REPL globals
  cannot be observed or mutated while tasks run.
- Each running task gets one fresh `starlark.Thread` carrying the parent
  context, exec identity and armed gate.
- A bounded lazy worker pool executes tasks: workers start on demand up to
  `concurrency`, never one goroutine per item.
- For each task, the function root and its argument are cloned **together**
  under one identity memo, so an object reachable from both (an argument
  aliased into a default, cell, or global) stays one object in the task.
- Cloning may be lazy per task: the parent is blocked and no worker can
  touch the source graph, so a deferred copy still observes exact
  call-entry state. A reused worker still builds a fresh clone per task;
  nothing carries between tasks, including repeated input elements.
- The pool lives in the existing instance process for `inproc` and
  `process`; no worker-protocol op is needed just to map. The combinators
  register through shared bindings (help, completion, docs, prompt
  metadata, EP-0002); extension context is inherited; imports are not
  re-executed.

## Capture and cloning semantics

A task's copy covers the transitive graph reachable from the function root:
parameter defaults, free-variable cells, the whole defining module's
globals and predeclared environment, and every callable reachable through
them, including functions defined in earlier REPL chunks, which close over
their own module rather than the current `s.Globals` dict.

- Aliases and cycles are preserved within a task: shared substructure stays
  shared, and recursive or mutually recursive function/module graphs work.
- Only intrinsically immutable values (scalars, strings, bytecode and
  program constants) are shared, never copied; no blanket container
  sharing. A tuple can hold a mutable list or bridge a cycle, so it is
  rebuilt element-wise under the same identity memo and shared only if
  recursively proven safe (that optimization is optional). Mutable values
  are deep-copied even when frozen; the clone preserves frozen status.
- Originals are never frozen or mutated. This is copy isolation, not freeze
  isolation.
- Modules are cloned whole: an unrelated unsupported value in the same
  defining module can fail preflight even if unused. Narrowing to
  bytecode-reachable references is a later optimization, not v1.

## Preflight and supported values

Before any task runs user code, the coordinator walks the full source
graph of all roots once (every function, element and captured module)
under node/depth/byte work bounds with cancellation. Unsupported values
fail with a sanitized structural path (task index and slot/ordinal
positions; never raw user dict keys or reprs, which could leak secret keys
or values), always **before** any capability op executes: noncopyability
is a pre-dispatch error. Later clone allocation/timeout failures need not
be all-or-nothing.

The supported graph is: None/bool/int/float/string/bytes, list, tuple,
dict, set, Starlark functions, cells and modules, cloned with memoized
construction so cyclic graphs of any of these copy correctly.

- Immutable scalars and known-pure builtins are shared; bound builtins such
  as `list.append` clone their receiver and rebind.
- Native or host-provided custom values are denied by default;
  struct/module-like values are supported only through explicitly
  registered adapters.
- Registered native capability bindings are an explicit exception to
  memory-copy isolation: the binding is an authority **handle** rebound to
  the task's inherited grant and gate rather than deep-copying Go state.
  Each such binding needs a thread-safety review of its captured Go state
  before it is enabled (a frozen extension wrapper does not imply the Go
  closure underneath is concurrency-safe); the registry fails closed for
  any binding not proven concurrency-ready.

## Result boundary

Only data crosses back: a task result must be a supported data graph (no
functions, cells, modules, bound methods, capability handles or other
opaque values). It is copied into the parent under its own identity memo,
preserving aliases within one result but never aliasing across tasks or
into source state; frozen status follows the value. There is no mutation
merge: the parent sees only explicitly returned data.

## Capability and I/O behavior

Tasks inherit the parent's exact authority: the same grants, policies,
secret domain allowlists and redaction rules, actor and exec context.
Every side effect, including calls through extension modules whose Starlark
ops re-enter the same gate on the caller's thread, goes through
`Gate.Invoke`, so hooks, policy checks and audit see each per-task call.
The combinators add no capability permission of their own.

- Gate-adjacent shared state must be audited for concurrent use before
  enabling: hooks, observers, the output buffer, quotas, the VFS, extension
  caches and any thread-locals or captured Go state.
- The filesystem and network are shared and are **not** snapshotted or
  transactional. Effect order across tasks is nondeterministic; there are
  no retries and no rollback. Two tasks writing the same path conflict;
  scripts should write independent paths and return data explicitly.
- Output shares the exec's bounded buffer: writes stay synchronized and
  secret-redacted, but line/chunk interleaving order is unspecified and is
  not input order.

## Failure and cancellation

The first real failure observed by the coordinator stops dispatching new
tasks, cancels siblings, and waits for every started task and all pending
audit to drain before returning, disarming the gate or releasing `ExecMu`.
The caller gets one error, never a partial result list.

- The error carries the task index, function and backtrace and preserves
  the classified type (`policy_denied`, `step_limit`, `timeout`, ...);
  which index fails first is not deterministic, and sibling cancellation
  noise must not replace the primary error. Capability side effects already
  performed are not rolled back.
- The parent context covers the whole operation (preflight, cloning,
  queueing, calls, result copy, join); cancellation reaches Starlark via
  `thread.Cancel` and native calls via context, so tasks cannot detach or
  outlive the join. Completed, failed and cancelled calls are audited.

## Resource budget

`concurrency` defaults to 8 and is bounded by an operator hard cap and a
runtime-wide task limit, not merely a per-call one. Exact tunings are
deployment configuration, but every bound below is mandatory and nothing is
silently truncated; new public limit fields would follow the schema-first
generation path.

- finite task count per call;
- preflight node/depth/byte bounds on the source-graph walk;
- aggregate copied-byte and result-byte bounds enforced **before** the
  allocations they would cause, with peak memory accounted;
- one aggregate Starlark step budget per exec covering the parent plus all
  children, not `maxSteps` per child, with aggregate steps reported.

The step budget needs an interpreter accounting seam (a shared counter
charged before child threads step, or a provably bounded reservation), so
N tasks cannot multiply the limit. Go builtin work is separately bounded
and cancellable; the memory watchdog/cgroup stays the last-resort fallback,
not the clone allocator.

Nested `map`/`amap` inside a task thread is explicitly rejected in v1 with
a clear error: a recursive pool could deadlock or multiply budgets. Nesting
is future work and requires a shared scheduler and shared budget.

## Interpreter prerequisites

Closures cannot be reconstructed through the exported `go.starlark.net`
API (pinned `v0.0.0-20260908191801-89a6a09411d5`): `Function` keeps
`funcode`, `module`, `defaults` and `freevars` private; `Module` keeps
`program`, `predeclared`, `globals` and `constants` private.
`Function.FreeVar`, `Function.ParamDefault`, `Module.Globals` and
`Module.Predeclared` only inspect; `Module.Globals()` copies the dict but
shares its values, and no API rebuilds a function bound to cloned cells
and a cloned module. Re-reading `s.Globals` cannot help either: functions
from earlier REPL chunks keep their own module.

This proposal therefore requires a small maintained interpreter change
(upstream preferred; a reviewed pinned patch/fork only if upstream
declines):

1. A graph-clone API that copies functions, modules and cells while
   preserving identity cycles and sharing immutable bytecode/constants.
2. A step-accounting seam so a shared counter can be charged before child
   threads step.

Excluded: `unsafe`/reflection on private fields, serialization round-trips,
re-executing source to rebuild functions, and process-per-task isolation.

## Testing plan

- Ordered results despite out-of-order completion (barriered functions);
  `amap` zip semantics, invalid arguments and empty inputs.
- Capture isolation: globals, defaults, transitive callables, functions
  from earlier REPL chunks; argument/capture aliasing inside a task.
- Repeated input elements share nothing across tasks; cyclic structures
  and recursive/mutually recursive functions.
- Frozen captures stay frozen in the clone and mutable originals are never
  frozen; bound-method receiver cloning; preflight rejects unsupported
  values with zero capability calls, including a bad `fns[i]` in `amap`.
- Capability handles cannot escape through results; per-task gate grants,
  denials, audit records, redaction and the same exec actor.
- Fail-fast cancel/join with no post-return events; child timeout and the
  aggregate step budget are not multiplied by concurrency.
- Output cap under interleaved writes; pool/task cap; nested-call
  rejection; `inproc` vs `process` parity under `-race`.

## Alternatives considered

- **Shared globals with locking.** Rejected: the gate lock covers arming
  and context setup only, and no lock makes the Starlark heap itself
  thread-safe; it also gives tasks a covert channel.
- **Freezing the captured graph.** Rejected: observable to user code,
  cannot be selectively undone per task, and mutates parent values.
- **JSON or other serialization copy.** Rejected: cannot represent
  functions, cells, modules, cycles or non-string dict keys, and silently
  changes types.
- **Source replay.** Rejected: re-executes module-level code (side effects,
  fresh identities) and cannot capture REPL-chunk state.
- **Futures / async surface.** Rejected: a plain list keeps the exec model
  simple; async result objects would leak task/callable handles.

## Open questions

- **Interpreter patch acceptance** is the main unresolved adoption choice:
  upstream review latency versus maintaining a reviewed pinned patch.
- Exact default/cap values need deployment tuning; the bounds are not
  optional. These are adoption/tuning choices on the proposed contract,
  not approval.
