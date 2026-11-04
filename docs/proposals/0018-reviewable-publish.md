# EP-0018: Reviewable publish for applications and modules

- **Status:** Draft
- **Created:** 2026-11-03
- **Area:** application and module publish services, store (release history), API, console, Python SDK
- **Related:** [EP-0014](0014-persistent-instances.md), [EP-0016](0016-application-interrupts.md), [EP-0017](0017-workspace-modules.md), [docs/interaction-stability.md](../interaction-stability.md)

## Summary

Publishing is the moment a draft becomes what other people and systems run:
an application's served release, or a module version that future instances
and applications attach. Today an application publishes with one click and
no view of what changes; EP-0017 modules would inherit the same gap.

This proposal makes every publish a **reviewed change from a base to a
candidate**:

1. *Preview* computes the exact candidate the server would publish — fully
   resolved and pinned — and returns it alongside the current base with a
   structured diff.
2. The human reviews source diffs and boundary changes in the console.
3. *Publish* is bound to what was reviewed: the request carries the base and
   candidate digests, and the server rejects it (`409`) if either moved.
4. Every publish is recorded in a per-resource history with publisher, time,
   and an optional note, so any two published versions can be compared later.

Review is done by the publisher. Calcside does not build an approval
workflow. Teams that need heavier controls (required reviewers, branch
protection, staged promotion) get them by driving publish from their own
CI/CD, such as GitHub Actions, through the public API (see *External CI/CD*).

## Motivation and current state

- `POST /api/v1/applications/{id}/publish` takes only the draft `version`.
  The console's Publish button calls it directly; the only signal is the
  `has_unpublished_changes` badge.
- The published state is `persistent_instances.release_id` → a
  content-addressed `serving_releases` row (`spec`, `policies` snapshot,
  `program`). That is enough to read the current base, but `serving_releases`
  has no application link and nothing records earlier releases, so there is
  no history to compare against.
- **The draft is not the whole candidate.** `release()` normalizes the spec,
  pins extension sums, and snapshots the workspace's library policy text at
  publish time. With EP-0017 it also resolves module versions and cascaded
  extensions. The same draft can therefore publish different behavior on
  different days. Reviewing only `code` would miss these changes.
- The no-op shortcut (`PublishedVersion == version` → return) means drift
  in policies or extension revisions cannot be published without a
  meaningless draft edit, and is never shown.

## Goals

- No publish without a server-computed preview of the exact candidate.
- Diffs cover everything that changes runtime behavior: sources, contracts,
  boundary (capabilities, hosts, extensions, modules, policies, secret
  names, env, limits), resolved pins, and policy text.
- Publish cannot apply something other than what was reviewed (TOCTOU-safe).
- Durable publish history with comparisons between any two versions.
- One mechanism and one UI pattern for applications and modules.

## Non-goals

- Built-in multi-person approval, required reviewers, review comments, or
  workspace publish-gating settings. These belong to the user's own DevOps
  tooling, connected through the API.
- Branches, merges, or concurrent drafts per resource.
- Automatic breaking-change detection for JSON Schema contracts beyond
  flagging that a contract changed.
- Rollback as a separate primitive. Restoring an old version means copying
  it into the draft, then doing an ordinary reviewed publish.

## Model

### Base, candidate, digest

| | Application | Module (EP-0017) |
|---|---|---|
| Base | current published release (or none) | latest published version (or none) |
| Candidate | release built from draft `version` by `release()` — normalized spec, pinned ext/module sums, policy snapshot, program bundle | draft tree after manifest ext pinning |
| Candidate digest | the `ServingRelease` id (already content-addressed by `releaseFrame`) | the tree `h1:` sum |
| Base digest | base release id, `""` if never published | base version's sum, `""` if none |

Applications already have a content-addressed release identity, so no new
hashing is needed. The digest covers spec, policy text, and program, which
makes policy and pin drift visible as a changed candidate even when the draft
version is unchanged.

Building the candidate must not decrypt secrets. Preview runs the boundary
and pinning path with secret **existence** checks only (vault refs resolve to
descriptors, never plaintext). Plaintext is resolved only inside the real
publish, as today.

### Preview

`preview` is a non-mutating computation (`POST` because it is expensive and
resolves state). It returns:

- `base`: digest, version/sequence, published_at, published_by, or `null`;
- `candidate`: digest, draft version, and the resolved artifacts needed to
  render the diff (source files, schemas, normalized spec, policy texts,
  resolved module/ext pins);
- `changes`: a server-computed **semantic summary**, described below;
- `identical`: true when candidate digest == base digest.

The extension artifact capture that pinning performs is content-addressed and
idempotent, so a preview leaves no observable state beyond cached artifacts.

### Semantic changes

The server computes structured changes, because it is the authority on
normalization. Clients must not diff raw JSON and guess. The summary lists
entries such as:

- **Sources**: per file added / removed / modified (renames appear as
  remove + add in v1). Line diffs are rendered client-side from the two
  file versions.
