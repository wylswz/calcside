# EP-0017: Workspace modules — publishable Starlark libraries

- **Status:** Draft
- **Created:** 2026-11-03
- **Area:** store, spec normalization, engine `load()` resolution, instance and application creation, worker protocol, console
- **Related:** [EP-0003](0003-community-extensions.md), [EP-0010](0010-workspaces-and-access.md), [EP-0012](0012-instance-addons-and-workflow.md), [EP-0014](0014-persistent-instances.md), [EP-0018](0018-reviewable-publish.md), [docs/extensions.md](../extensions.md)

## Summary

A **module** is a workspace-scoped library of `.star` files: developed in the
console, saved as a mutable draft, and published as immutable, content-pinned
versions. Instances and applications attach modules through a new
`spec.modules` field and reach their code through `load("@alias/path.star")`.

Modules reuse the extension tree's size, path, and `h1:` integrity rules, but
they are a different kind of thing: an extension is a *capability* — it has a
config surface, declared dependencies, and every call is a gated
`ext.<alias>.<op>` record. A module is *source* — it has no ops surface and no
per-call gate records. It compiles and initializes inside the consumer's own
module graph and sees the consumer's predeclared environment: a module that
calls `net.post` requires the consumer's `net` grant, and every capability
call it makes goes through the consumer's Gate, policies, secrets, and audit
unchanged.

Modules may **use extensions**. A module declares the extensions it needs in
an optional `module.yaml` manifest, pinned to `source` + `h1:` sum at publish.
Attaching a module **cascades** those extensions into the consumer:
resolution merges them into `spec.capabilities.ext` exactly as if the consumer
had installed them, so module code calls `ext.<alias>.<op>` through the
ordinary, gated ext binding. The cascade installs extension code only —
pinned to `source` + `sum`; all config and credentials come from the
consumer. It never grants a base capability, widens `net.allow_hosts`, or
attaches a policy (EP-0012).

## Motivation and current state

Today there is no way to share Starlark code between consumers:

- Instance exec runs REPL chunks; `thread.Load` is never set, so `load()` does
  not work at all in an instance.
- Applications store one file (`application.code` → `main.star`). The program
  engine supports `load()` only between files inside a single
  `ProgramBundle.Sources` — so teams copy helpers into every application.
- The only reusable-code mechanism is `ext`, which is deliberately heavyweight:
  an operator-placed source root or a pinned git repo, a `capability.yaml`
  manifest, dependency and config validation, and a gated op surface. That is
  the right shape for capability adapters (Tavily, LLM providers) and the wrong
  shape for "our team's `text.star` helpers".

The workspace already has the right asset model to copy: the Rego policy
library (`policies` table, unique `(workspace_id, name)`, resolved and
snapshotted at instance create) and application drafts (CAS `Version` +
`PublishedVersion`). Modules combine the two.

## Goals

- Author, edit, publish, and version Starlark libraries in the console;
  no server-side files, no git, no operator flags.
- Attach modules to instances and applications through the spec, with the
  same create-time snapshot semantics as policies (EP-0012 lifecycle: the
  boundary is fixed at creation; live instances never hot-update).
- One load address space shared by instance exec and application programs.
- Library code inherits the consumer's capability boundary; module code
  never widens authority.
- Reproducible attachments: a stored spec names module + version + `h1:` sum.
- A module can depend on extensions; attaching it installs them, pinned to
  the exact extension revisions the module version was published against.

## Non-goals

