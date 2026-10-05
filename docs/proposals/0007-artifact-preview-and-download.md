# EP-0007: Artifact preview, download, and ZIP export

- **Status:** Draft
- **Created:** 2026-10-05
- **Area:** File API, runtime export contract, fs accessors, console, Python SDK
- **Related:** [G07 gap register](../business-scenarios-and-gaps.md), [EP-0002](0002-standard-library-utilities.md), [EP-0005](0005-instance-file-uploads.md), [EP-0006](0006-pluggable-instance-vfs.md)

## Summary

Complete the output workflow: **write files -> inspect a CSV table or HTML report
-> download one file or selected files/directories as ZIP**.

An artifact is initially an ordinary file in the instance's VFS, not a separate
persistent object. Scripts continue using `fs.write`; no publication call, disk
backend, S3 bucket, or artifact database table is required. Existing files become
usable outputs even before CSV utilities or upload are implemented.

Provide bounded CSV/text previews, isolated HTML presentation, authenticated file
attachments, and all-or-nothing ZIP export. Static HTML is the default. Interactive
HTML charts require an explicitly enabled isolated preview mode, with no console
credentials or application-origin privileges. All APIs and limits below are proposed.

## Motivation and existing behavior

The current [FileBrowser](../../web/src/pages/InstanceDetail.tsx) lists `/work`
and displays an entire selected file in a `<pre>`. CSV is not presented as a table,
HTML remains source text, and there is no download or archive action.

The [public API](../../api/openapi.yaml) returns JSON from
`GET /api/v1/instances/{id}/files`. [instance.Browse](../../internal/instance/browse.go)
uses gated fs accessors and redacts file content before it leaves the instance.
Those protections are the foundation for export, not obstacles to bypass.

A successful analysis that leaves `differences.csv` or `report.html` in memory
is not a complete user workflow if the user cannot inspect or take the result away.
This output gap is independently useful to close before adding more connectors.

## Goals

- CSV table preview, HTML report preview, and a source/text fallback.
- One-click single-file download and ZIP export of selected files/directories.
- Identical permissions, audit, and secret handling for preview and export.
- No partially delivered archive on policy, quota, or selection failure.
- Bounded work and memory across API, worker, subprocess, and browser.
- Clear lifecycle, truncation, redaction, unsupported-content, and error states.

## Non-goals

- Durable artifact retention, public sharing, cross-instance libraries, or restoring
  files after an instance expires or is lost.
- Hosting arbitrary applications, exposing the console API to artifact scripts,
  or allowing arbitrary CDN/network access for report dependencies.
- General binary-file export, PDF/XLSX viewers, image generation, archive extraction,
  or recursively serving a website from VFS in the initial increment.
- A new Starlark archive or browser capability. ZIP is a host export format, not a
  grant to execute a shell or compress arbitrary host paths.
- Replacing the memory VFS, implementing S3, or requiring EP-0005 upload first.

## Artifact model and user workflow

Use the existing VFS path, file size, and modification time as file metadata.
A UI kind such as CSV, HTML, JSON, or text is a presentation hint, not an authority
boundary. Validate content separately; renaming a file cannot bypass export rules.
Artifact listings must also suppress secret-bearing names at the node before
returning metadata. Current Browse name filtering is conditional on `ListOnly`;
do not assume normal directory listings already provide that guarantee.

Recommend `/work/output/` in agent examples, without reserving the directory or
hiding authorized files elsewhere. A typical script leaves:

```text
/work/output/differences.csv
/work/output/report.html
/work/output/summary.json
```

After an exec completes, including one that failed after creating files, refresh
the file list. Do not label every visible file a successfully completed report:
execution failure and partial script output remain visible to the user.

The instance detail page provides a Files / Artifacts panel with:

- A navigable list, kind/size metadata, checkboxes, and Download selected as ZIP.
- A preview pane with Preview / Source, Refresh, and Download actions.
- CSV row/column-limit notices and literal cell rendering, never formula execution.
- HTML static/interactive mode labels, dependency warnings, and an explicit Close
  preview action. Do not launch interactive scripts merely by selecting a file.
- Clear empty, loading, malformed CSV, unsupported encoding, denied, too large,
  preparing ZIP, request failed, and instance unavailable states.
- An expiry notice explaining that downloads must complete before instance data
  becomes unavailable; previewing does not silently keep the instance alive.

Keyboard users can select files, open/close previews, switch tabs, and initiate
exports. Use a titled iframe and table semantics; expose progress/error states to
assistive technology. Keep download controls outside untrusted HTML.

