# EP-0021: Pull-based streams for Starlark

- **Status:** Implemented (core streams, lazy combinators, and application SSE output)
- **Created:** 2026-11-05
- **Area:** Starlark engine, capability gate, net capability, extensions (LLM), resource limits, audit
- **Related:** [EP-0013](0013-llm-providers.md) (LLM providers; supersedes its historical no-streaming non-goal), [EP-0015](0015-copy-isolated-map.md) (copy-isolated `map`), [EP-0016](0016-application-interrupts.md) (interrupts), [docs/extensions.md](../extensions.md)

## Summary

Add a native, pull-based `Stream` value to the engine: an ordered,
one-shot, single-cursor iterable whose items are produced on demand by the
owning execution, never replayed and never pre-buffered wholesale. On top
of it:

- a `stream` module (`stream.new`, `stream.sse`) exposed in engine
  globals and the extension predeclared environment;
- `net.stream(method, url, ...) -> {status, headers, body: Stream[bytes]}`,
  a streaming twin of `net.request` that keeps every existing security
  rule but yields the response body incrementally;
- `ext.llm.stream(provider, messages, ...) -> Stream[event]`, an optional
  provider-protocol addition that surfaces normalized
  `text_delta`/`done` events.

Streams are scoped to one execution inside the existing worker child. The core streams, SSE decoder, net.stream and ext.llm.stream are implemented. Lazy stream.from_list, map, reduce and flatmap extend this model. When main returns a stream, the application invoke/test response delivers validated items over SSE to the caller and console; ordinary JSON returns are unchanged. The API tier never executes Starlark or serializes live stream handles.

## Motivation

Two hot paths today are all-or-nothing: `net.request` buffers the whole
body under `max_response_bytes` before returning, and the current LLM
adapters' `respond`/`structured` calls block until the final message.
That makes long responses expensive and LLM output unobservable until
complete, which is the dominant latency for agent scripts.

A pull-based stream fits the existing execution model. `Engine.Exec`
holds `Session.ExecMu` and runs one thread at a time; lazy pulling means
the consumer's own `for` loop drives I/O on its thread, so no background
Starlark, generator, `yield`, async or fork is needed. Bounded buffers
plus per-pull gate checks keep hooks and audit in the loop for the whole
lifetime of the resource, not just the open.

## Stream semantics

`Stream` is a native `starlark.Value` implementing `Iterable` (pinned
go.starlark: `Iterate() Iterator`, `Next(*Value) bool`, `Done()`).

- **One-shot, shared cursor.** Pulling is ordered and destructive; a
  stream is not replayable and has no aggregate history. Exactly one
  active iterator exists at a time: nested or concurrent consumption is
  rejected with a scope fault. Aliases share cursor and close state.
- **Lazy, bounded.** Buffers are bounded; nothing is pre-buffered and
  replayed. `list(s)` materializes the remainder under ordinary memory
  and step limits — no constant-memory claim.
- **`.next()`** returns `(has_value, value)`; `(False, None)` is EOF and
  `(True, None)` is a legitimate `None` payload. Ordinary read failures
  return a builtin error. Direct `.next()` calls share the same cursor:
  they cannot be used to interleave or steal items from an active
  `for`-iterator on the same stream (the iterator's own pull uses the
  internal path), and the single-active-consumer rule applies to the
  cursor — a source wrapper's `next_fn` may still pull its separate
  upstream stream in a normal pipeline.
- **`.close()`** is idempotent and is the caller's early-stop control.
  `Iterator.Done` (`break`/`return`/unwind) releases only the cursor —
  the stream stays open and may resume via `.next()` or a later `for`.
  Every stream registers in the owner execution scope at creation, and
  EOF, timeout, cancellation, step/memory cancellation and exec end all
  close it natively — exec teardown is the mandatory safety net for
  never-consumed and never-closed handles alike.
- **Terminal state.** A stream closed by EOF or explicit close reports
  exhausted; a failed stream retains its original error for its final
  audit record, and any later same-scope `.next()`/iteration on it fails
  with that original error rather than reporting exhausted (cross-scope
  access is rejected regardless). Closed handles may persist in globals
  but cannot be read or reopened in a later exec.
- **Scope registration.** Every stream registers in its owner execution
  scope at creation — including eager-open handles that are never
  consumed and derived streams such as `stream.sse` output. Exec teardown
  closes all registered streams deterministically before the gate is
  disarmed; the native upstream close is idempotent. This registration,
  not `Iterator.Done` or GC, is what guarantees cleanup of
  never-consumed streams.
- **Owner scope.** Every operation verifies the owning exec scope and
  thread. `Iterable` has no thread access, so the owner's thread is
  captured at creation; no cross-thread exposure is permitted, and
  EP-0015's copy preflight rejects streams in clone graphs.
- **Opaque.** No `len`, indexing, hashing or JSON. `Truth`, `String` and
  REPL inspection never pull or render body bytes — the repr is opaque.
  `Freeze` cannot turn a stream into a persistable or shareable resource;
  it is a no-op opaque handle within its scope and never consumes.
  Streams are rejected recursively in application output, interrupt
  context and JSON.

