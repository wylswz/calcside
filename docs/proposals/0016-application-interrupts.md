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

Let an application run span human approvals, external callbacks and timers
without keeping any runtime alive. A run is a chain of short, independent
function calls:

- `main(input)` runs until it finishes or reaches an **interrupt**.
  Reaching an interrupt ends the call immediately.
- The interrupt is a durable record with a **unique id**, the name of the
  **handler** to call next, and the **complete context** the handler needs.
- When the interrupt is resolved (someone approves, a callback arrives, a
  timer fires), the named handler is called on a fresh session with that
  context, the interrupt id and the resolution event. If it expires
  instead, the `on_expire` handler is called. Either handler finishes the
  run or reaches another interrupt.
- Every interrupt id is resolved **at most once**: the first resolution or
  callback to arrive wins, every later one is rejected.

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
- The handoff between steps is explicit, schema-checked JSON instead of
  hidden interpreter state.

## Goals

- Interrupts are uniquely identified; every handler call knows the id of
  the interrupt that triggered it, and every step knows its run id.
- The interrupt carries the complete context for the next step; replicas
  and instances are stateless between steps.
- Handlers and their context contracts are declared release metadata,
  validated at publish, at `wait` and before dispatch.
- Human, callback and timer interrupts with deadlines, validated events,
  explicit expiry handling and explicit authorization for resolvers.
- Every interrupt has a console entry that follows the interaction
  stability invariants; no interrupt is resolvable only through the API.
- Single resolution: each interrupt id accepts exactly one resolution
  (first arriver wins), so a handler is dispatched at most once per
  interrupt no matter how many approvers or callback retries race.
- A waiting run survives API and worker restarts and application
  republishing.
- Every step is recorded and replayable on its own.
- Unknown outcomes stay unknown: no silent re-execution of a step that may
  have performed side effects.

## Non-goals

- Resuming execution inside a program, or snapshotting interpreter state.
- Exactly-once side effects. Handlers receive a stable interrupt id to use
  as an idempotency key with external systems.
- More than one pending interrupt per run, and joins over several
  interrupts (see *Fan-out*).
- Visual flow editing, knowledge bases, agents.
- Interrupts in instances. The playground has no handler dispatch.

## Language

- **Run** — one execution of an application from `main` to a final output,
  error, expiry or cancellation; a chain of steps. Durable, `run_` prefix
  (also used by EP-0001 as the tape owner).
- **Step** — one call of an entry function (`main` or a handler) on a fresh
  session. One step is one EP-0001 segment. A run has at most 64 steps.
- **Interrupt** — a durable request for exactly one external resolution,
  `int_` prefix: an approval, a callback or a timer. It names the handler,
  the optional `on_expire` handler and carries the context.
- **Wait** — ending a step on one interrupt reserved in that step.
- **Handler** — a top-level function of the run's release, declared in the
  release's handler table, called with the handler context.
- **Resolution** — what ends an interrupt, once: resolved with an event
  (first arriver wins), or expired.

## Program shape

