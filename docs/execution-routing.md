# Execution routing: API → worker → instance process → Manager

This document describes how an execution request travels from the API tier to
the Starlark interpreter that runs it, and why the path has the shape it has.

## The path

```
client
  │ HTTPS (public API, api/openapi.yaml)
  ▼
API tier            service/sandbox ──► runtime.Runtime = remote.Client
  │ HTTP/JSON + HMAC (api/worker.openapi.yaml), routed by instance → node binding
  ▼
worker              remote.NewHandler ──► runtime.Runtime = subproc.Supervisor
  │ same protocol over a per-instance unix socket, per-child HMAC key
  ▼
instance process    remote.NewHandler ──► runtime.Runtime = *instance.Manager (1 instance)
  │ in-memory call
  ▼
in-memory Manager   engine.Session (gate, bindings, globals, VFS) ──► engine.Exec
```

Every hop implements the same interface, `runtime.Runtime`
(`internal/runtime/runtime.go`), and every network hop speaks the same
generated protocol. A hop is therefore a thin router: it adds placement or
process management, and forwards the request otherwise unchanged. Ownership
(`Owner.UserID`) and fencing (`Epoch`) checks happen at the innermost layer,
the `Manager`, which trusts no caller.

### Hop 1: API tier → worker

- **Code:** `remote.Client` (`internal/runtime/remote/client.go`), enabled by
  `calcside serve --workers=…`.
- **Routing:** `Create` picks a live node and the API persists the binding
  (`instances.node_id`, `lease_epoch`). Every other call resolves the binding
  (with a route cache in front) and goes to that node.
- **Transport:** HTTP/JSON, HMAC-signed with the shared worker key.
- **Retries:** only `Keepalive`, `Browse`, `Prompt` and `Inspect` are retried
  on a stale route. `Create`, `Exec` and `Delete` have external side effects
  and are never retried transparently.
- See `docs/api-worker-split.md` for placement, fencing and idempotency.

### Hop 2: worker → instance process

- **Code:** `subproc.Supervisor` (`internal/node/subproc/supervisor.go`),
  served by `remote.NewHandler` in `cmd/calcside-worker`.
- **Create:** spawns a child, which is the worker binary re-executed as
  `calcside-worker __instance`. Its config (`subproc.ChildConfig`: node
  limits, ext policy, socket path, a freshly minted HMAC key) is sent as JSON
  on the child's **stdin**, never argv or env, because it carries keys. The
  child prints a ready line once it is serving. The supervisor then forwards
  `Create` and records `instance_id → child` in its routing table.
- **Other calls:** forwarded by instance id through `remote.Direct`, a
  single-endpoint client with no placement, over a unix socket in a private
  (0700) temp dir.
- **What the supervisor owns:**
  - node-wide limits: the `--max-instances-per-node` cap and global exec
    concurrency (`--max-concurrent-execs`);
  - reaping instances whose TTL lapsed and was not renewed;
  - killing a child whose exec overruns `MaxExecTimeout + 30s`.
- **What it does not own:** Starlark state of any kind.

### Hop 3: instance process → in-memory Manager

- **Code:** `subproc.RunChild` (`internal/node/subproc/child.go`).
- **Setup:** builds a normal execution node (`node.Build`: capability
  registry, engine, `instance.Manager`) and serves it with
  `remote.NewHandler`. Its Manager is capped at one instance and runs no
  reaper; lifecycle belongs to the supervisor.
- **The Manager does the actual work:**
  - re-checks owner and epoch;
  - deduplicates execs by `exec_id`;
  - builds the session with `engine.NewSession` from plain data
    (`SessionCreation`) plus live collaborators (`SessionDeps`);
  - runs code with `engine.Exec`.
- **State:** globals, the VFS, compiled Rego policies, loaded extensions and
  decrypted secrets all live in this process's memory for the instance's
  whole life.

## Deployment modes

`--instance-isolation=inproc|process` selects whether the innermost hop
exists. The other hops are unchanged.

| Mode | Path |
|---|---|
| `calcside-worker serve` (default `process`) | worker → instance process → Manager |
| `calcside-worker serve --instance-isolation=inproc` | worker → Manager (all instances in the worker process) |
| `calcside serve` (default `inproc`, no `--workers`) | API → Manager in the same process (dev, tests) |
| `calcside serve --instance-isolation=process` | API → instance process → Manager |
| `calcside serve --workers=…` | API → worker → … (as configured on the worker) |

Both `calcside` and `calcside-worker` understand the hidden `__instance`
subcommand, so whichever binary is the supervisor re-executes itself. There is
no second binary to deploy and no version skew between parent and child.
`cmd/calcside-exec` runs the same entrypoint standalone.

## Why this shape

### 1. Scalability

