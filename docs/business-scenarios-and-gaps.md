# Business scenarios and capability gaps

- **Status:** Research and open gap register, not an approved implementation plan
- **Research date:** 2026-10-05
- **Scope:** Five candidate business domains, current capability gaps, and validation priorities

## Positioning and evidence

The strongest initial fit is **bounded, cross-source operational investigation**:
an agent writes code to query, join, validate, and summarize authorized business
data, then returns exceptions and evidence rather than every intermediate record.

Provider documentation confirms that the workflows and data interfaces below
exist. It does not establish customer demand, adoption of calcside, willingness
to pay, or performance improvements over direct tool calling. Those are hypotheses
to validate with users and the two reference scenarios below.

Avoid competing with a single provider's existing report when one API call already
answers the question. Stable recurring accounting or reporting logic should
ultimately become a reviewed deterministic job, not newly generated code each run.
Calcside's opportunity is ad hoc composition and investigation across sources.

## Current implementation baseline

As inspected during this research:

- Base capabilities are `fs`, `net`, `io`, and `ext`; the predeclared pure modules
  are `json` and `math`. See [node registry](../internal/node/node.go) and
  [engine session construction](../internal/engine/engine.go).
- `net` supports HTTP, host/method restrictions, response-size and timeout limits,
  and send-time secret substitution. This is not a provider connection manager
  with OAuth refresh or request signing. HTTP error statuses can be inspected;
  transport errors fail the call, and Starlark has no `try/except`.
- [Extensions](extensions.md) are Starlark wrappers over granted base capabilities.
  An extension operation and its nested capability calls are gated. The script
  can also use the granted base capabilities directly.
- Files live in the instance's in-memory VFS. Public file access supports browsing
  and redacted text reads, not direct upload. There is no requirement to replace
  memory storage to run these scenarios.
- [ExecResult](../internal/runtime/message.go) returns printed output, an execution
  error, and execution statistics. A domain-specific structured result contract
  can initially be built with `print(json.encode(...))` and SDK conventions.
- [EP-0001](proposals/0001-record-replay.md) through
  [EP-0007](proposals/0007-artifact-preview-and-download.md) are drafts, not shipped features.
  Proposal coverage in this document must not be read as implementation status.

## Candidate domains

### Finance operations: order, payment, and payout reconciliation

**User:** Finance or payment operations.

**Task:** Compare internal orders with payment/refund records; identify missing
ledger entries, duplicates, unsynchronized refunds, and amount differences.

**Workflow:** Order CSV + payment data -> join by business identifiers -> apply
explicit currency/accounting rules -> exception CSV and an explanatory summary.

Stripe supports listing balance transactions associated with an automatic payout,
including expanded source payment/refund objects [R1]. Important constraints:

- Order value, gross payments, fees, net proceeds, and bank payouts are different
  measures. Define which quantities are being reconciled.
- Refunds can cross accounting periods; one order can have several payments.
- Never sum different currencies together or infer that every currency has two
  decimal places. Use explicit integer minor units or exact decimal arithmetic.
- Stripe cannot assign underlying transactions to manual payouts in the same way
  as automatic payouts. Do not reuse the automatic-payout rule indiscriminately.

**Gaps:** G01, G02, G03, G07. Start with two uploaded CSV files and a differences
report. Live Stripe integration is optional for the first validation. Do not
modify accounting records, initiate refunds, or connect bank accounts in the MVP.

### Sales operations: stale opportunity review

**User:** Revenue operations or a sales manager.

**Task:** Find deals expected to close this month that have had no meaningful
follow-up for 14 days and lack a next action; group them by owner with evidence.

**Workflow:** Search deals -> batch-read associated companies, contacts, and
activities -> evaluate rules -> owner-specific follow-up list.

HubSpot documents CRM search and association queries [R2]. At research time:

- Search is a read operation implemented with HTTP POST.
- Search pages contain at most 200 records, with at most 10,000 results per query.
- Search is limited to five requests per second per account.
- Newly created or updated objects can take time to appear in search results.

