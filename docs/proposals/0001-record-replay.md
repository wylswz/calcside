# EP-0001: Deterministic record / replay of instances

- **Status:** Draft
- **Created:** 2026-10-03
- **Area:** `internal/capability` (Gate), `internal/capability/net`, `internal/engine`, `internal/policy`, `internal/instance`, store, API

## Summary

Record every externally-sourced value an instance observes — today that is
the result of `net` operations — on a *tape*, alongside the instance's
inputs (spec, policy snapshot, exec code sequence). Replaying the tape on a
fresh instance re-executes every exec from zero, serving external ops from
the tape instead of the network, and reproduces the original globals, VFS,
outputs, errors and step counts exactly.

The recording point is a new **Gate interceptor** that sits between the
before hooks and the op body in `Gate.Invoke`. It is not a `Hook`: hooks can
neither see an op's value nor substitute one.

## Motivation

The Starlark language is deterministic and has no I/O. Every side effect
already flows through `Gate.Invoke`, so the external world reaches a script
through a single narrow, enumerable channel. Once that channel is recorded,
the whole instance becomes a pure function of its tape. This enables:

1. **Bug reproduction.** Take a failing agent session from production and
   replay it locally, step for step, without credentials or network access.
2. **Reproducible evals.** Agent benchmarks against live APIs are flaky. A
   tape freezes the external world, so a failure can be turned into a
   regression test.
3. **What-if policy evaluation.** Replay a recorded session under a modified
   Rego policy and see exactly which calls it would have denied.
4. **Fork / time travel.** Replay execs `0..k`, then continue with different
   code. This is useful for debugging or for exploring alternative agent
   trajectories.
5. **Recovery of `lost` instances.** Starlark state cannot be serialized
   (see `docs/execution-routing.md`, "Why not snapshot globals"). Event
   sourcing avoids the problem entirely: rebuilding state means replaying
   inputs, not deserializing values. This is a concrete implementation path
   for the currently empty `engine.Snapshotter`.

## Goals

- Byte-identical replay of `Output`, `Error.Type`, `Error.Message` and `Steps`
  for every exec, and identical final globals (`Inspect`) and VFS.
- Replay requires **no secrets** and makes **no network calls**.
- Tapes never contain plaintext secret values.
- Generic mechanism: any future capability whose ops observe the outside
  world opts in by declaring them external. No per-capability replay code.
- Divergence (replay asks for something the tape does not have) is detected
  and reported precisely. Replay never falls back to live I/O.

## Non-goals

- Replaying *side effects* on the outside world. A replayed `net.post` is
  served from the tape and is never re-sent.
- Cross-version replay guarantees. A tape is pinned to the calcside and
  Starlark versions that produced it; replay on another version is best
  effort and reported as such.
- Snapshot checkpoints (replay cost stays O(total steps)). This can be
  layered on later.

## Background: where nondeterminism enters today

`net` is the only *external* source of nondeterminism, but there are several
*internal* ones. All of them must be removed for byte-identical replay, and
most of them are bugs worth fixing on their own.

| # | Source | Location | Script-visible via | Fix |
|---|---|---|---|---|
| 1 | Go map iteration order → Starlark dict insertion order | `engine.NewSession` (`env` dict), `net.respValue` (`headers`), `fs.dictOf` (`stat` result), `ext.configDict` / `ext.toStarlark` | `keys()`, `for`, `str()`, `json.encode()` | Insert in sorted key order (or fixed field order for `stat`). |
| 2 | Wall clock | `fs.VFS.now = time.Now` (`mtime`) | `fs.stat()["mtime"]` | Per-exec frozen clock (see *Clock*). |
| 3 | Exec interruption timing | `exec_timeout_ms` is wall-clock | the step at which an exec is cancelled | Replay with `SetMaxExecutionSteps(recorded.Steps)` (see *Interrupts*). |
| 4 | Rego nondeterministic builtins | `internal/policy`: `time.now_ns`, `rand.intn`, `uuid.rfc4122` are allowed in user and global policies | allow/deny decisions | Evaluate with `rego.EvalTime(execClock)` and `rego.EvalSeed(seededReader)`. Both exist in OPA v1.20. |
| 5 | Policy eval timeout (fail closed) | `policy.Hook` `evalTimeout` | spurious denies under load | Residual. Recorded decisions detect it (see *Policies*). |
| 6 | Unpinned inputs | local ext roots (`--ext-local-roots`) have no integrity sum; server limits clamp spec values | the loaded module code; effective limits | Record the `h1:` sum of the resolved local tree and the effective (post-clamp) limits in the manifest. |
| 7 | Interpreter version | step accounting differs across `go.starlark.net` versions | `Steps` | Record versions in the manifest. |

