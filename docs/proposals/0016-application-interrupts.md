# EP-0016: Application interrupts and handlers

- **Status:** In progress — plan items 1–4 and 6 implemented; 5 waits
  for EP-0001. Handler steps run on an ephemeral replica of the run's
  pinned release (prepare → invoke → delete); `main` of an invoke uses the
  published persistent replica. Restart survival is covered by durable
  store state and stale-dispatch recovery, not yet by a restart
  integration test.
- **Created:** 2026-10-29
- **Area:** engine, capability gate, worker protocol, store, API, console
- **Related:** [EP-0014](0014-persistent-instances.md) (applications), [EP-0001](0001-record-replay.md) (record / replay), [EP-0015](0015-copy-isolated-map.md) (parallel `map`), [EP-0010](0010-workspaces-and-access.md) (access)

## Summary

Let an application run pause until something outside resumes it, without
keeping any runtime alive. A run is a chain of short, independent
function calls:

- `main(input)` runs until it finishes or reaches an **interrupt**.
  Reaching an interrupt ends the call immediately.
- The interrupt is a durable record with a **unique id**, the name of the
  **handler** to call next, and the **context** the handler needs.
- When someone **resumes** the interrupt with a JSON event, the handler
  is called on a fresh session with that context, the interrupt id and
  the event. If it expires instead, the optional `on_expire` handler is
  called. Either handler finishes the run (its return value is the run's
  output) or reaches another interrupt.
- Every interrupt id is resumed **at most once**: the first resume wins,
  every later one is rejected.

There is one kind of interrupt. What resumes it — a person, a payment
provider, a scheduler — is the external system's business; calcside only
guarantees the pause/resume semantics. The console is a debug entry.

Nothing is resumed inside a program. Replicas and instances hold no run
state between calls: everything a later step needs travels in the
interrupt. Any replica can serve any step, after any restart.

## Motivation

The target is enterprise SOPs on a high-code workflow platform (a code-first
counterpart to Dify / n8n, without knowledge bases or agents for now).
SOPs routinely wait: for an approver, for a payment provider's callback,
until the next business day. Today an application run is one synchronous
`main` call bounded by seconds (EP-0014).

Resuming a suspended Starlark program in place would require replaying its
recorded prefix (EP-0001) and keeping replay and live execution perfectly
aligned across releases, loops and parallel `map`. Splitting the process
into independent functions avoids all of that:

- No program state to rebuild; Starlark values never need serializing.
- Each step is a normal application call with fresh globals and `/work`,
  exactly the EP-0014 contract.
- Each step is independently recordable and replayable (EP-0001).
- The handoff between steps is explicit JSON instead of hidden
  interpreter state.

## Goals

- Interrupts are uniquely identified; every handler call knows the id of
  the interrupt that triggered it, and every step knows its run id.
- The interrupt carries the complete context for the next step; replicas
  and instances are stateless between steps.
- One interrupt kind with an optional deadline, resumed by a bound token
  (external systems) or by a user with Run (console, API).
- Handlers are plain top-level functions of the program, found by name
  when the program waits — nothing is declared ahead of time.
- Single resume: each interrupt id accepts exactly one resume (first
  arriver wins), so a handler is dispatched at most once per interrupt.
- A waiting run survives API and worker restarts and application
  republishing.
- Every step is recorded and replayable on its own.
- Unknown outcomes stay unknown: no silent re-execution of a step that may
  have performed side effects.

## Non-goals

- Resuming execution inside a program, or snapshotting interpreter state.
- Approval workflows, assignees, titles, forms, timers or event schemas:
  external systems orchestrate those and resume the interrupt.
- Exactly-once side effects. Handlers receive a stable interrupt id to use
  as an idempotency key with external systems.
- More than one pending interrupt per run, and joins over several
  interrupts (see *Fan-out*).
- Interrupts in instances. The playground has no handler dispatch.

## Language