## Engine `stream` module

Streams are an engine-level module, not an I/O capability; creation
grants no new authority. The module is bound into engine globals **and**
the extension module predeclared environment through shared bindings —
not a fake capability grant. `stream.new` only works inside an active
execution scope, never at extension module init.

- `stream.new(next_fn) -> Stream` invokes `next_fn`
  synchronously on the owner's Starlark thread per pull. It must return
  exactly `(bool, value)`; malformed returns are errors. There is no
  callback-supplied upstream or close hook — closures do not transfer
  lifetime; callers release captured resources explicitly, and the exec
  scope cleans up whatever remains. Native wrappers that take a real
  stream input (`stream.sse`, gated bodies) still cascade `.close()` to
  that input; aliases share the cursor, so there is no replay or
  independent iteration.
- `stream.sse(source) -> Stream` performs bounded, incremental
  server-sent-event framing over a `Stream[bytes]`. It emits
  `{event, data, id}` dicts, joins multiline `data:` fields per the SSE
  spec, ignores comments, and handles UTF-8 and CRLF fragmentation across
  chunk boundaries. There is no reconnect or `Last-Event-ID` retry
  machinery. Per the SSE spec an event is only dispatched by a
  terminating blank line, so at EOF an incomplete pending event is
  discarded rather than synthesized as complete; malformed framing or
  malformed UTF-8 is an explicit framing error. EOF without a complete
  final event is not itself a framing error — the LLM layer separately
  rejects a stream that ends without the vendor's terminal event.
- Provider adapters are plain Starlark: a `next_fn` that loops on
  `events.next()` over `events = stream.sse(response["body"])` where
  `response = net.stream(...)`, and returns normalized events or EOF.

## Errors, scope faults and cancellation

A `Next` failure stores the first typed error in the stream **and** in
the execution scope, cancels the owner `Thread` and returns `False`; it
never panics. The engine checks the scope sticky error on **all** exit
paths — before accepting success or `interrupt.wait`, even when
consumption happened inside a Go builtin (`list`, `any`, `stream.sse`
framing, a `next_fn`) — and returns the original error and
classification, never generic cancellation text. A direct `.next()`
failure latches the same typed scope error, so a wrapper callback cannot
swallow it. Calling
`Thread.Cancel` on a network error must not cancel the parent context or
misclassify it as a timeout; real caller, deadline, step and memory
cancellation keep precedence. A rejected second/nested iterator goes
through the same bridge. Voluntary early close is not failure.

Blocked native reads are checked for cancellation before blocking and are
unblocked via context plus native close — `Thread.Cancel` alone cannot
interrupt socket I/O.

## `net.stream`

`net.stream(method, url, body="", headers=None) -> {status, headers, body: Stream[bytes]}`
opens the request eagerly inside the gate — connect, TLS, headers — and
returns a body whose bytes are read on demand. `net.request` and the
other methods are unchanged.

- **Same security machinery exactly**: method/host rules, vetted DNS
  pinning, the selected private-network policy, secret host checks,
  redirect and header restrictions. Non-2xx still returns the response
  status with a readable body; bounded error diagnostics are the
  provider's problem.
- **Redaction is streaming-aware.** Secret replacement runs before bytes
  reach Starlark, holds a bounded overlap large enough for the longest
  known secret across chunk boundaries (including UTF-8-adjacent splits),
  and flushes safely at EOF — never per-chunk independent replacement or
  unsafe prefix exposure.
- **Bounds.** Redaction, decoder and SSE buffers are bounded; total raw
  bytes — including discarded comments, framing and redacted bytes — are
  charged against the existing `max_response_bytes` for the whole
  response, not reset per chunk. The whole-request `timeout_ms` is
  retained with no heartbeat extension. Chunk/event byte bounds and a
  max live streams per exec are required implementation limits under
  operator hard caps.
- **Backpressure** lives at the application reader level: HTTP/OS
  read-ahead is acknowledged and bounded, with no unbounded producer
  goroutine or channel.

## Gate integration

Opening, deferred reads, normalization callbacks and finalization cannot
escape the gated scope. A capability-originated stream (`net.stream`, or
an ext op returning a stream) carries an immutable record of its
originating capability/op, sanitized original args and a stream id.
Engine-created streams (`stream.new`, `stream.sse`) have no originating
capability/op: they register scope/ownership and inherit upstream
authority through `source`, and any capability I/O a `next_fn` performs
still goes through the nested `Gate.Invoke` of that capability — the
stream module is not a grant.

- Every pull runs `Before` before I/O or callback and `After` before the
  value is yielded, with `stream_action` (`open|pull|close`) and
  `stream_id` in the args under the same capability/op (`net.stream`,
  `ext.alias.stream`). No new grant and no implicit host permission. An
  `After` deny closes the stream and delivers no chunk for that pull;
  already-delivered chunks cannot be revoked.
- Opening returns the handle only after `After` succeeds; on deny the
  unattached body is closed immediately. A synchronous `After` at open is
  not terminal usage policy.
