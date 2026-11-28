# EP-0027: Application Test Mocks & Versioned Test Suites

- **Status:** Proposed
- **Created:** 2026-11-28
- **Related:** [EP-0016](0016-application-interrupts.md), [EP-0021](0021-stream.md), [EP-0025](0025-application-notebooks.md), [EP-0026](0026-notebook-release.md)

## Problem

Application test cases currently have three structural gaps:

1. **Real external side effects & non-determinism:** Executing a test case runs real capability calls (`ext.llm.respond`, `ext.tavily.search`, `net.post`, etc.), causing flaky assertions, API costs, and unintended external side effects during publish gates.
2. **Hard coupling to workspace secrets:** Preparing a test replica resolves and validates real workspace secrets even when the test only wants to verify application logic with mocked inputs/outputs.
3. **Test cases are detached from release versions:** `application_test_cases` is currently a single floating mutable list per application. Publishing `v1` and later modifying tests for `v2` loses `v1`'s test specification; `Compare` ignores test changes; `Restore` restores `v1`'s code/spec while leaving `v2`'s incompatible test cases behind; and editing a test case does not invalidate a previously passed run for the same code digest.

## Decisions

1. **Mock all capabilities and `ext` uniformly at `Gate.Invoke`:**
   - Every capability op (`ext.<alias>.<op>`, `net.<op>`, `fs.<op>`, `io.<op>`, and any future `capability.Factory`) routes through `Gate.Invoke` and can be mocked with the same declarative rule format.
   - `interrupt.*` remains driven by the first-class multi-turn `Interrupts` specification (pausing and dispatching `handler` / `on_expire`).
2. **No secrets required for test execution — pure input/output:**
   - Test replicas run with `TestOnly: true` and **zero real workspace secrets**: test preparation skips secret resolution and skips `env.Secrets.Lookup` validation in `ext.New` / `bindProvider`.
   - Mock rules specify only target + optional input match (`when`) + output (`response` / `stream` / `error`), with optional `times` and `expect_calls`.
   - Production release readiness (`CheckReadiness` / `Publish`) still validates that all required workspace secrets exist before going live.
3. **1-to-1 association between test cases and versions:**
   - The mutable **draft** owns the working test suite; creating, editing, or deleting a test case bumps `app.Version` and changes `tests_digest`, invalidating stale test runs.
   - **Publish** atomically snapshots the exact test cases (including mocks) and the passed test run onto the immutable `ApplicationRelease` (`seq`).
   - **Restore** restores both the release's code/spec/schemas and its snapshotted test cases into the draft in one transaction.
   - **Compare** diffs test cases alongside sections, spec, and schemas; **Release History** shows each version's immutable test suite and passed CI run.

## Mock Rule Model

### Schema

A mock rule is purely declarative input → output:

```go
type ApplicationTestMock struct {
	// Target is "<capability>.<op>" or "ext.<alias>.<op>", e.g.:
	//   "ext.llm.respond"
	//   "ext.tavily.search"
	//   "net.post"
	//   "fs.read"
	//   "io.print"
	Target string `json:"target"`

	// When optionally matches call arguments by parameter name (or kwargs).
	// Omitted/null matches any call to Target.
	When          json.RawMessage `json:"when,omitempty"`
	WhenMatchMode string          `json:"when_match_mode,omitempty"` // "subset" (default) | "exact"

	// Outcome — exactly one of Response, Stream, or Error must be set.
	Response json.RawMessage   `json:"response,omitempty"` // any bounded JSON value
	Stream   []json.RawMessage `json:"stream,omitempty"`   // finite stream items (EP-0021)
	Error    string            `json:"error,omitempty"`    // simulated error message

	// Times caps how many times this rule may match (0 = unlimited).
	// Once exhausted, matching falls through to later rules.
	Times int `json:"times,omitempty"`

	// ExpectCalls, when non-nil, asserts this rule matched this exact count
	// across the entire test case (spanning all interrupt turns).
	ExpectCalls *int `json:"expect_calls,omitempty"`
}
```

