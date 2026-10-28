# EP-0014: Persistent instances

- **Status:** In progress
- **Scope:** engine, runtime/worker protocol, API, store, console. Refines the
  JSON request/response subset of EP-0011; builds on the immutable boundary
  lifecycle of EP-0012.
- **Related:** EP-0010 (workspaces, access), EP-0011, EP-0012

## Delivery status

Delivered (JSON contract, execution, durable placement and node recovery —
the public build/publish/invoke flow is **not** usable yet):

- `internal/serving`: bounded JSON decoding (`DecodeJSON`: byte/depth/node
  budgets, duplicate-key, UTF-8 and number-token guards) and the pinned
  JSON Schema 2020-12 subset compiler/validator
  (`CompileSchema`/`Validate`/`JSON`), backed by
  `github.com/santhosh-tekuri/jsonschema/v6` behind a thin subset walker.
- `internal/engine`: bounded typed Starlark-to-JSON conversion
  (`MarshalJSONValue`), and the compiled-program path
  (`PrepareProgram`/`InvokeProgram`): config/path/schema validation,
  one-shot `SourceProgramOptions` compile with static load-graph checks,
  disarmed-gate pure init, fresh session + seeded assets + re-initialized
  modules per invoke, armed-gate `main` call, convert/redact/validate
  output, sanitized stdout. Cold-run coverage lives in the engine tests.
  Source, schema and policy snapshots now also have durable release identities.
- `internal/instance` + worker protocol + `internal/node/subproc` +
  `cmd/calcside/internal/worker`: ephemeral serving replicas
  (`runtime.ServingRuntime` embedded in `Runtime`,
  `/runtime/v1/prepare` + `/runtime/v1/invoke`): dedicated leased
  replicas sharing the instance namespace/quota, exact
  owner/epoch/release/generation fences, serialized calls with a
  bounded queue (1 active + 32 queued), per-call fresh
  session/audit, lease-pinned deadlines, failure audit always
  carried. Renewal extends future calls only — in-flight calls stay
  pinned to their lease snapshot.
- `internal/store` + paired SQLite/PostgreSQL migrations: immutable,
  content-addressed releases; persistent desired instances; node boot
  history; atomic replica claims, epochs, finite leases and failure backoff.
  Secret values are forbidden in releases. Extension snapshots are explicitly
  rejected until immutable extension dependency storage is implemented.
- `service/serving`, signed node-control callbacks and `internal/nodeagent`:
  bounded scheduler passes, deterministic placement, startup registration and
  assignment pull, independent heartbeats and prepare workers, ready-only
  renewal, audit acknowledgements and shutdown draining. New boots cannot
  shorten old leases or revive old epochs. API and node clocks need not agree:
  the node conservatively converts API leases into local deadlines.
- Startup wiring for API-local and remote-worker topologies. SQLite restart
  integration tests cover both in-process and child-process execution, API
  handle/service reconstruction, node boot replacement, and fresh globals/VFS.
  PostgreSQL execution still requires the separate configured backend gate.

Pending:

- Build/publish form carrying the two schema objects (editor UI included,
  see "Application editor" below).
- Public publish/invoke API routes and access-table entries, including
  publish-time spec normalization, capability ceilings and quotas.
- Release upgrades/rollback and remaining user-facing lifecycle controls.
- Console UI.

## Problem

An instance is a playground: code in it is callable through exec and MCP,
but there is no published, typed serving contract — nothing a caller can
invoke as a stable service. EP-0011 names the full serving shape — project,
release, environment, endpoint — but the first deliverable does not need all
of it. What is needed now is narrower: write Starlark in a dev instance,
publish it, and get a durable service that answers JSON requests under a
declared input/output contract, even after the dev instance and the workers
are gone. Three gaps block that today:

- Live runtime state survives nothing: a worker restart marks instances
  lost — globals and `/work` are gone, and while run/audit metadata is
  durable, no desired-state record or reconciler exists to rebuild a
  serving runtime from.
- "It ran in my REPL" is not "it runs identically per request": a service
  cannot depend on globals a session happened to leave behind.
- Callers cannot know the request shape, and the service cannot guarantee
  its response shape.

## Language

- **Release** — immutable, content-addressed snapshot: selected `.star`
  sources, bounded assets, and a normalized spec (capabilities, extensions,
  policies, limits, secret references). Its content hash covers the
  entrypoint, both canonical schemas, file bytes, the normalized boundary,
  and snapshots of user policies and extension dependency content/revisions
  (secret *references*, never values), workspace-scoped to avoid
  cross-workspace dedup leaks. Never mutated after creation.
