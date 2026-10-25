# EP-0011: Studio, environments, and endpoints

- **Status:** Draft
- **Scope:** engine, runtime/worker protocol, API, store, console. Builds on
  EP-0010 (workspaces, access) and the single-instance MCP mode.

## Problem

An instance today is a **playground**: each run continues from the globals the
previous run left behind, like a notebook. That is right for exploration (by
a human or an agent) and wrong for anything that must work again tomorrow:

- A result can depend on state that no longer exists in any code. "It worked
  in my instance" is not reproducible.
- There is no way to take code that works and run it on demand for someone
  else, with production credentials, under a separate access rule.

What users ask for is the opposite contract: **if it runs in dev, it runs in
production**. Code in the instance is a draft; what gets served must be a
program that runs from scratch, every time, identically except for the
configuration that is meant to differ.

## Language

Two modes, named for what the user is doing:

| Mode | State between runs | Purpose |
|---|---|---|
| **Playground** | kept (today's instance) | explore, draft, let an agent try things |
| **Studio** | none: every run starts from scratch | build something that will be served |

New nouns, and only these:

- **Project** — the Studio unit: a set of `.star` files plus the instance
  spec they need (capabilities, policies, limits). Belongs to a workspace.
- **Environment** — named configuration a project runs with: environment
  variables and secrets. Every workspace has `dev`; others (e.g. `prod`) are
  created by owners.
- **Release** — an immutable snapshot of a project (files + spec), identified
  by a content hash.
- **Endpoint** — serves one release in one environment at a URL.

Existing words keep their meaning: instance, run, call, policy, secret,
workspace, member, key. Not introduced: deployment, stage, app, function,
lambda, build, pipeline, revision, tenant.

## The contract: dev passes ⇒ prod passes

Production can only differ from dev in what the environment supplies.
Everything else is fixed by the release:

1. **Same code.** An endpoint serves a release by hash; the files are the ones
   that ran in Studio.
2. **Same boundary.** Capabilities, policies and limits live in the project
   spec, not in the environment. Prod cannot quietly allow more than dev, and
   dev cannot pass because it allows more than prod.
3. **Same start.** Every Studio run and every endpoint request starts from a
   fresh session: no globals carried over, empty `/work`. A Studio run is
   literally an endpoint request against `dev`.
4. **Same shape of configuration.** The project declares the environment
   variable and secret *names* it needs. Creating an endpoint fails unless
   the target environment provides every one. A missing value is a
   configuration error before traffic, never a runtime surprise.

What still legitimately differs, and is shown side by side in the console:
the values of those variables and secrets, and the outside world they point
to. Values are never part of a release.

## Program shape

- Entry point: `main.star` defines `def main(request)`.
- `request` is a frozen struct: `method`, `path`, `query` (dict of lists),
  `headers` (allowlisted names only), `body` (string), `json()`. Bounded by a
  request size limit.
- Return a JSON-able value (200, `application/json`) or
  `response(status=..., body=..., headers=...)` from a small built-in.
- `print` output goes to the run record, never into the response.
- `load("lib.star", "name")` resolves only against the project's own files.
  No other load sources. Extensions still come in through the spec.

The engine gains a second entry point beside today's REPL chunk: compile
the release once (`starlark.FileProgram` with the project-local loader),
then for each request initialize fresh globals and call `main`. The
compiled program is shared; globals are not.

## From playground to project

Drafting happens in a playground, often by an agent through single-instance
MCP. Promotion is explicit:

- **Save to project** on the instance page: pick `.star` files under `/work`
  (or a run's code) and write them into a new or existing project as files.
- The console then opens Studio, where **Run** executes `main` against `dev`
  from scratch with a saved example request. If it only worked because of
  playground state, this is where that shows.
- Later: an MCP tool for the same promotion, so an agent can hand its work
  over; a human still makes releases.

## Environments

- Workspace-level, shared by its projects. `dev` always exists.
- Hold environment variables (plain) and secrets (vault references or
  values, encrypted as today; AAD bound to environment ID + name).
- An environment may be **protected**: changing its values or pointing an
  endpoint at it requires `manage` (owners). `prod` is protected by default.
  Unprotected environments need `configure` (editors).
- Playground instances keep their own spec env/secrets (today's behavior).
  Studio runs always use `dev`, never instance config.

## Serving

### Ingress

Endpoint traffic arrives on a separate origin,
`CALCSIDE_ENDPOINT_BASE_URL` (like the artifact preview origin), at
`/{endpoint_id}/...`. The API tier authenticates, rate-limits and routes the
request; cookies and console sessions are never accepted there, so a served
program can never ride a human's session.

### Workers pull their context

A worker that receives an invocation for an endpoint it has not prepared
pulls the endpoint context from the API tier through a new `api-callback`
operation (as ext tree resolution does today): release files and spec,
resolved policies, environment variables and decrypted secrets, plus a
context version. The worker caches the compiled release and context per
`(endpoint, context version)` in memory only. A change to the endpoint or its
environment bumps the version, and the next request refetches. Secrets never
go to disk and are never shared between endpoints.

### Isolation

Requests run in child processes as instances do (`--instance-isolation`),
from a small per-endpoint pool. A child serves one endpoint only, so one
endpoint's secrets never share a process with another's. Each request gets a
fresh session (globals and `/work`) inside the child. Per-endpoint limits:
concurrency, request timeout, max steps, max output, request/response size.

### Records

Each request is a run with `via = endpoint`, the release hash and the
environment, so the existing run/call views (EP-0009) show what a served
program did and what the boundary refused. Errors and denials are always
kept; successful-run retention at volume is an open question.

## Calling an endpoint: a separate access rule

Calling an endpoint is not acting in a workspace, so it is not an API key
and not a role. Endpoint access is one of:

- **Endpoint keys** (default): bearer tokens bound to one endpoint; they can
  only call it. Created by whoever can configure the endpoint; shown once;
  revocable; optional expiry; per-key rate limit.
- **Public**: no credential, rate-limited per client address. Owner-only
  opt-in, shown clearly in the console.

Endpoint keys share nothing with API keys: different prefix, different
table, no workspace actions. A leaked endpoint key can call one program and
nothing else.

Inside the workspace, the `access` module gains the objects, not new roles:

| Action on | viewer | editor | owner |
|---|---|---|---|
| view projects, releases, endpoints, requests | ✓ | ✓ | ✓ |
| edit project files, Studio runs, make releases | | ✓ | ✓ |
| endpoints on unprotected environments, endpoint keys | | ✓ | ✓ |
| protected environments (values, endpoints), public access | | | ✓ |

API keys follow the same ceilings as today: they may edit project files and
run Studio (run), never configure endpoints or environments.

## Refactoring first

Following the per-proposal review, before any serving code:

1. **Engine:** split today's `Session` into a shared builder (spec →
   predeclared bindings, Gate, hooks, limits) used by both the REPL session
   and a new run-once program. No second way to assemble capabilities.
2. **Instance manager:** extract the "build a gated session from spec +
   policies + secrets" step from `instance.Manager.Create` so the endpoint
   pool reuses it instead of copying it.
3. **Secrets:** generalize vault resolution from "instance spec secrets" to
   "secret references in a scope" (instance or environment), keeping one
   cipher and one AAD rule.
4. **Runtime contract:** new worker operations (`invoke`) and the
   `api-callback` context pull go through `internal/runtime` contract types
   and `api/worker.openapi.yaml`, as today.

## Security

- Separate origin for endpoints; no cookies; CSP on responses; response
  headers are allowlisted (no `Set-Cookie`).
- Releases are immutable and hashed; an endpoint names the hash it serves.
- Secrets reach a worker only in a context pull for an endpoint it is
  serving, live in memory, and are redacted from outputs as today.
- The served program's authority is the release's spec, bounded by the
  same Gate, policies and audit as everything else. Being an endpoint does
  not grant anything.
- Rate limits per endpoint key and per public client; request bodies bounded
  before parsing.

## Delivery

1. Engine/manager/secrets refactor (no behavior change).
2. Projects, Studio editor and fresh runs against `dev`; Save to project.
3. Environments (variables, secrets, protected), declared names and
   validation.
4. Releases, endpoints, endpoint keys, worker context pull, separate origin.
5. Endpoint request records in the console; retention; public access.

## Open questions

- **Hosts per environment.** Dev and prod often call different hosts
  (staging vs production APIs). Keeping `allow_hosts` in the spec preserves
  rule 2 but forces both hosts into one allowlist. Alternative: environments
  may *narrow* (never widen) the spec's hosts. Narrowing keeps the guarantee
  "prod never allows more than the spec"; it is proposed but not settled.
- Files in served runs: empty `/work` only, or read-only release assets
  (e.g. a lookup CSV) as well.
- Retention and sampling of successful endpoint runs at high volume.
- Whether Studio should allow a "dev endpoint" (served from `dev`) for
  integration testing, or only console runs.
