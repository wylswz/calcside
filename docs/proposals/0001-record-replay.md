# EP-0001: Deterministic record / replay of segments

- **Status:** Draft
- **Created:** 2026-10-03
- **Revised:** 2026-10-29 — generalized from instances to a shared segment
  recording infrastructure consumed by instances and applications;
  application runs are recorded by default; added call addressing,
  matching modes, loop handling and the replay console. In-program
  resumption dropped: long-running application runs are chains of
  independent steps (EP-0016), each one segment.
- **Area:** `internal/capability` (Gate), `internal/capability/net`, `internal/engine`, `internal/policy`, `internal/instance`, worker protocol, store, API, console
- **Related:** [EP-0014](0014-persistent-instances.md) (applications), [EP-0015](0015-copy-isolated-map.md) (parallel `map`), [EP-0016](0016-application-interrupts.md) (interrupts and handlers)

## Summary

Record every externally-sourced value a Starlark program observes — today
that is the result of `net` operations — on a *tape*, together with the
inputs that determine the rest of the run (code, input, spec, policy
snapshot). Replaying the tape re-executes the program from zero, serving
external ops from the tape instead of the network, and reproduces the
original outputs, errors, step counts, globals and VFS exactly.

One recording infrastructure serves two consumers with different goals:

| Consumer | Recorded unit | Purpose |
|---|---|---|
| **Instance** (playground) | every exec of a live instance | bug reproduction, evals, what-if policy, fork / time travel |
| **Application** (EP-0014, EP-0016) | every step of every run (test or invoke; `main` and each handler) | run history, debugging a business run, what-if code/input/context/response |

Both share the Gate interceptor, call addressing, tape format, codec,
storage, replayer, divergence report and console components. They differ
only in where the manifest comes from, how many segments a tape has, and
the enablement and retention defaults.

## Motivation

The Starlark language is deterministic and has no I/O. Every side effect
already flows through `Gate.Invoke`, so the external world reaches a script
through a single narrow, enumerable channel. Once that channel is recorded,
a run becomes a pure function of its tape. This enables:

1. **Bug reproduction.** Replay a failing instance session or application
   run locally, step for step, without credentials or network access.
2. **Reproducible evals and regression tests.** A tape freezes the external
   world; a failure becomes a test case.
3. **What-if evaluation.** Replay a recorded run under modified policies,
   code, input or individual recorded responses and see exactly what
   changes: newly denied calls, calls the tape cannot answer, output diffs.
4. **Fork / time travel** (instances). Replay execs `0..k`, then continue
   with different code.
5. **Run history** (applications). An enterprise SOP must answer "what did
   this run see, what did it do, why did it produce this output".
6. **Lost-instance recovery** (follow-up). Starlark state cannot be
   serialized (see `docs/execution-routing.md`). Re-executing a recorded
   instance from zero rebuilds its state instead.

Recording is **not** the mechanism for long-running application runs.
Those are chains of independent steps that hand over explicit context
through interrupts (EP-0016); no program is ever resumed mid-call, and
each step is simply one more segment.

## Goals

- Byte-identical replay of output, error type and message, and steps for
  every segment, plus identical final globals (`Inspect`) and VFS.
- Replay requires **no secrets** and makes **no network calls**.
- Tapes never contain plaintext secret values.
- Generic mechanism: any capability whose ops observe the outside world
  opts in by declaring them external. No per-capability replay code.
- One infrastructure for instances and applications.
- Divergence is detected and reported precisely, addressed by structural
  call position. Replay never falls back to live I/O.
- Loops, pagination and polling are handled without user annotation, both
  in matching and in presentation.
- The tape format covers parallel `map` (EP-0015) and multi-step runs
  (EP-0016) without a format break.

## Non-goals

- Replaying *side effects* on the outside world. A replayed `net.post` is
  served from the tape and is never re-sent.
- Cross-version replay guarantees. A tape is pinned to the calcside and
  Starlark versions that produced it; replay on another version is best
  effort and reported as such.
- Snapshot checkpoints. Replay cost stays O(total steps).
- Resuming execution after a replayed prefix. Waiting and human tasks are
  EP-0016 interrupts, which end a step instead of pausing it.
- Exactly-once delivery of external effects.

## Language

- **Recording window** — one explicitly opened `Arm … Disarm` interval of
  the Gate that is recorded. Only callers that open a window *as recorded*
  produce tape entries (see *Recording windows*).