- Module-to-module dependencies (mirrors "extensions cannot depend on other
  extensions"). The dependency graph is exactly one level deep:
  consumer → modules → extensions.
- Cascading base capabilities. A module (or its extensions) needing `net`
  does not grant `net`; the consumer must grant it, and creation fails
  naming the missing grant.
- Cross-workspace or public sharing, a marketplace, ratings, discovery feeds.
- Mutating the module set of a live instance or a prepared replica.
- `load()` of arbitrary VFS files from exec (natural follow-up, separate
  proposal).
- Turning user modules into gated capability ops — that is the `ext` surface.
- Non-`.star` files in module trees (assets, README files, data files),
  other than the root `module.yaml` manifest.

## Model

### Library, draft, versions

A workspace holds named modules. Each module is:

- a **draft** — the editable tree, updated under a CAS `draft_version`
  (the same optimistic-concurrency shape as application drafts);
- zero or more **versions** — immutable, monotonically increasing integers
  cut from the draft by an explicit publish action. A version never changes
  after publish; its content is identified by `h1:` tree sum.

Deleting a module does not affect already-created instances or prepared
replicas: they hold their own snapshots. Saved specs that name the module
fail closed on the next create (`unknown module`), the same failure shape as
a renamed or deleted policy.

### Spec surface and pinning

```json
{
  "modules": {
    "strkit": {"version": 3},
    "txt":    {"name": "strkit", "version": 4}
  }
}
```

`spec.modules` maps **mount alias → reference**. The alias is the name the
consumer's code mounts the library under (`@alias`); it must match
`[A-Za-z0-9][A-Za-z0-9_-]{0,63}`. `name` defaults to the alias. `version`
may be omitted, meaning "latest published" — resolution writes the concrete
version back into the normalized spec.

Resolution happens at the same point library policies are snapshotted
(`selectedPolicies`): the sandbox service resolves each `(name, version)`
against the *creating workspace's* library, verifies the tree, and records
`name`, `version`, and `sum` in the stored spec. `SpecPinner` (which today
pins ext sources to `h1:` sums) is the natural place to fill `sum`; unknown
names and versions fail creation. `nil` and `[]`/omitted preserve their
existing semantics — there are no default modules — and `[]` must survive
serialization through API, worker, and subprocess boundaries, as policies do.

Application boundaries (`ApplicationBoundary`) resolve and pin identically,
so `Test`, publish, and `Invoke` all run the pinned snapshot.

### The `@` mount namespace and `load()` resolution

Module trees mount into a reserved load namespace, disjoint from program
sources and (later) VFS paths:

- From **consumer code** (application `main.star`, exec chunks):
  - `load("@alias/util.star", "helper")` resolves inside the tree mounted
    at `alias`;
  - `load("helpers.star", "x")` keeps today's meaning — a bundle source
    (applications); it remains an error in instance exec, which has no
    consumer source namespace in v1.
- From **module files**: `load("util.star", "x")` resolves relative to the
  *module root*, exactly as `ext` helpers resolve relative to the extension
  root today. `load("@...")` inside a module file is rejected at publish
  validation and at runtime — modules cannot name their own mount or other
  modules' mounts, so a module file never depends on the consumer-chosen
  alias and stays portable across consumers and aliases.

The loader resolves the requesting file from the thread's call-stack
position (`thread.CallStack()` — the innermost frame during `load()`
evaluation is the requesting file's top level), so the same `thread.Load`
closure serves both namespaces without rewriting source text.

`@` becomes a reserved leading element in the program-source namespace:
release source/asset names starting with `@` are rejected going forward
(previously valid under `checkReleaseName`, undocumented and unused).

### Execution semantics

- **Compilation.** Module files compile against the session's predeclared
  name set — the same rule bundle sources already follow. For instances this
  happens at create; for programs at `PrepareProgram`. Compile errors are
  create/prepare failures, not exec-time surprises.
- **Initialization.** Each module file initializes at most once per session
  (instances) or per program invocation (replicas), lazily on first
  `load()` reaching it — the same cache shape as `initModules`. The
  initialized globals live in the resolver, not `Session.Globals`, so
  instance `Inspect` output is unchanged.
- **Gate state.** Module init inherits the caller's gate state: armed during
  an exec `load()`, disarmed during program pure-init — identical to the
  code that triggered the load. Capability calls at module top level
  therefore fail in programs and work in execs; guidance matches `ext`:
  do side-effecting work inside functions.
- **Limits and cancellation.** Module init and function calls consume the
  caller's step budget and honor exec cancellation; there is no separate
  budget.
- **Cycles.** A file currently initializing that is reached again fails
  `load cycle`, as the program loader does today. Publish-time validation
  detects intra-tree cycles statically.
- **Freezing and sharing.** One initialized module dict serves every load
  reaching it in that init pass, like `initModules`. Mutable values inside a
  module shared across execs persist for the session's life.

### What a module can see

A module file's predeclared environment is the consumer's: `json`, `math`,
`env`, `secrets` (names only), `print`, and whichever capabilities the
consumer granted (`net`, `fs`, `io`, `ext`, and `interrupt` on the program
path). The `ext` binding includes every effective alias — explicit and
cascaded. Nothing else is injected for the module's benefit.
`{{secrets.NAME}}` placeholder strings in module code are plain strings;
secret injection still happens only inside `net` at send time under the
secret's domain allowlist — the same, correct mechanism extension code
already uses.

## Extension dependencies and cascade

### Manifest

A module tree may contain one root file, `module.yaml`. It is part of the
tree, so the version's `h1:` sum covers it:

```yaml
description: String helpers with web enrichment
requires:
  capabilities: [net]          # base capabilities the module itself uses
  ext:
    tavily:                    # alias the module code calls: ext.tavily.*
      source: contrib/tavily
      sum: h1:...              # filled at publish; required in versions
```

- Decoding is strict (unknown fields rejected), like `capability.yaml`.
- `requires.ext` entries carry only `source` and `sum` — never config,
  secret names or references, env, or runtime limits. A module declares
  *which* extension its code calls; the consumer supplies all extension
  config and credentials (through `spec.capabilities.ext` and the
  consumer's `spec.environment` bindings). Validation: alias is a
  Starlark identifier, the source is a valid identifier, the sum is
  `h1:`-shaped when pinned; source loading and pinning happen at publish.
- `requires.capabilities` lists base capabilities (`net`, `fs`, `io`) the
  module or its extensions need. It is informational in the manifest and
  enforced at attach time: every listed capability and every required
  extension's `dependencies` must be granted by the consumer.
- No manifest means no requirements; pure helper modules stay manifest-free.

### Publish pins the extension revisions

The draft may name a local source without a `sum`. **Publish** runs the
manifest's ext entries through the same `SpecPinner`/`extensions.Repository`
path instance creation uses: it computes the `h1:` sum, captures the
immutable tree into `extension_artifacts`, and writes the pinned sum into the
version's `module.yaml` before hashing the tree. So:

- a published version names exact extension revisions; later edits to an
  operator's local root cannot change what the version runs (a stale
  revision fails attach with an integrity error rather than running new code);
- workers keep fetching captured trees through the existing
  `GET /internal/v1/ext/tree` callback — no new transport;
- remote sources still need explicit `@version` + `h1:` and must pass
  `--ext-allow-sources` at publish **and** again at attach, since operator
  allowlists can change between the two.

### Cascade at attach

During spec resolution (`validateSpec` for instances, `ApplicationBoundary`
for applications, before `NormalizeSpec`), each resolved module version's
`requires.ext` entries merge into `spec.capabilities.ext`:

1. **New alias** → installed with the module's `source` and `sum` only.
   All extension config and credentials come from the consumer — its
   explicit `capabilities.ext[alias].config` and its `spec.environment`
   bindings for that alias. The module itself carries none.
2. **Same alias, same `source` + `sum`** (another module, or an explicit
   consumer entry) → deduplicated into one ext binding.
3. **Same alias, different `source` or `sum`** → creation fails, naming the
   alias and both modules/the explicit entry. No silent winner, no renaming:
   module code calls `ext.<alias>` literally, so the alias is an interface.

There is no config cascade: a module can declare *that* it needs
`tavily`, never *how* it is credentialed. Required secret fields on the
extension's manifest are satisfied by the consumer's environment
bindings (see below); a missing binding fails creation naming the
module, alias, and field. Explicit legacy `ext.config` values keep
working for current clients; an explicit legacy config value and an
environment binding for the same field conflict and are rejected rather
than silently overridden.

After the merge, the ordinary pipeline runs unchanged: `PinSpec`,
`NormalizeSpec` (dependency check — e.g. `tavily` needs `net`), secret
resolution, policy selection. The engine and worker see a normal ext map;
**no engine or worker change is needed for the cascade**.

### Consumer environment

`spec.environment` is the consumer-owned configuration scope shared by
instances, applications and module debug runs — not a globally shared
mutable environment resource. Its shape:

```json
{
  "environment": {
    "extensions": {
      "tavily": {
        "bindings": {
          "default": {
            "secrets": {
              "api_key": {"ref": "TAVILY_PROD", "allowed_domains": ["api.tavily.com"]}
            }
          }
        }
      }
    }
  }
}
```

- Aliases map to `spec.capabilities.ext` aliases, field names to the
  pinned extension manifest's `secret` fields (validated at resolution).
- Bindings carry vault **references** only — inline values are rejected.
  Multiple binding names and multiple secret fields exist in the data
  model; v1 execution always selects `default`, never falling back.
- `allowed_domains` narrows the vault entry's own allowlist.
- Modules have no environment. A module's debug settings (environment +
  execution boundary) are stored per module separately from its files
  and never enter a version's hash.
- `spec.env` keeps its existing semantics inside the same logical
  consumer scope; capability/policy/limit boundaries stay in the spec.

### Provenance and round-trip

The stored spec records what the cascade added, so the console can show it
and Create similar can round-trip the draft:

```json
{
  "modules": {
    "strkit": {"name": "strkit", "version": 3, "sum": "h1:...",
               "ext": ["tavily"]}
  },
  "capabilities": {"ext": {"tavily": {"source": "contrib/tavily", "sum": "h1:..."}}}
}
```

`modules[mount].ext` lists only aliases that module introduced (not those the
consumer had already added explicitly). On re-normalization (e.g. Create
similar, key specs), cascaded aliases are recomputed from the pinned module
versions, not trusted from input, so a client cannot spoof provenance. In
the draft editor, removing a module removes the aliases only it introduced;
aliases also required by another attached module or added explicitly stay.

### Audit and policy

Extension calls made from module code are ordinary `ext.<alias>.<op>`
records followed by their nested base-capability records; existing policies
such as "deny `ext.tavily.*`" apply whether the call came from consumer code
or a module. Audit metadata does not record which module issued the call in
v1 (that would require stack attribution in the Gate; see open questions).

## Storage and migrations

Two tables, added with `make migrate-new` in both dialects:

- `modules` — `mod_` ids, `workspace_id`, `user_id`, `name`
  (unique per workspace, policy-name grammar), `description` (≤300 chars),
  `files` (JSON blob of `map[string][]byte`, as `extension_artifacts.files`),
  `draft_version` CAS counter, timestamps.
- `module_versions` — `module_id`, `workspace_id`, `version` int64,
  `sum`, `files` blob, `created_at`; unique `(module_id, version)`,
  immutable after insert.

Tree validation reuses `extartifact.ValidatePath`, `extartifact.Bounds`
(≤256 files, ≤1 MiB), and `extartifact.TreeHash`; additionally every file
name must end in `.star` (except the root `module.yaml`) and no path element
may start with `@`. Version
files are stored inline — module trees are small and DB-resident, so there
is no artifact-fetch path, unlike remote extension trees.

Publish runs under `LockWorkspace`: `version = max(existing) + 1` computed in
the transaction. Publish is a reviewed change (EP-0018): the request carries
the reviewed base sum and candidate sum, and the server rejects it if either
moved. A candidate identical to the latest version is a no-op, not a new
version. `module_versions` also records `published_by`, `note`, and
`base_version`.

New `ModuleStore` methods join the store interface, the GORM
implementation, and the `internal/store/storetest` conformance suite.

## API surface

Edit `api/openapi.yaml` first, then `make gen`; every generated operation
gets an access-table entry (`TestRouteCoverage`):

- `GET /api/v1/modules` — workspace list (id, name, description,
  draft_version, latest published version, updated_at).
- `POST /api/v1/modules` — create with initial files.
- `GET /api/v1/modules/{id}` — draft files plus the version list.
- `PATCH /api/v1/modules/{id}` — update draft files/name/description,
  CAS `draft_version` (`409` on stale, like applications).
- `DELETE /api/v1/modules/{id}` — drops draft and version history.
- `POST /api/v1/modules/{id}/publish/preview` — compute the pinned candidate,
  base, and semantic diff (EP-0018).
- `POST /api/v1/modules/{id}/publish` — cut a version bound to the reviewed
  `expected_base` + `candidate` sums, with an optional `note`; returns
  `{version, sum}`.
- `GET /api/v1/modules/{id}/versions[/compare]`,
  `POST /api/v1/modules/{id}/versions/{version}/restore` — history,
  comparison between any two versions, restore into draft (EP-0018).
- `GET /api/v1/modules/{id}/versions/{version}` — read one version's files
  (editor diff/base).
- `POST /api/v1/modules/validate` — publish-validity check without a write,
  mirroring `policies/validate`.

Publish validation: bounds and path rules, `.star` plus optional
`module.yaml`, strict manifest decoding, ext entry validation (valid
alias + source identifier, `h1:` sum — there is no module-side config to
validate), compile every file against the union predeclared set (stdlib +
`env` + `secrets` + every registered capability name), static intra-tree
load-cycle check, reject `@` loads. The same validation runs on `PATCH`
before a draft is stored — an invalid draft should not be savable — except
ext pinning and artifact capture, which happen only on publish preview and
publish.

`GET /api/v1/modules/{id}/versions/{version}` also returns the parsed
requirements (ext aliases with source/sum, base capabilities) so pickers
can show them without parsing YAML client-side.

## Worker transport and runtime contract

`runtime.CreateRequest` gains `Modules map[string]ModuleTree` where
`ModuleTree = {name, version, sum, files}` — sources cross the API→worker
boundary inline, exactly like `PolicyBundle` (never store ids). The field
flows for free through the signed remote protocol, the subproc stdin config,
and `PrepareRequest` (which embeds `CreateRequest`), so ephemeral serving
replicas and persistent instances get identical semantics. The plain-data
type lives in a leaf contract package both `internal/runtime` and
`internal/engine` can import (`internal/types` or a new `internal/module`).

On the node, `SessionCreation.Modules` carries the trees into `NewSession`,
which verifies each tree's `h1:` sum against the pinned value (fail closed
on mismatch), compiles every file, and builds the resolver. The Program
path receives the same trees through its `SessionCreation`; `@` load names
are checked against attached mounts inside the engine's static load-graph
pass — `validateProgramConfig` itself stays bundle-only, while
`checkLoadGraph` learns to dispatch on the `@` prefix using the attached
set, keeping missing mounts a prepare error.

