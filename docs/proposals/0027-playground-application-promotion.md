# EP-0027: Promoting playground notebooks to applications

- **Status:** Draft
- **Created:** 2026-11-27
- **Area:** instance notebooks, application notebooks, promotion service, console, MCP/agent workflow, verification
- **Related:** [EP-0022](0022-instance-notebooks.md), [EP-0023](0023-notebook-dag.md), [EP-0024](0024-notebook-charts.md), [EP-0025](0025-application-notebooks.md), [EP-0026](0026-notebook-release.md), [EP-0016](0016-application-interrupts.md), [EP-0018](0018-reviewable-publish.md)

## Summary

A playground notebook and an application notebook use similar editing vocabulary, but they are not the same computational object.

- A **playground notebook** explains how a live, stateful exploration arrived at its current state.
- An **application notebook** is a fresh-session program invoked through explicit entry functions.

Promotion is therefore not “copy these sections into an application”. It is a **semantic transformation with evidence**: identify which parts of the exploration encode reusable logic, separate them from state that merely happened to exist, parameterize what was improvised, neutralize one-time effects, and prove the result starts cleanly. The promotion product must make the gap visible instead of pretending that a successful playground run is already production logic.

This proposal defines the gap, the transformation challenges, the user interaction, and the initial promotion product. It does not implement application publishing; EP-0026 owns release readiness, verification and live publication.

## Why this is the central workflow

Calcside's most important user motion is:

```text
agent explores in a bounded playground
  → human inspects what happened
  → reusable logic becomes an application draft
  → draft is verified, reviewed and published
  → other agents/systems call the application
```

The first and last steps already exist in some form. The middle transition is the weak link. Without it, users must manually copy code, guess which sections matter, rediscover hidden state, and learn only after publish that the application never worked from a fresh start.

Research reinforces the same point:

- Classic notebook state is a function of execution history; EP-0022 cites evidence that many notebooks do not reproduce top-to-bottom.
- Hex runs publish previews in a fresh kernel precisely because editor kernels hide deleted variables and stale state.
- marimo can deploy notebooks as apps, but its model primarily turns an interactive notebook into a web artifact; calcside applications are callable operational programs with schemas, effects, interrupts and policies.
- Low-code platforms separate draft and live versions because users need safe iteration; high-code platforms use immutable versions because production behavior must be reproducible.
- Calcside adds a harder problem none of those products fully owns: exploratory sections may already have performed external side effects, and repeating them can be dangerous.

## Semantic gap

| Dimension | Playground notebook | Application notebook | Why promotion is hard |
|---|---|---|---|
| State | Globals survive across section runs within one incarnation | Every invoke starts from a fresh session | Logic may depend on bindings created earlier but not reconstructed by the selected entry |
| Restoration | After process loss, sections are `not_run`; memory and `/work` are empty | There is no incarnation-local memory to restore | Promotion must prove the entry closure initializes all required state |
| Execution unit | A section is the authoring/run unit | A function entry (`main` or interrupt handler) is the execution unit | Sections must be reorganized around callable functions without losing dependencies |
| Input | Often implicit in literals, files, prior fetches, or agent context | Explicit JSON `input` validated by schema | The user must decide which discovered values become parameters, constants or external lookups |
| Ordering | DAG assists execution, but exploration can be iterative and partial | Dependency closure initializes deterministically for each entry | Exploratory order may encode accidental dependencies, mutation or repeated overrides |
| Scratch/drift | Scratch execs and deleted/edited sections can leave residue | No scratch state exists | Residue is invisible unless the fresh verification fails; static analysis cannot prove all mutation |
| Side effects | Sections may already have sent email, created tickets, written files or mutated systems | Every invocation repeats the program for a new request | One-time effects must not be copied blindly into a repeatable application |
| Files | `/work` contains uploaded/generated files from exploration | `/work` starts empty per invocation | Inputs/assets must become declared assets, regenerated artifacts, inputs or external reads |
| Plots/displays | `io.display` attaches charts to interactive exec results | Application output is typed JSON/stream; display transport is not equivalent | Diagnostic and deliverable plots need different treatment |
| Interrupts | Interactive exploration can call capabilities opportunistically | Interrupts are durable control-plane operations with handlers and expiry | Promotion must design handler functions and context explicitly |
| Boundary | Instance spec was created for exploration and may be broad or narrow | Application boundary is part of the published contract | Grants/secrets/modules must be reconciled, least-privilege reviewed and pinned |
| Failure | Failed/stale sections can remain in the notebook as history | Published code must initialize and execute reliably | Exploratory dead ends should not become runtime paths |
| Audience | Supervisor/agent inspects activity and state | Integrators/resolvers/operators consume a stable service | Output, errors, evidence and operational semantics must become intentional |