- **Persistent instance** — the durable, user-facing serving resource: a
  stable identity, a desired state, and a pointer to the active release. It
  is the EP-0011 *endpoint* role made concrete for JSON request/response
  programs — not a new project/environment/deployment stack.
- **Replica** — an ephemeral runtime unit (a worker process with a prepared
  context) serving invokes for one persistent instance; no durable truth.

The dev instance is unchanged: publishing reads from it but does not turn it
into serving, and its later expiry does not affect the release. The MVP does
not require the Studio/project/environment subsystem; EP-0011's generic HTTP
handler, dedicated ingress, environments and endpoint keys remain the
direction for full serving — deferred, not replaced.

## What "persistent" means

Durable: the release, the desired service spec, and the instance identity —
after an API or worker restart, a reconciler rebuilds replicas from the
release. Not durable: globals, closures, REPL history, or a runtime-mutated
`/work` — Starlark values cannot be serialized, and pretending they can
produces services that cannot be reproduced from source. Every invoke
initializes a fresh gated session: fresh globals, writable `/work` seeded
only with release assets. The compiled program and its process may be reused
across invokes; mutable session and capability state is never reused.

MVP shape: desired replicas = 1, invokes serialized per instance, bounded
queue and per-invoke timeout. Fresh sessions are still required — one
replica is a resource decision, not a state-sharing mechanism. Replicas run
under finite, server-managed worker leases that the reconciler renews or
reclaims; persistence of the *service* is not implemented via
`ttl_seconds=0`, which would smuggle an unbounded playground into a serving
role.

## Program shape

The entrypoint file (default `main.star`) defines one top-level name:

```python
def main(input):
    tags = input.get("tags", [])
    return {"greeting": "hello, " + input["name"], "tag_count": len(tags)}
```

- The schemas are **not** declared in code: they are release metadata
  supplied at publish time (see "Publishing" and "Application editor"),
  so a program cannot change its contract by editing source.
- `main` receives the validated input as a frozen JSON-shaped value and
  returns a JSON-able value, served as `application/json`.
- `load()` resolves only against release files — VFS-only, no host or
  network fallback. Extensions come through the spec, pinned by version and
  sum; local-root extension content is snapshotted into the release.
- Top-level code defines pure values only; effectful capability calls happen
  inside `main`.
- `policies` omitted/null vs `[]` survives the release unchanged.

## Publishing

`POST /api/v1/instances/{id}/publish` snapshots the selected sources into a
release and records the new persistent instance's desired state in one atomic
write; preparation proceeds asynchronously (`preparing` → `ready`, or
`degraded` with an explicit error).

The request carries required `input_schema` and `output_schema` JSON
objects supplied by the build form: `{}` means explicitly unconstrained;
an absent schema is an error, not a default. Publish bounds the documents
and compiles them with the pinned-subset validator (`serving.CompileSchema`);
invalid schemas fail the request before any release is written. The
canonical compiled schemas are stored on the release and covered by its
content hash, so retries and idempotency keys compare the same contract.

The snapshot is taken under the source instance's existing
request/execution/artifact synchronization, in the acquisition order used
today, yielding one coherent bounded snapshot of the selected sources,
assets and immutable config. Those locks are released *before* compile,
validation and storage writes run — publish never compiles or writes while
holding instance locks. No hidden dev state travels: only selected `.star`
files and bounded assets — never VFS leftovers, globals, or run history.

The entrypoint is verified by compiling the program and running a pure
init — top-level evaluation with effectful capabilities denied and
step/time/memory bounds — which checks that `main` exists and is callable.
Publishing never runs `main` and never performs a network or file side
effect. Prepare and invoke use the release's captured schemas as
authoritative; there is no code-based schema override to re-read or
re-compare. A cold-run test uses the same initialize/validate/call path as
invoke.

Publish carries a workspace-scoped idempotency key: a retry returns the same
release and instance; the same key with different snapshot/options conflicts.

## Application editor

The build/publish form derives the two schema drafts from sample JSON
objects — one click per side — then lets the user edit the result before
publishing. Generation rules:

- A sample object produces an object schema whose `properties` map every
  observed key to an inferred schema; by default all observed properties
  land in `required`, and a per-property "optional" toggle *removes* the
  name from the parent's `required` list (it never becomes a nullable
  union type).
- Objects recurse; arrays infer a single `items` schema. An empty array
  yields `"items": {}` (explicitly unconstrained); a heterogeneous array
  yields a bounded `anyOf` of the observed element shapes.
