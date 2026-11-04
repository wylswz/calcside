# EP-0020: Versioned extension identifiers and immutable version bindings

- **Status:** Draft
- **Created:** 2026-11-04
- **Area:** extension identity and resolution, store (artifact and version bindings), catalog API, console extension selection and editing
- **Related:** [EP-0003](0003-community-extensions.md), [EP-0004](0004-extension-discovery-ui.md), [EP-0013](0013-llm-providers.md), [EP-0017](0017-workspace-modules.md), [EP-0018](0018-reviewable-publish.md), [docs/extensions.md](../extensions.md), [docs/interaction-stability.md](../interaction-stability.md)

## Summary

An extension is identified today by a source string (`root/name` for local
roots, `domain/group/name@ref` for remote). Captured artifacts are stored
content-addressed by `(source, sum)`, so a source may hold several sums over
time. Existing consumers that pin `sum` already get a reproducible tree, but
neither the identifier nor the catalog has a version-qualified way to
distinguish one revision from another. This proposal makes versions
first-class:

1. An **extension family** is the unversioned name (`namespace/name` or
   `domain/group/name`). The **canonical source** is `family@version`
   (e.g. `contrib/tavily@0.1.0`) and remains the JSON field `source`.
2. Every persisted, normalized extension reference carries both `source`
   and `sum`; the server binds `(family, version) -> sum` permanently and
   immutably, installation-wide.
3. The catalog lists one record per captured version, so consumers pick a
   version explicitly.
4. The console selects among versions, shows the exact `source` + `sum`
   being added, and requires an explicit change-version action with
   review for existing consumers.

Different content for the same version is rejected; the version binding is
a promise that `family@version` means one tree forever. This is not
automatic semantic versioning and nothing derives ordering from version
names.

## Motivation and current state

- `Repository.Capture` persists an archive under `(source, sum)`; repeat
  captures of an edited tree add sums for the same source. Consumers pin
  `sum`, but catalog entries and the UI only know `source`, so they cannot
  name or compare revisions.
- The local identifier parser accepts only `root/name`; remote requires
  `domain/group/name@ref` where the ref is tag-like or a full SHA.
  `manifest.version` is free-form display metadata the loader never
  compares to the identifier.
- `ExtensionPicker` tracks selected state by `entry.source`, the instance
  extension editor finds the catalog row by `source`, and `Providers`
  reuses the same catalog. None of them can express "version 2 of tavily"
  distinct from "version 1".
- Workers resolve extensions from API-owned immutable artifacts, so the
  runtime substrate is already content-addressed; only the naming layer
  is missing.

## Goals

- One canonical, version-qualified identifier per published revision.
- Immutable `(family, version) -> sum` bindings, enforced transactionally.
- All captured versions remain resolvable while their family stays
  permitted, regardless of later edits to local roots.
- Catalog and console expose versions distinctly; multi-version selection
  is explicit, never "latest".
- Backward compatibility for already-pinned `source + sum` pairs.

## Non-goals

- Semver ranges, `latest`, upgrade suggestions, or any ordering semantics
  derived from version strings.
- Remote tag discovery or a marketplace; remote versions are admitted only
  by explicit reference.
- Runtime hot swap of extensions inside a live instance.
- Per-workspace extension publishers or new sharing/tenancy rules.
- Changes to Gate, allowlist, hook, or audit rules.
- New management APIs for publishing/unpublishing/deleting versions.

## Design

### Identifiers and version grammar

- **Family** = the existing unversioned identifier: `namespace/name` for
  local roots, `domain/group/name` for remote. **Canonical source** =
  `family + "@" + version` (e.g. `contrib/tavily@0.1.0`).
- The JSON field stays `source`; no separate `spec` version field and no
  rename to `identifier`. Aliases (`ext.tavily`, etc.) are unchanged.
- Parsing splits on the version separator *before* classifying the family,
  so a local `contrib/tavily@0.1.0` is never misread as remote.
- Local version tokens come from a **required, validated**
  `manifest.version`. Grammar: `^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`,
  plus rejection of `..` anywhere in the token, path separators,
  whitespace, and empty. Matching is exact and case-sensitive; there is
  no silent `v`-prefix normalization or semver interpretation.
- For an ordinary newly captured local candidate, the identifier version
  suffix must equal the `manifest.version` inside the archived tree. The
  only exception is the operator-registered legacy mapping documented
  under *Compatibility and migration*.
- Remote identifiers keep the current ref support (tag-like or 40-hex
  SHA); the identifier version **is** the ref. `manifest.version` stays
  package display metadata and is never a second resolver input -- a
  remote manifest's display value cannot override the identity given by
  `domain/group/name@ref`.
