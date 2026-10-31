# EP-0005: Upload files into an instance's virtual filesystem

- **Status:** Implemented (v1, chunked transfer still future work)
- **Created:** 2026-10-05
- **Revised:** 2026-10-31 — added implementation research; no code has shipped.
- **Revised:** 2026-10-31 — first increment shipped end to end.
- **Area:** Public/worker APIs, runtime, instance manager, fs capability, web, Python SDK
- **Related:** [EP-0002](0002-standard-library-utilities.md), [EP-0006](0006-pluggable-instance-vfs.md), [EP-0001](0001-record-replay.md), [EP-0007](0007-artifact-preview-and-download.md)

## Implementation status

As of 2026-10-31 the bounded single-file upload is implemented end to
end: `PUT /api/v1/instances/{id}/files?path=...&overwrite=...`
(`application/octet-stream`, 1 MiB ceiling) routes through
`worker.Client` -> `remote.Direct` -> `subproc.Supervisor` ->
`instance.Manager.Write` into a gated `fs.write` call on the VFS, with a
receipt-style `WriteResponse` (error + audit + `Committed` on the 200
path), conflict/quota/read-only/denied codes, create-only default,
sliding-TTL renewal on committed writes, SDK `upload_file`
(sync+async), and a console upload queue (button + drop target +
progress + overwrite confirm) in `ArtifactBrowser`. Text browse now
rejects non-UTF-8/NUL file contents explicitly. Chunked transfer and
record/replay interaction remain open. The research table below is
kept for the follow-up increments.

| Requirement | Current state | Location |
|---|---|---|
| `PUT /instances/{id}/files` | Absent; only `GET .../files` exists | `api/openapi.yaml` (`/api/v1/instances/{id}/files`) |
| Runtime write contract | `runtime.Runtime` exposes Create/Exec/Keepalive/Delete/**Browse**/Export/Prompt/Inspect only | `internal/runtime/runtime.go` |
| Cross-hop forwarding | Browse is implemented in the worker client, `remote.Direct`, `subproc.Supervisor`, and `instance.Manager`; a write must follow the same four hops | `internal/runtime/message.go`, `internal/runtime/remote/direct.go`, `internal/runtime/subproc/supervisor.go`, `internal/runtime/instance/browse.go` |
| Byte-oriented fs write | `VFS.Write` takes a string and always replaces; `Accessor` has Read/Stat/List/ListBounded but no write | `internal/capability/fs/fs.go` |
| Gate/policy/audit | `withConsole` already arms a console `fs` session; browse returns audit on gated failures except `ErrNoCapability` | `internal/runtime/instance/instance.go` (`withConsole`), `internal/runtime/instance/browse.go` |
| Body limits | Public mutating bodies are capped at 1 MiB; worker HMAC bodies at 4 MiB (16 MiB for prepare) | `cmd/calcside/internal/api/api.go` (`maxBodyBytes`, `bodyLimit`), `internal/runtime/remote/server.go` |
| Stable errors | No conflict/quota/read-only/upload-too-large sentinels; `sandbox.fail` defaults to `bad_spec` | `internal/runtime/runtime.go`, `cmd/calcside/internal/service/sandbox/sandbox.go`, `internal/types/types.go` |
| Binary-safe preview | `GET .../files` returns a Go string; JSON encoding replaces invalid UTF-8 with U+FFFD. Only artifact export rejects non-UTF-8 | `internal/runtime/instance/browse.go`, `internal/runtime/instance/artifacts.go` |
| SDK | `files()` only; no upload method | `sdk/python/src/calcside/client.py` |
| Console | `ArtifactBrowser` lists, previews and downloads; no upload, drop target, queue or progress | `web/src/components/Artifacts.tsx`, `web/src/pages/InstanceDetail.tsx` |
| Route authorization coverage | A new operation must be registered or `TestRouteCoverage` fails | `cmd/calcside/internal/api/workspaces_test.go` |
| Record/replay interaction | EP-0001 is still a draft and no recorder exists, so the "reject uploads while recording" guard has nothing to hook into yet | `docs/proposals/0001-record-replay.md` |

Constraints confirmed by the research:

- The memory VFS already charges an overwrite as `new size - old size` under its
  mutation lock, so upload quota needs no second counter in the HTTP handler.
- One MiB of raw bytes becomes roughly 1.37 MiB after base64 in the internal
  JSON contract and still fits the 4 MiB worker limit; larger uploads must not
  be implemented by raising that limit.