- **Segment** — the tape entry for one recording window: its code identity,
  input, frozen clock, recorded calls and result. An instance exec is one
  segment; an application run is one segment.
- **Tape** — a manifest plus an ordered list of segments, owned by one
  instance or one application run.
- **External call** — an invocation of an op declared `External`; its value
  is recorded and, on replay, substituted.
- **Span** — a non-substituted structural record of a non-external gated
  call that encloses external calls (today: `ext.*` ops). Spans exist for
  presentation and addressing only.
- **Call address** — the structural position of an external call or span
  inside a segment (see *Call addressing*).
- **Call site** — `file:line` and function stack of the Starlark code that
  made the call.

## Background: where nondeterminism enters today

`net` is the only *external* source of nondeterminism, but there are several
*internal* ones. All of them must be removed for byte-identical replay, and
most of them are bugs worth fixing on their own.

| # | Source | Location | Script-visible via | Fix |
|---|---|---|---|---|
| 1 | Go map iteration order → Starlark dict insertion order | `engine.NewSession` (`env` dict), `net.respValue` (`headers`), `fs.dictOf` (`stat` result), `ext.configDict` / `ext.toStarlark` | `keys()`, `for`, `str()`, `json.encode()` | Insert in sorted key order (or fixed field order for `stat`). |
| 2 | Wall clock | `fs.VFS.now = time.Now` (`mtime`) | `fs.stat()["mtime"]` | Per-segment frozen clock (see *Clock*). |
| 3 | Timeout timing | `exec_timeout_ms` and invoke timeouts are wall-clock | the step at which a segment is cancelled | Replay with `SetMaxExecutionSteps(recorded.steps)` (see *Timeouts and resource stops*). |
| 4 | Rego nondeterministic builtins | `internal/policy`: `time.now_ns`, `rand.intn`, `uuid.rfc4122` are allowed in user and global policies | allow/deny decisions | Evaluate with `rego.EvalTime(segmentClock)` and `rego.EvalSeed(seededReader)`. Both exist in OPA v1.20. |
| 5 | Policy eval timeout (fail closed) | `policy.Hook` `evalTimeout` | spurious denies under load | Residual. Recorded decisions detect it (see *Policies*). |
| 6 | Unpinned inputs | local ext roots (`--ext-local-roots`) have no integrity sum; server limits clamp spec values | the loaded module code; effective limits | Record the `h1:` sum of the resolved local tree and the effective (post-clamp) limits in the manifest. Application releases already snapshot their boundary. |
| 7 | Interpreter version | step accounting differs across `go.starlark.net` versions | `Steps` | Record versions in the manifest. |
| 8 | Parallel scheduling (EP-0015, not implemented) | `map` / `amap` worker threads | order of gated calls | Structural call addresses (see *Call addressing*); never global temporal order. |

Already deterministic (verified): `secrets.names()` (sorted), `fs.list` and
`fs.glob` (sorted), `json` and `math` modules, exec ordering (serialized by
`Session.ExecMu`), application input decoding (`serving.DecodeJSON`) and
outputs (bounded buffer, redacted).

## Design

### Recording windows

The Gate is armed in several places, and they are not equivalent:

| Arm site | Window | Recorded |
|---|---|---|
| `engine.Exec` (instance exec) | one exec | yes, when the instance records |
| `engine` program invoke (around `main`, application test/invoke) | one run | yes, when application recording is on |
| `instance.withConsole` (`ExecID: "console"`, e.g. Browse) | host-side console op | **no** |
| `instance` artifact export (`ExecID: "artifact"`, preview bundling) | host-side read | **no** |
| application prepare / pure init | Gate disarmed; effectful calls denied | nothing to record; covered by the code identity |