- No range or symbolic resolution: an identifier without an explicit
  version is not admitted by new code paths.

### Immutable version bindings

- A new binding maps `(family, version)` to exactly one `sum`, scoped to
  the server installation (not per workspace); existing visibility and
  allowlist policy still apply.
- Capturing the same `(family, version)` with the same `sum` is
  idempotent. A different `sum` for an existing binding is a conflict:
  no overwrite, and deletion or unlisting can never remap it.
- The conflict guarantee is enforced by the store -- a `UNIQUE`/`PRIMARY
  KEY` on `(family, version)` with insert-then-compare -- not by a
  process-level lock or check-then-write; concurrent captures produce one
  winner.
- The same `sum` under different versions is permitted (a manifest-only
  edit changes the tree hash anyway; deduplication is harmless).
- Existing `h1:` `TreeHash` semantics are unchanged: same file selection,
  ordering, limits, manifest, and packaged icon inclusion.
- Artifact bytes and the version binding are persisted in one transaction,
  so a failed or conflicting capture is never advertised. Resolution fails
  closed: a tree that no longer matches its binding is never served.

### Catalog and operator workflow

- The `<root>/<extension>/capability.yaml` layout stays; it provides the
  *current candidate* only. On catalog refresh/admission the server
  captures the candidate under its `manifest.version`, stores the archive,
  and binds `family@version -> sum`.
- All previously captured versions remain available from the database
  after the current source changes or disappears, provided the family
  stays configured. Multi-version needs **no** nested directory format.
- Changing configured roots never deletes bindings. A revoked or
  unconfigured family hides its catalog entries and denies resolution even
  on cache hit; removing the current directory does not unpublish captured
  versions.
- A candidate edited under an already-bound version is rejected with a
  visible diagnostic. The catalog still shows the archived original for
  that version and never advertises the changed candidate.
- Catalog admission is authoritative on the API `Repository` -- workers
  never mutate bindings, and Starlark is never executed merely to inspect
  a candidate.
- Remote versions become catalog entries only after an explicit,
  successful reference/capture; there is no scan of remote tags.
- Only captured/admitted entries a caller is permitted to see appear in
  the catalog; no new tenant sharing is introduced.

### Storage

- `extension_artifacts` keeps its immutable `(source, sum) -> bytes`
  contract for workers and legacy consumers.
- New table `extension_versions`: `PRIMARY KEY (family, version)`, `sum`,
  `artifact_source`, `created_at`; each row points at an existing
  `(artifact_source, sum)` artifact. New captures store the canonical
  source as `artifact_source`; migrated historical rows may retain the
  original source string.
- Delivered via the store interface, the GORM implementation, the
  conformance suite, and new Atlas migrations for both dialects; applied
  migrations and history are untouched.
- Catalog metadata (ops, config schema, models, icon) is derived from the
  pinned captured tree. Version bindings never cascade-delete with any
  consumer.
- On resolution the API re-checks the expected sum against the binding
  even on artifact hits.

### API and catalog shape

- The catalog stays a flat `extensions[]` with one record per version.
  Each record requires `source` (canonical `family@version`), `family`,
  `version`, and `sum`, plus the existing version-specific metadata
  (`config`, `ops`, `models`, `icon_url`). `source == family + "@" +
  version`; for remote entries `version` is the ref, not manifest display
  text. No `package_version` field: manifest display metadata is not used
  for resolution, and `catalog.version` is the identifier version.
- `icon_url` resolves from the captured tree per version; a live source
  icon never drifts into an old version's entry.
- No new endpoints are required; spec changes go through
  `api/openapi.yaml` then `make gen`.
- Error surface: version-remap attempts are a stable `conflict` kind (409
  where the API supports it); malformed identifiers/versions are
  `invalid_argument`; integrity mismatches are bad input; permitted-source
  checks run before artifact lookup. No new error enums are invented.

### Console

- One card per family with an explicit version picker built on the shared
  `ui.tsx` primitives. Versions are shown distinctly alongside the
  selected `source` + `sum`.
- A single-version family may preselect it; a multi-version family
  requires an explicit choice -- no latest/default auto-selection.
- *Add* writes the selected `source` and `sum` together. Different
  versions of one family may be added under different aliases;
  `selectedSources` is keyed by the full canonical source, not the family,
  and exact duplicates keep the current Added/show-settings behavior.
- Two versions of one family can coexist under distinct aliases in
  `capabilities.ext` (sums shown here are illustrative placeholders, not
  valid sums):

  ```json
  {
    "capabilities": {
      "ext": {
        "tavily": { "source": "contrib/tavily@0.1.0", "sum": "h1:<tree-sum-v1>" },
        "tavily_v2": { "source": "contrib/tavily@0.2.0", "sum": "h1:<tree-sum-v2>" }
      }
    }
  }
  ```