The gap is therefore four gaps at once:

1. **State gap** — what memory merely exists vs what the program reconstructs.
2. **Control gap** — exploratory execution order vs deterministic entry invocation.
3. **Effect gap** — actions already taken vs actions that should repeat per request.
4. **Contract gap** — improvised inputs/outputs vs typed callable API.

## Research-derived product rules

1. **Never silently copy.** Every removed section, moved statement, changed plot, excluded file or parameterized literal appears in a promotion report.
2. **Fresh proof beats static confidence.** Static analysis can find likely problems, but only a fresh-session application dry run demonstrates the transformed entry closure.
3. **Preserve provenance.** The application draft and promotion report link back to the source instance, notebook revision, sections and selected runs.
4. **Do not erase exploration.** Promotion copies into an application draft; it never mutates or cleans the source notebook.
5. **Classify before transform.** Some sections are reusable logic, some are setup, some are evidence, some are one-time operations, and some are dead ends. The user/agent must choose.
6. **A promotion can fail usefully.** The output may be “not promotable yet” plus exact findings. Failure is a valid product result.

## User stories

### 1. Agent supervisor harvests a successful exploration

> As an agent supervisor, I watched an agent build a working notebook in an instance. I want to turn the reusable part into an application without manually reverse-engineering the agent's state.

**Flow**

1. The supervisor clicks **Promote to application** on the instance notebook.
2. Calcside analyzes the current saved notebook and produces a promotion report before asking for a destination.
3. The report groups sections:
   - reusable logic;
   - input/setup candidates;
   - one-time or dangerous effects;
   - exploration/diagnostic code;
   - sections with graph or freshness problems.
4. The supervisor chooses **New application** or an existing application draft, then selects the intended entry behavior in business language: for example, “reconcile invoices for a date range”.
5. The supervisor maps discovered literals into input fields: `start_date`, `end_date`, `threshold`. Constants remain constants only if they are truly configuration.
6. Calcside runs a fresh-session dry run of the proposed application closure. It does not use the live playground memory or `/work`.
7. The dry run either succeeds or produces findings tied to the exact missing state/file/effect.
8. Only then does promotion write the application draft. The source notebook remains unchanged.

**Done when:** the new application draft opens with a promotion banner, the dry-run evidence is visible, and the draft can enter EP-0026 release readiness.

### 2. Application developer receives a partial handoff

> As an application developer, I did not perform the exploration. I need to understand what the agent intended, what was copied, and what still requires my judgment.

**Flow**

1. The application draft shows a **Promotion source** panel: instance id/name, notebook revision, promoting user/agent, time, source section list and dry-run result.
2. Findings are divided into `must fix`, `decision made`, and `accepted risk`.
3. Each copied section has a provenance label. Hovering or opening details shows the source section name and whether it was unchanged, reordered, wrapped in a function, or edited.
4. The developer can open side-by-side source vs promoted code without leaving the application.
5. The panel is removable once all findings are resolved, but the provenance metadata remains in history.

**Done when:** the developer can answer “what did this application inherit, and what did promotion intentionally not copy?” without inspecting the original instance manually.