**Gaps:** G01, G02, G03, G04, G06. Avoid an N+1 query for every associated object,
report incomplete searches explicitly, and distinguish record modification time
from meaningful customer contact. A single read-only HubSpot extension is enough
for a first scenario; sending emails or changing deal stages is outside its scope.

### Ecommerce operations: fulfillment exceptions

**User:** Store operations or customer support.

**Task:** Find paid orders past their promised shipping deadline, distinguishing
partial fulfillment, holds, stock shortages, and missing information.

**Workflow:** Filter Shopify orders -> inspect line items and fulfillment -> apply
store SLA/exception rules -> evidence-backed order exception list.

Shopify exposes financial/fulfillment/date filters and cursor pagination [R3].
Its GraphQL rate limit is based on calculated query cost, not simply request
count [R4]. Order access is limited to the last 60 days by default; older records
require additional access [R5].

**Gaps:** G01, G02, G03, G04, G06. Distinguish order-level from line-item state,
model preorders/holds/business deadlines explicitly, and constrain GraphQL through
real provider read scopes. Query and mutation can share the same POST endpoint;
an HTTP-method allowlist alone is not business read-only authorization.

The MVP reports exceptions. It does not refund, cancel, or fulfill orders.

### Engineering operations: post-release investigation

**User:** SRE, release owner, or engineering lead.

**Task:** Compare release health under comparable environments and traffic,
identify regressions, and associate relevant commits and owners with the evidence.

**Workflow:** Release metadata -> comparable metric windows -> new/worsened issues
-> related commits -> investigation report with sources and missing-data warnings.

Sentry provides a release-health statistics API grouped by project, release,
environment, and session status. Its documented interval is at least one hour and
it returns at most 10,000 data points [R6]. Session and issue data come from
different collection paths; issue data can be sampled/filtered and is not an
interchangeable denominator for session health [R7].

**Gaps:** G01, G02, G03, G04, G07. Use provider-side aggregation, consistent metric
definitions, bounded windows, and explicit partial results if GitHub or Sentry
fails. Increasing error counts can reflect increased traffic, not degradation.
A temporally associated commit is a candidate explanation, not a confirmed cause.

This is close to today's `net + ext` capabilities. Start with small read-only
Sentry/GitHub integrations; file upload, a disk backend, and automatic rollback
are not prerequisites.

### FinOps: cloud cost growth investigation

**User:** Cloud platform team or finance operations.

**Task:** Compare two cost periods, identify the largest account/service growth
contributors, and relate them to team ownership and deployment records.

**Workflow:** Cost Explorer aggregated data -> growth contribution calculation ->
account/tag ownership -> deployment evidence -> cost investigation report.

AWS `GetCostAndUsage` supports filters, grouping, pagination, decimal-string
amounts, and an `Estimated` flag [R8]. Grouping supports up to two groups per query.
Cost metrics have distinct meanings, such as amortized versus unblended cost.

**Gaps:** G01, G02, G03, G05, and G08 when using asynchronous exports. AWS requests
need SigV4 signatures derived from credentials and request contents [R9]. Current
send-time placeholder substitution is not sufficient to sign arbitrary requests.
Do not fix this by exposing credential values to a Starlark extension.

Use a trusted host signer/connection adapter or an existing authorized cost-query
service. Exported CSV is a lower-cost first validation. Query aggregation belongs
at the provider; do not load an entire cloud billing warehouse into the sandbox.

## Gap register

All entries below are open gaps at research time. Priority is a recommendation:

- **P0:** Needed for correctness, safety, or the selected reference workflow;
  not a requirement to build every item into the runtime immediately.
- **P1:** Needed when adopting a particular integration or scaling a validated
  scenario; not a universal prerequisite for the first demo.

