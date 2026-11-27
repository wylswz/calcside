# EP-0026: Releasing notebook applications

- **Status:** Draft
- **Created:** 2026-11-27
- **Area:** application notebook, publish service, run verification, release history, console, SDK/MCP
- **Related:** [EP-0018](0018-reviewable-publish.md), [EP-0025](0025-application-notebooks.md), [docs/console-user-stories.md](../console-user-stories.md), [docs/interaction-stability.md](../interaction-stability.md)

## Summary

This proposal defines the product interaction for answering: **how does a notebook become the live application?**

The unit being published is an **application notebook draft**: ordinary sections, one public `main(input)`, optional interrupt handler functions, schemas, execution boundary, and resolved dependencies. Publishing creates an immutable release and moves the application's live pointer to it. The release interaction is not a bare button; it is a short production-readiness flow:

1. **Check** the draft: graph validity, entry functions, schemas and boundary completeness.
2. **Verify** it in a fresh execution session, because application invokes never inherit playground or editor state.
3. **Review** the exact resolved candidate against the currently live release using EP-0018's digest-bound review.
4. **Publish** atomically: the immutable release exists first, then the live pointer moves.
5. **Confirm** the live release is ready and callable, then guide the user to one real invoke.

EP-0018 defines the underlying preview/digest/history mechanism. This proposal
defines the user-facing release contract, readiness checks, console flow,
verification semantics, and the boundary between what calcside does in v1 and
what belongs to external CI/CD or future environments. It intentionally refines
EP-0018's rollback non-goal: the cross-platform research shows that incident
rollback is a first-class release interaction, so this proposal adds a
conservative live-pointer rollback while keeping restore-into-draft for repair
workflows.

## Research synthesis

The recurring pattern across low-code and high-code platforms is a deliberate transition from mutable draft traffic to immutable live behavior. The details differ, but successful products make four things explicit: what is draft, what is live, whether the candidate actually runs, and how to recover.

| Source | Relevant interaction | Product lesson for calcside |
|---|---|---|
| n8n save/publish docs | Autosaved edits stay draft; publishing activates production triggers asynchronously; publish can be `published`, `partial`, or `failed`, and unchanged triggers keep running | “Publish clicked” is not the same as “live and ready”. The UI needs a post-publish activation/readiness state and must preserve partial-failure evidence. |
| n8n history docs | Versions are separate from executions; users can preview, restore, clone, download, and name versions | Release history and run history answer different questions. Do not mix “what code was live” with “what happened when it ran”. |
| Dify version control and CLI docs | Draft vs latest version; publish creates a new live version; restore copies an old version into draft; CLI has check/publish/version/restore operations | Restore-into-draft is safer and easier to reason about than silently rewriting live code. Programmatic release operations should mirror console operations. |
| Retool releases/publishing docs | Current working version is separate from numbered releases; publish can run readiness checks and tag major/minor/patch releases | A publish checklist is a natural place to surface missing configuration, conflicting changes, and unverified behavior before release. |
| Appsmith Git environments docs | Git branches and separate instances represent staging/prod; datasource configuration is environment-specific | Full environment promotion is a larger system. Calcside should not pretend one release pointer is a complete environments product. |
| Vercel preview/production promotion docs | Preview deployments can be inspected and tested, then promoted without rebuilding; generated URLs remain available | Once a candidate is verified, promotion should not silently rebuild into different bits. EP-0018's candidate digest gives calcside the same property. |
| AWS Lambda versions/aliases docs | `$LATEST` is mutable; published versions are immutable; aliases route traffic and can weight two versions | Immutable versions plus mutable pointers are the core serving model. Traffic splitting is optional routing policy, not part of correctness. |
| Hex publish docs | Publish preview runs the whole app in a fresh kernel; errors often reveal state that existed only in the author's kernel | This is the most important notebook-specific lesson: editor success is insufficient. A fresh-session verification run is the only meaningful pre-publish evidence. |
| marimo app deployment docs | `marimo edit` and `marimo run` are different modes; notebooks can be deployed as read-only apps, with layout metadata treated as part of the artifact | The authoring artifact and serving artifact may share source but not interaction semantics. Publishing must explicitly switch from editing mode to serving contract. |