- The config editor gets an explicit *change version* action with review
  before Apply: the alias stays, and the delta in ops, config fields,
  dependencies, and models is shown. Config and env secret references are
  preserved where they still apply but are never silently dropped,
  remapped, or granted; incompatible entries block Apply until the user
  resolves them. Cancelling or a failed apply preserves the original
  draft, including invalid JSON.
- Live instances are immutable for this feature: changes apply only to
  drafts, new instances, and publish boundaries. Polling or refetch never
  auto-updates a selection; an unavailable version renders as unavailable
  instead of falling back.
- Applies to the SpecForm instance and application surfaces, module
  dependency/debug/publish surfaces, and LLM provider extension selection
  including nested registries.
- On implementation, `docs/interaction-stability.md` status is updated
  and tests cite the relevant invariants -- S1 (paint-only states),
  S2 (menus do not chase triggers), S4 (`SwapLabel`), S5 (held order),
  C1-C3 (context/draft preservation), P2/P5 (shared primitives,
  predictable apply), F2/F3 (confirmation and no silent destructive
  drops). This proposal changes no status.

### Compatibility and migration

- Existing pinned `source + sum` pairs keep resolving at runtime; there is
  no hard break. New writes always emit canonical `family@version`
  identifiers.
- A legacy local `source + sum` pin migrates by reading the manifest from
  the **exact stored artifact** -- never the mutable current root -- and
  binding its version.
- A legacy unversioned reference without a sum is resolved against the
  current manifest only at an explicit write or migration step and is
  stored as canonical `source + sum`; unversioned references are never
  newly persisted.
- Remote refs are unchanged; previously pinned remote artifacts are bound
  under their ref as the version.
- Backfill never rewrites frozen published releases or audit history; a
  compatibility lookup adapts their exact old `(source, sum)` pairs.
- If multiple old sums claim the same local manifest version, the
  migration does **not** invent versions or pick a winner: a dry-run audit
  reports the affected identifiers and sums, and strict admission stays
  blocked until the operator supplies an explicit per-artifact version
  mapping. Such operator-chosen version IDs still reference the unchanged
  archived bytes -- a documented, registration-scoped exception to local
  manifest equality -- rather than any hash or manifest rewrite.
- Artifact bytes are never altered, snapshots never discarded, databases
  never reset. The staged backfill produces a compatibility map so old
  executions keep working while strict activation is gated, and dependent
  saved references reconcile atomically or through exact legacy lookup
  mappings.

## Acceptance criteria

- Parser: local vs remote distinction after version split; version-token
  safety rejections (`..` anywhere in the token, e.g. `1..2`; separators,
  whitespace, empty).
- Two local versions captured across a root edit are both executable under
  different aliases.
- Recapture with the same sum is idempotent; altered code, manifest, or
  icon under an existing version is rejected.
- Concurrent captures of one `(family, version)` yield a single winner
  across two repository handles/stores.
- Expected-sum mismatch on capture/resolution is denied.
- An archived version resolves after its source tree disappears, but is
  denied after root removal or allowlist revocation.
- Catalog config, ops, models, and icons are exact per version.
- Remote retag: a cached pinned execution keeps using the original
  archive without any refetch; admitting different bytes (a different
  sum) for the same ref is a binding conflict; a fresh fetch that
  produces a sum differing from the expected one mismatches rather than
  rebinds.
- Legacy pinned backfill is exact; ambiguous multi-sum cases produce the
  audit report and gate without rewriting history.
- UI: version picker Add writes `source` + `sum`; per-version Added
  state; explicit switch with review, cancel, and failed-apply draft
  preservation; incompatible config blocks Apply without silent loss or
  grants; absent catalog version shows unavailable with no fallback;
  geometry, focus, and held-order checks at desktop and mobile.
- Integration: LLM provider selection and module dependency/publish
  surfaces carry versioned pins end to end.

## Implementation sequence

1. Identifier/version grammar and validation in the extension contract and
   parsers (local and remote).
2. Store `extension_versions` table, bindings API, GORM implementation,
   conformance coverage, and dual-dialect Atlas migrations.
3. Repository capture/binding transaction and resolution checks; catalog
   admission and diagnostics.
4. `api/openapi.yaml` catalog shape (`family`, `version`, `sum`), then
   `make gen`.
5. Console pickers and editors (SpecForm, modules, LLM providers) with
   change-version review.
6. Staged backfill: dry-run audit, explicit mapping input, strict-admission
   gate.
7. Acceptance tests above; interaction-stability status update.