| ID | Gap | Current support and missing behavior | Suggested layer | Priority and existing proposal coverage |
|---|---|---|---|---|
| G01 | Data transformations and numeric semantics | JSON/math exist; common CSV, URL, date, and exact monetary transformations lack a shared tested library. | Pure utilities; business-specific accounting rules in scenario code. | P0 for CSV reconciliation. EP-0002 covers CSV/URL/datetime, but not a Decimal API or complete monetary conventions. |
| G02 | Domain connectors and scenario packages | `net + ext` can compose APIs, but the researched provider integrations and their business fixtures are not bundled. | Contrib extensions with bounded operations, typed configuration, and fixture tests. | P0 for an API scenario. EP-0003 supplies distribution/metadata foundations, not these implementations; EP-0004 improves discovery, not integration correctness. |
| G03 | Completeness, freshness, and partial-result semantics | No shared contract distinguishes empty results from missing pages, query caps, stale search indexes, or failed sources. | Extension result conventions, scenario validation, and SDK consumption. | P0 for all domains. Requires follow-up design beyond the current EPs. |
| G04 | Provider-call reliability and budgets | Per-request timeout/size and execution limits exist; shared credential/account rate limits, provider cost budgets, safe bounded retries, and recoverable transport failures are not a unified feature. | Trusted runtime/connection layer plus provider-specific pagination and throttling rules. | P0 for honest failure/completeness handling; P1 for multi-instance quota coordination. Not specified by the existing EPs. |
| G05 | Dynamic credentials and request signing | Static secret injection exists; outbound OAuth refresh, expiring provider credentials, and SigV4 are not connection primitives. | Trusted host connection/authentication layer, or existing authorized host tools. | P1 overall; a blocker for native AWS integration. Not covered by the current EPs. |
| G06 | Business-operation and resource authorization | Gate, policies, allowlists, and secret domain restrictions exist; they do not automatically make an ext wrapper a narrower authority than its base capabilities. | Upstream least-privilege credentials, trusted tool/resource enforcement, and carefully designed policy metadata. | P0 for every live integration. EP-0003/0004 preserve boundaries but do not add a new business authorization mechanism. |
| G07 | File input, artifact preview/download, and structured results | Print output and gated redacted reads exist; CSV tables, HTML report previews, file attachments, ZIP export, upload, and a standard evidence/result contract are missing. | Public API, SDK, console, and host-owned artifact storage where needed. | Current P0 focus: EP-0007 covers preview/download/ZIP; EP-0005 covers upload. Both remain drafts. Durable retention, general binary export, and a structured exec result contract are separate; no disk/S3 prerequisite. |
| G08 | Asynchronous business orchestration | Bounded execs are not durable workflows for report generation, webhook waiting, scheduled runs, or recovery. | Hosting application/workflow orchestrator first; small runtime job primitives only if justified. | P1 when an integration requires it. EP-0001 replay is not a scheduler or a replacement for this lifecycle. |

### G03: proposed result convention

Begin with a convention, not a mandatory rewrite of the execution API:

```json
{
  "data": [],
  "complete": false,
  "warnings": [{"code": "source_unavailable", "source": "payments"}],
  "next_cursor": null,
  "sources": [{"name": "orders", "records_processed": 120}],
  "as_of": "2026-10-05T00:00:00Z"
}
```

This is an illustrative result, not an implemented schema or real customer data.
Define completeness relative to explicit query scope and a data cutoff: collecting
all pages does not guarantee real-time freshness or a transactionally consistent
cross-system snapshot. Do not mark partial/truncated work complete. Source lists
identify successful and failed inputs without embedding credentials or unnecessary
personal data. `as_of` must have documented provenance, not be an invented timestamp.

Extensions can return dicts and the SDK can validate printed JSON initially. Schema
versioning and a dedicated structured execution result can follow demonstrated need.

### G04: reliability without unsafe retries

An HTTP 429 or other response status can already be inspected by a script; that is
different from catching a transport exception, which the language cannot do today.
Do not conflate worker routing retries with retries of external provider operations.

Retry only operations known to be safe under their provider contract, with bounded
attempts/time and explicit idempotency where required. Never retry policy denial,
resource-limit failure, or an ambiguous business write automatically. A provider's
read-only POST may be retryable, but that decision requires trusted operation
semantics, not an assumption that all POSTs are reads or all errors are transient.