```python
APPROVAL_EVENT = {
    "type": "object",
    "properties": {"approved": {"type": "boolean"}, "note": {"type": "string"}},
    "required": ["approved"],
}

def main(input):
    run_id = interrupt.run_id()
    req = interrupt.new(
        "approval",
        title = "Refund order %s" % input["order_id"],
        assignees = ["usr_finance_lead", "usr_finance_backup"],
        event_schema = APPROVAL_EVENT,
        expires_in_s = 3 * 86400,
    )
    net.post(CHAT_WEBHOOK, json = {
        "text": "Please review %s: %s" % (run_id, req["console_url"]),
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

Release metadata, supplied next to the input/output schemas:

```jsonc
"handlers": {
  "on_decision": {"context_schema": {
    "type": "object",
    "properties": {"order_id": {"type": "string"}, "amount": {"type": "integer"}},
    "required": ["order_id", "amount"]
  }},
  "on_timeout": {"context_schema": {
    "type": "object",
    "properties": {"order_id": {"type": "string"}},
    "required": ["order_id"]
  }}
}
```

- `main(input)` is unchanged from EP-0014: validated input in, output out.
- A handler takes one argument, the frozen handler context, and returns the
  run's output (validated against the application's output schema) or
  reaches another wait.
- Handlers are ordinary top-level functions of the same release; names
  starting with `_` are not allowed.

## Handler declarations

Following EP-0014 (contracts are release metadata, not code), the
application draft carries a `handlers` table: handler name → optional
`context_schema`.

- **Publish** compiles every `context_schema` with the pinned JSON Schema
  subset (`serving.CompileSchema`, same bounds as input/output schemas),
  requires each to be an object schema, and the pure init verifies that
  every declared name is a callable top-level global. A release with an
  undeclared or missing handler does not publish.
- An omitted `context_schema` means an unconstrained object
  (`DefaultApplicationSchema`).
- `wait` may name only declared handlers. The context is validated against
  the `handler`'s schema **and**, when given, the `on_expire` handler's
  schema, since either may receive it. All `required` fields must be
  present; `additionalProperties` follows the schema.
- Dispatch validates the stored context again against the pinned release
  before calling the handler, as defense in depth. A failure there fails
  the run; it cannot happen without a bug, because the release is pinned.
- The editor derives a draft `context_schema` from a sample, as it does for
  input/output (EP-0014 *Application editor*).

## The `interrupt` capability

`interrupt` is a capability like `fs` or `net`: it implements
`capability.Factory`, every op goes through `Gate.Invoke` (hooks, policy and
audit see it), and it is available only to application programs, only
while a step is running (not during top-level module initialization).

| Op | Returns | Effect |
|---|---|---|
| `interrupt.run_id()` | `"run_…"` | The current run's id, for correlation in notifications and external records. |
| `interrupt.new(kind, *, title=None, assignees=None, event_schema=None, expires_in_s=None, fire_in_s=None)` | `{"id", "kind", "console_url", "expires_in_s"?, "fire_in_s"?}` plus `"callback_url"` and `"callback_token"` for `callback` | Claims the next of the step's pre-reserved interrupts (see *Reservation and early callbacks*). Does not stop the step. At most 4 per step. |
| `interrupt.wait(id, *, handler, on_expire=None, context)` | never returns | Commits the reserved interrupt and ends the step with a *waiting* outcome. |

Two ops instead of one because the id must exist **before** the step ends:
the program usually has to notify someone (a chat message, an email, a call
to the system that will later call back) and embed the id or link in that
notification.

`kind` is one of:

| Kind | Resolved by | Event | Deadline |
|---|---|---|---|
| `approval` | a workspace user in the console inbox or through the API; restricted to `assignees` when given | validated against `event_schema` (default: `{approved: boolean, note?: string}`) | `expires_in_s`, server default and max |
| `callback` | an external system calling `callback_url` | request JSON validated against `event_schema` (default: unconstrained object) | `expires_in_s`, server default and max |
| `timer` | the scheduler at `fire_in_s` (required, server max) | none | fires instead of expiring |

A timer firing is a resolution, not an expiry, and calls `handler`.
"Approve within 4 hours, otherwise escalate" is an approval with
`expires_in_s` and an `on_expire` handler.

Rules:

- `wait` must name an id reserved by `new` in the **same step**. Reserved
  ids not passed to `wait` are cancelled when the step ends, whatever the
  outcome.
- `handler` and `on_expire` must be declared handlers, and `context` must
  satisfy their schemas (see *Handler declarations*); otherwise the step
  fails (`bad_handler` / `invalid_context`) and the reservations are
  cancelled.
- A wait in the 64th step fails with `chain_limit`, and the run fails.
- `wait` is rejected inside a `map`/`amap` task (EP-0015) and inside
  extensions: only the application's own code on the step's main thread
  may end the step.
- Starlark has no `try`/`except`. `wait` returns an internal sentinel error
  that unwinds the whole call stack; the engine classifies it as the
  waiting outcome, not as a failure. Nothing after `wait` runs.

## Handler context

The handler receives one frozen value:

```jsonc
{
  "run_id": "run_…",
  "step": 1,                           // 0 is main
  "interrupt_id": "int_…",             // the interrupt that triggered this call
  "kind": "approval",
  "handler": "on_decision",
  "context": { /* exactly what wait() stored */ },
  "resolution": "resolved",            // "resolved" (handler) or "expired" (on_expire)
  "event": { /* validated event; null for timer or expired */ },
  "resolved_by": "usr_…",              // user id, "callback", "timer" or "expiry"
  "resolved_at": "2026-10-30T09:12:00Z",
  "attempt": 1                          // > 1 only after an explicit retry
}
```

- The interrupt id is the natural idempotency key for side effects the
  handler or `on_expire` performs: it is resolved once, and a retried
  handler call carries the same id.
- `context` is the **only** program data that crosses steps. Globals,
  `/work` and stdout of the previous step are gone.

### Context rules

- A JSON object converted with the same bounded typed conversion as
  application output (`MarshalJSONValue`: depth, node and byte budgets,
  exact integers, no non-JSON values), then validated against the declared
  schemas. Default cap 64 KiB; larger data belongs in an external system,
  referenced by id.
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
   │  └──(wait)──▶ waiting ─┬─(resolved, first arriver)──▶ running (handler) ─▶ …
   │                        ├─(expired, on_expire)───────▶ running (on_expire) ─▶ …
   │                        └─(expired, none)────────────▶ expired
   └──(worker lost / timeout)──────────────────────────▶ unknown
waiting ──(cancel)─────────────────────────────────────▶ cancelled
```