On `ApplicationTestCase`:
- `Mocks []ApplicationTestMock` (`json:"mocks,omitempty"`), max `32` per case.
- `UnmatchedPolicy string` (`json:"unmatched_policy,omitempty"`):
  - `""` (default / `"fail"` for external caps `ext.*` and `net.*`; `"passthrough"` for pure in-memory `fs.*` and `io.*`): prevents accidental real external I/O during tests while keeping `/work` and `print` usable unless explicitly mocked.
  - `"fail_all"`: any unmocked capability call (except `interrupt.*`) fails with `mock_unmatched`.
  - `"passthrough"`: unmocked calls execute the real capability implementation (note: because `TestOnly` injects no secrets, unmocked calls that rely on secrets will fail cleanly).

Bounds:
- `MaxApplicationTestMocks = 32` per case.
- `MaxMockStreamItems = 256` per rule.
- Combined test case payload remains capped at `MaxTestCasePayloadBytes` (64 KiB).

### Example

```json
{
  "name": "summarizes search result and posts webhook",
  "input": { "topic": "calcside" },
  "mocks": [
    {
      "target": "ext.tavily.search",
      "when": { "query": "calcside" },
      "response": { "results": [{ "title": "Calcside", "url": "https://calcside.dev" }] },
      "expect_calls": 1
    },
    {
      "target": "ext.llm.respond",
      "when": { "model": "gpt-4o-mini" },
      "response": { "text": "Calcside is a Starlark sandbox." },
      "times": 1
    },
    {
      "target": "net.post",
      "when": { "url": "https://hooks.example.com/notify" },
      "response": { "status": 200, "headers": {}, "body": "{\"ok\":true}" },
      "expect_calls": 1
    }
  ],
  "expect_status": "succeeded",
  "expected_output": { "summary": "Calcside is a Starlark sandbox.", "notified": true }
}
```

### Mock Call Trace & Assertions

Each turn records bounded mock activity in `ApplicationTestTurnResult.MockCalls`:

```go
type ApplicationTestMockCall struct {
	Target       string `json:"target"`
	RuleIndex    int    `json:"rule_index"`             // -1 when unmatched
	Outcome      string `json:"outcome"`                // "response" | "stream" | "error" | "unmatched" | "passthrough"
	ArgsSummary  string `json:"args_summary,omitempty"` // bounded JSON summary
	ErrorMessage string `json:"error_message,omitempty"`
}
```

If any rule's `expect_calls` does not match its actual total hit count across the test case, the case fails with `error_code = "mock_assertion"` and reports expected vs. actual counts per rule.

## Runtime Architecture

### 1. Uniform Interception at `Gate.Invoke`

All capabilities (`ext`, `net`, `fs`, `io`, etc.) already funnel every operation through `Gate.Invoke(ctx, cap, op, args, fn)`.

```
Script calls ext.tavily.search(query="calcside")  OR  net.post(url, ...)  OR  fs.read(path)
  │
  ▼
Capability binding
  ├─ builds sanitized Call.Args (for policy & audit — unchanged)
  ├─ if gate.HasMocks(): attaches bounded JSON rawArgs (via capability.JSONValue)
  ▼
Gate.Invoke(ctx, cap, op, cargs, fn)
  1. Armed / revoked check (unchanged)
  2. Before hooks: Rego policies evaluate against sanitized cargs (unchanged)
  3. Mock router (ONLY when gate has mocks attached):
       - Target key = "<cap>.<op>" (e.g. "ext.tavily.search", "net.post", "fs.read")
       - Matches ordered rules with remaining budget against rawArgs (subset or exact)
       - Hit (response): converts JSON → Starlark value; meta = {"mocked": true, "mock_rule": idx}
       - Hit (stream): builds in-memory *stream.Stream over items, wrapped via Gate.WrapStream
       - Hit (error): returns simulated error; meta = {"mocked": true, "mock_rule": idx}
       - Miss + policy requires fail: returns ErrMockUnmatched (never executes fn)
       - Miss + passthrough: executes real fn(ctx, args)
  4. After hooks run (unchanged)
  5. Audit observer records the call (unchanged)
```