- **The API tier is stateless.** It holds no Starlark state, so API nodes
  scale horizontally and any of them can serve any request.
- **Workers scale independently of the API.** Instances are pinned to one
  worker for life (their state is not serializable), and placement spreads new
  instances across live workers.
- **The contract is the only coupling.** Each tier depends only on
  `runtime.Runtime` and its serializable messages. Adding the process hop
  needed no new protocol: the supervisor is one more `Runtime` in front of the
  same handler. Further hops (a per-host agent, a sandboxing runtime) compose
  the same way.

### 2. Isolation: Starlark heap memory cannot be isolated inside one process

- **One Go heap.** Every instance's Starlark values (lists, dicts, strings,
  closures) are ordinary Go objects on the same heap. Go has no per-goroutine
  or per-arena memory accounting and no way to cap one interpreter's
  allocations. The only memory figures available (`runtime/metrics`) are
  process-wide.
- **Step limits bound CPU, not memory.** One step can allocate an arbitrarily
  large value (`"a" * 10**9`), so `MaxSteps` does not contain memory use.
- **The old in-process heap watchdog could only act on everyone.** It sampled
  the process heap and, once over the limit, cancelled *every* running exec,
  because it could not tell which instance owned the memory. One tenant's
  runaway list cost every co-located tenant its exec.
- **Failures are process-wide.** Go's out-of-memory is fatal and cannot be
  recovered, and so is a fatal runtime error in any builtin. Either one kills
  every instance on the node. GC pressure from one instance also stalls all
  the others.

The OS process is the smallest unit the kernel can account, limit (rlimit,
cgroups) and kill on its own. One instance per process means:

- a runaway allocation, a fatal error or an OOM kill takes down only its own
  instance;
- memory limits can be enforced per instance by the OS, not guessed from a
  shared heap;
- the supervisor can always reclaim an instance, even a wedged one, with a
  kill.

### Why not snapshot globals and run one process per exec

Starlark state is not serializable in general:

- `def`/`lambda` values capture compiled code, their module globals and
  closure cells;
- capability bindings (`fs`, `net`, ext modules, `print`) are bound to live
  gates, VFS instances and buffers;
- mutable values keep aliasing and cycles that a JSON round-trip loses.

A per-exec process would therefore break the REPL guarantee that globals
persist across execs. The instance process keeps that guarantee for free:
state never leaves memory, and the process lives exactly as long as the
instance.

## Lifecycle and failure semantics

| Event | Effect |
|---|---|
| `Create` | Spawn child, wait for ready, forward `Create`. On failure the child is terminated. |
| `Delete` accepted by child (or child says `not_found`) | Child terminated: stdin closed, then killed after 5s. Owner/epoch rejections leave it running. |
| TTL lapsed + reap grace | Supervisor reaper terminates the child. |
| Exec overruns `MaxExecTimeout + 30s` | Child is killed and the instance is gone (`not_found`). The child enforces the real exec timeout; this only catches a wedged child. |
| Child crashes or is OOM-killed | Unrouted immediately. Later calls return `not_found` and the instance's state is lost. |
| Worker dies, even with `kill -9` | Children see EOF on stdin and exit, so no orphans. |
| Worker restarts | Same as before: a new `boot_id`, and instances bound to the node are marked lost. |

## Security notes

- Child config, including keys, travels only on stdin.
- Each child gets its own random HMAC key and a socket in a private temp dir,
  so only the supervisor can reach it.
- Owner and epoch are re-checked in the child, as on any node.
- Children ignore SIGINT/SIGTERM; their lifecycle is driven by the supervisor
  through stdin.

## Known gaps

- **Per-instance memory limits need cgroup v2.** `--instance-memory-max`
  (default 64 MiB, `0` disables) gives each child its own cgroup under
  `--instance-cgroup-parent` (default: the supervisor's own group, which it
  leaves for a `supervisor` leaf) with `memory.max` set and swap disabled, and
  sets the child's `GOMEMLIMIT` to 90% of it. Off Linux, or without a writable
  cgroup v2 hierarchy (e.g. an unprivileged container), the supervisor logs a
  warning and runs children uncapped. `--exec-memory-limit` is still parsed
  but not enforced.
- **The child holds the API shared key** so it can resolve local ext sources
  over `/internal/v1/ext/tree`. The supervisor could proxy this instead.
- **Browse audit events are lost on error.** Over the wire, a failed `Browse`
  returns only the error envelope, so its audit events are dropped. Remote
  workers already behave this way.
- **Process start cost.** `Create` pays a process start, about 10–20ms
  locally. A pool of pre-forked idle children would remove it if it matters.
- **Keepalive still makes the full trip.** It goes all the way to the child.
  Phase 5 of `api-worker-split.md` moves it to an API-side write.