- A run is **pinned to the release** that ran `main`. Handler steps use the
  same release even if the application is republished meanwhile, so the
  handler and its context schema always match the code that produced the
  context. Releases referenced by non-terminal runs or retained tapes are
  not garbage-collected.
- A run has at most **64 steps** (`main` plus handler calls). There is no
  separate maximum run age for now: every wait has a server-bounded
  deadline, so a run's age is bounded by 64 deadlines.
- Deleting an application cancels its waiting runs and their interrupts.
  Disabling it pauses dispatch; resolutions are still accepted and queued.
- The run records its chain: steps (entry, timings, outcome), interrupts
  (id, kind, status, resolver), final output size and
  error type. Bodies, inputs, contexts and outputs live in encrypted
  storage.

### Delivery and failures

- **One resolution per interrupt id, first arriver wins.** Resolving is one
  compare-and-swap `pending → resolved` that also queues the handler
  dispatch, atomically. Every later attempt on the same id — a second
  approver, a duplicate or retried callback, a resolution racing the
  deadline — gets `409 already_resolved` and changes nothing. The response
  says the interrupt is already resolved, so an external system that
  retried after a lost response can treat it as delivered; it never learns
  the winning payload.
- **Expiry** is the same compare-and-swap, `pending → expired`: whichever of
  resolution and deadline commits first wins. `on_expire` is dispatched if
  given, otherwise the run ends `expired`. Timers never expire; they
  resolve.
- A resolved or expired interrupt is dispatched through a durable queue
  owned by the API tier (the serving reconciler already owns placement and
  leases). Dispatch survives API restarts, and each interrupt is
  dispatched at most once (explicit retries aside).
- A handler step that ends with a definite error fails the run.
- A handler step whose outcome is unknown (timeout, worker loss after
  dispatch) moves the run to `unknown` and is **not** retried
  automatically. A user with Configure may retry it explicitly; the retry
  gets the same context and interrupt id with `attempt + 1`. A retry is
  not a second resolution.
- A step that never started (no replica, queue full) is retried with
  backoff: it has performed no side effects.

### Reservation and early callbacks