## Preview contracts

### CSV

Parse using a real CSV parser with quoted fields, escaped quotes, embedded newlines,
UTF-8/BOM handling, and explicit delimiter options. Server-side parsing can use
Go's existing `encoding/csv`; this UI feature does not depend on exposing a CSV
module to Starlark first. Share pure parsing code with EP-0002 where appropriate.

- Render cells as text. Keep values as strings, including leading zeros.
- Let users select whether the first row is a header. Empty/duplicate headers do
  not silently overwrite columns; use positional column identities in the viewer.
- Bound source bytes, displayed rows/columns, total cells, and cell display length.
  Report each kind of truncation explicitly. Preview truncation never truncates
  a successful download or archive member.
- Report malformed records without inserting field contents into error messages.
  Offer source view/download when CSV parsing fails but text export is permitted.
- Do not invent a total row count when only a prefix was parsed. A preview is not
  a guarantee that the rest of the document is well formed.
- Show spreadsheet-formula risk where relevant. Do not silently rewrite formulas
  or number formats during export; an explicitly transformed spreadsheet-safe
  export can be a future option separate from lossless text download.

### Text and JSON

Provide bounded source view. JSON may have a bounded pretty-print view, but the
exported file retains its original formatting except required secret redaction.
Other textual formats, including Markdown, fall back to plain text in v1 rather
than gaining another active-content renderer implicitly.

### HTML: static by default

Render a sanitized presentation inside an iframe with an empty `sandbox` attribute;
never use `dangerouslySetInnerHTML` in the console document. Static mode preserves
reviewed text/table/layout features and a reviewed safe SVG subset for charts, but
removes scripts, event handlers, forms, nested frames, objects, meta refresh, base
URL changes, navigation targets, and active/external resource references. Disable
report links rather than navigating the console or an embedded frame.

Use a reviewed HTML/SVG/CSS sanitization implementation, not regex substitutions.
The project currently has no declared HTML sanitizer; dependency selection and
its supported safe subset require explicit review and the normal release-age rule.
Do not let parsing untrusted markup in the console trigger resource fetches before
sanitization. Reject unsupported constructs or show source instead of weakening
isolation to make a report render.

Use restrictive CSP for the rendered document: deny scripts, connections, frames,
objects, forms, and base changes; permit only the needed inline styling and
validated embedded image data. Relative URLs must not resolve against the console
or become authenticated API calls. A trusted `srcdoc` wrapper may add an early CSP
meta tag for supported directives, but the actual iframe `sandbox` attribute is
mandatory: CSP `sandbox` is not supported in a meta tag [R2].

Sanitization affects the rendered view, not the source file or downloaded report.
Display a notice when features were removed. A script-dependent chart can be blank
in static mode; offer source, download, or explicit interactive mode rather than
claiming that its visualization was successfully previewed.

### HTML: isolated interactive charts

Interactive mode supports a self-contained report with embedded JavaScript/data.
It is an explicit user action, enabled only when the deployment provides a dedicated
preview origin. Without that configuration, retain static preview and downloads;
do not fall back to executing scripts at the console origin.

Required boundaries:

1. Use a dedicated origin with no console/API routes, cookies, or shared storage.
   Prefer a separate registrable domain; a different port alone does not isolate
   cookies. The console must not grant this origin credentialed CORS access.
2. Serve only an already authorized, redacted, immutable HTML snapshot. No user-
   supplied URL fetching, bucket browsing, directory serving, or arbitrary proxying.
3. The frame uses `sandbox="allow-scripts"`, never `allow-same-origin`, forms,
   popups, top navigation, downloads, storage access, or privileged device features.
   The response also enforces CSP `sandbox allow-scripts` as an HTTP header so
   opening the preview outside the parent frame does not restore origin privileges.
4. CSP denies connections, external scripts/styles/images/fonts, frames, objects,
   workers, forms, and base changes. Permit only the inline script/style and
   validated embedded image forms needed by the supported report profile. No
   `unsafe-eval`, implicit CDN downloads, or automatic dependency fetching.
5. No API key/session cookie is placed in HTML, iframe messages, or a preview URL.
   Access uses an opaque, short-lived, snapshot-scoped ticket minted after owner
   authorization and gated file reads. Treat that ticket as a bearer credential:
   do not log it, include it in analytics, or send it in referrers. It grants no
   ability to list files or call APIs. Recheck instance liveness and ticket expiry
   when serving it; invalidate outstanding tickets on deletion/expiry.