Mixed-version rollouts: a worker without module support must not silently
create a module-less instance — the runtime contract addition rides the
worker protocol's existing contract typing, and deploy order is
workers-then-API; the release notes call this out.

## Authorization and trust model

Authorization stays exclusively in `service/access`: `View` to read a
module or version, `Configure` to create, update, publish, and delete;
listing follows the policy list rule (workspace `View`). Resolution at
instance/application creation uses the *workspace's* library, so a key-bound
or shared-execution actor needs no module read permission — matching how
policies resolve under the workspace today. Bound keys see nothing except
their bound resource, so module endpoints are invisible to them by
construction; a bound key's stored spec still works because resolution is
server-side.

A module grants no authority. It is code the consumer could have written
inline; it executes with the consumer's predeclared capabilities, Gate,
policy set, secret injection rules, step/time/output budgets, and audit.

The ext cascade is the one thing a module adds to the boundary, and its
limits are explicit:

- It installs extension code (new gated ops) pinned to `source` + `sum`.
  This matches what the consumer could have done by adding the extension
  themselves.
- It never carries config, credentials, or secret references — the
  consumer owns all of those. It never grants a base capability, never
  adds or widens `net.allow_hosts`/methods, never changes
  `spec.policies`, and never carries inline secret values.
- The review step and the instance's Boundary panel show every cascaded
  alias with its originating module. Nothing is installed that the human
  creating the instance cannot see before Create.