- **Run** — one execution of an application from `main` to a final output,
  error, expiry or cancellation; a chain of steps. Durable, `run_` prefix
  (also used by EP-0001 as the tape owner).
- **Step** — one call of an entry function (`main` or a handler) on a fresh
  session. One step is one EP-0001 segment. A run has at most 64 steps.
- **Interrupt** — a durable request for exactly one resume, `int_` prefix.
  It names the handler, the optional `on_expire` handler and carries the
  context.
- **Wait** — ending a step on one interrupt reserved in that step.
- **Handler** — a top-level one-argument function of the run's release,
  named by `wait` and called with the handler context.
- **Resume** — the single event that ends an interrupt (first arriver
  wins); **expiry** is the alternative ending.

## Program shape

```python
def main(input):
    req = interrupt.new(expires_in_s = 3 * 86400)
    net.post(CHAT_WEBHOOK, json = {
        "text": "Refund %s needs review" % input["order_id"],
        "resume_url": req["resume_url"],
        "resume_token": req["resume_token"],
    })
    interrupt.wait(
        req["id"],
        handler = "on_decision",
        on_expire = "on_timeout",
        context = {"order_id": input["order_id"], "amount": input["amount"]},
    )

def on_decision(ctx):
    order = ctx["context"]["order_id"]
    if not ctx["event"]["approved"]:
        return {"status": "rejected", "order_id": order}
    r = net.post(REFUND_API, json = {
        "order_id": order,
        "idempotency_key": ctx["interrupt_id"],
    })
    return {"status": "refunded", "order_id": order, "refund_id": json.decode(r["body"])["id"]}

def on_timeout(ctx):
    return {"status": "expired", "order_id": ctx["context"]["order_id"]}
```

- `main(input)` is unchanged from EP-0014: validated input in, output out.
- A handler takes one argument, the frozen handler context, and returns the
  run's output (validated against the application's output schema) or
  reaches another wait.
- Handlers are ordinary top-level functions of the same release. `wait`
  checks the names exist as one-argument functions when it runs; names
  starting with `_` and `main` are not handlers. Nothing is declared in
  application settings.

## The `interrupt` capability

`interrupt` is a capability like `fs` or `net`: it implements
`capability.Factory`, every op goes through `Gate.Invoke` (hooks, policy and
audit see it), and it is available only to application programs, only
while a step is running (not during top-level module initialization).

| Op | Returns | Effect |
|---|---|---|
| `interrupt.run_id()` | `"run_…"` | The current run's id, for correlation in notifications and external records. |
| `interrupt.new(expires_in_s=None)` | `{"id", "resume_url", "resume_token"}` plus `"expires_in_s"` when given | Claims the next of the step's pre-reserved interrupts (see *Reservation and early resumes*). Does not stop the step. At most 4 per step. |
| `interrupt.wait(id, *, handler, on_expire=None, context)` | never returns | Commits the reserved interrupt and ends the step with a *waiting* outcome. |

Two ops instead of one because the id must exist **before** the step ends:
the program usually has to notify someone (a chat message, an email, a call
to the system that will later resume it) and embed the id or resume URL
in that notification.

Rules:

- `wait` must name an id reserved by `new` in the **same step**. Reserved
  ids not passed to `wait` are cancelled when the step ends, whatever the
  outcome.
- `handler` and `on_expire` must name top-level one-argument functions of
  the program; otherwise the step fails and the reservations are
  cancelled. `context` must be a dict.
- An interrupt without `expires_in_s` gets the server default deadline
  (7 days, maximum 30).
- A wait in the 64th step fails with `chain_limit`, and the run fails.
- `wait` is rejected inside a `map`/`amap` task (EP-0015) and inside
  extensions: only the application's own code on the step's main thread
  may end the step.
- Starlark has no `try`/`except`. `wait` returns an internal sentinel error
  that unwinds the whole call stack; the engine classifies it as the
  waiting outcome, not as a failure. Nothing after `wait` runs.