- **Contract**: input schema changed, output schema changed (flag + JSON
  views of both sides).
- **Boundary — grants**: capability added/removed; `net.allow_hosts` and
  methods added/removed; `fs` settings; ext alias added/removed or
  source/sum/config-key changes; module attached/detached or version change
  (with the nested module diff available, below); cascaded extension
  changes with the module that caused them.
- **Boundary — rules**: policies added/removed, and **policy text changed**
  for a policy present on both sides (snapshot drift), with a text diff.
- **Inputs**: secret names added/removed and allowed-domain changes (never
  values); env keys added/removed/changed (env values are non-secret spec
  data already visible to viewers); environment bindings — extension
  aliases, binding names, secret-field mappings and effective domain
  allowlists added/removed/changed, always shown as references, never
  values; limits and TTL changes.
- **Module-specific**: manifest `requires` changes (ext alias, source,
  sum, base capabilities). Module debug settings — the per-module debug
  script, environment and execution boundary — are not publish artifacts:
  they are never diffed, reviewed, or hashed into a version.

Grant widening is marked distinctly from narrowing. For example, a new host,
a new extension, or a new capability is shown as widening, while a removed
host or an added policy is shown as narrowing. The console uses the existing
EP-0012 vocabulary: abilities grant access, rules constrain it.

**Nested module diffs.** When an application's attached module moves from
v3 to v4, the change entry links to the module's own v3→v4 comparison
(files + manifest). The reviewer sees the library code they are about to
start running without leaving the review.

### Publish binding

Publish requests gain three fields:

```json
{
  "version": 12,
  "expected_base": "rel_…",       // "" when publishing for the first time
  "candidate": "rel_…",           // digest returned by preview
  "note": "Use strkit v4 for slug handling"
}
```

The server re-runs candidate construction inside publish, then:

- draft `version` CAS fails → `409 application changed` (as today);
- current base ≠ `expected_base` → `409 published version changed since
  review` (someone else published);
- rebuilt candidate ≠ `candidate` → `409 candidate changed since review`
  (policy edited, module published, ext revision changed);
- candidate == base → no-op success, returning the existing pointer. This
  **replaces** the `PublishedVersion == version` shortcut, so drift-only
  publishes become possible and reviewable.

Module publish uses the same fields (`expected_base` = base sum, `candidate`
= candidate sum), plus EP-0017's `draft_version` CAS. The base check and the
version insert run in one transaction under `LockWorkspace`.

The fields are **required**. Calcside has not been released yet (see the
migration consolidation), so this is an intentional breaking API change, not
an optional mode. Programmatic and agent publishers go through the same
two-step flow (see SDK below).

## History

### Applications

New table `application_releases`: `application_id`, `workspace_id`,
`seq` (int64, per-application counter from 1), `release_id`,
`draft_version`, `published_by`, `note` (≤500 chars), `published_at`;
unique `(application_id, seq)`. `PublishApplication` inserts the row in the
same transaction that moves the persistent-instance pointer. The
`serving_releases` rows it references are retained for as long as history
references them.

Existing published applications get no back-filled row (there is nothing
to record). The first reviewed publish shows the current release as the base
with `seq` unknown, and creates seq 1.

### Modules

EP-0017's `module_versions` gains `published_by`, `note`, and `base_version`
(the version the review compared against, nullable).

### Comparisons

- `GET /api/v1/applications/{id}/releases` / `…/releases/{seq}`.
- `GET /api/v1/applications/{id}/releases/compare?base=&head=` returns the same
  shape as preview (`base`, `candidate`→`head`, `changes`) for any two
  sequences. `head=draft` is an alias for preview without the binding digest.
- Modules: `GET /api/v1/modules/{id}/versions` (with publisher/note) and
  `…/versions/compare?base=&head=` with the same shape.

**Restore into draft** copies a historical version's code/spec/schemas (or
module tree) into the draft under the draft CAS. Publishing it afterwards is
an ordinary reviewed publish that shows the diff against the current base.

## API surface

Edit `api/openapi.yaml`, then run `make gen`. Every new operation gets an
access-table entry:

| Operation | Access |
|---|---|
| `POST /applications/{id}/publish/preview` | `Configure` (builds a candidate, same bar as publish) |
| `POST /applications/{id}/publish` (new required fields) | `Configure` |
| `GET /applications/{id}/releases`, `…/{seq}`, `…/compare` | `View` |
| `POST /applications/{id}/releases/{seq}/restore` | `Configure` |
| `POST /modules/{id}/publish/preview` | `Configure` |
| `POST /modules/{id}/publish` (new required fields) | `Configure` |
| `GET /modules/{id}/versions`, `…/compare` | `View` |
| `POST /modules/{id}/versions/{version}/restore` | `Configure` |

Application-bound keys can read their own application's history (they can
already see the application). They cannot preview or publish unless their key
ceiling includes `Configure`, as today.

