# EP-0010: Workspaces, sharing, and key access

- **Status: Implemented
- **Scope:** API-tier authorization (`cmd/calcside/internal/service`), store
  schema, API keys, console navigation. Depends on EP-0008 (restricted keys).

## Problem

Today every resource belongs to exactly one user, and each service checks
that by hand:

- `sandbox.owned` compares `in.UserID != a.UserID` (plus `KeyID` for
  restricted keys); `policy`, `vault`, `audit` and `iam` each repeat a
  similar comparison and their own `a.ViaKey()` / `a.Restricted()` branches.
- `users.is_admin` is stored and shown, but nothing checks it.
- The worker repeats the ownership check (`runtime.ErrNotOwner`) against the
  caller's user ID.

That is enough for one person and their agents. It does not support:

1. **Teams.** Several people supervising the same instances and sharing the
   same policies and secrets.
2. **Handing off one instance.** Letting a reviewer look at an agent's runs
   without joining a team.
3. **Narrower agent keys.** A key that can only watch, or that stops working
   when its creator leaves the team.

The scattered checks are also a liability for any extension: a new route that
forgets its comparison is an access bug, and nothing tests for that.

## Should we use SpiceDB (or OpenFGA)?

Not now. The relationships we need are two levels deep: a workspace contains
resources, and a share links one user to one instance. There are no nested
groups, folders or inherited hierarchies.

| | SpiceDB | OpenFGA | Cedar (`cedar-go`) | In-process, existing store |
|---|---|---|---|---|
| Runs as | separate gRPC service | separate service | library | library |
| Storage | Postgres / CockroachDB / Spanner / MySQL; memdb is not persistent; no SQLite | Postgres / MySQL / SQLite | none (entities passed per request) | existing SQLite / Postgres |
| Writes | every create/delete also writes relationships to a second system: drift, retries, consistency tokens | same | n/a | same transaction as the resource |
| Lists | `LookupResources` per list page | `ListObjects` | not supported | one SQL join |
| Cost to Calcside | breaks the single-binary + SQLite deployment; a new failure mode on every request | same, slightly easier to deploy | a second rule language next to Rego, overloading the word "policy" | small |

A Zanzibar-style service pays off when relationships form deep graphs
(organizations → teams → sub-teams, folders inside folders), when several
services need the same decisions, or when "who can see X?" must be answered
at large scale. None of those holds. Cedar adds a rule language without
solving storage, and "policy" already means the Rego rules that judge an
agent's capability calls (EP-0009); a second kind of policy would blur that.

**Decision:** implement access in-process against the existing store, but keep
the data in the shape such a system uses: every fact is *(user, role,
object)*. Moving to SpiceDB or OpenFGA later is then a mechanical export plus
swapping the implementation behind one interface.

**Revisit when** any of these becomes a real requirement: nested teams or an
organization above workspaces; more than two levels of containment (e.g.
projects inside workspaces with their own roles); another service that must
make the same decisions; or access checks showing up as a measured bottleneck.

## Language

New nouns, and only these:

- **Workspace** — owns instances (and through them runs, calls, files),
  policies, secrets and keys. Every user has a **personal** workspace.
- **Member** — a user in a workspace, with a **role**: `owner`, `editor` or
  `viewer`.
- **Share** — one instance shared with one user, as `viewer` or `editor`.

Existing words keep their meaning: *policy* (Rego rules on agent calls),
*full access* / *restricted* key (EP-0008), *run*, *call*. Not introduced:
team, organization, tenant, principal, permission, grant, relation, tuple,
ACL. "Team" may appear in prose, but the object is always a workspace.

Internally the code uses four actions. The console shows only role names.

| Action | Covers |
|---|---|
| `view` | instance details, runs, calls, files, state, prompt, artifact preview/export |
| `run` | create, exec, keepalive and delete instances |
| `configure` | create, edit and delete policies, secrets and keys |
| `manage` | members, shares, workspace rename/delete |

| Role | view | run | configure | manage |
|---|---|---|---|---|
| viewer | ✓ | | | |
| editor | ✓ | ✓ | ✓ | |
| owner | ✓ | ✓ | ✓ | ✓ |