- Misuse errors (no slots, unknown handler, bad deadline) surface with
  their fixed message in the step result, code `runtime`.

## Handler context

The handler receives one frozen value:

```jsonc
{
  "run_id": "run_…",
  "step": 1,                           // 0 is main
  "interrupt_id": "int_…",             // the interrupt that triggered this call
  "handler": "on_decision",
  "context": { /* exactly what wait() stored */ },
  "resolution": "resolved",            // "resolved" (handler) or "expired" (on_expire)
  "event": { /* the resume event; null when expired */ },
  "resolved_by": "usr_…",              // user id, "token" or "expiry"
  "resolved_at": "2026-10-30T09:12:00Z",
  "attempt": 1                          // > 1 only after an explicit retry
}
```

- The interrupt id is the natural idempotency key for side effects the
  handler or `on_expire` performs: it is resumed once, and a retried
  handler call carries the same id.
- `context` is the **only** program data that crosses steps. Globals,
  `/work` and stdout of the previous step are gone.

### Context rules

- A JSON object converted with the same bounded typed conversion as
  application output (`MarshalJSONValue`: depth, node and byte budgets,
  exact integers, no non-JSON values). Default cap 64 KiB; larger data
  belongs in an external system, referenced by id.
- **Fails closed on known secrets.** A context containing a known secret
  value makes `wait` fail instead of redacting it, because a redacted
  context would silently corrupt the next step. Programs pass secret
  *names*; the handler's step has the same secret bindings.
- Stored encrypted at rest with the existing `--secret-key` cipher (it may
  contain business data and PII). Never logged, never in audit or hook
  metadata.

## Runs

```
running ──(output)────────────────────────────────────▶ succeeded
   │  ├──(error, chain_limit)──────────────────────────▶ failed
   │  └──(wait)──▶ waiting ─┬─(resumed, first arriver)───▶ running (handler) ─▶ …
   │                        ├─(expired, on_expire)───────▶ running (on_expire) ─▶ …
   │                        └─(expired, none)────────────▶ expired
   └──(worker lost / timeout)──────────────────────────▶ unknown
waiting ──(cancel)─────────────────────────────────────▶ cancelled
```

- A run is **pinned to the release** that ran `main`. Handler steps use the
  same release even if the application is republished meanwhile, so the
  handler always matches the code that produced the context. Releases
  referenced by non-terminal runs or retained tapes are not
  garbage-collected.
- A run has at most **64 steps** (`main` plus handler calls). Every wait
  has a server-bounded deadline, so a run's age is bounded by 64
  deadlines.
- Deleting an application cancels its waiting runs and their interrupts.
  Disabling it pauses dispatch; resumes are still accepted and queued.
- The run records its chain: steps (entry, timings, outcome), interrupts
  (id, handler, status, resolver), final output size and error type.
  Inputs, contexts, events and outputs live in encrypted storage.

### Delivery and failures

- **One resume per interrupt id, first arriver wins.** Resuming is one
  compare-and-swap `pending → resolved` that also queues the handler
  dispatch, atomically, and wakes the dispatcher. Every later attempt on
  the same id — a second user, a retried webhook, a resume racing the
  deadline — gets `409 already_resolved` and changes nothing. An external
  system that retried after a lost response can treat it as delivered; it
  never learns the winning payload.
- **Expiry** is the same compare-and-swap, `pending → expired`: whichever
  of resume and deadline commits first wins. `on_expire` is dispatched if
  given, otherwise the run ends `expired`.
- A resumed or expired interrupt is dispatched through a durable queue
  owned by the API tier. Dispatch survives API restarts, and each
  interrupt is dispatched at most once (explicit retries aside).
- A handler step that ends with a definite error fails the run.
- A handler step whose outcome is unknown (timeout, worker loss after
  dispatch) moves the run to `unknown` and is **not** retried
  automatically. A user with Configure may retry it explicitly; the retry
  gets the same context and interrupt id with `attempt + 1`.