### Conclusions

1. **Draft/live separation is expected.** Users understand autosave plus deliberate publish. Calcside already has this shape; the UX should make it impossible to confuse.
2. **Fresh execution is the central notebook problem.** A notebook can work because of residue, ordering, deleted variables, or side effects that already happened. Publishing must test the serving semantics, not the editor's current memory.
3. **Review and verification are complementary.** Verification asks “does this candidate run?” Review asks “is this the behavior and authority we meant to ship?” Neither replaces the other.
4. **History needs identity, not just source copies.** Releases need immutable digests, publication metadata, notes, comparison, and a way to restore safely.
5. **Rollback is a release-pointer or draft operation, not time travel.** It cannot undo external side effects already performed by the bad release.
6. **Environments are a later layer.** dev/staging/prod promotion, canary traffic, and external Git promotion are important, but they must not block a correct single-live-release interaction.

## Deep dive: lessons from Hex

Among existing products, **Hex** is the closest structural analog to Calcside: both center on a **DAG-backed reactive/dependency notebook** that authors iterate on in a mutable draft and then **publish** into an isolated, consumer-facing **application**. Studying Hex's publishing, versioning, and component-reuse flows surfaces specific interaction patterns that should shape EP-0026 — as well as places where Calcside must deliberately diverge because it serves governed, capability-bounded APIs and agents rather than BI dashboards.