Before every step of an application whose release declares handlers, the
API writes 4 durable `reserved` interrupts, each with its own bound
callback key, and sends their ids, URLs and tokens on the invoke request.
`new` claims the next one, so its id and URLs are real immediately and the
worker never calls back into the API. Slots the step does not wait on are
cancelled and their keys revoked when the step ends. A resolution that arrives while the interrupt is
still `reserved` (the external system called back before the step ended)
is validated and stored as the interrupt's single resolution — the same
first-arriver rule applies, later ones get `409` — and is dispatched when
`wait` commits. If the step ends without waiting on it, the reservation is
cancelled and the stored resolution is discarded and audited. This makes
"notify, then wait" race-free.

### Fan-out

A step ends at its first `wait`, so a run has at most one pending
interrupt.

- **Several approvers.** One approval with several `assignees`: the first
  to decide wins, the others see it as already resolved.
- **Several decisions in one form.** One approval whose `event_schema`
  covers a decision per item (the console renders a list form).
- **Sequential chain.** The handler waits again with an updated context
  (a cursor into a list kept in context). Loops that cross interrupts
  become explicit state in the context, bounded by the 64-step limit.
- **Child runs and joins** (follow-up). Starting other application runs
  and waiting on several interrupts at once are a separate proposal.

## Callbacks and authorization

- `callback_url` addresses `POST /api/v1/interrupts/{id}/callback`,
  authenticated by `callback_token` (as `Authorization: Bearer …` or
  `?token=…`): an API key bound to the `int_…` id through the existing
  `api_keys.bound_id` mechanism. Access hides everything except that
  interrupt's callback; `access.Checker` rejects `int_`-bound keys on every
  other route, including workspace-level lists. The key stays valid until
  the run ends, so a callback after the interrupt was resolved or expired
  gets `409 already_resolved` rather than an authentication error; it is
  revoked when the run reaches a terminal state.
- The token is a credential. The runtime registers it as a known secret of
  the step, so it is redacted from outputs, audit, contexts and tapes like
  any other secret; the program can still send it to the allowed external
  system through `net`. On replay it is a dummy value (EP-0001 normalizes
  known secrets in request fingerprints).
- Approvals are resolved through `POST /api/v1/interrupts/{id}/resolve`
  by workspace users with Run on the application, narrowed to `assignees`
  when given. The same route lets users with Configure resolve a
  `callback` interrupt manually (operator override, audited as resolved by
  that user). Timers cannot be resolved by hand. View-only keys and
  restricted keys cannot resolve.
- `GET /api/v1/interrupts?status=pending` is the console inbox: interrupts
  the caller may resolve, with title, kind, deadline, run and application.
- All new operations get `service/access` table entries;
  `TestRouteCoverage` applies.
- Audit: `interrupt.new` / `interrupt.wait` are gated ops and audited like
  any capability call; resolution (by whom), expiry, cancellation, retry
  and dispatch are recorded as workspace audit events with capability
  `interrupt`. Metadata carries ids and kinds only, never contexts or
  event bodies. Rejected duplicate resolutions are not audited yet.
- Contexts, events and run outputs are sealed with the vault cipher
  (`--secret-key`). Without it runs still record (without output), no
  slots are minted, and a step that reaches `wait` fails with
  `interrupts_unavailable`.

## Worker protocol and engine

- `InvokeRequest` gains `entry` (`main` with the validated input, or a
  handler name with the handler context), `run_id`, `step` and
  `interrupts` (the pre-reserved slots, tokens included; never logged).
  Workers keep no run state. All fields live in the `internal/runtime`
  contract types mapped by `x-go-type`, so the worker spec is unchanged.
- `ProgramBundle` / the release gain the `handlers` table; publish
  validates it as described above.
- `ProgramResult` gains a waiting outcome
  `{interrupt_id, handler, on_expire, context, kind, title, assignees,
  event_schema, expires_in_s, fire_in_s}`; the API commits it atomically
  with the run transition.
- The `interrupt` module is built by the engine for program sessions only
  (`internal/capability/interrupt`, a `capability.Factory` that is never
  registered, so specs cannot request it). Callback tokens join the call's
  secret set as unnamed redactions.