- Restricted (key) specs keep their vault-references-only rule.

Writing a manifest that declares an extension requirement is a
`Configure`-level act. The module grants no credentials — the consumer
still has to supply them — so the dependency declaration is reusable and
visible without widening what a publisher can do.

Because module source is rendered in the console to any workspace viewer,
publish is a `Configure` action and `POST /api/v1/modules` requires it too —
source control over shared library code, same bar as policy writes.

## Console

- **Workspace → Modules** page: list, create, and a detail editor — a file
  list plus the shared code editor (codemirror/editor.ts), add/rename/delete
  files (destructive file delete uses `Confirm`, F2), Save draft, and a
  **Review & publish** action that opens the shared EP-0018 review dialog
  (file diffs, manifest/requirements changes, cascaded-extension changes,
  note). Detail shows draft vs latest-published divergence
  (`has_unpublished_changes`, the application-status shape) and a History
  tab where any two versions can be compared or restored into the draft.
- **Module dependencies editor**: a Requirements panel edits `module.yaml`
  (alias, `source`, pinned `sum`, base-capability checkboxes) with a raw
  YAML mode that never silently drops fields. It never edits extension
  config or credentials — those belong to the consumer. Published versions
  show their pinned extension revisions.
- **Module debug**: a Debug panel on the module detail page runs the
  *current draft* (unpublished trees allowed) mounted as `@self` inside a
  scratch instance, against a user-editable debug script and per-module
  debug settings — a separate environment (extension bindings, vault
  references only) and execution boundary (capabilities, policies,
  limits) stored per module, never part of the module files, version, or
  hash. **Run** always starts a *fresh* scratch instance (a new session —
  source or settings changes always take effect on the next run, never
  mid-run); **Reset** clears results and tears the scratch instance
  down. Nothing persists between runs — there is no stateful debug REPL.
  Debugging requires `Configure` and uses the real Gate, policies, and
  audit — a module debug run is just an instance exec with `@self`
  attached.