A share gives `view` (viewer) or `view` + `run` (editor) on **that instance
only**. It never gives access to the workspace's policies, secrets or keys.

## Rules

1. **One place decides.** All checks go through `access` (below). Services
   do not compare user IDs.
2. **Hidden, then forbidden.** No `view` on an object → `not_found`, exactly
   as for a nonexistent ID. `view` but not the requested action →
   `forbidden`. This keeps today's no-enumeration behavior.
3. **Computed each request.** Effective actions are derived from current
   membership, shares and key settings on every request. Nothing is copied
   onto sessions or keys, so removing a member or downgrading a role takes
   effect on their next request, including for their keys. Requests already
   past the check (an exec in flight) finish.
4. **Keys never exceed their creator.** A key's actions = its creator's role
   in the key's workspace ∩ the key's ceiling. A key belongs to exactly one
   workspace and does not follow shares.
5. **Keys never configure or manage.** A policy is the human's limit on the
   agent; the agent's key must not be able to edit it. This tightens today's
   behavior, where a full-access key can edit library policies (see
   Compatibility).
6. **Personal workspaces have exactly one member.** They cannot gain
   members; their instances can still be shared.
7. **A workspace always has an owner.** Removing or demoting the last owner
   is rejected.

### Keys

| Key access | Ceiling | Instances |
|---|---|---|
| Full access | view, run | all in the workspace |
| Restricted (EP-0008) | view, run | only instances it created, only from its spec |
| View only | view | all in the workspace |

"View only" is the one new key option: for dashboards or a second agent that
reviews another agent's work. Restricted keeps all EP-0008 rules.

## Design

### Module

`cmd/calcside/internal/service/access` (API-only), a small surface:

```go
type Action uint8 // View, Run, Configure, Manage; unexported set type

type Checker struct{ /* store */ }

// Instance loads the instance and checks act on it (workspace role,
// share, key ceiling, restricted-key ownership).
func (c *Checker) Instance(ctx context.Context, a service.Actor, id string, act Action) (*store.Instance, error)

// Workspace checks act on the workspace itself (configure, manage, and
// run for create).
func (c *Checker) Workspace(ctx context.Context, a service.Actor, wsID string, act Action) error

// Instances returns the store filter for instances a may see:
// its workspace's (narrowed for restricted keys) plus those shared with it.
func (c *Checker) Instances(ctx context.Context, a service.Actor) (store.InstanceFilter, error)
```

Executions and audit are checked through their instance; policies, secrets
and keys through their workspace. `sandbox.owned`, `CheckInstanceAccess` and
the per-service `ViaKey` / `Restricted` branches go away. `Actor` gains
`WorkspaceID` (resolved per request, below) and keeps `KeyID`; the key's
spec and ceiling are read by `access`, not by each service.

### Workspace in requests

Instance IDs are global, so instance routes stay `/instances/{id}`. Routes
that list or create (instances, policies, secrets, keys, audit) take an
optional `workspace` query parameter; omitted means the caller's personal
workspace, so existing SDK calls keep working. A key's workspace is fixed:
a different `workspace` value is `forbidden`.

New routes: `GET/POST /workspaces`, `PATCH/DELETE /workspaces/{id}`,
`GET/PUT/DELETE /workspaces/{id}/members/{user}` (add by email of an
existing user), `GET/PUT/DELETE /instances/{id}/shares/{user}`, and
`GET /api/v1/instances?shared=true` returning instances reached via a
share with their share role and owning workspace name (keys get 403).

### Store

New tables, each a set of *(user, role, object)* facts:

- `workspaces(id, name, personal, created_at)`
- `members(workspace_id, user_id, role)`, unique `(workspace_id, user_id)`
- `shares(instance_id, user_id, role, created_by, created_at)`, unique
  `(instance_id, user_id)`

New column `workspace_id` (not null, indexed) on `instances`, `policies`,
`secrets`, `api_keys`. Policy and secret names become unique per workspace
instead of per user. `api_keys.ceiling` (`run` | `view`, default `run`).