- `null` in a sample maps to `"type": "null"` explicitly — it never marks
  a property optional.
- Inference is provisional: a sample cannot express business constraints
  (`minLength`, `minimum`, `enum`, formats). The editor stays editable so
  users tighten before publishing and must review before publish.
- Exact integers are preserved end to end: inference must not round the
  sample through `JSON.parse`-style float64 (a `9007199254740993` member
  stays an exact integer in the generated schema and in preview).
- The backend remains authoritative: publish validates the raw schema
  documents with `serving.CompileSchema` regardless of what the editor
  produced; drafts stay bounded by the same limits.

A console draft editor is available with the stateless infer/validate
contract endpoints; snapshot/publish is still not connected.

## Schemas

The contract is a bounded subset of JSON Schema 2020-12, pinned so the
validator is an implementation detail rather than a moving target. Allowed:
`type` (including `null` and multi-type lists), `properties`, `required`,
`additionalProperties` (boolean or nested schema), `items` (single schema
only, no tuple form), `enum`, `const`, `minimum`/`maximum`,
`minLength`/`maxLength`, `minItems`/`maxItems`, `anyOf` (bounded members),
and the annotations `title`/`description` plus `$schema` restricted to the
exact 2020-12 URI.

Everything else — `$ref` (remote *and* local), `$defs`, `format`, `pattern`,
composition beyond `anyOf`, `default` — is rejected at publish. Boolean
subschemas exist nowhere except `additionalProperties`. No coercion or
default injection; unknown fields follow `additionalProperties`.

Validation uses `github.com/santhosh-tekuri/jsonschema/v6` behind a thin
subset walker that enforces the keyword allowlist and product rules above;
draft 2020-12 semantics are the library's, including enum elements that
need not be unique. Bounds are size/structure only: schema bytes ≤64 KiB,
nesting depth, node count, `anyOf` ≤16 members, `minLength`/`maxLength`/
`minItems`/`maxItems` capped at 2^31-1 (the library truncates them to
platform int), and number tokens capped at
`serving.MaxNumberTokenBytes` (256 bytes) with bounded exponents so every
converter output also decodes. There is deliberately no work/CPU budget;
request body limits bound input size, not validation execution time.

## Invoking

`POST /api/v1/persistent-instances/{id}/invoke` takes a raw JSON body —
**data only**, never code, entrypoint, spec overrides, or schemas — and
returns `{invocation_id, release_id, output}`. Request path, in order:

1. Bound input bytes, depth and value count *before* validation.
2. Validate against the release's `input_schema`; invalid input is rejected
   before any user code or capability runs.
3. Initialize the fresh session and call `main`.
4. Convert the return value to a typed Go value with traversal, depth, node
   and byte budgets enforced *during* conversion — before any unbounded Go
   value or encoding is constructed. Integers are preserved; non-finite
   numbers, cycles, non-string dict keys and non-JSON values are rejected.
5. Redact known secrets, then validate the **redacted** output against the
   release's `output_schema`. Fail closed: a redaction that breaks the
   schema is an `invalid_output` error, never a silently invalid success.

Errors: `422` invalid input; `500` `invalid_output` or sanitized runtime
error; `429` queue overload; `503` not ready or disabled; `504` timeout with
an explicitly ambiguous outcome (below). Stdout is a bounded, redacted field
on the invocation record, separate from the typed return. Responses are JSON
only (`application/json` + `nosniff`); preview HTML is never serving output.

## Lifecycle, upgrade and recovery

States: `preparing` → `ready`; `degraded` records an explicit error;
`disabled` is durable and stops both routing and recreation. Desired state is
re-checked against a generation/epoch at every step so a superseded or
cancelled prepare cannot resurrect a route or accept new invokes.

Upgrade stages a candidate release to readiness, then compare-and-swaps the
active release. Candidate failure keeps the previous release serving;
in-flight invokes stay pinned to the context they started on; rollback is an
explicit activate-release call. Failover after worker loss rebuilds from the
release — never from process state.

Timeouts and worker deaths leave **unknown outcomes unknown**: no auto-replay
of a possibly-completed side effect; exactly-once delivery is a non-goal.
In-flight work on a superseded release cannot undo external effects it
already performed; drain/cancel is bounded and recorded.

## API (proposed, illustrative)

All routes land in `api/openapi.yaml` first when implemented; shapes are illustrative.