- **Instance create** gains a Modules card under a Libraries heading:
  mount alias, module, and version (`latest` by default). Selecting a
  module immediately shows its cascaded extensions as read-only entries
  in Abilities, tagged with the module that brought them ("via strkit").
  Missing base grants (e.g. Network needed by `tavily`) block Create with a
  validation that names the item to fix. Network is never toggled on
  automatically. Alias conflicts are shown inline before submission.
  Removing a module removes only the extensions it alone introduced. The
  review step lists cascaded extensions and the environment bindings they
  need. The
  **application editor** gets the same attachment control and preview, and
  the draft spec round-trips modules and provenance.
- **Completion and help**: the editor completion source suggests
  `load("@alias/` mount paths from attached modules; `help("modules")`
  lists mounts and `help("@alias")` lists a mount's files — extending the
  EP-0012 progressive-disclosure surface. The instance prompt names attached
  mounts without inlining source.

## Alternatives considered

- **Extend `ext` with a `user/<name>` source scheme.** Rejected as the
  primary model: it forces every shared helper through the manifest +
  config + gated-op machinery, and per-op Gate records mislabel plain
  function calls as capability invocations. It remains a valid later step
  for *packaged* user capabilities; a module could even be promoted into an
  extension source in a follow-up.
- **Flatten module files into `ProgramBundle.Sources` on the API.**
  Rejected: it leaks mount layout into the bundle namespace, breaks
  module-root-relative loads, and does nothing for instance exec, which
  needs its own mechanism anyway.