- The engine calls the named entry with the same fresh-session,
  armed-gate, bounded-conversion path as `main` (`InvokeProgram`), and
  validates handler outputs against the output schema.
- Single-binary and remote/process transports keep identical semantics.

## Record / replay

Each step is one EP-0001 segment of the run's tape, with its entry, input
or handler context, recorded calls and outcome. Because steps share no
state, **any step replays on its own**: no prefix replay, no ordering
constraint between steps.

- `interrupt.new` is an external op: its value (id, URLs, with the token
  redacted) is taped and served from the tape on replay. Replay never
  reserves real interrupts.
- `interrupt.run_id` returns the **source** run id during replay (recorded
  with the segment), so notifications and request fingerprints match.
- `interrupt.wait` is not external; on replay it ends the segment with the
  recorded waiting outcome, which is compared like any other result.
  Context schema validation runs on replay as well.
- What-if replay can edit a handler context or event to explore branches.
- The console shows a run as its chain of steps and interrupts, each step
  opening the EP-0001 call tree.

## API (illustrative)

All routes land in `api/openapi.yaml` first.

| Route | Purpose |
|---|---|
| `POST`/`PUT /api/v1/applications…` | drafts gain the `handlers` table |
| `POST /api/v1/applications/{id}/invoke` | as today; returns either the output or `{status: "waiting", run_id, interrupt_id}` |
| `GET /api/v1/runs/{id}` | run status and chain |
| `POST /api/v1/runs/{id}/cancel` | cancel a waiting run |
| `POST /api/v1/runs/{id}/retry` | retry an `unknown` handler step |
| `GET /api/v1/interrupts` | inbox / listing |
| `GET /api/v1/interrupts/{id}` | interrupt detail (no context for resolvers without View on the run) |
| `POST /api/v1/interrupts/{id}/resolve` | resolve an approval (or a callback manually, Configure) with an event; `409 already_resolved` after the first |
| `GET /api/v1/applications/{id}/runs` | runs list for the Runs card |
| `POST /api/v1/interrupts/{id}/callback` | external callback, bound-key auth; `409 already_resolved` after the first |

## Console

An application that uses interrupts is only usable if people can act on
them, so the console **must** expose an entry for every interrupt, and
every entry follows `docs/interaction-stability.md` and the existing
application-page conventions (bounded `app-card` blocks, `ui.tsx`
primitives, `SwapLabel`, `Confirm`, `useHeldOrder` + `RevealNew`). Invariant
IDs are cited below and must be cited in the implementing code and tests.

### Where interrupts surface

| Entry | Who | Shows | Actions |
|---|---|---|---|
| **Inbox** (new top-level nav item, `/inbox`) | every workspace user | pending interrupts the user may resolve, across applications | open, resolve |
| **Runs** card on `/applications/:id` | View on the application | the application's runs, waiting ones with their interrupt | open run |
| **Run page** `/applications/:id/runs/:runId` | View on the application | step chain, current interrupt, recordings | resolve, cancel run, retry step |
| **Develop** card (test runs) | editors | a test run that reached `wait` shows its interrupt in place of the output | resolve inline, open run |
| **Invoke** card | editors | an invoke that reached `wait` shows `waiting` and the interrupt in place of the output | open run |

Every interrupt is reachable from at least the run page; approvals are also
in the inbox of each user allowed to resolve them. There is no interrupt
that can only be resolved through the API.

| Kind | Console resolution |
|---|---|
| `approval` | resolve form for assignees (or Run when no assignees) |
| `callback` | read-only status; **Resolve manually** for users with Configure, as an operator override when the external system cannot call back (audited as resolved by that user) |
| `timer` | read-only countdown; the run can be cancelled, not fired early |

### Navigation and lists

- **Inbox nav item** carries a pending count. The count slot is always
  rendered and uses tabular figures, so the nav does not shift when the
  count appears, grows or drops to zero (S4, S3).
- **Polled lists hold their order.** The inbox and the Runs card poll like
  the applications list and keep the shown order with `useHeldOrder`; new
  items arrive behind `RevealNew` "Show N new" (S5, C3).