6. Set `Referrer-Policy: no-referrer`, `Cache-Control: private, no-store`, and
   `X-Content-Type-Options: nosniff`. Only the configured console may frame the
   preview. The console does not accept arbitrary messages as commands; any future
   size/readiness messages must validate the frame window, schema, and a bounded
   channel token, not trust an opaque frame's `origin: null` alone.

Browser JavaScript is outside Starlark's Gate, step counter, and memory quota.
Sandbox/CSP isolate application authority and restrict resource loading, but are
not a universal network firewall: self-navigation and browser differences mean
we must not claim complete egress isolation for arbitrary active HTML. Deployments
requiring no untrusted browser execution or strict no-egress behavior keep this
mode disabled. Explain that trade-off before enabling it. An isolated renderer
with enforced network controls is a separate design, not silently added here.

Render one active report at a time and discard the frame on close/switch. Size
limits and a close control do not provide a hard browser CPU/memory sandbox.
Do not promise to stop every infinite loop with a JavaScript timer.

## Export formats and secret handling

### Initial supported content

Export bounded UTF-8 textual files, including CSV, HTML, JSON, Markdown, CSS, and
JavaScript, without relying solely on filename extensions. Reject NUL-containing
or invalid UTF-8 input with an explicit unsupported-content error in v1. ZIP is a
server-generated container of those approved text files, not an unrestricted
binary-file read path. A selected PNG/PDF or existing ZIP is not silently omitted.

Opaque binary export needs a separately reviewed byte-preserving secret policy;
string replacement can corrupt binary formats. This limitation does not prevent
self-contained HTML reports with embedded image data. Do not claim support for
all file formats merely because ZIP itself is binary.

### One exportable representation

Apply the existing [secret redaction semantics](../../internal/secrets/secrets.go)
on the instance side after successful gated reads and before content reaches API
caches, preview parsing, ZIP compression, or download responses. Add a budget-aware
redaction path: calling unbounded `Set.Redact` and checking the result afterwards
is insufficient when replacements amplify a short input. Preserve matching and
encoding-variant behavior while bounding allocation before expansion. Use the
same exported text for Source, CSV parsing, HTML sanitization, and archive members.

- If no redaction occurs, preserve UTF-8 bytes, BOM, line endings, and formatting.
- If redaction occurs, flag the exported representation as redacted. Never offer
  an unredacted bypass for convenience or claim the result is byte-identical.
- Redaction can change CSV/HTML validity or numeric meaning. Report rendering
  errors honestly; do not repair a redacted report using the original secret.
- Known-secret pattern matching is not general PII classification or universal
  detection of arbitrarily encoded content. Do not expand that security claim.
- Validate names too: reject an export whose filename/path contains a known secret
  rather than leaking it through ZIP entries, headers, errors, or URLs generated
  by the console. Do not silently rename colliding paths after redaction.

Return a bounded redaction indicator in preview JSON and download/archive response
headers so UI/SDK clients can warn the user without exposing secret values.
Downloaded HTML executes outside calcside's viewer controls if opened locally;
make that distinction visible. The download action is not an endorsement of its code.

## Download and ZIP behavior

Single-file downloads use an authenticated attachment response with a safe basename,
`filename` plus UTF-8 `filename*` handling, `nosniff`, and private/no-store caching.
Never concatenate untrusted paths into headers. HTML from the authenticated console
origin is JSON/source data or an attachment, never unrestricted inline HTML.

ZIP selection accepts normalized file or directory paths under `/work`:

- Resolve paths, reject escapes, expand selected directories through gated typed
  listing/traversal, deduplicate overlaps, and use stable entry ordering. Reject
  an empty selection. Preserve selected empty directories without adding an
  absolute/root entry; exporting an empty `/work` yields a valid empty ZIP.
- Preserve paths relative to `/work`, rather than flattening all basenames. Check
  every archive name: no absolute paths, `..` components, backslashes, drive-like
  paths, control characters, or unsafe/colliding extraction names. Fail clearly
  on unsupported names, including case/normalization collisions under the chosen
  portable-name profile, rather than create an ambiguous archive.
- Authorize every traversal and file read. Approval to list a directory is not
  approval to read its children. An after-hook denial also excludes the content.
- Any denied, vanished, unsupported, oversized, or invalid selected entry fails
  the entire export. No silent skipping and no success response with a partial ZIP.
- Finalize the archive before writing successful download headers or body bytes.
  Use Go's `archive/zip`; never invoke an external zip command or read host paths.
  A subsequent network interruption can still leave a partial local download;
  UI/SDK clients must report transfer failure rather than successful completion.