- A step that never started (no replica, queue full) is retried with
  backoff: it has performed no side effects.

### Reservation and early resumes

Before every step of a program that references `interrupt` (a parse-only
check of the release sources), the API writes 4 durable `reserved`
interrupts, each with its own bound resume key, and sends their ids, URLs
and tokens on the invoke request. `new` claims the next one, so its id
and URL are real immediately and the worker never calls back into the
API. Slots the step does not wait on are cancelled and their keys revoked
when the step ends. A token resume that arrives while the interrupt is
still `reserved` (the external system answered before the step ended) is
stored as the interrupt's single resume — later ones get `409` — and is
dispatched when `wait` commits. If the step ends without waiting on it,
the reservation is cancelled and the stored event discarded. This makes
"notify, then wait" race-free.

### Fan-out

A step ends at its first `wait`, so a run has at most one pending
interrupt.

- **Several decisions in one event.** One resume whose event carries a
  decision per item.
- **Sequential chain.** The handler waits again with an updated context
  (a cursor into a list kept in context). Loops that cross interrupts
  become explicit state in the context, bounded by the 64-step limit.
- **Child runs and joins** (follow-up). Starting other application runs
  and waiting on several interrupts at once are a separate proposal.

## Resume and authorization

There is one route: `POST /api/v1/interrupts/{id}/resume`, body = the event
(any JSON object, 64 KiB cap), response `202 {"status":"accepted"}`.

- **Token.** `resume_url` addresses this route; `resume_token` is an API
  key bound to the `int_…` id through `api_keys.bound_id`, sent as
  `Authorization: Bearer …` or `?token=…`. It may resume the interrupt
  even while reserved (see above). `access.Checker` hides everything else
  from `int_`-bound keys. The key stays valid until the run ends, so a
  late resume gets `409 already_resolved` rather than an authentication
  error; it is revoked when the run reaches a terminal state.
- **Caller with Run.** A session or application/workspace key with Run on
  the application may resume a pending interrupt — the console's debug
  entry, or an orchestrator that started the run through `invoke` and got
  `interrupt_id` back. Cookie sessions need the CSRF header like any
  mutation. View-only and restricted keys cannot resume.
- The token is a credential: the runtime registers it as a known secret of
  the step, so it is redacted from outputs, audit, contexts and tapes; the
  program can still send it to the allowed external system through `net`.
- `GET /api/v1/interrupts?status=pending&resumable=true` lists interrupts
  the caller may resume, with handler, deadline, run and application.
- All operations have `service/access` table entries; `TestRouteCoverage`
  applies.
- Audit: `interrupt.new` / `interrupt.wait` are gated ops audited like any
  capability call; resume (by whom), expiry, cancellation, retry and
  dispatch are recorded as workspace audit events with capability
  `interrupt`. Metadata carries ids only, never contexts or events.
- Contexts, events and run outputs are sealed with the vault cipher
  (`--secret-key`). Without it runs still record (without output), no
  slots are minted, and `interrupt.new` fails with a message naming the
  missing `--secret-key`.

## Worker protocol and engine

- `InvokeRequest` gains `entry` (`main` with the validated input, or a
  handler name with the handler context), `run_id`, `step` and
  `interrupts` (the pre-reserved slots, tokens included; never logged).
  Workers keep no run state. All fields live in the `internal/runtime`
  contract types mapped by `x-go-type`, so the worker spec is unchanged.
- `ProgramResult` gains a waiting outcome
  `{interrupt_id, handler, on_expire, context, expires_in_s}`; the API
  commits it atomically with the run transition.
- The `interrupt` module is built by the engine for program sessions only
  (`internal/capability/interrupt`, a `capability.Factory` that is never
  registered, so specs cannot request it; the editor metadata still
  documents it for completions). Resume tokens join the call's secret set
  as unnamed redactions.