For multiple instances sharing a credential, coordinate rate/cost budgets outside
individual Starlark loops. Long waits and polling should yield to host orchestration
rather than spin inside the execution step/time budget.

### G06: policy metadata is not business authorization by itself

[EditorSymbols](../internal/policy/editor.go) describes the editor's policy input;
[inputFor](../internal/policy/policy.go) constructs the actual input. The current
[net operation metadata](../internal/capability/net/net.go) is primarily HTTP
method/URL/host/scheme/secret-reference information, not the full request body.

Consequences:

- Denying all POSTs blocks legitimate read-only HubSpot searches.
- Allowing a Shopify GraphQL endpoint does not distinguish query from mutation.
- Naming an extension `read_orders` does not stop direct use of a granted `net`.
- Caller-supplied tenant IDs or instance labels are not trusted resource bindings
  merely because a policy can inspect them.

Use genuine upstream read-only scopes for the initial integrations. Enforce finer
resource/operation restrictions in a trusted connection or tool boundary. Any new
policy metadata must be bounded, validated, and derived from the operation being
authorized; do not copy entire request/response bodies into hooks or audit. Adding
editor completion fields alone does not implement enforcement.

### G07: current priority is artifact preview and export

The immediate product gap is **generate -> preview -> download/package**. A CSV
or HTML file in VFS was not a complete deliverable when the console only showed
source text and offered no download action.

The first [EP-0007](proposals/0007-artifact-preview-and-download.md) increment now
provides CSV/TSV table previews, isolated static/interactive HTML modes, single-file
attachments, and ZIP export of selected files/directories in the UI/API/Python SDK. Every file read keeps Gate and secret
redaction enforcement; an archive is not a way to bypass per-file policy.

This can ship independently of upload, a new business connector, or a storage
backend change: existing scripts can already write files. Keep memory VFS and
instance-scoped lifetime, and distinguish static HTML from opt-in JavaScript
charts with separate-origin requirements. Do not claim post-expiry availability
or unrestricted binary download. This first delivery gap is now implemented for
bounded UTF-8 text while the instance is live. HTML deployment requires a separate
registrable domain and random snapshot subdomains. JavaScript is off by default;
CSP/sandbox isolate console authority but are not a strict no-egress firewall.
Durable retention, binary exports and shared multi-replica preview storage remain
open. The six utility modules and Tavily contrib migration are also implemented in this
increment; the earlier scenario analysis describes the pre-implementation baseline.

## Relationship to the current proposals

| Proposal | How it contributes | What remains outside it |
|---|---|---|
| [EP-0002: utilities](proposals/0002-standard-library-utilities.md) | Removes repeated parsing/encoding/date work. | Evaluate exact decimal/money semantics; provider/business accounting rules are not generic utilities. |
| [EP-0003: contrib](proposals/0003-community-extensions.md) | Establishes package validation, metadata, distribution, and catalog indexing. | Actual domain connectors, shared result conventions, and scenario fixtures. |
| [EP-0004: discovery UI](proposals/0004-extension-discovery-ui.md) | Makes available extensions discoverable and configurable. | Correctness, completeness, and backend authorization cannot be replaced by UI labels. |
| [EP-0005: uploads](proposals/0005-instance-file-uploads.md) | Enables the CSV input path while preserving Gate and quotas. | Larger transfers are a follow-up; durable artifacts and raw binary downloads are not included. |
| [EP-0006: pluggable VFS](proposals/0006-pluggable-instance-vfs.md) | Keeps storage choices behind a stable application-defined contract. | No disk/S3 implementation or POSIX compatibility is required for these MVPs. |
| [EP-0007: artifact delivery](proposals/0007-artifact-preview-and-download.md) | Adds CSV/HTML previews, file downloads, and ZIP selection with existing read authorization/redaction. | No durable retention, public sharing, or unrestricted binary export; interactive HTML requires explicit isolation. |
| [EP-0001: replay](proposals/0001-record-replay.md) | Could later help reproduce and regress already useful workflows. | Not a prerequisite for initial usefulness, nor durable job scheduling; uploads need explicit recording semantics. |