- **Resolved rows stay in place.** When an inbox item is resolved —
  by this user or by another assignee first — its row keeps its box and
  turns into a same-size "Resolved by … · View run" row until the user
  reveals the refreshed list, so no other row's controls slide under the
  pointer (S3, F1).
- **Places are addressable.** The inbox selection
  (`/inbox?interrupt=int_…`), the run page and the selected step live in
  the URL; Back returns to them (C4). Inbox, chat and email notifications
  link to the console URL returned by `interrupt.new` (`console_url`).

### Run page

Bounded cards with their primary action in the title bar (P6):

- **Page header:** run status (`app-status` style), application and
  release, `TimeLeft` until the current deadline. **Cancel run** is a
  danger-styled header action through `Confirm` naming the run and its
  pending interrupt (F2), spatially separate from resolve (P6).
- **Waiting on** card (only while waiting): kind, title, assignees,
  deadline, the resolve form. Users who may not resolve see the same card
  read-only — assignees are shown as structure, not as explanatory prose
  (P6).
- **Steps** card: the chain `main → handler → …` with entry, outcome,
  duration and resolver per step; each step opens its EP-0001 recording.
  Expansion and selection survive polling (C3).
- **Retry step** (only for `unknown`): a header action through `Confirm`
  that names the step and states that the handler may repeat side effects
  already performed, with the same interrupt id as idempotency key (F2,
  P3).

### Resolve form

- **Approval with the default event schema:** a note field and two
  buttons, **Approve** (primary) and **Reject**. Both open `Confirm`
  naming the interrupt title, because a resolution is single-shot and
  irreversible (F2); the confirmation shows exactly the event that will be
  sent (P1 "know what will happen").
- **Custom `event_schema`** (and manual callback resolution): an event
  editor in the Invoke card's style — "Event — a JSON object" textarea —
  validated against the schema before submit, errors shown next to the
  field (P5). A schema-driven form renderer is a follow-up; it must keep
  the same submit path.
- **Pending:** the submit control shows `SwapLabel` Approve/Approving…,
  Reject/Rejecting…, Resolve/Resolving… and cannot fire twice (P1, S4).
- **Lost race (`409 already_resolved`):** the form becomes read-only in
  place, keeps what the user typed, and shows "Already resolved by … at …"
  next to the submit control (P5, C2, F3). It never claims success.
- **Drafts are kept.** Note and event drafts are scoped by user,
  workspace and interrupt id, survive polling, refetches and navigation
  between inbox and run page (C2). A resolve dialog with input asks before
  discarding on Escape or outside click (`Modal`, C2).
- **No hidden effects.** Resolving does not navigate; the run page or
  inbox row updates in place to show the dispatched handler step (P3,
  P5).

### Application editor

- **Handlers** in the Settings card, next to the input/output schema
  controls: one row per declared handler (name + optional context
  schema), edited with the existing `ContractEditor` (sample → Generate
  schema → fields), autosaved through the same snapshot pipeline as the
  rest of the draft (C2 save indicator). Removing a handler uses `Confirm`
  naming it (F2).
- Publish errors for undeclared or missing handlers appear in the Develop
  card's result area, like other publish failures (P5).

### Verification

- e2e (`web/e2e`): inbox count slot and rows keep their boxes while
  polling and when an item is resolved elsewhere (S3, S4, S5); resolve
  `Confirm` + pending guard (F2, P1); lost-race message at the control with
  input preserved (P5, C2); run page cancel and retry confirmations (F2);
  test-run interrupt resolved inline from Develop.
- Static checks in `interactionStability.test.ts` already cover the new
  controls (paint-only states, `SwapLabel` for state-driven labels,
  `Confirm` for destructive controls).
- `docs/interaction-stability.md` gains an "Interrupts (EP-0016)" section
  and status-table updates when this lands.

## Security considerations

- Contexts and events are user data: bounded, schema-validated, encrypted
  at rest, never in logs, audit or hook metadata; contexts reject known
  secret values.