Migration (SQL only, both dialects): create one personal workspace per user
with id `'ws_' || substr(users.id, 5)`, owner membership, and backfill
`workspace_id` from `user_id`. `user_id` stays on every row as **created
by**: execution records and audit keep showing who acted.

Store interface, GORM implementation and `storetest` conformance all change
together, as usual. Instance list filtering is one query:
`workspace_id = ? OR id IN (SELECT instance_id FROM shares WHERE user_id = ?)`.

### Secrets

Vault ciphertext is bound by AAD `userID/name`. Re-encrypting under a
workspace would need the cipher key during migration, which SQL migrations
cannot have. Instead the AAD keeps using the secret's **creator** and name.
Both are immutable, so the binding still stops ciphertexts being swapped
between rows, and no re-encryption is needed. Secrets never move between
workspaces.

Members use workspace secrets in instance specs; nobody can read a secret
value back, as today. Viewers and shares cannot see a secret's existence
beyond what the instance's allowed summary already shows (names, never
values).

### Runtime owner

The worker checks that requests come from the instance's owner. With
workspaces the actor and the owner differ, so the API sends the stored
instance's `user_id` as `runtime.Owner` (the worker check stays as defense in
depth), and sends the acting user separately for the execution record and
audit. Audit events must record the actor, not the instance creator: verify
where the audit batch's user comes from and fix it in the same change.

### Console

- A workspace switcher in the header, shown only when the user belongs to
  more than one.
- Workspace settings (owners): name, members and roles.
- Instance page: **Share** (owners), showing who has access and how.
- Instance list: a **Shared with you** section.
- Keys page: keys belong to the current workspace; access gains **View
  only**.
- Runs already show who ran them (EP-0009); in a shared workspace that is
  now essential, not decorative.
- Controls the user cannot use are not rendered. The API stays the
  authority.

## Security

- Every route is checked by `access`. A test enumerates every operation in
  the generated server interface and calls it as a stranger, a viewer and a
  view-only key, failing on any route not covered or not denied. New routes
  cannot silently skip authorization.
- `not_found` for invisible objects, everywhere, including executions and
  audit reached by ID.
- Adding a member requires the user to have signed in once; no invitation
  email. Whether an email has an account is revealed only to workspace
  owners.
- Membership removal also marks that member's keys in the workspace as
  revoked, so the keys page tells the truth; rule 3 makes them stop working
  regardless.
- Share and membership changes are recorded (who, what, when) and shown in
  workspace settings. They are not capability calls, so they do not go into
  the call audit.
- `is_admin` remains unused by authorization; operator-wide oversight is out
  of scope and would be its own proposal.

## Refactoring first

Following the per-proposal review: before adding any new concept, phase 0
moves the existing checks into `access` with no behavior change, proved by a
role × action × resource matrix test that passes before and after. This
shrinks every service's interface (no more ownership helpers exported
across packages) and means workspaces later change one module, not five.

## Compatibility

- Existing users see one personal workspace containing everything they had;
  URLs, SDK calls and key secrets are unchanged.
- **Behavior change:** full-access keys can no longer create, edit or delete
  library policies (rule 5). Scripts that manage policies with a key must
  use a console session.
- Worker protocol: an `actor` field is added to exec requests (contract
  types in `internal/runtime`, then `make gen`).

## Delivery

0. `access` module; move existing checks; matrix test; route coverage test.
   No behavior change.
1. Workspaces, members, migration and `workspace` parameter; personal
   workspaces only. Runtime owner/actor split; audit actor fix.
2. Shared workspaces: create, members, roles, switcher, settings page.
3. Instance shares.
4. Key ceilings (view only), key ↔ workspace binding, rule 5.

Each phase passes the Go, web and browser checks and ships on its own.

## Decisions

- Only owners share instances: sharing goes outside the team and is a
  `manage` decision.
- `view` includes artifact export (download). Exported content is already
  bounded and secret-redacted; a viewer who can preview can copy anyway.
- Rule 5 is accepted: keys never configure.

## Open questions

- Per-workspace instance limits in addition to today's per-user limit.