Keep memory as the default VFS implementation. Logical storage quotas and runtime
memory limits are still necessary, but neither S3 nor a POSIX filesystem fixes
missing business rules, authentication, pagination, or evidence quality.

## Recommended validation sequence

### Immediate deliverable: artifact viewing and export

Prioritize EP-0007 before broadening the connector catalog. Use scripts that
write a CSV, a static HTML/SVG report, and an optional self-contained JS chart.
Verify preview behavior, complete single-file downloads, ZIP structure,
redaction, policy-denied members, and instance expiry. This does not require
upload or a new utility module to be implemented first.

### Reference A: CSV reconciliation

Validate the data path: **upload -> parse -> join -> exact calculation -> exception
file**. Start with synthetic or explicitly authorized order/payment exports and
reviewed expected results. Dependencies are G01 and G07 plus G03's honest scope
reporting; a live payment connector is not required.

Acceptance fixtures cover duplicates, absent IDs, multiple payments per order,
refunds across periods, currencies with different scales, rounding, malformed
rows, and exceeded input/output limits. Verify output totals and exception rows
against a deterministic reference. Use EP-0007 for CSV preview, attachment
download, and ZIP export; SDK text reads remain a fallback. Do not add S3
merely to deliver these instance-scoped results.

### Reference B: Sentry + GitHub release investigation

Validate the API path: **authorized queries -> bounded multi-call composition ->
normalization -> comparison -> evidence report**. Dependencies are small G02
connectors and G03/G04/G06, with G01 helpers where needed.

Acceptance covers pagination, provider throttling, a failed source, incompatible
metric windows, missing session data, and an irrelevant but temporally adjacent
commit. Never report missing data as "no regression" or a correlated change as a
proven cause. Exercise unauthorized projects/repositories and direct base-capability
calls, not only the happy path through an ext wrapper.

### Follow-up domains and evaluation

After these references, validate CRM and ecommerce with actual users. Add native
FinOps integration only when trusted authentication is available; CSV exports can
validate its business value earlier. Do not build five full connector families
before learning from the first workflows.

Compare each reference against both reviewed expected results and an agent that
calls tools directly, using the same data, permissions, and task:

- Correctness of exceptions/calculations and accuracy of evidence attribution.
- Missed pages, false completeness, freshness, and explicit failure behavior.
- Intermediate data entering model context, model turns, and external API calls.
- End-to-end latency and provider cost; fewer model turns need not mean lower cost.
- Boundary enforcement, memory consumption, and recovery from partial failures.
- User task completion and whether the workflow is useful enough to reuse.

This register records gaps and suggested ownership; it does not approve new
capabilities, dependencies, external side effects, or changes to security controls.

## Official references

Provider limits and schemas change. Recheck them when implementing a connector;
examples should pin a supported API version rather than rely blindly on `latest`.

- **R1:** [Stripe payout reconciliation](https://docs.stripe.com/payouts/reconciliation)
- **R2:** [HubSpot CRM Search](https://developers.hubspot.com/docs/api-reference/latest/crm/search-the-crm)
- **R3:** [Shopify orders query](https://shopify.dev/docs/api/admin-graphql/latest/queries/orders)
- **R4:** [Shopify GraphQL query-cost limits](https://shopify.dev/docs/apps/build/apis/graphql-admin/rate-limits)
- **R5:** [Shopify Order access and history](https://shopify.dev/docs/api/admin-graphql/latest/objects/order)
- **R6:** [Sentry release-health statistics API](https://docs.sentry.io/api/releases/retrieve-release-health-session-statistics/)
- **R7:** [Sentry release-health data definitions](https://docs.sentry.io/product/releases/health/)
- **R8:** [AWS GetCostAndUsage](https://docs.aws.amazon.com/aws-cost-management/latest/APIReference/API_GetCostAndUsage.html)
- **R9:** [AWS SigV4 authentication](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv.html)