| Route | Body → result |
|---|---|
| `POST /api/v1/instances/{id}/publish` | `{name, entrypoint, sources[], assets[], input_schema, output_schema, idempotency_key}` → `{release_id, persistent_instance_id, status}` |
| `GET /api/v1/persistent-instances/{id}` | → `{status, active_release, input_schema, output_schema, error}` |
| `POST /api/v1/persistent-instances/{id}/invoke` | raw JSON input → `{invocation_id, release_id, output}` |
| `POST /api/v1/persistent-instances/{id}/disable` | durable stop |
| `POST /api/v1/persistent-instances/{id}/activate-release` | `{release_id}` upgrade/rollback |

`sources` is the explicit `.star` set (entrypoint among them) that `load()`
may reach; `assets` adds only explicit bounded input files. Both schema
fields are required JSON objects in the pinned subset.

## Access and security

Authorization stays in `service/access` (roles ∩ key ceilings, invisible →
`not_found`), and no new ordinal actions are added. Invoke maps to the
existing **Run** ceiling on the persistent instance: editor/owner sessions
and unrestricted workspace Run keys may invoke; viewers and view-only keys
cannot. Publish requires View/Run on the source instance plus **Configure**
in its workspace — editor/owner sessions only, so API keys capped at Run
cannot publish or manage. Restricted and development-instance-bound keys can
neither invoke serving resources nor publish, and development shares grant
no publication rights. The distinct invoke operation authorizes calling a
served program only — it grants no arbitrary exec or MCP access — and source
and serving resource live in the same workspace, leaving the existing role
ceiling unchanged.

The served program's authority is the release's pinned spec and policies,
independent of the invoker, enforced by the same Gate, hooks and audit as
everything else; audit records both invoker and config authority, and
metadata never carries bodies, prompts, or secrets.

Releases store secret *references* only — no decrypted copies. Workers never
acquire vault or store access: an API-side config-resolution service
resolves and decrypts references and supplies a scoped context over the
authenticated worker protocol; workers keep resolved values memory-only.
Explicit rotation bumps a context revision and reprepares; deletion or
revocation invalidates the context so no new invokes start on it — effects
an in-flight call already performed cannot be reversed. Deny overlays
tighten immediately. Provider configs and extension dependencies pin to
immutable revisions — no silent catalog drift.

## Architecture

- **Engine:** one program path — compile once, initialize, call `main` —
  shared by prepare, cold-run test, and invoke. Input passes as a typed
  value, never JSON interpolated into an exec chunk. Reuse the existing
  capability assembly, session locks held through audit draining, and
  `Request.Context`, never a pooled transport context.
- **Worker protocol:** new ops go through `internal/runtime` contract types
  and `api/worker.openapi.yaml`; generated code only, no hand edits.
- **Store:** new tables on the store interface, the GORM implementation and
  the conformance suite, with paired new Atlas migrations — the consolidated
  baseline stays untouched.
- **Reconciler:** API-side; owns desired state, lease claims and fencing.
  Workers keep no database access.
- **Parity:** single-binary and remote/process transports implement the same
  semantics.

## Verification

- Publish captures only selected sources: dev globals and unselected files
  are unavailable to the release; every invoke sees fresh globals/`/work`.
- Schema acceptance: valid/invalid schema objects supplied at publish,
  unsupported keywords, any `$ref`; pathological but bounded inputs.
- Invalid input produces zero capability calls; invalid or redaction-broken
  output never returns as success.
- Publishing executes no effectful calls (denied-capability probe).
- Release survives source expiry, API restart and worker loss; `disabled`
  holds under reconciler races and generation fencing.
- Upgrade: candidate failure preserves the old release; in-flight invokes
  stay pinned; a lost worker does not auto-retry a mutating call.
- Access-table coverage for every new operation; workspace isolation; store
  conformance on both dialects; transport parity.
- Console implementation is separate from this backend MVP; any UI must
  preserve the `docs/interaction-stability.md` invariants.

## Non-goals and follow-ups

Non-goals for the MVP:

- Exactly-once invoke semantics or durable idempotency keys on invoke.
- Any persisted runtime state: globals, closures, REPL history, mutated `/work`.
- Generic HTTP handlers, dedicated ingress, public access, environments,
  endpoint keys — EP-0011 scope, deferred.
- Multiple replicas and streaming responses.

Product follow-ups:

- Durable application state via an explicit external-store capability or a
  declared state area — designed, not leaked through replica reuse.
- Replicas > 1 with routing that preserves per-request freshness; streaming.
- Public/authenticated ingress and endpoint keys per EP-0011.