- Do not write the archive back into VFS, expand source archives, include secrets
  metadata, or create a persistent artifact implicitly.

Preview and a later download are separate reads of mutable instance state. Show
when a preview was captured and provide Refresh; do not claim they are the same
revision. Each individual export is a consistent snapshot, as described below.

## API, runtime, and concurrency

Proposed public routes, alongside the unchanged existing browse endpoint:

| Route | Purpose |
|---|---|
| `GET /api/v1/instances/{id}/files/preview?path=...&kind=csv` | Bounded text/CSV/static-HTML preview data, safe metadata, limits, and redaction/truncation flags. |
| `GET /api/v1/instances/{id}/files/download?path=...` | One full exportable file as an attachment. |
| `POST /api/v1/instances/{id}/files/archive` | JSON selection such as `{"paths":["/work/output"]}`; complete ZIP attachment or a structured error. |
| `POST /api/v1/instances/{id}/files/preview-session` | Explicit interactive-preview request; returns only a short-lived scoped preview URL and expiry after checks. |

`kind` chooses a viewer, not a permission or encoding bypass. Mutating HTTP methods
use the existing CSRF protection even though export does not mutate script files.
API-key clients use normal owner authorization. Return stable errors for missing
fs, non-running instance, denied reads, path/selection errors, unsupported content,
limits, disabled interactive mode, and preparation failure. Never put auth keys in
a download link; ZIP POST and SDK downloads use authenticated response bodies.

Add a typed export/snapshot request to `runtime.Runtime` carrying owner, instance
ID, epoch, purpose, and bounded selection. Its result contains only approved,
redacted data, safe metadata, and `AuditBatch`. Implement every routing hop:
Manager, remote client, direct child transport, and subprocess supervisor.

The manager checks owner/epoch/liveness, acquires the instance serialization boundary
with cancellation/bounded waiting, rechecks liveness, and performs every fs operation
through `Gate.Invoke` using typed accessors. Capture the bounded redacted selection
and drain its audit batch before another exec can arm the same Gate. Release the
instance boundary before browser delivery; export must not hold `ExecMu` while a
slow client downloads. Serialize teardown against capture and cancel pending work.

Use that approved snapshot to parse previews or prepare ZIP at the API tier, with
separate admission, memory, size, and deadline bounds. Temporary snapshot buffers
are private, bounded, short-lived, and not authoritative storage. Do not call the
current whole-tree `VFS.Files()` copier, access the map directly, or hold locks
while copying an unbounded workspace.

Carry audit batches on success and failure. The current worker Browse adapter drops
its result when returning an error; the new export contract must preserve denied/
failed read audit records instead of copying that response pattern. Hook/audit
metadata includes only bounded operation metadata, never file contents or ZIP bytes.

The current worker protocol buffers JSON; its HMAC middleware limits request bodies
to 4 MiB. Export selections must stay within that bound. Responses need their own
explicit limits, including JSON/base64 expansion and multiple process-hop copies;
the request cap does not protect response memory. Start with bounded snapshots,
not an unsupported claim of end-to-end streaming. A future large-export protocol
must preserve Gate, redaction, completeness, and audit behavior across chunks.

Edit `api/openapi.yaml` and `api/worker.openapi.yaml` before `make gen`. Runtime
payloads live in `internal/runtime` with the existing `x-go-type` mapping. Strict
adapters pass `Request.Context()`, not pooled `*gin.Context`. Never hand-edit generated
clients, schemas, or injectors.

## Resource limits and lifecycle

Initial candidate defaults, to be confirmed under the deployment's child memory
cap before implementation acceptance:

| Limit | Candidate default |
|---|---|
| Source bytes per preview or single-file export | 1 MiB |
| CSV preview rows / columns / displayed cells | 200 / 50 / 10,000 |
| Plain source preview bytes / cell display bytes | 64 KiB / 16 KiB |
| Expanded ZIP entries / total uncompressed source bytes | 100 / 4 MiB |
| ZIP response bytes, including archive overhead | 5 MiB |
| Export preparation deadline | 10 seconds |
| Interactive snapshot ticket lifetime | At most 5 minutes and never beyond instance expiry |

Bound path length/depth, parser work, sanitizer work, redacted output expansion,
active requests, queued requests, and retained preview snapshots as well. Deployment
configuration may tighten these limits; no caller can request unlimited work.
Check file size with gated stat under the same capture boundary before reading
contents, and bound directory traversal before materializing a whole tree. Bound
sanitized DOM node count/depth and embedded image dimensions as well as source
bytes. Do not allocate first and enforce limits only on the final result.
Do not raise defaults without measuring encoded copies, existing VFS/global memory,
audit buffering, and concurrent users. Embedded chart libraries count against HTML
size limits; some require larger deployments or a different output profile.