- **Module-scoped ext bindings (cascade without merging).** Each module would
  see a private `ext` containing only its declared aliases, installed under
  hidden instance-level names. That isolates modules from each other's
  aliases and removes conflicts, but it changes audit op names
  (`ext.tavily.*` policies would no longer match), gives module files a
  different predeclared set from the consumer's, and hides installed code
  from the consumer's own `ext`. Rejected for v1 in favor of a visible,
  shared merge with explicit conflict errors.
- **Require the consumer to add module-required extensions by hand.**
  Simplest and fully explicit, but it pushes dependency knowledge onto every
  consumer and defeats the point of a reusable library. The cascade keeps
  the same explicitness by showing everything in review.
- **Mount by module name only, no alias.** Simpler (`spec.modules` becomes a
  `name@version` list) but loses the ability to mount two versions side by
  side or adapt a name collision — the map shape costs little and keeps
  the door open.

## Migration and delivery

1. Store: `modules` + `module_versions` migrations (both dialects), store
   interfaces, GORM impl, storetest conformance.
2. Service: `service/module` CRUD + publish + validate; `module.yaml`
   parsing/validation; publish-time ext pinning and artifact capture via
   `extensions.Repository`; access entries; OpenAPI + `make gen`.
3. Spec: `runtime.Spec.Modules` normalization and name/alias/version rules;
   `SpecPinner` resolution; `selectedModules` snapshot in
   `validateSpec`/`place` and `ApplicationBoundary`; ext dependency merge
   (source+sum dedupe/conflict rules, recomputed provenance — no config
   cascade) before `PinSpec`/`NormalizeSpec`.