Key properties:
- **Policy fidelity:** Rego `Before` hooks still run before the mock intercepts, so a test cannot pass if production policy would deny the call.
- **Audit hygiene:** `Call.Args` stays sanitized/truncated; raw argument matching only runs when mocks are active on the gate and never enters audit logs.
- **No Starlark monkey-patching:** Extensions and base capabilities keep their exact binding shape, completion metadata, and argument unpacking.
- **Streaming support (EP-0021):** `stream: [...]` produces a real `*stream.Stream` wrapped by `Gate.WrapStream`, preserving `stream.map`, `stream.collect`, pull-time gating, and teardown cleanup.

### 2. Zero-Secret Test Execution (`TestOnly`)

When `RunTestCases` prepares an ephemeral replica:
- `PrepareRequest.TestOnly = true`, and `PrepareRequest.Secrets` is **empty** (`nil`).
- In `service/application/test_cases.go`, `s.releaseForTest(ctx, a, app)` builds the `ServingRelease` **without** resolving workspace secrets (no `MissingSecret` error during test runs).
- In `server/internal/capability/ext/ext.go`, `InstanceEnv.TestOnly = true` causes `ext.New` and `bindProvider` to skip `env.Secrets.Lookup(name)` checks when initializing extension manifests. Extension manifests and `config` keys are still validated so typos and unknown ops are caught.
- **Production boundary:**
  - `programReplica.testOnly` is immutable after `Prepare`.
  - Live serving replicas (`testOnly == false`) reject any non-empty `InvokeRequest.Mocks` with `ErrBadSpec`.
  - Public `/invoke` never sets `TestOnly` or `Mocks`.
  - `CheckReadiness` and `PublishReviewedApplication` still require all real workspace secrets to be present before publishing.

### 3. Worker Protocol (`server/internal/runtime/serving.go`)

- `PrepareRequest.TestOnly bool` (`json:"test_only,omitempty"`)
- `InvokeRequest.Mocks *MockConfig` (`json:"mocks,omitempty"`) — carries ordered rules with remaining `Times` budget and `UnmatchedPolicy`.
- `InvokeResponse.MockCalls []MockCallRecord` (`json:"mock_calls,omitempty"`) — returned to the API tier after each turn so `executeTestCase` updates remaining `Times` across interrupt turns and evaluates `ExpectCalls` at case completion.
- Log discipline: `InvokeRequest.String()` / `LogValue()` logs only `mocks=<count>`, never mock payloads.

## Version-Bound Test Suites (1-to-1 with Releases)

### Ownership & Lifecycle

| State | Test Suite Storage | Mutability |
|-------|-------------------|------------|
| **Draft** (`app.Version`) | `application_test_cases` (working set for the draft) | Mutable; every create/update/delete bumps `app.Version` (CAS-checked) |
| **Published Release** (`seq = 1, 2, ...`) | Immutable snapshot on `application_releases` (`test_cases_json`, `tests_digest`, `test_run_json`) | Immutable forever |

### Exact Versioning Semantics