Already deterministic (verified): `secrets.names()` (sorted), `fs.list` and
`fs.glob` (sorted), `json` and `math` modules, exec ordering (serialized by
`Session.ExecMu`), and outputs (bounded buffer, redacted).

## Design

### Interception point

Three candidate layers were considered:

| Layer | Verdict |
|---|---|
| `http.RoundTripper` | **Rejected.** Requests carry *expanded* secrets and responses are *not yet redacted*. Recording here writes plaintext secrets to disk. |
| `Hook` (`Before` / `After`) | **Rejected.** `After` only sees `Result{Meta, Err}`, not the value, and putting bodies in `Meta` violates the "no contents/bodies in hook metadata" rule and would leak them into user Rego and audit. `Before` can only deny; it cannot short-circuit with a substitute value. |
| Inside `net.client.do` | Workable but net-specific, and it sits *after* policy evaluation, so replay could not re-run hooks. |
| **Gate interceptor (chosen)** | Generic across capabilities. Wraps exactly the op body, so before/after hooks and audit still run during replay. Sees the post-redaction value. |

```go
// Interceptor wraps an op body. Unlike Hook it sees the op's value and may
// substitute it. At most one interceptor is installed per Gate.
type Interceptor interface {
	Intercept(ctx context.Context, c *Call, next Op, args map[string]any) (starlark.Value, map[string]any, error)
}
```

`Gate.Invoke` only knows `(capability, op)`. Record and replay interceptors
are built with the set of external ops taken from the `Registry` (see
below), and they pass non-external ops straight to `next`.

`Gate.Invoke` becomes:

```
armed check → before hooks → interceptor(next = op body) → after hooks → observe
```

With no interceptor installed, `Invoke` behaves exactly as today.

### Which ops are taped

Add `External bool` to `capability.OpInfo`. Only external ops are recorded
and substituted. Everything else executes for real during replay, because
it is what *rebuilds* state:

| Capability | External | Why |
|---|---|---|
| `net.get` / `net.post` / `net.request` | yes | observes the outside world |
| `fs.*` | no | in-memory VFS; must execute to rebuild state |
| `io.print` | no | must execute to rebuild output |
| `ext.*` | no | Starlark composition; nested base-cap calls re-enter the Gate and are taped at the leaf. Taping at the ext level would skip nested `fs` writes. |

Console `Browse` also goes through the Gate, but it is read-only and outside
any exec. It is never taped and does not advance the cursor.

### Tape model

```jsonc
{
  "version": 1,
  "manifest": {
    "calcside_version": "…", "starlark_version": "…",
    "spec": { /* normalized spec; inline secret values stripped */ },
    "secret_names": ["GITHUB_TOKEN"],
    "secret_domains": {"GITHUB_TOKEN": ["api.github.com"]},
    "limits": { /* effective server limits after clamping */ },
    "policies": {"global": {"name.rego": "…"}, "user": {"pol_…": "…"}},
    "ext": [{"name": "tavily", "version": "0.1.0", "sum": "h1:…"}],
    "seed": "base64…"                      // Rego EvalSeed source
  },
  "execs": [
    {
      "seq": 0,
      "exec_id": "ex_…",
      "code": "…",
      "clock": "2026-10-03T08:00:00Z",      // frozen exec clock
      "result": {
        "error_type": null, "error_message": "",
        "steps": 12345,
        "output_sha256": "…", "output_bytes": 42
      },
      "calls": [
        {
          "seq": 0,
          "capability": "net", "op": "get",
          "args": { /* Call.Args incl. req_sha256 */ },
          "value": { /* codec-encoded starlark value */ },
          "meta": {"status": 200, "bytes": 1234, "secrets_injected": 1},
          "error": null
        }
      ]
    }
  ]
}
```

Notes:

- Execs are appended in the order they acquire `Session.ExecMu`, not request
  arrival order. Execs that never ran (for example "waiting for exec slot")
  are not recorded. Idempotent re-deliveries served from `execLog` are not
  recorded twice.
- `calls` contains only external ops that **reached the op body**. Calls
  denied by a before hook are not on the tape; replay re-derives them from
  policy.

### Matching and divergence detection

The replay interceptor keeps a cursor `(exec seq, call seq)`. For each
external call it pops the next entry and requires an exact match on
`capability`, `op` and `args`.

Today `net`'s `Call.Args` carries only `method/url/host/port/scheme/secrets`.
Two POSTs to the same URL with different bodies are indistinguishable. This
proposal adds:

```go
args["req_sha256"] = sha256(canonical(headers_template, body_template, content_type))
```

computed over **templates** (`{{secrets.X}}` unexpanded), never over expanded
values. It is a JSON-able fingerprint with no content, so it fits the
hook/audit metadata rule and is also useful for audit on its own.

Divergence conditions:

| Condition | Meaning |
|---|---|
| `tape_mismatch` | the next call differs from the recorded one |
| `tape_miss` | a call was made after the recorded calls for this exec ran out |
| `tape_unconsumed` | the exec ended with recorded calls left over |
| `result_mismatch` | output hash, error or steps differ from the recorded result |

In faithful mode any divergence fails the replay with the cursor position.
In what-if mode (below) divergences are expected outcomes and are reported as
a diff.

### Value codec

External op values are encoded with Starlark's `json.encode` and decoded with
`json.decode`. `net` values are `{status: int, headers: {str: str}, body:
str}`, which round-trip exactly, provided fix #1 (sorted header insertion) is
in place. A capability whose values do not round-trip through JSON must
provide its own codec. That is a property declared alongside `External`.

### Errors

Op errors are recorded as their (already redacted) message and replayed as
`errors.New(msg)`. Starlark has no `try`/`except`, so an op error terminates
the exec and only its message is observable. `classify` keys on Gate-level
sentinels (`ErrOutOfScope`, `DeniedError`), which are produced outside the op
body and are re-derived on replay, not taped.

### Clock

Each exec gets a **frozen clock**: `now` is sampled once when the exec starts
and recorded as `execs[i].clock`. The VFS `now` func (already injectable)
returns it for every `mtime` written during that exec. Replay feeds back the
recorded value. Policy evaluation uses the same instant through
`rego.EvalTime`.

This changes observable semantics slightly: all writes within one exec share
an `mtime`. That is acceptable for a sandbox VFS, and it makes behavior
deterministic even outside of recording.

### Interrupts

Step counting is deterministic for a fixed interpreter version. If an exec
was interrupted (`timeout`, `memory_limit`), replay runs it with
`SetMaxExecutionSteps(recorded.steps)` and maps the resulting step-limit stop
back to the recorded error type. Wall-clock timeouts are disabled during
replay. An exec that ran to completion also has its step count compared
exactly.

### Policies