4. Contract + engine: `ModuleTree` contract type; `CreateRequest.Modules`;
   `SessionCreation` tree handling, sum verification, compile-at-create;
   two-namespace `load()` resolver for `Exec` and `initModules`;
   `checkLoadGraph` `@` handling; reserve `@` in release names.
5. Console: module pages/editor, create-flow card, completion + `help()`.
   Publish review and history come from EP-0018 and are required before
   modules ship, so modules never had an unreviewed publish path.
6. Docs: extensions.md sibling doc, README pointer, prompt surface.

Backward compatibility: specs without `modules` are unchanged; `@` load
paths in old code were never valid load targets (nothing satisfied them),
so the new interpretation breaks no working program.

## Acceptance and verification

- Publish → attach → `load("@a/f.star")` from an instance exec and from an
  application program; editing the draft after create does not move the
  running instance; publishing a new version does not either.
- Modules see only granted capabilities: `net` calls inside a module obey
  the instance's `allow_hosts`, policies deny module-initiated calls the
  same as inline calls, and gate audit attributes them to the exec/program.
- Integrity: a version's stored bytes always hash to its `sum`; a tampered
  `module_versions` row fails session create.
- Semantics: module-init once per session/invocation, cycle detection,
  module-root-relative loads, `@` rejection inside module files, collision
  between two aliases of one module, name vs alias defaults.
- Cascade: attaching a module that requires `tavily` installs the
  extension pinned to the module's `source` + `sum`; the consumer
  supplies all config and credentials, and module code calling
  `ext.tavily.search` is audited as `ext.tavily.search` with nested `net`
  records. Without a `net` grant, creation fails with a message naming
  `net`; `allow_hosts` is never modified. Cases covered: same-alias/
  same-sum dedupe across two modules and with an explicit entry; same
  alias with different source/sum rejected; missing consumer binding for
  a required secret field rejected; spoofed `modules[*].ext` provenance
  recomputed; removing a module removes only its aliases.
- Debug: an unpublished draft runs under `@self` with the stored debug
  environment and boundary; debug state never enters the published
  tree, version, or hash.
- Pinning: publish captures the ext artifact; editing the local extension
  root afterward does not change what an attached version runs (attach
  fails on a stale revision instead); a remote source removed from
  `--ext-allow-sources` after publish fails at attach.
- Store: dual-dialect migration checksums, `storetest` conformance,
  workspace uniqueness, CAS conflicts, publish counter isolation per module.
- Console: draft edit/publish/version browse, create-flow attachment, spec
  round-trip, `interactionStability` rules (Confirm/SwapLabel/held order).
- No file contents, spec text, or secret values in logs, audit metadata, or
  hook inputs — module names, versions, and sums only.

## Open questions

- Should exec `load()` also read `.star` files from the instance VFS when
  `fs` is granted? Natural next step; it needs a second namespace decision.
- Compile caching across instances keyed by `sum` — worthwhile once module
  use is heavy; per-session compilation is the simple correct start.
- Rename semantics: policies allow rename, which breaks saved specs naming
  the old name; mirror that, or make names immutable post-publish?
- Retention: module versions are small but unbounded; is a per-module
  version cap or a retention sweep wanted, and does delete cascade?
- Whether `help("@alias")` should show doc comments — needs a defined
  docstring convention for module files.
- Should audit records note which module issued an ext/base call (via
  call-stack attribution in the Gate)? Useful for debugging; it must stay
  within the "metadata is JSON-able, no contents" rule.
- Should the cascade also surface *suggested* `net.allow_hosts` (from the
  extension's documented domains) as a one-click, explicitly confirmed add in
  the create flow? It would never be automatic.