Reject an oversized HTML preview instead of rendering truncated markup. CSV/text
may provide an explicitly limited preview, but download/export always succeeds in
full or fails. Never silently export only the preview rows.

Files remain instance-scoped. New reads fail after deletion, expiry, or loss;
release pending snapshot tickets/buffers on those events. Already delivered bytes
cannot be revoked. API/worker restart may invalidate pending previews/exports, and
clients must not mistake temporary snapshot availability for durable retention.
No store schema change or artifact retention policy is introduced in v1.

## SDK and agent integration

Extend the web client with binary/attachment response handling while preserving
its authentication, CSRF, and error behavior; the current helper assumes JSON.
Distinguish receiving request/response bytes from successful ZIP preparation.
Revoke browser object URLs and release preview frames/buffers on close or navigation.

Add sync/async Python SDK helpers for file download and ZIP selection. Stream the
already prepared HTTP response to a caller-controlled sink where practical; that
does not mean the internal snapshot preparation is streaming. Expose response
warnings and redaction state; leave local overwrite decisions to the caller.

Update the agent prompt to explain that writing CSV/HTML files creates downloadable
outputs, recommend self-contained reports and `/work/output`, and ask the agent to
return paths plus a concise summary. Do not dump the entire report into model context
or imply that every CDN-dependent chart will work in the restricted viewer.

## Delivery and acceptance

1. Add bounded gated export snapshots and single-file/ZIP endpoints on memory VFS,
   with SDK support and transport/error/audit tests.
2. Add CSV/text/static-HTML previews and file selection/download UI. This is an
   independently useful initial release; it does not claim JS chart support.
3. Enable interactive HTML only behind its separate-origin deployment requirements
   and browser security tests. This remains part of the proposal, not an implicit
   same-origin fallback when static previews are insufficient.

Acceptance scenarios:

- A script writes CSV and a static HTML/SVG report; the console previews both,
  downloads either, and produces a ZIP with both at their expected relative paths.
- An inline-JavaScript chart renders in configured interactive mode; without that
  mode it has a clear static limitation rather than an unexplained empty panel.
- CSV fixtures cover quoting, BOM, multiline cells, duplicate headers, formulas,
  malformed rows, and display limits. Downloads contain all authorized rows.
- Raw/encoded known secrets in content are redacted before preview/export; unsafe
  names and unsupported binary members fail without leaking bytes or metadata.
- Cross-user/epoch requests, missing fs, denied nested reads, and after-hook denials
  fail consistently through direct, remote, and subprocess runtimes, with audit.
- ZIP tests cover traversal, backslashes, overlapping selections, portable-name
  collisions, empty directories, file-count/size limits, and no partial success.
- Upload/script write/delete/expiry races cannot mix files from different capture
  states. Cancellation, encoding/compression overhead, queues, and capped-child
  memory tests exercise peak allocations rather than only final output size.
- Browser tests cover script/event-handler injection, SVG/CSS/URL payloads, meta
  refresh, parent DOM/storage access, API reads/writes, forms, popups, top navigation,
  external resources, forged messages, opened-in-new-tab previews, and ticket expiry.
  Test supported browsers; unit tests alone cannot establish iframe/CSP behavior.

Run Go checks, `make gen-check`, web typecheck/lint/build/tests, and SDK tests for
implementation changes. The current web suite is Node-only, so explicitly select
browser test tooling before claiming the security matrix is automated. No preview,
sanitization or browser-test dependency is assumed to be installed today;
the proposed ZIP writer is part of Go's standard library.

## Open questions

- Which reviewed sanitizer and safe HTML/SVG/CSS profile provide useful static
  reports without an excessive compatibility or maintenance burden?
- Is opt-in interactive preview needed in the first release, or can static reports
  ship first? It must not weaken the defined origin/credential boundaries.
- Do real report sizes fit the initial bounds, especially embedded chart libraries?
  If not, design bounded transfer before simply raising memory limits.
- What exact binary export policy should follow text/ZIP support? Retention and
  public sharing require a separate lifecycle/authorization proposal as well.

## References

- [R1: iframe sandbox behavior and origin caveats](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/iframe)
- [R2: CSP sandbox and its HTTP-header requirement](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Content-Security-Policy/sandbox)
- [R3: Content-Disposition attachment and filename handling](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Content-Disposition)