- **Faithful replay.** Compile the manifest's policy snapshot and evaluate
  with `rego.EvalTime(exec clock)` and `rego.EvalSeed(seed)`. Hooks run as
  normal. A decision that differs from the original shows up as
  `tape_miss` / `tape_unconsumed`. The only expected cause is a residual
  eval timeout (#5). Replay uses a generous eval timeout.
- **What-if replay.** Same tape, but the caller supplies replacement policies
  (and optionally replacement code from exec `k`). Output is a per-exec
  report: calls newly denied, calls newly reached (`tape_miss`, which are
  *not* executed live), and result diffs.

To make faithful divergence attributable, record mode may optionally store
each before-hook decision for external calls. This is an open question.

### Secrets during replay

The op body never runs, so no secret is ever expanded. The replay instance is
created with **dummy inline secrets** that have the recorded names and
domains, so `secrets.names()`, placeholder validation and `args.secrets`
behave identically. Tape values were captured *after* redaction, so the
script sees the same `[REDACTED:NAME]` markers it saw originally. Redacting
dummy values is a no-op.

### Producing and storing tapes

- The recorder lives in the instance process (`instance.Manager`). Each
  `ExecResponse` carries a `TapeSegment` (manifest on the first exec, then
  one exec entry), the same way it already carries `AuditBatch`. This is a
  worker protocol change: add the field to the `internal/runtime` contract
  types (`x-go-type`), not to the spec.
- The API tier appends segments to a new store entity (interface + GORM impl
  + `storetest` conformance), **encrypted at rest** with the existing
  `--secret-key` AES-256-GCM cipher. Response bodies may contain PII even
  though they contain no secrets.
- Access is owner-only, under the same authorization as the execution detail
  endpoint. A retention cap applies: max bytes per tape plus a TTL after
  instance end.

### Enablement

Recording is **off by default** and requires both:

- server opt-in: `--record-allow` (plus `--record-max-bytes`), and
- per-instance opt-in: `spec.record: true`.

When either is missing, no interceptor is installed and behavior is
identical to today.

### Surface

- `GET /api/v1/instances/{id}/tape` downloads the tape (owner-only).
- `POST /api/v1/replays` takes `{tape | instance_id, policies?, code_overrides?, until_exec?}` and replays into a new instance, returning a report and, optionally, the live instance for fork-and-continue.

## Security considerations

- **No plaintext secrets on tape.** The capture point is post-redaction, the
  request fingerprint is computed over templates, the manifest strips inline
  values, and replay uses dummy values.
- **Tapes are sensitive data.** They hold full response bodies. They are
  encrypted at rest, owner-only, size-capped and TTL-bound, and never part of
  audit or hook metadata.
- **Replay cannot reach the network.** The replay interceptor never calls
  `next` for external ops. A miss is an error, not a fallback.
- **Policy authority on replay.** What-if policies are user policies and are
  compiled under the restricted capability set, as today.
- **Redaction is still best effort.** Tapes inherit the documented limits of
  response redaction (it catches raw, base64, URL- and JSON-escaped
  reflections). Domain allowlists remain the primary control.

## Plan

1. **Determinism fixes** (independent, useful on their own): sorted dict
   construction (#1), per-exec frozen VFS clock (#2), and `EvalTime` /
   `EvalSeed` in policy evaluation (#4). Add tests that run the same exec
   twice and assert identical `str(env)`, headers order and stat dicts.
2. **Gate interceptor:** the `Interceptor` interface, `OpInfo.External`, and
   `req_sha256` in net args. No behavior change without an interceptor.
3. **Recorder / replayer and tape format,** exercised through an in-process `Manager`.
   Acceptance test: record a session that mixes `net` (`httptest` server),
   `fs`, `print` and an `ext` module, shut the server down, replay from
   zero, and assert identical outputs, errors, steps, `Inspect` and VFS.
4. **Persistence and API:** `TapeSegment` in the worker protocol, encrypted
   store, download and replay endpoints, server and spec opt-in.
5. **What-if and fork:** policy and code overrides, a diff report, and a
   console UI.
6. **Lost-instance recovery:** implement `engine.Snapshotter` as "replay
   tape" for instances with recording enabled.

## Alternatives considered

- **Serialize globals / VFS snapshots.** Not possible in general: closures,
  live bindings, aliasing and cycles (`docs/execution-routing.md`). The VFS
  alone could be snapshotted, but that does not restore globals.
- **HTTP-level cassette (VCR-style) proxy.** Sees expanded secrets and
  unredacted bodies, and cannot cover future non-HTTP external capabilities.
- **Record everything at the Gate, including fs and io.** Replay would then
  skip the ops that rebuild state. Only external ops may be substituted.

## Open questions

1. Should before-hook decisions for external calls be recorded, so that a
   faithful-replay policy divergence is attributed precisely rather than
   surfacing as `tape_miss` / `tape_unconsumed`?
2. Should recording be allowed for instances whose policies use `time.*`,
   `rand.*` or `uuid.*`, given that `EvalTime` / `EvalSeed` pin them? Or
   should a warning be emitted?
3. What tape size limits and retention defaults should apply? What should
   happen when the cap is hit: stop recording and mark the tape truncated, or
   fail the exec?
4. Cross-version replay: refuse, or warn and report a step-count drift
   separately from other mismatches?
5. Is `output_sha256` enough, or should full outputs be stored to make
   result diffs readable? Exec outputs are bounded by `max_output_bytes`.