Responses carry source and policy text to viewers who can already read
them. They never carry secret values, resolved plaintext, or host paths
(extension sources appear as identifiers such as `contrib/tavily`). Payloads
are bounded by the existing tree/code/spec limits on each side.

## Console

- **Publish becomes "Review & publish".** The header button keeps its box
  (S4 `SwapLabel`, existing unpublished-changes badge). It opens the shared
  `ui.tsx` dialog primitive with the review:
  - a summary strip that counts changes by category, with widening entries
    listed first;
  - a **Changes** list (the semantic summary). Each entry expands inline;
  - a **Files** view: file list plus a unified diff per file, rendered with
    `@codemirror/merge` on the existing CodeMirror stack (new dependency,
    pinned to a release at least 7 days old);
  - nested module diffs that open in place, with a back affordance (C2,
    no context loss);
  - an optional note field and a single **Publish** action
    (`SwapLabel`: Publish / Publishing…).
- **Identical candidate**: the dialog says "No changes to publish" and
  Publish is disabled. **Drift only** (same draft, different candidate): a
  notice explains that the change comes from workspace policies, module
  versions, or extension revisions, not from the draft.
- **409 after review**: the dialog keeps the note, explains which side moved,
  and offers **Refresh review**. It never re-publishes automatically (P2:
  closing or refreshing with a typed note asks before discarding it).
- **History tab** on application and module detail: the list keeps its order
  while polling (S5 held order) and shows publisher, time, note, and digest.
  Selecting two entries opens the same review view read-only. "Restore into
  draft" uses `Confirm` when it would overwrite unsaved or diverged draft
  changes (F2).
- Coverage and invariant IDs are cited in tests, and the
  `interaction-stability.md` status table is updated.

## SDK and agents

The Python SDK replaces `publish(app_id, version)` with:

```python
review = client.preview_publish(app_id, version)   # base, candidate, changes
client.publish(app_id, version, base=review.base_digest,
               candidate=review.candidate_digest, note="...")
```

`review.changes` is a plain structured object an agent can inspect, for
example to refuse widening without a human. There is deliberately no
single-call "publish whatever is there" helper. The MCP surface, if it
exposes publishing, follows the same two calls.

## External CI/CD (future direction)

Heavier DevOps is out of scope here. This proposal only keeps the door
open so that a later integration needs no redesign:

- Preview, compare, and publish are plain API operations with stable JSON,
  not console-only flows.
- The digest binding supports a "review in PR, publish on merge" flow. A CI
  job writes the draft from the repository, calls preview, and posts the
  semantic summary and diff to the pull request. After merge, it publishes
  with the recorded `expected_base` + `candidate`. If anything changed in
  Calcside in the meantime, publish fails with `409` instead of shipping
  something unreviewed.
- History rows carry `published_by` and `note`, so a CI identity and a
  commit SHA/PR link can be recorded without schema changes.

A later proposal will cover the integration itself: a CI-scoped
credential (for example an application-bound key with a `Configure`
ceiling), a source-from-git or upload-draft endpoint, and a GitHub Action.

## Delivery

1. Store: `application_releases` table (both dialects) and the
   `module_versions` columns, store interfaces, GORM impl, storetest
   conformance (seq counter, atomic pointer + history insert).
2. Service: secret-free candidate construction; semantic change computation
   (pure Go with fixtures per category); preview/compare/restore; publish
   binding and the new no-op rule.
3. API + `make gen`; SDK update and tests (`make sdk-test`).
4. Console review dialog, history tab, merge-view dependency; browser and
   visual tests.
5. EP-0017 modules adopt the same endpoints from day one.

## Acceptance and verification

- The candidate digest from preview equals the release id publish creates,
  for unchanged inputs.
- Each `409` reason is produced deterministically: draft edit, a concurrent
  publish, a library policy edit, a module publish, and a local extension
  edit between preview and publish.
- Drift-only publish works: same draft version, edited policy → preview
  shows the policy text diff, publish creates a new release and history row.
- An identical candidate is a no-op and creates no history row.
- The semantic summary covers each category, including widening and
  narrowing classification and cascaded-extension attribution. Secret values
  never appear (seeded sentinel secret is checked in every response and log).
- History: seq increments atomically per resource. Compare works across any
  two entries and against the draft. Restore respects the draft CAS.
- Console: review renders, nested module diff, refresh after 409, note
  preserved, history held order, invariants S4/S5/C2/P2/F2 tested; mobile
  layout with no page-level horizontal scroll.

## Open questions

- Should `has_unpublished_changes` reflect drift? That would require
  building candidates on list views. The likely answer is a periodic
  background check or "check on open", not per-list computation.
- Schema compatibility hints, for example a new required input field or a
  removed output field. These would be useful, but could give false
  confidence.
- Retention for `serving_releases` and history rows. Should history be
  unbounded, or capped per application?
- Should module publish show which applications pin older versions
  ("update available"), and should application review offer a one-click
  bump to the latest module version (still reviewed)?