- `withConsole` takes `ExecMu` without honoring cancellation, while
  `lockRequest` polls with the request context. Upload must consume the bounded
  public body first, then take the instance locks in the same order as export
  (`requestMu` -> `ExecMu` -> `artifactMu`) and re-check liveness/epoch after
  taking them.
- A successful write should renew the sliding TTL through `RenewedExpiresAt`,
  as exec does; browse does not renew today.
- An after-hook denial happens after the bytes may be committed. The write
  receipt therefore needs an explicit committed/pre-commit distinction, not
  only a generic error envelope.
- Deployments can lower the public ceiling only if it becomes configuration; the
  artifact limits today are constants and `MaxFSQuotaBytes` is hard-coded.
- No store migration is needed for v1.

Proposed delivery order: (1) `VFS.WriteBytes` with create-only/overwrite and
tests; (2) runtime request/response, worker schema, generated adapters, and all
forwarding implementations; (3) service/API route, access action, error
mapping, admission bounds and audit-on-error tests; (4) reject non-UTF-8/NUL
file preview; (5) SDK sync/async upload; (6) console upload queue and browser
coverage; chunked transfer remains a separate increment.

Decisions still needed before coding: whether upload requires `Run` rather than
`View`; whether the first ceiling is a constant or a deployment setting; and
whether a committed-but-errored write returns a 200 receipt (like artifact
export) or a 4xx with an extended error envelope.

## Summary

Allow an instance owner to upload files from the console or SDK into `/work`.
Uploads are typed filesystem operations, not generated Starlark source. They use
the same authorization, Gate, policy, quota, and namespace rules as script writes.

Keep the in-memory filesystem as the initial implementation. Uploads target the
VFS contract, not an OS path, and do not require a disk backend, POSIX rename,
shared API/worker storage, or object storage.

Ship a bounded single-file upload first. Larger transfers require a subsequent
bounded-chunk protocol; do not increase request limits and call that streaming.
All routes and limits below are proposed.

Artifact preview, single-file download, and ZIP export are specified separately
in [EP-0007](0007-artifact-preview-and-download.md). They can be delivered before
upload using files that scripts already produce; both proposals retain memory
VFS and the existing gated read/redaction boundary.

## Motivation and current state

The existing `GET /api/v1/instances/{id}/files` lists paths or returns redacted
file contents. There is no direct write endpoint. Users must embed input text in
code or fetch it from a remote service, which adds quoting problems, context cost,
and unnecessary network grants.

Typical workflows are:

- Upload `orders.csv` and `payments.csv`, run a reconciliation script, inspect the
  resulting differences using EP-0002's CSV utilities.
- Upload a JSON fixture or configuration, then reuse it across several execs.
- Upload an opaque binary asset for storage or explicit encoding/decoding, without
  pretending it is UTF-8 or promising document extraction.

`instance.Browse` already demonstrates owner/epoch validation and a gated console
operation. Its current remote error adapter drops audit batches on failures; the
new write protocol must not repeat that behavior.

## Goals

- Console file picker and drag-and-drop, with destination, progress, and errors.
- Sync and async Python SDK upload methods that do not construct source code.
- Byte-preserving storage, safe conflict behavior, and no partially visible file.
- Identical behavior through direct, remote-worker, and child-process runtimes.
- Bounded request buffers and explicit failure/commit semantics.

## Non-goals

- Folder import, archive extraction, resumable transfers across worker restarts,
  file synchronization, or URL-to-file fetching.
- Durable artifacts, public download links, or instance state recovery.
- Automatically granting `fs`, making a read-only instance writable, or changing
  a user's policies to allow an upload.
- An unrestricted binary download route. Existing read/redaction rules remain.

## First-release API

Propose:

```text
PUT /api/v1/instances/{id}/files?path=/work/orders.csv&overwrite=false
Content-Type: application/octet-stream
```

The request body is the file's raw bytes. One request writes one file; the UI may
queue several independent requests. A successful response contains the normalized
virtual path, size, mtime, and whether an existing file was replaced. It never
returns uploaded contents or a host/storage path.

- `path` is explicit. The browser's filename is only an editable suggestion.
- `overwrite` defaults to false. Existing files return a conflict instead of being
  silently replaced. Overwrite requires explicit UI confirmation or an SDK flag.
- An empty body is a valid empty file. A directory target and `/work` are invalid.
- Reuse VFS relative and `/work` path normalization; reject escapes, NUL, and
  excessive path length/depth. Do not interpret a multipart filename, browser
  pseudo-path, or object key as an authorized destination.
- Preserve bytes exactly. Do not normalize newlines or attempt content sniffing
  as a parser. MIME/extension labels are display hints, not trust decisions.