- Callback tokens are bound keys scoped to one interrupt's callback, accept
  at most one resolution, are revoked when the run ends, and are redacted
  as secrets.
- A handler runs with the release's authority (spec, policies, secrets),
  not the resolver's; the resolver only supplies a schema-validated event.
- Event and context schemas use the EP-0014 pinned JSON Schema subset and
  bounds.
- Deadlines are mandatory and server-bounded, and runs are limited to 64
  steps, so abandoned runs end.

## Plan

1. Store: runs (steps, status), interrupts (reserved / pending /
   resolved / expired / cancelled), dispatch queue; applications and
   releases gain the handlers table; interface, GORM, `storetest`, paired
   migrations.
2. Publish: handler table validation, context schema compilation, pure-init
   handler checks.
3. Engine and worker protocol: entry selection, run id and step, waiting
   outcome, pre-reserved slots on the invoke request, the `interrupt`
   capability with `wait` as the unwinding sentinel; transport parity
   tests.
4. API: invoke returning waiting runs, single first-arriver resolution,
   resolve/callback/cancel/retry, inbox, bound callback keys, scheduler for
   timers and deadlines, 64-step limit, access entries.
5. Record/replay integration (EP-0001): per-step segments, taped
   reservations, recorded run ids, independent step replay.
6. Console (required for any application that uses interrupts): inbox and
   nav entry, Runs card, run page, resolve form, Develop/Invoke waiting
   states, handlers panel, with the e2e coverage listed under *Console*.

Acceptance:

- A run that notifies, waits for an approval, survives API and worker
  restarts while waiting and after an application republish, resolves in
  the console, and completes in the handler on a different replica with
  the same release.
- Two approvers resolving the same interrupt concurrently, and a callback
  delivered twice: exactly one resolution and one handler call; every
  later attempt gets `409 already_resolved` and changes nothing.
- A resolution racing the deadline: exactly one of `handler` or
  `on_expire` runs.
- An early callback before `wait` commits is applied exactly once.
- Expiry calls `on_expire`, or ends the run `expired` when none is given.
- A context missing a required field fails `wait` with zero interrupts
  committed; a wait in step 64 fails the run with `chain_limit`.
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
- **Declared DAG of steps.** Easier to visualize, but moves control flow
  out of code. Handlers keep control flow in Starlark; only their names
  and context contracts are declared, and the chain is derived from what
  ran.
- **Context schemas in code.** Convenient, but EP-0014 keeps contracts in
  release metadata so a program cannot change them by editing source; the
  same rule applies here.
- **One op that creates and waits.** Leaves no window to notify anyone with
  the id before the step ends.
- **Single handler branching on expiry.** Works, but mixes the happy path
  with timeout handling and forces every context schema to serve both.
  `on_expire` is explicit and optional.
- **Accepting repeated resolutions** (last writer wins, or redelivering
  the handler per callback). Makes handler dispatch depend on how often an
  external system retries; one resolution per id keeps every handler call
  traceable to exactly one event.
- **Keeping context in a server-side key/value store.** Hides the handoff
  and adds mutable shared state; the context is carried explicitly instead.

## Decisions

Resolved during review (2026-10-29):

1. **Context schemas:** declared per handler in release metadata,
   validated at publish, at `wait` and before dispatch; all `required`
   fields must be present.
2. **Expiry:** a separate, optional `on_expire` handler; without it an
   expired wait ends the run `expired`.
3. **Limits:** at most 64 steps per run; no separate run age cap for now.
4. **Run id:** visible to `main` (and every step) through
   `interrupt.run_id()`.
5. **Resolution:** each interrupt id can be resolved (or called back)
   exactly once; first arriver wins, every later attempt gets
   `409 already_resolved`.

## Open questions

1. Server defaults and maxima for `expires_in_s` and `fire_in_s`.
2. Should the 64-step limit become a per-application setting under a server
   cap, or stay fixed?
3. Child runs and waiting on several interrupts at once: needed, and with
   which join semantics?