- Ext-op streams keep the wrapper running each deferred callback through
  the same ext `Gate.Invoke` preserving alias/provider context; nested
  `net` reads go through the net gate — no raw body-read bypass.
- Final close is once; native resource cleanup is unconditional even if a
  hook denies or the context is cancelled, and no arbitrary user cleanup
  runs. Final metadata records status, byte/chunk counts and
  `complete|cancelled|error|closed`; usage is only what is actually
  known — absent/null, never a fake zero, and never prompt/body/text or a
  stream handle.
- Audit gets phase-linked records and exactly one final record per
  acquired resource. All cleanup happens before `Gate.Disarm` and runtime
  audit draining/lock release — including `InvokeProgram`, which
  currently disarms right after the entry call, before output handling
  and the `interrupt.wait` path. No external callbacks run after exec.

## `ext.llm.stream`

Additive, Starlark-only, optional:

- `ext.llm.stream(provider, messages, model=None, options=None) -> Stream[event]`
  in `contrib/llm`.
- Provider protocol gains an optional `stream(messages, model, options=None)`;
  it is not added to the mandatory `ProviderOps` (`respond`/`structured`
  unchanged) and existing extensions keep working. A provider without it
  fails with `unsupported_stream` **before** any network call; there is
  no fake buffering fallback.
- v1 events are text only: `{type: "text_delta", text: "..."}` plus
  exactly one terminal `{type: "done", finish_reason: string,
  usage: dict|None}`. Usage reports the provider's actual input/output
  tokens with nullable/missing fields — never invented. Tool calls,
  reasoning and structured incremental output are deferred.
- Vendor heartbeats and internal events are filtered; the provider's
  terminal success is required. Abrupt transport EOF, malformed SSE/JSON
  or a provider error fail the stream — they do not synthesize `done`.
  `done` is followed by EOF and the network closes on `done`; there is no
  implicit full-text collection. An early break yields no synthetic
  `done` and claims no final usage.
- Adapters (`llm-openai`, `llm-openai-compat`, `llm-anthropic`,
  `llm-gemini`) are Starlark built on `net.stream` + `stream.sse` +
  `stream.new`.

```python
resp = ext.llm.stream(provider, messages, model="...")
text = ""
for ev in resp:
    if ev["type"] == "text_delta":
        text += ev["text"]
    elif ev["type"] == "done":
        usage = ev["usage"]
```

## Scope and non-goals

Streams live inside the existing worker child, bound to one exec/program
step. There is no live-handle serialization across the worker/API
protocol, no resumed stream across `interrupt.wait`, and no globals
snapshot of handles; no worker-protocol change is needed to consume a
stream internally.

Application main results now have an SSE delivery protocol with disconnect, per-item redaction, validation and backpressure semantics. Exec stdout/results streaming, SDK streaming, persisted/replayable streams and reconnection to later interrupt handlers remain out of scope. Later handler streams are consumed and validated for statistics only; their items are not retained. This supersedes EP-0013's historical no-streaming note without treating its native-provider design as shipped.

Non-goals: persisted or replayable streams, generators/`yield`, unbounded
event subscriptions, and generic async.

## Testing plan

- Genuine laziness: first item delivered before the server finishes;
  slow pulls with bounded buffers; `None` payload; malformed `next_fn`
  return.
- Early `break`/`return`/unwind release the cursor without closing;
  never-consumed and never-closed streams still clean up the eager open
  at exec teardown.
- EOF idempotence, second-iterator and wrong-exec/thread rejection;
  closed-handle behavior across execs.
- Errors inside `for`/`list`/`any` and just before function return never
  surface as success or `interrupt.wait` acceptance; deadline/cancel
  during a blocked read and step-cap cancellation classify correctly.
- Gate denial at open/pull/after: prior chunks not revoked, attribution
  kept, exactly one terminal audit record drained under locks.
- Secret split at every byte boundary including UTF-8 adjacency;
  redirects/DNS under the same policy as `net.request`.
- SSE fragmentation, comments, multiline data, oversized frames, partial
  frame at EOF.
- Provider: abrupt EOF after deltas, provider error, unsupported
  provider rejected before network, `done`/usage semantics.
- Recursive rejection in output/interrupt context; inspection does not
  consume; the full pipeline stays inside the worker; no external
  delivery implied.

## Implementation sequence

1. Shared `Stream` value + execution-scope sticky error.
2. Gate deferred wrapping (`open|pull|close`, `stream_id`, final record).
3. `net.stream` + redaction/SSE framing.
4. Optional provider `stream` contract + Starlark adapters.
5. Docs, completions, tests.

## Alternatives considered

- **Generators / `yield` / async.** Rejected: requires interpreter
  coroutine support and leaks resumable state; pull iteration on the
  owner thread fits the single-threaded exec model.
- **Pre-buffer then replay.** Rejected: defeats laziness and the memory
  point of streaming; `list(s)` already materializes on demand.
- **Replayable multi-iterator streams.** Rejected: requires unbounded
  history or a shared tee buffer; a second consumer can re-issue the
  request.
- **Capability-per-stream.** Rejected: a stream is a read cursor, not new
  authority; gate attribution comes from the originating op.
