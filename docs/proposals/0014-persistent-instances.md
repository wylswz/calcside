# EP-0014: Persistent instances

- **Status:** Draft
- **Scope:** engine, runtime/worker protocol, API, store, console. Refines the
  JSON request/response subset of EP-0011; builds on the immutable boundary
  lifecycle of EP-0012.
- **Related:** EP-0010 (workspaces, access), EP-0011, EP-0012

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
  entrypoint, both schemas, file bytes, the normalized boundary, and
  snapshots of user policies and extension dependency content/revisions
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

The entrypoint file (default `main.star`) defines three top-level names:

```python
input_schema = {
    "type": "object",
    "properties": {
        "name": {"type": "string", "minLength": 1},
        "tags": {"type": "array", "items": {"type": "string"}, "maxItems": 16},
    },
    "required": ["name"],
    "additionalProperties": False,
}
output_schema = {
    "type": "object",
    "properties": {
        "greeting": {"type": "string"},
        "tag_count": {"type": "integer"},
    },
    "required": ["greeting", "tag_count"],
    "additionalProperties": False,
}

def main(input):
    tags = input.get("tags", [])
    return {"greeting": "hello, " + input["name"], "tag_count": len(tags)}
```

- Both schema declarations are required at publish: `{}` means explicitly
  unconstrained; omitting one is an error, not a default.
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

The snapshot is taken under the source instance's existing
request/execution/artifact synchronization, in the acquisition order used
today, yielding one coherent bounded snapshot of the selected sources,
assets and immutable config. Those locks are released *before* compile,
validation and storage writes run — publish never compiles or writes while
holding instance locks. No hidden dev state travels: only selected `.star`
files and bounded assets — never VFS leftovers, globals, or run history.

Schemas are extracted statically: `input_schema`/`output_schema` must be
bounded literal dict/list JSON values, not dynamically computed. The
entrypoint is verified by compiling the program and running a pure init —
top-level evaluation with effectful capabilities denied and step/time/memory
bounds. Publishing never runs `main` and never performs a network or file
side effect. At prepare time the engine re-reads the schema globals and
rejects the release if they differ from the captured metadata. A cold-run
test uses the same initialize/validate/call path as invoke.

Publish carries a workspace-scoped idempotency key: a retry returns the same
release and instance; the same key with different snapshot/options conflicts.

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
composition beyond `anyOf`, `default` — is rejected at publish. No coercion
or default injection; unknown fields follow `additionalProperties`. Schema
nesting depth and validation work are bounded.

## Invoking

`POST /api/v1/persistent-instances/{id}/invoke` takes a raw JSON body —
**data only**, never code, entrypoint, or spec overrides — and returns
`{invocation_id, release_id, output}`. Request path, in order:

1. Bound input bytes, depth and value count *before* validation.
2. Validate against `input_schema`; invalid input is rejected before any
   user code or capability runs.
3. Initialize the fresh session and call `main`.
4. Convert the return value to a typed Go value with traversal, depth, node
   and byte budgets enforced *during* conversion — before any unbounded Go
   value or encoding is constructed. Integers are preserved; non-finite
   numbers, cycles, non-string dict keys and non-JSON values are rejected.
5. Redact known secrets, then validate the **redacted** output against
   `output_schema`. Fail closed: a redaction that breaks the schema is an
   `invalid_output` error, never a silently invalid success.

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
| `POST /api/v1/instances/{id}/publish` | `{name, entrypoint, sources[], assets[], idempotency_key}` → `{release_id, persistent_instance_id, status}` |
| `GET /api/v1/persistent-instances/{id}` | → `{status, active_release, input_schema, output_schema, error}` |
| `POST /api/v1/persistent-instances/{id}/invoke` | raw JSON input → `{invocation_id, release_id, output}` |
| `POST /api/v1/persistent-instances/{id}/disable` | durable stop |
| `POST /api/v1/persistent-instances/{id}/activate-release` | `{release_id}` upgrade/rollback |

`sources` is the explicit `.star` set (entrypoint among them) that `load()`
may reach; `assets` adds only explicit bounded input files.

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
- Schema acceptance: valid/invalid schemas, unsupported keywords, any `$ref`;
  pathological but bounded inputs.
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