- The engine calls the named entry with the same fresh-session,
  armed-gate, bounded-conversion path as `main` (`InvokeProgram`), checks
  after module init that a handler entry is a top-level one-argument
  function, and validates handler outputs against the output schema.
- Single-binary and remote/process transports keep identical semantics.

## Record / replay

Each step is one EP-0001 segment of the run's tape, with its entry, input
or handler context, recorded calls and outcome. Because steps share no
state, **any step replays on its own**: no prefix replay, no ordering
constraint between steps.

- `interrupt.new` is an external op: its value (id, URL, with the token
  redacted) is taped and served from the tape on replay. Replay never
  reserves real interrupts.
- `interrupt.run_id` returns the **source** run id during replay (recorded
  with the segment), so notifications and request fingerprints match.
- `interrupt.wait` is not external; on replay it ends the segment with the
  recorded waiting outcome, which is compared like any other result.
- What-if replay can edit a handler context or event to explore branches.

## API

All routes land in `api/openapi.yaml` first.

| Route | Purpose |
|---|---|
| `POST /api/v1/applications/{id}/invoke` | as today; returns either the output or `run_status: "waiting"`, `run_id` and `waiting.interrupt_id` |
| `GET /api/v1/runs/{id}` | run status, chain, interrupts and the final output |
| `POST /api/v1/runs/{id}/cancel` | cancel a waiting run |
| `POST /api/v1/runs/{id}/retry` | retry an `unknown` handler step |
| `GET /api/v1/applications/{id}/runs` | runs list for the Runs card |
| `GET /api/v1/interrupts` | listing (`status`, `resumable`) |
| `GET /api/v1/interrupts/{id}` | interrupt detail, with `can_resume` and `resume_url` |
| `POST /api/v1/interrupts/{id}/resume` | resume with an event; token or Run; `409 already_resolved` after the first |

## Console

The console is a debug entry: every interrupt is inspectable and can be
resumed manually by a user with Run. Every entry follows
`docs/interaction-stability.md` and the existing application-page
conventions (bounded `app-card` blocks, `ui.tsx` primitives, `SwapLabel`,
`Confirm`, `useHeldOrder` + `RevealNew`).

| Entry | Who | Shows | Actions |
|---|---|---|---|
| **Inbox** (`/inbox`) | every workspace user | pending interrupts the user may resume, across applications | open, resume |
| **Runs** card on `/applications/:id` | View on the application | the application's runs | open run |
| **Run page** `/applications/:id/runs/:runId` | View on the application | step chain, current interrupt with its context and `resume_url`, output | resume, cancel run, retry step |
| **Develop** / **Invoke** cards | editors | the run follows to its outcome: waiting (inline resume), running handler, then the handler's return value as Output | resume inline, open run |

- **Resume form:** one "Event — a JSON object" editor, validated before
  submit (P5); submit opens `Confirm` showing the exact event and the
  handler it calls (F2, P1). The control shows `SwapLabel`
  Resume/Resuming…/Resumed — running handler… and cannot fire twice
  (P1, S4). A lost race (`409 already_resolved`) turns the form read-only
  in place with the typed event kept (P5, C2, F3). Drafts are scoped by
  user, workspace and interrupt (C2). Resuming never navigates (P3).
- **Lists hold their order** (`useHeldOrder`, S5) and resumed inbox rows
  keep their box until reveal (S3, F1); the inbox count slot is always
  rendered (S4).
- **Settings** have no handler section: handlers are just functions in
  the code.

## Security considerations

- Contexts and events are user data: bounded, encrypted at rest, never in
  logs, audit or hook metadata; contexts reject known secret values.
- Resume tokens are bound keys scoped to one interrupt's resume route,
  accept at most one resume, are revoked when the run ends, and are
  redacted as secrets.