- Authenticate and authorize the owner before reading a large body. Browser
  sessions use existing CSRF protections; API keys obey normal instance ownership.

Proposed initial hard ceiling: **1 MiB per file**, lowerable by deployment policy.
The UI reads the effective ceiling from server metadata instead of hard-coding it.
The ceiling is independent of the instance's total `fs.quota_bytes` and may be
lower still when the file would exceed its quota or a deployment's memory budget.

Use an actual bounded reader with an extra byte to detect overflow, even if
`Content-Length` is absent or incorrect. Return 413 before forwarding an oversized
body. The UI uploads one file at a time per instance initially. API and node
admission/queue limits must independently bound concurrent buffered requests,
including those from multiple clients; serializing only the final writes does
not prevent many waiting request bodies from exhausting memory.

### Errors and uncertain outcomes

Use stable errors for invalid path, conflict, file too large, quota exceeded,
read-only filesystem, missing capability, policy denial, instance not running,
stale placement, cancellation, and backend failure.

Storage failures before commit leave an existing file unchanged. A connection
failure after commit can leave the caller uncertain; do not report cancellation
as proof that nothing was written, and do not retry automatically. The UI offers
Refresh and an explicit retry/replacement action rather than blind resubmission.
A create-only retry cannot silently overwrite a file created by the first attempt.

## Runtime and worker protocol

Add a typed write request/response to `internal/runtime` and a method to
`runtime.Runtime`, implemented by `remote.Client`, `remote.Direct`,
`subproc.Supervisor`, and `instance.Manager`.

The request carries instance ID, owner, fencing epoch, normalized path, overwrite
mode, and bytes. The response carries write outcome, bounded error information,
and `AuditBatch`, including for policy-denied or failed writes that reached the
instance. Update the worker schema and generated strict adapters so that an error
cannot discard this receipt/audit data.

The current worker transport is JSON/HMAC and buffers the serialized body; its
server reads at most 4 MiB. For v1, encode the bounded byte payload as base64 in the
internal JSON contract. One MiB raw fits with headroom, but tests must account for
all envelope/base64 overhead and enforce the effective encoded limit on every hop.
This is deliberately bounded buffering, not streaming. Do not globally relax
worker authentication, request size limits, or retry rules.

Public `application/octet-stream` parsing needs a transport adapter outside normal
JSON binding. Keep the route declared in `api/openapi.yaml`; use generated route
integration and an explicit bounded body reader rather than bypassing auth. Worker
operation declarations belong in `api/worker.openapi.yaml`, with payloads mapped
to runtime contract types via the established `x-go-type` pattern.

Public and internal adapters pass `Request.Context()`, never pooled `*gin.Context`.
Forward only bounded data; the API tier does not retain files in a database or
create a second authoritative filesystem. Routing goes to the instance's owner,
not whichever API machine handled the upload.

## Gate, concurrency, and quota semantics

Extend the fs typed accessor with a byte-oriented host write method. It invokes
`Gate.Invoke` as `fs.write`, using the normalized path and byte count as metadata,
with the same semantics as the script operation. File contents never enter
Call/Result metadata, audit, error text, tracing, or request-body logs.

At the manager:

1. Resolve the live instance and verify owner and fencing epoch.
2. Acquire the instance execution serialization boundary with cancellation and a
   bounded wait; recheck liveness/epoch before mutation.
3. Arm a console-operation Gate context, invoke the accessor, then disarm it.
4. Return the write outcome and audit batch even on a gated operation error.

This prevents script execution, upload, and instance termination from racing over
file contents or an armed Gate. Consume the bounded public body before taking the
instance execution lock. Document lock ordering with manager lifecycle and backend
locks; do not copy `withConsole` without reviewing cancellation/deletion races.

Preserve current Gate semantics: before-hook denial prevents writing; after hooks
observe an operation that may already have committed. An after-hook denial is not
transaction rollback. The new write receipt must distinguish a known committed
write from a pre-commit failure, even when the request returns an error. The UI
must explain that distinction. Atomic file replacement is a storage guarantee,
not a claim that all policy failures undo side effects.

The VFS validates total bytes and file count under the same mutation lock used by
script writes. For an overwrite, charge the new size minus the old size, while
separately bounding temporary memory/staging. Failed writes do not leak quota.
A storage abstraction must not introduce a second, competing quota counter in the
HTTP handler. A successful write can renew sliding TTL using the API's existing
owner of expiry semantics; reads or failed attempts do not do so implicitly.

## Byte handling and the file browser