### 3. Reviewer evaluates production risk

> As a reviewer, I need to know whether promotion hid state, repeated dangerous effects, widened permissions or dropped outputs.

**Flow**

1. Release review surfaces unresolved promotion findings alongside semantic diff.
2. Boundary changes are compared not only against the previous application release but also against the source playground boundary: copied, narrowed, widened or unresolved.
3. One-time effects excluded from promotion are listed by source section so the reviewer can confirm they were intentionally omitted.
4. Verification evidence must come from the application candidate, not from the original playground run.

### 4. Agent writes promotably from the start

> As an agent, I want guidance while building a playground notebook so the result can later become an application.

**Flow**

1. The instance MCP prompt already requires section-based execution. Promotion adds lightweight authoring guidance:
   - put reusable logic in functions rather than top-level one-off statements;
   - keep external reads separate from transformations;
   - mark one-time effects `rerun: once`;
   - avoid relying on scratch execs;
   - treat files as either inputs/assets or generated outputs;
   - parameterize values that would differ per invocation.
2. The notebook outline can show a `promotion` hint count, but editing remains lightweight. Hints are informational, not blocking.
3. An MCP tool can ask for a promotion analysis before the user opens the console, so the agent can fix obvious issues itself.

### 5. Failure diagnosis after a “worked in playground” moment

> As a user, my dry run fails even though the notebook worked. I need the product to explain which hidden dependency caused it.

Examples of useful findings:

```text
missing_state: enrich uses `orders`, but no selected section defines it in the application entry closure.
source: section transform_enrichment, line 4
likely provider: fetch_orders (excluded as one-time effect)
next: include fetch_orders as a repeatable input step, or pass orders as application input.
```

```text
missing_file: report reads /work/exchange_rates.csv, but application `/work` starts empty.
source: section build_report, line 8
next: choose one — upload as release asset, regenerate in fetch_rates, or accept file content as input.
```

```text
one_time_effect: notify_ticket created FIN-1234 during exploration and is marked `once`.
next: exclude it, replace it with a parameterized notification step, or explicitly accept repeat behavior.
```

## Promotion analysis

The analysis is a structured report generated from saved notebook state, not live memory. It may use runtime metadata as evidence, but the transformation source is code plus durable notebook metadata.

### Inputs

- notebook sections, order, descriptions, code, `rerun`, states and graph diagnostics;
- notebook revision, incarnation, restoration history and drift count;
- observed capability effects per section;
- execution history metadata for selected sections;
- file list/metadata, never plaintext file contents unless the user explicitly selects a file;
- instance spec boundary references;
- display/chart usage detected statically and from run metadata;
- interrupt usage detected statically.

### Finding categories

| Category | Examples | Default severity |
|---|---|---|
| `state` | missing provider in selected closure, dependency on mutated state, stale provider, unread duplicate ambiguity | blocker when in entry closure |
| `input` | hard-coded date range/customer id/limit that appears invocation-specific | decision required |
| `effect` | `once` section, network write-like operation, external mutation in setup section | decision required; blocker if unresolved |
| `file` | reads unknown `/work` file, writes artifact expected by caller, depends on uploaded file | decision required |
| `plot` | `io.display` in reusable path, chart spec constructed but not returned | decision required |
| `interrupt` | interrupt wait without valid handler, dynamic handler target, top-level wait during init | blocker or decision depending on location |
| `boundary` | instance grants unavailable in app, missing secret/policy/module, broader privileges copied by default | blocker until mapped |
| `shape` | no candidate `main`, multiple possible entries, section returns no reusable function | blocker |
| `freshness` | current code never ran in this incarnation, failed section included, stale dependency | warning; verification decides |
| `output` | prints instead of returning, multiple ad hoc outputs, stream/displays mixed without schema | decision required |

## Transformation model

Promotion produces an application draft through explicit operations. It does not “auto-fix” code invisibly.

### Allowed automatic operations