- A handler runs with the release's authority (spec, policies, secrets),
  not the resumer's; the resumer only supplies a bounded JSON object.
- Deadlines are mandatory and server-bounded, and runs are limited to 64
  steps, so abandoned runs end.

## Plan

1. Store: runs (steps, status), interrupts (reserved / pending /
   resolved / expired / cancelled), dispatch queue; interface, GORM,
   `storetest`, paired migrations.
2. Release: application sources are the only handler contract (no
   declarations).
3. Engine and worker protocol: entry selection, run id and step, waiting
   outcome, pre-reserved slots on the invoke request, the `interrupt`
   capability with `wait` as the unwinding sentinel; transport parity
   tests.
4. API: invoke returning waiting runs, single first-arriver resume
   (token or Run), cancel/retry, listing, bound resume keys, deadline
   scheduler, 64-step limit, access entries.
5. Record/replay integration (EP-0001): per-step segments, taped
   reservations, recorded run ids, independent step replay.
6. Console: inbox, Runs card, run page, resume form, Develop/Invoke run
   progress.

Acceptance:

- A run that notifies, waits, survives API and worker restarts while
  waiting and after an application republish, is resumed, and completes
  in the handler on a different replica with the same release.
- Two concurrent resumes, or one delivered twice: exactly one resume and
  one handler call; every later attempt gets `409 already_resolved`.
- A resume racing the deadline: exactly one of `handler` or `on_expire`
  runs.
- An early token resume before `wait` commits is applied exactly once.
- Expiry calls `on_expire`, or ends the run `expired` when none is given.
- A wait naming a missing handler fails with zero interrupts committed; a
  wait in step 64 fails the run with `chain_limit`.
- A handler killed mid-step leaves the run `unknown` until an explicit
  retry with the same interrupt id.

## Alternatives considered

- **Resume inside the program by replaying a recorded prefix.** Rebuilds
  state without serialization, but couples correctness to perfect replay
  across loops, parallel `map` and policy evaluation, and makes every
  resumption cost O(all previous steps). Rejected in favor of explicit
  steps.
- **Snapshot interpreter state.** Not possible in general
  (`docs/execution-routing.md`).
- **Several interrupt kinds (approval / callback / timer) with titles,
  assignees and event schemas.** Couples the mechanism to console
  presentation and to workflows external systems already own. One kind
  with one resume route keeps the guarantee small: pause, resume once,
  call the handler.
- **Declared handler tables with context schemas.** Duplicates what the
  code already says and adds a settings step before anything works.
  Handlers are found by name when `wait` runs.
- **Declared DAG of steps.** Easier to visualize, but moves control flow
  out of code.
- **One op that creates and waits.** Leaves no window to notify anyone with
  the id before the step ends.
- **Single handler branching on expiry.** Works, but mixes the happy path
  with timeout handling. `on_expire` is explicit and optional.
- **Accepting repeated resumes** (last writer wins, or redelivering the
  handler per call). Makes handler dispatch depend on how often an
  external system retries.
- **Keeping context in a server-side key/value store.** Hides the handoff
  and adds mutable shared state; the context is carried explicitly instead.

## Decisions

1. **One interrupt kind**, resumed by its bound token or by a caller with
   Run; timers and approvals are orchestrated externally.
2. **Handlers are not declared**; `wait` resolves names against the
   program's top-level functions.
3. **Expiry:** optional `expires_in_s` (server default and maximum) and an
   optional `on_expire` handler; without it an expired wait ends the run
   `expired`.
4. **Limits:** at most 64 steps per run; no separate run age cap for now.
5. **Run id:** visible to every step through `interrupt.run_id()`.
6. **Resume:** each interrupt id can be resumed exactly once; first
   arriver wins, every later attempt gets `409 already_resolved`.

## Open questions

1. Should the 64-step limit become a per-application setting under a server
   cap, or stay fixed?
2. Child runs and waiting on several interrupts at once: needed, and with
   which join semantics?