1. **`tests_digest`:** Canonical SHA-256 over the ordered list of test cases (name, input, interrupts, mocks, unmatched policy, expected status/output/error).
2. **Test run validity (`application_test_runs`):**
   - Keyed by `(application_id, candidate, tests_digest)` (where `candidate` is the draft's `ServingRelease.ID`).
   - Editing **either** code/spec/schemas **or** any test case/mock immediately invalidates the previous suite run and requires re-running tests before publish.
   - `RunTestCases` continues to require `version == app.Version` (optimistic concurrency).
3. **Publish (`PublishReviewedApplication`):**
   - Inside the same publish transaction:
     - Verifies that if the draft has `len(test_cases) > 0`, a passed `ApplicationTestSuiteRun` exists for `(app.ID, candidate, tests_digest)`.
     - Persists the immutable `test_cases_json`, `tests_digest`, and `test_run_json` on the newly created `ApplicationRelease` row (`seq`).
4. **Restore (`RestoreRelease(seq)`):**
   - Atomically copies `seq`'s sections, spec, schemas **and** `seq`'s snapshotted `test_cases_json` into the mutable draft (`application_test_cases`), bumping `app.Version` in one transaction.
   - Restoring `v1` gives you `v1`'s exact code AND `v1`'s exact test cases.
5. **Compare (`CompareReleases(base, head)`):**
   - Compares `base`'s test suite snapshot against `head`'s (draft or release `seq`) and emits typed `test_case` diff entries (added, removed, modified — including mock changes).
6. **Release History & Rollback:**
   - `GET /applications/:id/releases/:seq` (and the History detail view) returns that release's immutable test cases and its passed CI run snapshot.
   - `RollbackApplicationRelease(targetSeq)` creates a new release `seq+1` pointing to `targetSeq`'s `ReleaseID` and carrying `targetSeq`'s immutable `test_cases_json`, `tests_digest`, and `test_run_json`.

## Database Migrations (SQLite + PostgreSQL)

1. `application_test_cases`:
   - `mocks_json TEXT NOT NULL DEFAULT '[]'`
   - `unmatched_policy TEXT NOT NULL DEFAULT ''`
2. `application_test_runs`:
   - `tests_digest TEXT NOT NULL DEFAULT ''`
   - Update uniqueness/lookup to `(application_id, candidate, tests_digest)`.
3. `application_releases`:
   - `tests_digest TEXT NOT NULL DEFAULT ''`
   - `test_cases_json TEXT NOT NULL DEFAULT '[]'`
   - `test_run_json TEXT NOT NULL DEFAULT ''`

## API, Console UI & SDK

- **API (`api/openapi.yaml`):**
  - Test case CRUD requests accept optional `version` (draft CAS) and return updated `app_version` + `tests_digest` alongside the test case.
  - Test case payloads include `mocks` and `unmatched_policy`.
  - Release detail / history endpoints expose `test_cases`, `tests_digest`, and `test_run` for each `seq`.
  - Compare response includes `test_case` change entries.
- **Console UI:**
  - `ApplicationTests.tsx`: collapsible **Mocks** section (Radix `Disclosure`, `Select`, `JsonEditor`) with target picker grouped by configured `ext.<alias>.<op>` and base capabilities (`net.*`, `fs.*`, `io.*`), input `when` matcher, and output (`Response` / `Stream` / `Error`).
  - `TestChecksPanel.tsx`: displays `Mocked` badge and expandable per-turn **Mock Calls** trace.
  - `ReleaseHistory.tsx` & `PublishReviewDialog.tsx`: shows each release version's immutable test suite & CI run, and diffs test case changes in Compare/Review.
- **Python SDK (`sdk/python`):**
  - Support `mocks` and `unmatched_policy` on `create_application_test_case` / `update_application_test_case`, and expose versioned `test_cases` / `test_run` on release history responses.

## Rollout Plan

1. **Runtime & Gate:** Add `TestOnly`, `MockConfig`, `MockCallRecord` to `server/internal/runtime/serving.go`; wire uniform mock interception at `Gate.Invoke` for all capabilities (`ext`, `net`, `fs`, `io`); skip secret lookup in `TestOnly` mode. Unit tests for every capability, `subset`/`exact` matching, `times`, `stream`, `error`, `unmatched_policy`, `TestOnly` isolation, and Rego `Before` hook precedence.
2. **Store & Migrations:** Add SQLite + PostgreSQL migration for `mocks_json`, `unmatched_policy`, `tests_digest`, and release snapshots (`test_cases_json`, `test_run_json`); update `gormstore` and `storetest` conformance tests.
3. **Service & Versioning:** Update `service/application` so test case CRUD bumps `app.Version`, `RunTestCases` runs secret-free with per-turn mock budgets and `expect_calls` checks, `Publish` snapshots tests + run onto `ApplicationRelease`, `Restore` restores tests with code, and `Compare` diffs test cases.
4. **OpenAPI, Console UI & Python SDK:** Regenerate OpenAPI bindings, update `ApplicationTests.tsx`, `TestChecksPanel.tsx`, `ReleaseHistory.tsx`, `PublishReviewDialog.tsx`, Python SDK, and Playwright e2e tests covering mocked `ext`/`net` calls without secrets and versioned restore/compare.