The upload route stores bytes without converting through JSON strings at the
public boundary. Internal base64 is decoded exactly once before VFS mutation.

The existing browser returns text in JSON. Before enabling arbitrary uploads,
update text browsing to reject invalid UTF-8/binary preview explicitly rather
than corrupt bytes with replacement characters. Keep secret redaction for text
previews, and bound preview work before loading content. Binary files appear with
metadata and a no-preview state. Scripts may use existing Starlark string byte
semantics and explicit encoding utilities; this does not introduce automatic
XLSX/PDF/image interpretation or a raw, unredacted download escape hatch.

Uploads can contain sensitive business data that is not a vault secret. Do not
claim the existing secret-redaction mechanism classifies or removes all such data.

## Console and SDK experience

Enhance `FileBrowser` in `web/src/pages/InstanceDetail.tsx` with:

- Upload button and drop target when a running instance has writable `fs`.
- Destination directory/path, size validation, conflict confirmation, and a
  per-file queue with queued/uploading/succeeded/failed/cancelled states.
- Byte progress using a browser transport that actually reports it. Request bytes
  sent is not the same as commit complete; show a finalizing state until receipt.
- Refresh listing and invalidate relevant inspector/completion queries on success.
- Preserve pending choices and errors, and explain quota, policy, and lifecycle
  failures without treating all of them as a generic network error.

Add `Client.upload_file(instance_id, path, content, overwrite=False)` and its async
counterpart. Define accepted bytes/file-like inputs and bound reads in both. A
caller reading a local file does so in its own application; the server is never
given a local-machine path to open. Return structured file metadata and errors.

## Larger-file follow-up

Larger uploads should use bounded chunks, not a bigger base64 object:

- Begin a transfer with a path, declared size, and conflict mode; reserve logical
  quota and a separate backend staging budget.
- Route each bounded chunk to the same instance/epoch. Number chunks, reject gaps,
  make an identical retransmission idempotent, and reject mismatched duplicates.
- Keep staging invisible to `fs.read/list`; finalize with the authoritative
  `fs.write` checks under the instance lock and one atomic visible replacement.
- Authenticate every transfer operation, audit bounded transfer metadata, expire
  abandoned transfers, and clean staging on abort, deletion, or worker loss.
- An in-memory backend still stores those chunks in memory; chunking reduces
  transport peaks, not total resident file bytes. A future S3 backend can stage
  multipart objects, but multipart completion alone is not the VFS quota/namespace
  transaction. See EP-0006.

This protocol is a separate implementation increment. Do not advertise resumable
or large uploads until its backend-neutral semantics and resource budgets exist.

## Replay interaction

EP-0001 currently assumes console operations do not mutate instance state. Uploads
invalidate that assumption. A recorded instance needs ordered input-file mutation
events between execs, including contents in the protected recording store, or it
must reject uploads while recording. File hashes alone cannot rebuild inputs.

Do not put input contents into audit events. Until replay supports these events,
choose explicit rejection for recorded instances rather than producing a tape
that silently cannot reconstruct its filesystem. EP-0001 is still a draft today.

## Delivery, verification, and acceptance

1. Add a backend-neutral typed write operation and tests; preserve the memory VFS.
2. Add public/worker contracts, generated adapters, audit/error receipts, and all
   Runtime forwarding implementations.
3. Add SDK and console upload/preview behavior.
4. Design and implement chunked transfer only when larger inputs justify it.

Acceptance covers byte equality (including empty/binary files), create/overwrite,
quota deltas, read-only/missing fs, owner/epoch rejection, path traversal, truncated
bodies, cancellation before/after commit, after-hook denial, and audit on errors.
Run the same tests through direct, remote, and subprocess runtimes. Stress upload
versus exec/delete, memory limits, concurrent tenants, and request size enforcement.

Update OpenAPI first and run `make gen`, Go checks, `make gen-check`, web checks,
and SDK tests. Browser progress/drop/conflict flows require explicit interaction
coverage in addition to the current Node-only tests. No store migration is needed
for v1: the upload is instance data, not a new persistent store entity.

## Open questions

- Whether the initial 1 MiB ceiling covers the first CSV/JSON use cases; if not,
  prioritize chunked transfer instead of stretching the worker body limit.
- Whether to later add content-version preconditions for replacements made by
  multiple clients. An explicit overwrite is initially last-writer-wins.
- General binary artifact export remains a separate design question. EP-0007
  covers bounded text downloads and generated ZIP containers without bypassing
  the project's secret-handling contract; it does not enable arbitrary binary reads.