- Copy selected sections into application sections preserving names where possible.
- Reorder sections into dependency order for readability; dependency order remains semantic, not page position.
- Wrap top-level reusable logic in functions only when the transformation is mechanical and the generated wrapper is shown in the diff.
- Generate a minimal `main(input)` skeleton that calls selected functions, with TODO markers for unresolved input mapping.
- Copy required workspace module/extension references when the target application lacks them, preserving version pinning semantics.
- Copy read-only small files as explicit release assets only after user selection; this is a future capability, not implied by current `/work` behavior.

### Operations requiring a user/agent decision

- Convert a literal into an input field.
- Exclude or rewrite a one-time effect.
- Treat an exploration fetch as repeatable per-invocation behavior.
- Convert `print`/display output into returned output.
- Select files/assets/input data.
- Choose whether a chart is diagnostic, public output or run evidence.
- Broaden the application boundary to match the playground.
- Design interrupt handler names/context/expiration behavior.

### Never allowed silently

- Copying plaintext secrets or resolving secret values.
- Re-executing a one-time effect to “test” promotion.
- Using live playground memory as part of the application candidate.
- Treating successful exploration output as proof that the application works.
- Dropping a section/effect/file/display without listing it in the report.
- Mutating the source notebook.

## Promotion flow

### Step 1 — Choose destination and intent

The user chooses:

- New application or existing application draft;
- target entry (`main`) behavior;
- whether this is a first promotion, an update to existing logic, or a replacement draft.

For existing applications, promotion never overwrites unpublished user edits silently. It either creates a conflict report or copies into new sections under a namespace/prefix chosen by the user.

### Step 2 — Analyze and classify sections

The default classification is conservative:

- Functions and pure transformations → reusable logic.
- Sections defining configuration/constants → setup candidates.
- Sections with external reads → repeatable input candidates, but flagged for effect semantics.
- Sections with write-like external effects or `rerun=once` → one-time/dangerous effects.
- Display/print/probe/debug sections → diagnostics.
- Failed/stale/not-run sections → unverified candidates.

Users can override classification, but every override is recorded.

### Step 3 — Map input and output

The input mapper proposes a schema from:

- user-selected literals;
- function parameters;
- prior example values;
- existing application input schema when updating.

The output mapper asks the user to choose the primary result shape. `print` output is never public application output. Displays are classified:

| Playground display intent | Promotion treatment |
|---|---|
| Diagnostic/exploratory | omit from application runtime; record decision |
| Caller deliverable | return chart spec/data explicitly in output JSON/schema |
| Operational evidence | future run metadata/artifact path, not public output by default |

### Step 4 — Build proposed closure and dry run

The dry run constructs a temporary application candidate-like object:

- fresh session;
- selected sections only;
- generated `main` or selected entry;
- mapped input fixture;
- target application boundary, not the live instance boundary;
- empty `/work` except explicitly selected assets;
- no scratch state or playground globals.

The dry run is not a publish verification and does not create a release. It is promotion evidence. EP-0026 verification later repeats the check against the real candidate.

### Step 5 — Resolve findings and write draft

Findings use the same interaction vocabulary as release readiness:

- `blocker` — cannot write a coherent application draft;
- `decision` — user must choose before continuing;
- `warning` — can continue but remains visible in the application promotion panel;
- `accepted` — user explicitly acknowledged.

The draft write is transactional where possible: either the complete proposed section set and provenance metadata are written, or none is. For existing applications, draft CAS conflicts behave like ordinary editor conflicts.

### Step 6 — Handoff into release readiness

Promotion does not publish. Completion opens the application draft with:

- a promotion summary banner;
- unresolved findings;
- dry-run evidence;
- next primary action **Check release readiness**.

This keeps responsibilities clear: promotion answers “can exploration become application-shaped logic?” Release answers “is this application ready to become live?”

## Example transformations

### 1. Straightforward reusable logic

Playground:

```python
# fetch_orders
orders = stripe.list_orders(start="2026-11-01", end="2026-11-27")

# reconcile
def reconcile(orders):
    ...
    return mismatches

mismatches = reconcile(orders)
io.display(chart.spec(...))
```

Promoted:

```python
# fetch_orders
def fetch_orders(start_date, end_date):
    return stripe.list_orders(start=start_date, end=end_date)

# reconcile
def reconcile(orders):
    ...
    return mismatches

# main
def main(input):
    orders = fetch_orders(input["start_date"], input["end_date"])
    mismatches = reconcile(orders)
    return {"mismatches": mismatches, "count": len(mismatches)}
```

Decisions visible in the report:

- hard-coded dates became input fields;
- exploration fetch became per-invocation input fetch;
- diagnostic chart was omitted or explicitly returned;
- top-level `mismatches` state was removed because `main` reconstructs it.

### 2. One-time side effect

```python
# create_ticket  (rerun: once)
ticket = linear.create_issue(...)

# enrich
def build_report(rows):
    return {"ticket": ticket["id"], "rows": rows}
```

This is not mechanically promotable. Possible outcomes:

1. ticket creation becomes intentional per-invocation behavior with an idempotency key;
2. ticket id becomes input/configuration;
3. the ticket reference is removed from the application;
4. promotion stops as not suitable.

The wrong outcome is silently repeating ticket creation on every invoke.

### 3. File dependency

```python
rates = load_csv("/work/rates.csv")
```

The application cannot inherit `/work`. The user must choose:

- bundle `rates.csv` as a declared release asset;
- fetch rates in a repeatable section;
- pass rates as input;
- make the file path configurable and read from controlled external storage.

### 4. Interrupt design

```python
req = interrupt.new(...)
interrupt.wait(req["id"], handler="on_ok", context=ctx)
```

Promotion must confirm:

- `on_ok` exists exactly once as a top-level one-argument function;
- context is bounded JSON and contains what the handler needs;
- expiry behavior is intentional;
- waits are inside function execution, not module/section initialization;
- the public output contract represents waiting/final outcomes correctly.

## Product/API surface

### Instance console

- Notebook toolbar gains **Promote to application** beside notebook actions.
- The action opens a full-page or large-dialog wizard; this is not a menu item with instant copy.
- The wizard steps are: Analyze → Classify → Input/output → Dry run → Write draft.
- Steps are resumable because analysis/decisions are stored as a promotion session.

### Application console

- Application draft displays promotion provenance and unresolved findings until resolved.
- Release readiness consumes unresolved blockers as release blockers.
- Provenance is included in release metadata or linked run metadata once published.

### API/MCP

Initial operations:

- `POST /instances/{id}/promotions` — create analysis session for a target application or new application.
- `GET /promotions/{id}` — report, findings, decisions, dry-run evidence.
- `POST /promotions/{id}/decisions` — record classification/input/output/file/plot decisions.
- `POST /promotions/{id}/dry-run` — fresh-session check of proposed closure.
- `POST /promotions/{id}/apply` — write application draft under CAS.
- `GET /applications/{id}/promotion` — provenance and unresolved findings.

MCP can expose analysis and dry-run to agents. Applying promotion or publishing should require explicit user-visible privilege, because both change reusable production assets.

## Data model sketch

New durable objects:

- `promotion_sessions`: id, workspace, source instance, notebook revision, destination application/null, created_by, status, timestamps.
- `promotion_findings`: session, code, severity, section, message, data JSON, resolution state.
- `promotion_decisions`: session, finding/category, chosen action, actor, rationale.
- `promotion_results`: dry-run status, input fixture reference/hash, error category, effects summary, duration/steps, created draft version.
- Application draft metadata links current active/latest promotion session for provenance.

The source notebook remains immutable input to the session. If it changes after analysis, the report is stale and must be refreshed; applying a stale report is rejected.

## Initial scope

V1 should optimize for a narrow but valuable path:

1. New application from one instance notebook.
2. User selects one logical entry flow.
3. Static classification + manual decisions.
4. Fresh dry run with one input fixture.
5. Copy sections and provenance into draft.
6. Handoff to EP-0026 release readiness.

Explicitly included in v1:

- state/missing-provider detection;
- hard-coded input mapping;
- `once`/effect warnings;
- missing `/work` file detection;
- plot classification;
- dry-run evidence;
- provenance report.

## Non-goals for v1

- Automatic refactoring of arbitrary exploratory code into clean production architecture.
- Semantic understanding of every HTTP method or connector operation as read/write.
- Full data lineage through aliases and mutations.
- Copying live memory values into application defaults.
- General file/asset packaging beyond small explicitly selected assets.
- Multi-flow extraction from one notebook into several applications.
- Merging promotion into an existing diverged application draft intelligently.
- Automatic idempotency-key generation for external writes.
- Publishing as part of promotion.

## Acceptance criteria

- A notebook whose logic depends only on reconstructable sections can be promoted, dry-run and opened as an application draft.
- A notebook depending on scratch-only state produces a `missing_state` finding naming the consumer and likely provider.
- A `rerun=once` network effect is never copied silently; the report requires an explicit decision.
- A read of `/work` produces a file decision and the dry run fails until resolved.
- A diagnostic `io.display` is not silently carried into public output; the user chooses omit/return/evidence.
- The dry run uses a fresh session and cannot observe playground globals or files.
- The generated draft contains provenance links and a list of all automatic transformations.
- Existing application drafts are not overwritten silently; stale notebook revisions and draft CAS conflicts fail safely.
- Promotion never publishes and never grants new authority by itself.

## References

- [Hex — Publish and share Apps](https://learn.hex.tech/docs/share-insights/apps/publish-and-share-apps): publish previews run in a fresh kernel and expose hidden editor state.
- [Hex — History and versions](https://learn.hex.tech/docs/explore-data/projects/history-and-versions): publish and version history are related but distinct.
- [marimo — Deploy notebook servers or apps](https://docs.marimo.io/guides/deploying/) and [Run as an app](https://docs.marimo.io/guides/apps/): one notebook artifact can have separate edit and app-serving modes.
- [n8n — Save and publish workflows](https://docs.n8n.io/build/understand-workflows/save-and-publish-workflows): autosave/draft vs live publication and post-publish activation states.
- [Dify — Version Control](https://docs.dify.ai/en/self-host/use-dify/build/version-control): current draft, latest live version, restore-into-draft.
- [Vercel — Promote preview to production](https://vercel.com/docs/deployments/promote-preview-to-production): verify the exact deployment candidate before promotion.
- [AWS Lambda — Function versions](https://docs.aws.amazon.com/lambda/latest/dg/configuration-versions.html): mutable `$LATEST` vs immutable published versions.
- [EP-0022](0022-instance-notebooks.md): calcside instance state, restoration and one-time-effect semantics.
- [EP-0023](0023-notebook-dag.md): calcside notebook dependency graph and its static-analysis limits.
- [EP-0025](0025-application-notebooks.md): calcside application entry-function and fresh-session execution model.
- [EP-0026](0026-notebook-release.md): release readiness and verification after promotion.

## Open questions

- Should a promotion session be user-owned draft state, or shared workspace state reviewable by multiple users?
- How much automatic code rewriting is acceptable before users stop trusting provenance? A conservative first version may copy code and ask developers to refactor manually.
- Should the application boundary start from the playground boundary, the target application boundary, or least-privilege inferred capabilities? Starting least-privilege is safer but may create many missing-grant findings.
- Can connector metadata distinguish reads from writes well enough to classify repeatability, or must users decide every network effect?
- Should promotion preserve section names exactly or generate application-oriented names? Exact names improve provenance; new names may improve readability.
- How should large exploration data become test fixtures without copying sensitive business data into durable application tests?
- Is promotion a one-time handoff or a repeatable synchronization workflow while the playground continues evolving?