Recording is therefore not a side effect of `Arm`. A caller opens a window
*as recorded* by passing a recorder to the arm call (for example
`Gate.ArmRecorded(ec, rec)`); plain `Arm` never records and never advances
any cursor. Console and artifact windows keep using plain `Arm`. This keeps
the decision with the caller that knows the window's meaning, and a new arm
site is unrecorded by default.

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
// substitute it. At most one interceptor is active per recording window.
type Interceptor interface {
	Intercept(ctx context.Context, c *Call, site CallSite, next Op, args map[string]any) (starlark.Value, map[string]any, error)
}
```

`Gate.Invoke` becomes:

```
armed check → before hooks → interceptor(next = op body) → after hooks → observe
```

With no interceptor installed, `Invoke` behaves exactly as today. Two
interceptors exist: **record** (calls `next`, appends the result) and
**replay** (never calls `next` for external ops).

### Which ops are taped

Add `External bool` to `capability.OpInfo`. Only external ops are recorded
and substituted. Everything else executes for real during replay, because
it is what *rebuilds* state:

| Capability | External | Why |
|---|---|---|
| `net.get` / `net.post` / `net.request` | yes | observes the outside world |
| `interrupt.new` (EP-0016) | yes | reserves a durable interrupt; replay serves the recorded id and never reserves |
| `interrupt.wait` (EP-0016) | no | ends the segment; its waiting outcome is compared like any result |
| `fs.*` | no | in-memory VFS; must execute to rebuild state |
| `io.print` | no | must execute to rebuild output |
| `ext.*` | no — recorded as a **span** | Starlark composition; nested base-cap calls re-enter the Gate and are taped at the leaf. Substituting at the ext level would skip nested `fs` writes. |

An `ext` op opens a span when it starts and closes it when it returns; the
external calls it makes are children of that span. Spans carry the ext
name, op, call site, duration and error, never values. They give the
console "which extension did this" without changing what is substituted.

A future capability whose external values do not round-trip through JSON
declares its own codec alongside `External`.

### Call addressing and call sites

Every external call and span gets a **structural address** inside its
segment, not a global sequence number:

```
call[3]                       third top-level gated call in the segment
span[1]/call[0]               first call inside the second ext span
map[0]/task[7]/call[2]        (EP-0015) third call of task 7 in the first map
```

Within one sequential scope, indices count in execution order, which is
deterministic. A parallel `map` opens a scope per task, indexed by input
position, so its address is independent of scheduling. Sequential code
pays nothing for this; parallel code needs no new format.

The interceptor also records the **call site**: `thread.CallStack()` at
`Gate.Invoke`, reduced to `file:line` plus function names for at most a
bounded number of frames. Call sites are used for presentation only; they
never participate in matching, so reformatting code (which moves line
numbers) does not by itself cause a divergence.

### Loops

Loops need no special handling in the core:

- **Faithful replay.** Determinism guarantees the same sequence of calls in
  every scope. A loop of 1,000 iterations is 1,000 consecutive addresses,
  exactly like straight-line code.
- **Loops across interrupts.** A loop that waits for a person per
  iteration is not one segment: under EP-0016 each wait ends the step and
  the loop cursor travels in the interrupt context. Every iteration is a
  separate segment and replays on its own.
- **Edited code.** When the code or input changes, iteration counts change
  and sequential addresses shift. This is handled by *keyed* matching.
- **Polling.** The same request returns different responses over time.
  Keyed matching serves them in recorded order; asking once more than
  recorded is a `tape_miss`, which is correct — nobody knows what the world
  would have answered. The console lets the user supply or edit a response
  for that slot.
- **Presentation.** Consecutive calls from the same call site collapse into
  one row ("`net.get` @ `main.star:12` ×237") that expands to individual
  iterations; nested loops nest by call stack. No loop instrumentation in
  the interpreter is required.
- **Volume.** Large loops produce large tapes. Bounded by *Limits*.

### Matching and divergence detection

Strict equality needs a request fingerprint. Today `net`'s `Call.Args`
carries only `method/url/host/port/scheme/secrets`; two POSTs to the same
URL with different bodies are indistinguishable. Add:

```go
args["req_sha256"] = sha256(canonical(headers_template, body_template, content_type))
```

computed over **templates** (`{{secrets.X}}` unexpanded), never over
expanded values. Known secret values that appear literally in a template
(for example an EP-0016 callback token, which the program holds as a plain
string) are replaced by their name marker before hashing, so the live run
and a replay with dummy values produce the same fingerprint. It is a
JSON-able fingerprint with no content, so it fits the hook/audit metadata
rule and is also useful for audit on its own.

The match key of an external call is `(capability, op, canonical args)`.

| Mode | Matching | Used for |
|---|---|---|
| **strict** | each call must match the entry at the same address exactly | faithful replay, regression tests |
| **keyed** | per-key FIFO queues; address and call site are ignored | what-if replay with edited code, input, context or policies |

Keyed mode makes per-iteration requests (pagination cursors, per-item
URLs) line up when iterations are inserted, removed or reordered. Inside a
parallel `map`, a call is first matched against its own task's queue and
falls back to the segment-wide queue, so identical requests from different
tasks (such as fetching the same token) do not steal each other's
responses, while changed input lists still align by content.

Divergence conditions:

| Condition | Meaning |
|---|---|
| `tape_mismatch` | strict: the call at this address differs from the recorded one |
| `tape_miss` | no recorded entry answers this call |
| `tape_unconsumed` | the segment ended with recorded calls left over |
| `result_mismatch` | output hash, error or steps differ from the recorded result |

In faithful mode any divergence fails the replay with the address and call
site. In what-if mode divergences are expected outcomes and are reported as
a diff. A `tape_miss` in what-if mode either fails the segment with a
Starlark error at that call or, if the user supplied an override for that
key, returns the override (see *Overrides*). It never reaches the network.

### Tape model

```jsonc
{
  "version": 1,
  "owner": "ins_… | run_…",
  "consumer": "instance | application",
  "truncated": false,
  "manifest": {
    "calcside_version": "…", "starlark_version": "…",
    "source": {
      // instance:    { "spec": {…inline secret values stripped}, "policies": {…}, "ext": [{…, "sum": "h1:…"}] }
      // application: { "application_id": "app_…", "release_id": "rel_…" }
    },
    "secret_names": ["GITHUB_TOKEN"],
    "secret_domains": {"GITHUB_TOKEN": ["api.github.com"]},
    "limits": { /* effective server limits after clamping */ },
    "seed": "base64…"                      // Rego EvalSeed source
  },
  "segments": [
    {
      "seq": 0,
      "id": "exe_… | inv_…",
      "code": "…",                          // instance only; applications use the release
      "entry": "main",                      // application only: "main" or a handler name
      "input": { /* application main: validated input JSON */ },
      "handler_context": { /* application handler: the EP-0016 context, incl. interrupt_id */ },
      "clock": "2026-10-03T08:00:00Z",      // frozen segment clock
      "actor": "usr_…",
      "result": {
        "error_type": null, "error_message": "",
        "steps": 12345,
        "output": "…",                      // bounded, redacted
        "waiting": null                     // EP-0016: {interrupt_id, handler, context}
      },
      "tasks": [                            // EP-0015: one entry per map task
        {"addr": "map[0]/task[7]", "outcome": "cancelled", "calls": 2, "steps": 812, "output_bytes": 40}
      ],
      "calls": [
        {
          "addr": "span[0]/call[0]",
          "site": {"file": "main.star", "line": 12, "stack": ["main", "fetch_page"]},
          "capability": "net", "op": "get",
          "args": { /* Call.Args incl. req_sha256 */ },
          "value": { /* codec-encoded starlark value */ },
          "meta": {"status": 200, "bytes": 1234, "secrets_injected": 1},
          "error": null,
          "duration_ms": 87
        }
      ],
      "spans": [
        {"addr": "span[0]", "capability": "ext", "op": "tavily.search",
         "site": {"file": "main.star", "line": 10, "stack": ["main"]},
         "duration_ms": 120, "error": null}
      ]
    }
  ]
}
```

Notes:

- For applications the release (EP-0014) is already immutable and
  content-addressed, covering sources, schemas, normalized boundary,
  policy snapshots and pinned extensions; the manifest refers to it
  instead of copying it. A release referenced by a retained tape must not
  be garbage-collected.
- Instance segments are appended in the order they acquire
  `Session.ExecMu`, not request arrival order. Execs that never ran are not
  recorded; idempotent re-deliveries served from `execLog` are not recorded
  twice.
- `calls` contains only external ops that **reached the op body**. Calls
  denied by a before hook are not on the tape; replay re-derives them from
  policy.
- A segment is delivered when it ends. Nothing depends on a partial
  segment being durable.
- Outputs are stored (bounded and redacted, as the exec/run already
  returns them) rather than only hashed, so result diffs are readable.

### Value codec

External op values are encoded with Starlark's `json.encode` and decoded with
`json.decode`. `net` values are `{status: int, headers: {str: str}, body:
str}`, which round-trip exactly, provided fix #1 (sorted header insertion) is
in place. A capability whose values do not round-trip through JSON must
provide its own codec.

### Errors

Op errors are recorded as their (already redacted) message and replayed as
`errors.New(msg)`. Starlark has no `try`/`except`, so an op error terminates
the segment and only its message is observable. `classify` keys on
Gate-level sentinels (`ErrOutOfScope`, `DeniedError`), which are produced
outside the op body and are re-derived on replay, not taped.

### Clock

Each segment gets a **frozen clock**: `now` is sampled once when the window
opens and recorded as `segments[i].clock`. The VFS `now` func (already
injectable) returns it for every `mtime` written during that segment.
Replay feeds back the recorded value. Policy evaluation uses the same
instant through `rego.EvalTime`.

All writes within one segment share an `mtime`. That is acceptable for a
sandbox VFS, and it makes behavior deterministic even outside of
recording.

### Timeouts and resource stops

Step counting is deterministic for a fixed interpreter version. If a
segment was stopped (`timeout`, `memory_limit`), replay runs it with
`SetMaxExecutionSteps(recorded.steps)` and maps the resulting step-limit
stop back to the recorded error type. Wall-clock timeouts are disabled
during replay. A segment that ran to completion also has its step count
compared exactly.

### Parallel `map`

EP-0015 runs tasks on worker threads; which task fails first, how far
cancelled siblings got and which task exhausts the shared step budget all
depend on scheduling. Structural addresses fix call matching but not these
outcomes, so the recorder stores one entry per task (`tasks` above):
`completed`, `failed` with its error, or `cancelled` with the number of
calls it made, its steps and its output bytes at cancellation.

Replay does not need parallelism: there is no I/O to wait for. The
replayer runs tasks **sequentially in input order**:

- `completed` and `failed` tasks depend only on the tape and reproduce.
- A `cancelled` task is cancelled at the next gated call after consuming
  its recorded calls, with its output cut at the recorded byte count.
- The map's error is the recorded primary error, not re-raced.
- Before-hook decisions for calls inside a map are recorded, because any
  shared counter near the Gate (quotas, rate limits) makes which task is
  denied scheduling-dependent.

This relies on EP-0015 keeping task-visible state deterministic: tasks see
a read-only VFS (v1) and write output to per-task buffers joined in input
order. A task that could observe another task's writes would make replay
unreliable no matter what is recorded.

### Policies

- **Faithful replay.** Instances compile the manifest's policy snapshot;
  applications use the release's. Evaluation uses `rego.EvalTime(segment
  clock)` and `rego.EvalSeed(seed)`; hooks run as normal. A decision that
  differs from the original shows up as `tape_miss` / `tape_unconsumed`.
  The only expected cause is a residual eval timeout (#5). Replay uses a
  generous eval timeout.
- **What-if replay.** The caller supplies replacement user policies; the
  report lists calls newly denied, calls newly reached (`tape_miss`, *not*
  executed live) and result diffs. Server `--policy-dir` policies always
  apply, as at run time.

### Secrets during replay

The op body never runs, so no secret is ever expanded. The replay runtime
is created with **dummy secrets** that have the recorded names and
domains, so `secrets.names()`, placeholder validation and `args.secrets`
behave identically. Tape values were captured *after* redaction, so the
script sees the same `[REDACTED:NAME]` markers it saw originally. Redacting
dummy values is a no-op. No vault access is needed to replay.

### Overrides

A replay request may override, on top of the recorded tape:

| Override | Instance | Application | Effect |
|---|---|---|---|
| code | from segment `k` onward | whole program (sources only; schemas and boundary stay those of the recorded release) | runs edited logic against recorded responses |
| input | — | replacement input (`main`, validated against the release input schema) or replacement handler context / event (handlers) | explores another business case or branch |
| policies | user policies | user policies | what-if authorization |
| responses | per call address (strict) or per match key (keyed) | same | edit a recorded response or fill a `tape_miss`, e.g. to exercise a failure branch |

Any override implies what-if mode. Overrides are inputs of the replay
report, never written back into the original tape.

### Consumers

#### Instances

- Opt-in, as before: server `--record-allow` **and** `spec.record: true`.
  The playground stays unrecorded by default because instances are long
  lived and interactive.
- Tape owner is the instance id; one segment per recorded exec.
- Replay creates a new instance, replays segments `0..k` (default: all),
  and returns a report plus, optionally, the live instance for
  fork-and-continue.
- Retention: TTL after instance end.

#### Applications

- **Recorded by default.** Every application run produces a tape unless
  the operator disables application recording server-wide
  (`--record-applications=false`). There is no per-application opt-out in
  this phase: run history depends on every run having a tape.
- An application **run** is a durable record (`run_` prefix, shared with
  EP-0016): application id, release id, actor, trigger (`test` / `invoke`
  / `replay`), status, steps, timings, error type, output size and tape
  id. Run metadata never holds input, context, output or bodies; those
  live in the encrypted tape. This is also the run history EP-0014 lacks.
- Tape owner is the run id; one segment per step (`main`, then each
  handler call). Steps share no state, so **every segment replays
  independently**: there is no prefix replay for applications.
- Replay runs on an ephemeral replica (same path as `Test`): prepare the
  recorded release (or the code override), install the replay
  interceptor and dummy secrets, call the segment's entry with the
  recorded or overridden input / handler context. A replay is itself a run
  with trigger `replay`, referencing the source run and segment, and
  produces a report. A replayed `wait` never commits an interrupt.
- Retention: per-workspace run retention (default bounded by count and
  age), independent of application deletion only insofar as audit
  requires; tapes of deleted applications are removed with them.

### Producing and storing tapes

- The recorder lives in the instance child process, in the engine layer
  shared by exec and program invoke. Each `ExecResponse` and
  `InvokeResponse` carries a `TapeSegment`, the same way it already carries
  `AuditBatch`; failure paths carry it too. This is a worker protocol
  change: add the field to the `internal/runtime` contract types
  (`x-go-type`), not to the spec.
- The API tier appends segments to one store entity for both consumers
  (interface + GORM impl + `storetest` conformance, paired Atlas
  migrations), keyed by a prefixed `owner_id` (`ins_…` or `run_…`) in the
  same spirit as `api_keys.bound_id`. Segment bodies are **encrypted at
  rest** with the existing `--secret-key` AES-256-GCM cipher. Response
  bodies and inputs may contain PII even though they contain no secrets.
- Access goes through `service/access` like everything else: reading a
  tape or replaying requires View on the owner plus Configure for
  overrides; restricted, bound and view-only keys cannot read tapes.
  `TestRouteCoverage` applies to every new operation.

### Limits

- Per segment: max external calls, max encoded bytes, max spans.
- Per tape: max bytes; per workspace: retention by count/age.
- When a cap is hit, recording **stops and the tape is marked
  `truncated`**; the segment itself continues and succeeds or fails on
  its own merits. Truncated segments can be viewed but not replayed
  faithfully.

### Surface

All routes land in `api/openapi.yaml` first; shapes are illustrative.

| Route | Purpose |
|---|---|
| `GET /api/v1/instances/{id}/tape` | download an instance tape |
| `GET /api/v1/applications/{id}/runs` | list runs (metadata only) |
| `GET /api/v1/runs/{id}` | run detail: metadata, output, call tree |
| `GET /api/v1/runs/{id}/tape` | download a run tape |
| `POST /api/v1/replays` | `{source: ins_…|run_…, segment? (application) or until_segment? (instance), mode: strict|keyed, overrides?}` → report (+ optional forked instance or replay run id) |

The call tree endpoint returns addresses, call sites, spans, metadata and
durations; values are fetched per call so large bodies are loaded on
demand.

### Console

A shared replay workspace, opened from an instance ("Recording") or an
application run:

1. **Run list** (applications): status, trigger, actor, duration; polled
   lists keep order behind "Show N new" (S5).
2. **Call tree**: segments (for applications, the step chain with its
   interrupts, EP-0016) → spans / map tasks → calls, consecutive same-site
   calls collapsed as loops (×N), nested by stack; map tasks also get a
   timeline so the slowest item is visible. Selecting a call shows args,
   meta, duration and the decoded value (redacted), with its code location
   highlighted in the source.
3. **Replay**: choose strict or keyed; edit code, input, policies or a
   single response (or fill a miss); run.
4. **Diff report**: consumed / missed / unconsumed / mismatched calls by
   address and site, newly denied calls, output and error diff against the
   source run.

UI work keeps every `docs/interaction-stability.md` invariant; editing
dialogs ask before discarding input (C2), destructive actions use
`Confirm` (F2).

## Security considerations

- **No plaintext secrets on tape.** The capture point is post-redaction,
  the request fingerprint is computed over templates, the manifest strips
  inline values, and replay uses dummy values.
- **Tapes are sensitive data.** They hold full response bodies and
  application inputs. They are encrypted at rest, size-capped,
  retention-bound, authorized through `service/access`, and never part of
  audit or hook metadata. Recording applications by default makes this the
  normal case, not an opt-in one; operators can disable it server-wide.
- **Replay cannot reach the network.** The replay interceptor never calls
  `next` for external ops. A miss is an error or an explicit override, not
  a fallback.
- **Policy authority on replay.** What-if policies are user policies and
  are compiled under the restricted capability set, as today; server
  policies always apply.
- **Redaction is still best effort.** Tapes inherit the documented limits
  of response redaction (raw, base64, URL- and JSON-escaped reflections).
  Domain allowlists remain the primary control.

## Plan

1. **Determinism fixes** (independent, useful on their own): sorted dict
   construction (#1), per-segment frozen VFS clock (#2), and `EvalTime` /
   `EvalSeed` in policy evaluation (#4). Tests run the same exec and the
   same program invoke twice and assert identical `str(env)`, header order
   and stat dicts.
2. **Gate interceptor and recording windows:** `Interceptor`,
   `OpInfo.External`, recorded vs plain arm, `req_sha256` in net args
   (with known-secret normalization), call sites, ext spans, structural
   addresses. No behavior change without an
   interceptor; console and artifact windows provably unrecorded.
3. **Recorder / replayer, tape format, strict and keyed matching,**
   exercised in-process for both consumers. Acceptance: record an instance
   session and an application run that mix `net` (`httptest` server),
   `fs`, `print`, an `ext` module and a paginated loop; shut the server
   down; replay from zero and assert identical outputs, errors, steps,
   `Inspect` and VFS. Keyed replay with an inserted loop iteration reports
   exactly one `tape_miss`.
4. **Persistence and API:** `TapeSegment` in the worker protocol (both
   exec and invoke responses, including failure paths), the tape entity,
   application runs, encryption, limits and truncation, access table
   entries, application recording on by default, instance opt-in.
5. **Replay surface and overrides:** `/replays`, code/input/policy/response
   overrides, diff report, fork-and-continue for instances.
6. **Console:** run list, call tree with loop collapsing, replay and diff.
7. **Integrations:** per-step segments for EP-0016 runs; per-task outcomes
   and sequential replay when EP-0015 lands.
8. **Follow-up proposal:** lost-instance recovery as an
   `engine.Snapshotter` implemented by replay.

## Alternatives considered

- **Serialize globals / VFS snapshots.** Not possible in general: closures,
  live bindings, aliasing and cycles (`docs/execution-routing.md`).
- **HTTP-level cassette (VCR-style) proxy.** Sees expanded secrets and
  unredacted bodies, and cannot cover future non-HTTP external capabilities.
- **Record everything at the Gate, including fs and io.** Replay would then
  skip the ops that rebuild state. Only external ops may be substituted.
- **Substitute at the ext level.** Skips nested `fs` writes inside the
  extension. Ext calls are spans instead.
- **Global sequence numbers.** Simple, but break under parallel `map` and
  give edited-code replay nothing to align on. Structural addresses plus
  keyed matching cover both.
- **Explicit step/DAG declarations for addressing.** Would force users to
  name steps, at the cost of the high-code model. Determinism already
  provides stable addresses.
- **Using replay to resume suspended runs.** Rebuilds state without
  serialization, but couples correctness of production runs to perfect
  replay across loops, parallel `map` and policy evaluation. EP-0016 splits
  runs into independent steps instead; recording stays a debugging and
  history tool.
- **Separate infrastructures for instances and applications.** Their goals
  differ, but the capture point, format, storage and replayer are
  identical; only manifest source, segment count and defaults differ.

## Open questions

1. Inside parallel `map` scopes before-hook decisions are recorded (see
   *Parallel `map`*). Should they be recorded for all external calls, so
   that a faithful-replay policy divergence is attributed precisely rather
   than surfacing as `tape_miss` / `tape_unconsumed`?
2. Should recording be allowed for instances or releases whose policies use
   `time.*`, `rand.*` or `uuid.*`, given that `EvalTime` / `EvalSeed` pin
   them? Or should a warning be emitted?
3. Default limits and retention: per-segment call/byte caps, per-workspace
   run retention count and age.
4. Cross-version replay: refuse, or warn and report step-count drift
   separately from other mismatches?
5. Should application `Test` runs share the run history with invokes, or
   be kept in a separate, shorter-retention list?
6. Keyed matching key: are canonical args sufficient, or should the call
   site be an optional tie-breaker for identical requests issued from
   different places?