| Dimension | Hex pattern | Calcside equivalent | Decision for EP-0026 |
|---|---|---|---|
| **Draft vs Live separation** | Mutable *Notebook* view vs immutable *Published App*; Publish button highlights when unpublished edits exist. | Application *Editor* draft vs *Live release* (`persistent_instances.release_id`). | **Adopt:** make draft-vs-live unmistakable in header, Editor, Invoke, and History; distinguish source edits from policy/pin drift. |
| **Fresh-kernel publish preview** | Clicking *Publish* boots a clean kernel and runs the DAG top-to-bottom (*Restart and run all*), catching hidden-state bugs before flipping live. | Fresh-session **Verification run** initializing the `main(input)` closure via EP-0025's execution path. | **Adopt:** fresh-session verification is part of the publish dialog itself, not a separate manual ritual; stale/editor runs never count as publish evidence. |
| **Localized failure & DAG propagation** | Preview errors highlight the failing cell and mark downstream dependents blocked/stale so authors see root cause immediately. | EP-0025 step trace (`init` / `entry` / `output_schema`) + DAG diagnostics. | **Adopt:** verification failures map back to the owning section, phase, and error category with a one-click jump to that section. |
| **Input defaults for one-click preview** | Typed input widgets carry default values so Publish Preview runs immediately without hand-pasting JSON. | `input_schema` + saved verification input(s) (replacing today's hardcoded `{"name": "world"}`). | **Adopt:** default verification to the last successful input or schema example so verification is one click; record input name/hash in evidence. |
| **Human-readable diffs** | Avoids raw `.ipynb` JSON dumps; shows readable logic/structure changes against the last published version. | EP-0018 semantic summary + per-section Starlark diffs (`@codemirror/merge`). | **Adopt:** replace the current raw `JSON.stringify` snapshot dump in `PublishReviewDialog` with categorized semantic changes (widening first) and section-level code diffs. |
| **Chronological version history** | *History & versions* timeline separates autosave checkpoints, manual named versions, and published versions; supports inspect and *Restore*. | `application_releases` history tab with publisher, note, digest, verification badge, compare, *Restore into draft*, and *Rollback live*. | **Adopt (scoped):** v1 ships immutable published release history with compare, restore-into-draft, and rollback-live; named draft checkpoints remain a v2 layer. |
| **Reusable component propagation** | Publishing a new *Component* version never silently mutates downstream published apps; updates land in the app's draft and require republish. | Workspace Modules (`spec.modules`) + Extensions + Workspace Policies. | **Adopt:** upstream module/extension/policy updates never silently change live traffic; surface pin/policy drift explicitly in Readiness and Review. |
| **Consumer/API surface** | Published apps and Public API (`runProject`) always target the published version, decoupled from draft work. | Invoke view + `/api/v1/applications/{id}/invoke`. | **Adopt:** Invoke shows live release `vN`, short digest, and contract-change notice; never reads draft state. |
| **BI layout, caching & schedules** | Drag-and-drop App Builder layout, SQL query result cache (and bypass toggles), PDF snapshots, recurring schedules. | Headless JSON/stream API + Starlark capability Gate + Rego policies + human interrupts. | **Diverge:** no visual dashboard layout builder or query cache; invest that complexity into **boundary/grant-widening review**, **digest-bound TOCTOU safety**, and **interrupt-safe release pinning**. |

### Key product takeaways from Hex

1. **Make "clean-room execution" feel native, not bureaucratic.** Hex succeeds because clean-kernel execution is built straight into the Publish modal with immediate feedback (`Running…` → `Publish version`). If Calcside makes verification a separate detour outside the publish flow, users will treat it as friction; putting readiness + one-click verification + semantic diff into one cohesive dialog makes safe publishing the path of least resistance.
2. **Never dump raw serialized artifacts as a "diff".** Hex's readable project diffs are a core reason teams trust Publish Preview. Calcside's current `PublishReviewDialog` renders raw `JSON.stringify(rev.base)` / `JSON.stringify(rev.candidate)` blocks; upgrading this to structured semantic cards + per-section code diffs is a prerequisite for EP-0026 to feel production-grade.
3. **Treat dependency & policy drift as first-class release events.** Just as Hex keeps Component updates explicit so a library change cannot silently break a live app, Calcside must distinguish three states clearly: *clean* (draft == live), *draft modified* (author edited sections/schemas/spec), and *resolved drift* (workspace policy text or unpinned dependency changed since live was built).
4. **Separate "undo an incident" from "repair the draft".** Hex's *Restore* copies an older version into the working draft, which is ideal for repair but adds steps during an outage. Calcside keeps both: **Rollback live** (atomic pointer move for incidents, recorded in history) and **Restore into draft** (copy into draft for repair and re-verification).

## Definitions

- **Draft** — the mutable application notebook, schemas, spec, policies and module references currently being edited.
- **Candidate** — the server-resolved release that would be created from the draft: normalized spec, pinned extensions/modules, policy snapshots, notebook sections and entry metadata.
- **Release** — an immutable, content-addressed candidate that can be served. It has a sequence in application history once published.
- **Live release** — the release currently used by public invoke. This is a pointer, not a mutable release.
- **Verification run** — a bounded run of the candidate in a fresh session using the release's own dependency initialization path. It is evidence about the candidate, not a scratch test against editor state.
- **Readiness** — the server-computed set of blocking and non-blocking findings that determine whether Review & publish can proceed.
- **Activation** — the post-publish process that prepares replicas and makes the release callable. In v1 there are no external triggers; when triggers exist, activation includes them and can be partial.

## Product principles

1. **The draft is not the release.** Editing never changes live behavior.
2. **The reviewed candidate is the published candidate.** Digest binding from EP-0018 is mandatory.
3. **Freshness beats convenience.** A publish flow must surface failures caused by hidden state before the release becomes live.
4. **Verification is evidence, not a guarantee.** External systems can change after the run; the UI must say what was tested, with what input, and when.
5. **Publish is atomic, activation is observable.** The live pointer moves atomically; preparing workers/replicas/triggers may complete afterward and must expose status.
6. **Rollback does not rewrite history.** A rollback creates evidence: either repoint live to an old immutable release or restore that release into draft and publish it again through review.
7. **The console and API tell the same story.** Agents and CI must be able to perform check, verify, preview, publish and inspect readiness without screen scraping.

## User stories

### 1. First publish: application developer

> As an application developer, I finish a notebook draft and want to know whether it is safe to make it callable.

**Flow**

1. The developer clicks **Review & publish** in the application header.
2. The dialog opens with a readiness checklist before the diff:
   - graph diagnostics: no blocking `cycle`, `missing_provider`, or read-relevant `duplicate_define`;
   - entry contract: exactly one valid public `main(input)`; statically known interrupt handlers are unique and valid;
   - schemas: input/output schemas parse and validate their examples if examples exist;
   - boundary: referenced extensions/modules/policies/secrets resolve; no plaintext secrets appear in source;
   - execution: candidate construction succeeds and produces a digest;
   - verification: latest verification status for this exact candidate digest.
3. Blocking findings appear in the checklist with a direct jump to the owning section/settings field. Review is visible but **Publish** is disabled.
4. The developer clicks **Run verification**. The server builds the candidate, initializes the selected entry closure in a fresh session, and runs `main` with the selected saved test input.
5. The verification result is shown next to the checklist item: `passed · 182ms · 3 steps` or the error category. The result links to the step trace.
6. The developer reviews semantic changes and publishes. The dialog moves through `Creating release → Switching live → Preparing replicas → Ready`.
7. Completion lands in a concise success state: `Published v4 · ready`. The next primary action is **Send test invoke**, not a generic close.

**Done when:** the application header reads `Live v4 · ready`, the release history contains v4, and one invoke against the live release succeeds.

**Failure handling:** if verification fails, the user can still inspect the review but cannot publish unless the failure is explicitly non-blocking. If activation partially fails, the release remains live, the state is `live with issues`, and the failure names the affected replica/trigger rather than implying rollback.

### 2. Iterate after publication: application developer

> As an application developer, I edit a live application and want to ship a small fix without guessing what callers will receive.

**Flow**

1. Editing the draft never interrupts live traffic. The header shows `Live v4 · draft has unpublished changes`.
2. Review & publish shows base `v4` and candidate digest. Semantic changes are ordered by risk: boundary widening, contract changes, handler/entry changes, source changes, dependency pin drift.
3. The verification input defaults to the last successful live or test input. The user can choose another saved input without leaving the dialog.
4. Publish is bound to the reviewed digests. If another publish or dependency drift invalidates the review, the dialog keeps the note and asks the user to refresh the review.
5. After publish, the invoke view defaults to the new live release and shows a one-line comparison link against the previous release.

**Done when:** the new version is live, the old version remains inspectable in history, and the user can compare the two behavior-affecting artifacts.

### 3. Release reviewer or workspace owner

> As a reviewer, I need enough evidence to decide whether the new release should receive production authority.

**Flow**

1. The reviewer opens the same review dialog or a read-only shared review link if they did not initiate it.
2. The first screen is not raw code. It is a release summary:
   - verification status and input used;
   - semantic changes grouped by widening/narrowing;
   - contracts and schemas;
   - new or removed capabilities/extensions/hosts/modules/policies;
   - interrupt handlers and expiry behavior;
   - unresolved promotion warnings inherited from a playground.
3. Source diffs remain one click away for each changed section or file.
4. The reviewer records a note such as `Adds Stripe reconciliation; widens stripe read-only access`.
5. The history row preserves publisher, note, candidate digest, verification status and timestamp.

Calcside does not implement multi-person approval in v1. The human publisher remains accountable. External CI or future workspace policy can require approvals by calling the API.

### 4. Integrator after a release

> As an integrator, I need to know which version my system calls and whether a publish changed the contract.

**Flow**

1. The Invoke view shows the live release sequence and digest prominently beside the URL.
2. Request examples include the current input schema. If a publish changes required inputs, the integrator-facing callout says the contract changed and links to the release comparison.
3. Application-bound keys can read release history and compare releases, subject to the access rules in EP-0018.
4. The integrator can pin their own client logic to a release sequence for change management, while invoke still uses the application's live pointer. Version-specific invoke URLs are a future compatibility decision, not v1.

### 5. Bad release: incident responder

> As an incident responder, I need to stop a bad release quickly without pretending its external effects did not happen.

**Flow**

1. Release history marks the currently live release and its activation state.
2. The responder selects the last known-good release and chooses **Rollback live**.
3. The console explains precisely what rollback does: future invokes use the selected immutable release; it does not undo external side effects, rewrite history, or mutate interrupted runs already pinned to the bad release.
4. In v1, rollback is implemented as a server-side pointer move to an existing release with its own history row and note (`rollback_to`). This avoids requiring a draft rewrite during an incident.
5. **Restore into draft** remains available for non-urgent repair: copy the old release into draft, then run the normal verify/review/publish flow.
6. After rollback, activation status is shown again. Runs already started continue on their pinned release; interrupt resumes also use their original pinned release, consistent with EP-0016/EP-0025.

### 6. Agent or CI publisher

> As an agent or CI job, I need the same release evidence as the console so I can decide whether publishing is allowed.

**Flow**

1. `POST /applications/{id}/release/check` returns structured readiness findings.
2. `POST /applications/{id}/release/verify` builds the candidate and runs verification with a named input or inline bounded input.
3. `POST /applications/{id}/publish/preview` returns the EP-0018 semantic diff and digest.
4. `POST /applications/{id}/publish` requires `expected_base`, `candidate` and `note`.
5. `GET /applications/{id}/releases` exposes sequence, digest, publisher, note, verification evidence and activation state.

There is no API bypass that skips verification policy. If verification is required, the same rule applies to console, SDK, MCP and CI.

## Release readiness model

Readiness is a list of typed findings, not a wall of prose:

```json
{
  "candidate": "rel_…",
  "status": "blocked",
  "findings": [
    {
      "code": "graph_cycle",
      "severity": "blocker",
      "section": "enrich_orders",
      "message": "cycle: load_orders -> enrich_orders -> load_orders",
      "fix": "Break the shared-name cycle before publishing"
    },
    {
      "code": "verification_missing",
      "severity": "warning",
      "message": "No verification run exists for this candidate"
    }
  ]
}
```

Initial categories:

| Category | Blocking examples | Non-blocking examples |
|---|---|---|
| Notebook graph | cycle, missing provider in `main` closure, ambiguous handler/name resolution | unread duplicate names, graph warnings outside the entry closure |
| Entry contract | missing/duplicate `main`, invalid handler signature, handler target not statically resolvable when statically declared | dynamically computed handler target; publish warns that resolution happens at runtime |
| Schema | invalid JSON schema, valid input example fails schema | missing example, output schema absent |
| Boundary | missing secret reference, unpublished/missing module, unresolvable extension pin, invalid policy | boundary narrowing that may reduce functionality |
| Candidate build | normalization/pinning failure, digest mismatch | drift-only candidate identical in source but changed in pins/policies |
| Verification | verification failure; missing or stale verification on the normal publish path | explicit recorded verification override, if retained |

V1 policy: structural blockers cannot publish. Verification failure blocks publish. Missing or stale verification blocks the normal publish path. If an override is kept at all, it must be explicit, require a written rationale, and be recorded in release history; it is never a quiet checkbox. The default product direction is mandatory fresh-candidate verification, with fixture/mocking support making that safer over time.

## Verification semantics

Verification is deliberately narrower than a full test framework:

- It runs against the exact candidate digest, in a fresh session, through the application notebook execution path from EP-0025.
- It starts from `main(input)` with a selected input. Direct handler debug runs remain a development tool, not release verification.
- **Input ergonomics (Hex lesson):** the dialog pre-selects the last successful verification/test input, falling back to an example derived from `input_schema` (or `{}` when no schema is set) rather than an unrelated hardcoded placeholder. Users can edit the input inline before clicking **Run verification**; the run records the input name and SHA-256 hash so reviewers know exactly what was exercised.
- **Section-level failure localization (Hex + EP-0025):** when verification fails, the result identifies the failing phase (`init`, `entry`, or `output_schema`), the owning section ID, and the classified error, and links to the EP-0025 step graph trace showing executed vs failed vs unreached sections. In the console, clicking the failing section jumps directly to that card in the notebook editor.
- It uses the candidate's resolved boundary. Secret plaintext is never shown; verification does not prove credentials are correct unless the run actually uses them.
- It records bounded evidence: input name/hash, output schema validation result, duration, steps, error category, failing section/phase (if any), capability/effect summary, and candidate digest.
- It is not a replay. External calls are real unless a future fixture/mocking mode exists. The UI labels this honestly as `live-effect verification`.
- If the run waits on an interrupt, verification result is `waiting`, not success. The user may resume it as part of verification; publishing a release whose only verification is still waiting is blocked in v1.
- Long-running/streaming verification is bounded by the application's normal limits.

Saved regression test cases are a follow-on. The release dialog should reserve a `Verification` section for multiple named cases, but v1 requires only one explicit run.

## Publish and activation state machine

```text
draft edited
  → check (findings)
  → verify (per candidate digest)
  → preview reviewed
  → publish accepted
  → release created
  → live pointer switched atomically
  → activation: preparing → ready | live_with_issues | failed
```

Semantics:

- Publishing is rejected if readiness has an unresolved blocker, the verification evidence does not match the candidate digest, or EP-0018 digest binding fails.
- Release creation and live-pointer movement happen in one transaction, as in EP-0018.
- Replica preparation is asynchronous. The application is `live` once the pointer moves, even if some workers are still preparing. The console must not imply that old replicas instantly disappear.
- There are no production triggers in v1. When triggers are added, activation becomes per-trigger and can be partial, following n8n's lesson: unchanged triggers should not be interrupted, failures must be retryable, and partial activation must remain visible.

## Console definition

### Header and entry point

- Header state is one of `No live release`, `Live vN · ready`, `Live vN · preparing`, `Live vN · issues`.
- The primary action is **Review & publish** whenever the user can configure the application. It is disabled only while a check/publish request is in flight.
- The unpublished-changes badge means the draft candidate differs from live after considering source and resolved drift, not merely unsaved editor keystrokes. When the draft itself is unchanged but workspace policies or pinned dependencies drifted, the badge/summary says `policy/dependency drift` rather than implying edited code.

### Review & publish dialog

The current `PublishReviewDialog` (which renders raw `category/path` lines and `<details>` JSON snapshot dumps) must be replaced by a structured, human-readable release review flow.

Layout, in order:

1. **Release summary** — base (`vN` or `First release`), candidate short digest, drift-only notice if applicable, verification state, activation expectation.
2. **Readiness** — blockers first, warnings second, each with a concise fix hint and one jump action to the owning section or settings field.
3. **Verification** — selected/editable input (pre-populated from last successful run or schema example), **Run verification** (`SwapLabel`: `Run verification` / `Verifying…`), latest result (`passed · 182ms · 3 steps` or failing section + phase + error), and link to the EP-0025 step trace.
4. **Changes** — EP-0018 semantic summary grouped by risk (boundary widening badges first, then contracts, entries/handlers, policies/pins, sources) plus expandable per-section Starlark unified diffs rendered with `@codemirror/merge` (never raw JSON snapshot dumps by default; raw candidate JSON, if kept for debugging, sits behind an explicit secondary disclosure).
5. **Publish note** — optional but recommended, max 500 chars.
6. **Primary action** — `Publish` / `Publishing…` in a reserved-width `SwapLabel`.

The dialog never hides blockers inside tabs. It preserves note and selected input after a 409 or transient failure, and guards dirty-note dismissal via the shared `Modal` primitive (`C2`/`P2`).

### Post-publish state

The success state is not a toast alone. The dialog transitions to a compact completion panel:

- `Published v5 · preparing` → `ready`;
- **Send test invoke** primary action;
- **Open release history** secondary action;
- `Live since … · candidate rel_…` metadata.

### History

The application detail gains a dedicated **History** tab (peer to Editor / Invoke / Activity, addressable via `?view=history`, `C4`):

- chronological timeline rows (`useHeldOrder`, `S5`): sequence (`v5`), `Live` badge on the active pointer, publisher, note, published time, short digest, verification badge (`verified` / `override` / `rollback`), activation state;
- compare any two rows (reusing the same semantic + `@codemirror/merge` diff view read-only);
- **Restore into draft** (with `Confirm` when draft has unsaved/diverged changes, `F2`);
- **Rollback live** for users with configure access (with named `Confirm` stating exact consequences: future invokes switch to `vN`, existing runs/interrupts and external effects are unchanged, `F2`);
- run history remains in Activity and is never conflated with release history.

### Interaction-stability contract

All EP-0026 console surfaces must preserve `docs/interaction-stability.md`:
- `S1`/`S4`: paint-only states; `SwapLabel` on `Review & publish`, `Run verification`, `Publish`, and post-publish status transitions.
- `S3`/`S5`: fixed-slot readiness/verification result rows so checking or verifying never jumps the Publish footer; `useHeldOrder` + `RevealNew` on the History table.
- `C2`/`P2`: shared `Modal` and `Confirm` primitives; dirty-note discard guard; 409 conflict keeps typed note and input and offers explicit **Refresh review** (never silent retry).
- `F2`/`F3`: explicit `Confirm` on rollback and overwrite-restore; honest `preparing` / `live_with_issues` / `verification failed` states.

## API and SDK implications

New or refined public operations:

- `POST /api/v1/applications/{id}/release/check`
- `POST /api/v1/applications/{id}/release/verify`
- `GET /api/v1/applications/{id}/releases`
- `POST /api/v1/applications/{id}/releases/{seq}/rollback`
- Existing EP-0018 preview/publish/compare/restore operations remain the underlying mechanism.

Verification responses must be structured enough for agents to reason about:

```json
{
  "candidate": "rel_…",
  "input_name": "stripe reconciliation sample",
  "status": "failed",
  "error": {"kind": "output_schema", "message": "missing required field `as_of`"},
  "duration_ms": 842,
  "steps": 1932,
  "effects": ["net.request", "fs.write"],
  "run_id": "run_…"
}
```

Python SDK exposes `check_release`, `verify_release`, `preview_publish`, `publish`, `list_releases`, and `rollback_release`. MCP publication tools, if added, must be read/check/verify oriented by default; publish itself should remain an explicit high-privilege tool.

## Non-goals for v1

- Multiple named environments (dev/staging/prod), environment-specific hosts, or promotion between instances.
- Canary/weighted traffic, blue/green traffic splitting, or automatic rollback based on metrics.
- Multi-person approval, required reviewers, comments, or branch protection inside calcside.
- Full saved-test suite management. One verification run is the minimal proof; regression suites are the next proposal/increment.
- Automatic semantic versioning. Human-readable major/minor/patch labels can be notes/metadata later; they are not needed for correctness.
- Automatic conversion of playground state into releases; any future application-editing or agent workflow must define that provenance separately.
- Undoing external effects through rollback.

## Delivery slices

> **Scoping note:** Single-input ad-hoc `verify` is superseded by dedicated **Application Test Case Management** (supporting strong expected outputs and multi-turn `interrupt` steps) in a separate PR. The initial EP-0026 PR delivers slices 1, 3 (readiness + semantic/code diff + publish), 4, 5, and 6 cleanly without `/release/verify`.

1. **Readiness model:** server-side check operation for graph/entry/schema/boundary/candidate construction; structured findings.
2. **Test-case verification (follow-up PR):** multi-turn application test cases (inputs, expected `interrupt` turns and resume inputs, strong output assertions) executed against the candidate before publish.
3. **Release dialog integration:** combine readiness + EP-0018 semantic/code review/publish; preserve notes and handle 409 refresh.
4. **Activation state:** expose preparing/ready/issues in header, dialog completion and history.
5. **History operations:** release metadata, comparison, restore into draft, rollback-live transaction.
6. **SDK/API parity:** typed operations (`check_release`, `preview_publish`, `publish`, `list_releases`, `compare_releases`, `restore_release`, `rollback_release`) and errors for agents/CI.

## References

- [n8n — Save and publish workflows](https://docs.n8n.io/build/understand-workflows/save-and-publish-workflows)
- [n8n — View change history](https://docs.n8n.io/build/manage-workflows/view-change-history)
- [n8n — Environments](https://docs.n8n.io/administer/environments)
- [Dify — Version Control](https://docs.dify.ai/en/self-host/use-dify/build/version-control)
- [Dify CLI — publish/check/restore commands](https://docs.dify.ai/en/cli/reference/command-index)
- [Retool — Publish an app](https://docs.retool.com/build/apps/guides/publishing)
- [Retool — Version and release apps](https://docs.retool.com/mobile/guides/deploy/releases-history)
- [Appsmith — Version Control with Git](https://docs.appsmith.com/advanced-concepts/version-control-with-git)
- [Appsmith — Multiple environments using Git](https://docs.appsmith.com/advanced-concepts/version-control-with-git/environments-with-git)
- [Vercel — Promote a preview deployment to production](https://vercel.com/docs/deployments/promote-preview-to-production)
- [Vercel — Environments](https://vercel.com/docs/deployments/environments)
- [AWS Lambda — Function versions](https://docs.aws.amazon.com/lambda/latest/dg/configuration-versions.html)
- [AWS Lambda — Weighted aliases/canary deployments](https://docs.aws.amazon.com/lambda/latest/dg/configuring-alias-routing.html)
- [Hex — Publish and share Apps](https://learn.hex.tech/docs/share-insights/apps/publish-and-share-apps)
- [Hex — History and versions](https://learn.hex.tech/docs/explore-data/projects/history-and-versions)
- [marimo — Deploy notebook servers or apps](https://docs.marimo.io/guides/deploying/)
- [marimo — Run as an app](https://docs.marimo.io/guides/apps/)

## Acceptance criteria

- A draft that only works because of stale in-memory playground state fails verification before publish.
- A valid candidate can be verified, reviewed, published, and invoked; the verification evidence matches the published release digest.
- Editing the draft after publication does not affect the live release.
- A concurrent publish, draft edit, policy edit, module publish or extension drift after review causes a deterministic 409 and no partial release switch.
- Verification failure blocks publish. Missing or stale verification blocks normal publish; any explicit override is recorded and visible in history.
- A successful publish reaches an explicit terminal state (`ready` or `live_with_issues`) and never remains visually ambiguous.
- Rollback repoints future invokes to the selected release, writes a history row, and clearly states that existing runs and external effects are unchanged.
- Release comparison shows semantic changes and verification evidence without exposing secret values.
- Console, SDK and CI use the same digest-bound publish rules.

## Open questions

- Should publishing without verification be impossible, or allowed only through the explicit recorded override described above?
- Should calcside support version-specific invoke URLs (`/invoke?v=4`) for compatibility testing, or only the live pointer?
- Should rollback reuse the existing release sequence or create a new sequence whose candidate digest equals the old release? The latter preserves monotonic history but may confuse users.
- How should verification handle intentional external side effects? A safe mode may require connector-level idempotency keys, test credentials, or fixture bindings.
- Should activation health block publish completion in the dialog, or always complete asynchronously after the atomic pointer move?
- What retention policy applies to verification inputs/outputs, especially when outputs embed sensitive business data?
