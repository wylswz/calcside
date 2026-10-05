# EP-0002: Bounded built-in utility libraries

- **Status:** Draft
- **Created:** 2026-10-05
- **Area:** Starlark environment, extension loader, completion metadata, agent prompt
- **Related:** [EP-0003](0003-community-extensions.md), [EP-0005](0005-instance-file-uploads.md), [EP-0006](0006-pluggable-instance-vfs.md)

## Implementation status

The initial six modules (`url`, `csv`, `base64`, `hashlib`, `regex`, `datetime`)
are implemented as bounded pure utilities, shared by scripts, extensions, prompt
and completion metadata. The registry in `internal/stdlib` and repository README
describe the shipped names, signatures and limits; design alternatives below are
not additional implemented APIs. No I/O, host clock or new capability grants are
introduced.

## Summary

Provide a documented, deterministic standard library for common agent data
handling, starting with URL and CSV operations, followed by encoding, hashing,
regular expressions, and explicit date/time conversion. Utilities are pure
functions over supplied values, not new capabilities: they cannot read files,
make requests, inspect the host, read secrets, or obtain the current time.

Use the same implementation and metadata in instance scripts and community
extensions. New APIs and defaults below are proposals, not existing features.

## Motivation

The engine and extension module initializer currently expose `json` and `math`
separately. Scripts otherwise have to implement routine transformations or ask
the model to manipulate data itself. Examples include:

- Construct a search URL without corrupting Unicode, repeated query values, or
  reserved characters.
- Read uploaded orders and payments as CSV, join by ID, and write discrepancies.
- Decode a provider's base64 payload or compute a checksum without a network call.
- Normalize explicit timestamps before comparing events from several APIs.

The implementation entry points are `internal/engine/engine.go`,
`internal/capability/ext/module.go`, `internal/service/catalog/catalog.go`, and
`internal/prompt/prompt.md.tmpl`. Adding only engine globals would leave extensions,
editor completion, and agent instructions inconsistent.

## Goals

- Familiar, small APIs with examples usable by code-generating models.
- Identical results for identical inputs on a pinned runtime version.
- Predictable memory and CPU bounds, including work inside Go builtins.
- Shared runtime bindings, signatures, documentation, and completion metadata.
- No expansion of filesystem, network, secret, or Rego privileges.

## Non-goals

- Python compatibility, `pip`, native extension plugins, pandas, or NumPy.
- Utilities that implicitly load files, fetch URLs, or use ambient configuration.
- Wall-clock access, sleep, random numbers, local timezone discovery, or locale
  dependent behavior.
- XLSX/PDF parsing, archive extraction, YAML, or a general templating engine in
  the first release. These need separate complexity and resource-limit reviews.

## Proposed library surface

| Module | Initial functions | Semantics |
|---|---|---|
| `url` | `parse`, `resolve`, `query_encode`, `query_decode`, `path_escape`, `path_unescape` | Pure URL/reference manipulation; never resolves DNS or authorizes a request. |
| `csv` | `parse`, `parse_dicts`, `format`, `format_dicts` | String input/output; explicit delimiter, columns, and bounded row counts. |
| `base64` | `encode`, `decode` | Explicit standard or URL-safe alphabet and padding behavior; strict decoding. |
| `hashlib` | `sha256` | Lowercase hexadecimal digest of caller-supplied bytes or UTF-8 text; no secret references are expanded. |
| `regex` | `search`, `find_all`, `replace`, `split` | Go RE2 semantics, not Python regex; pattern and output bounds. |
| `datetime` | `parse_rfc3339`, `format_rfc3339`, `parse_date`, `format_date` | Explicit UTC/offset inputs and integer epoch units; no `now()` or system timezone. |

`url` and `csv` are the first delivery. The remaining modules are a second,
independently testable tranche, not prerequisites for file upload or contrib.
Existing `json` and `math` keep their public behavior.

### URL contract

- `parse(text)` returns a fixed-shape dict containing scheme, hostname, port,
  path, raw query, and fragment. Reject userinfo in the initial API rather than
  accidentally exposing embedded credentials through diagnostic messages.
- `resolve(base, reference)` follows URI reference resolution. It may produce a
  different host; it is explicitly not a same-origin or allowlist validator.
- `query_encode(values)` accepts a dict of strings to strings or lists of
  strings. Sort keys; preserve the order of repeated values.
- `query_decode(text)` returns a dict of strings to lists of strings. It must not
  silently discard duplicate values.
- Distinguish query escaping from path-segment escaping. Define treatment of
  spaces, `+`, percent escapes, Unicode, and malformed encodings in tests.
- `net` remains the authority for schemes, destinations, and secret injection.
  Utilities do not expand or reinterpret `{{secrets.NAME}}` placeholders.

### CSV contract

- Parse quoted fields, embedded newlines, escaped quotes, CRLF and LF using a
  standards-based parser, not `split(',')`.
- Accept UTF-8 text with an optional leading BOM. Reject invalid UTF-8; do not
  perform implicit encoding detection.
- Keep cells as strings. Callers explicitly convert numbers, booleans, and dates.
  Financial examples use integer minor units, not binary floating point.
- `parse` returns rows. `parse_dicts` consumes a header row and rejects duplicate
  or empty header names and inconsistent row widths by default.
- A delimiter must be one permitted character. Do not guess delimiters or headers.
- `format_dicts` requires explicit `columns`, so dict iteration cannot change
  output shape. Default output uses LF with a trailing newline.
- Spreadsheet formula neutralization is an explicit formatting option, disabled
  for lossless data round-trips. Export examples intended for spreadsheets enable
  it and explain that it changes values; it is not HTML sanitization.
- Errors contain row/column positions and error codes, not cell contents.

Illustrative script using the proposed API:

```python
rows = csv.parse_dicts(fs.read("orders.csv"))
large = [r for r in rows if int(r["amount_minor"]) > 100000]
fs.write("large-orders.csv", csv.format_dicts(large, columns=["id", "amount_minor"]))
print(json.encode({"matched": len(large), "path": "/work/large-orders.csv"}))
```

Every `fs` access still goes through the Gate. Parsing the returned string does
not, just as `json.decode` does not today.

## Runtime and metadata design

Introduce a shared utility registry, provisionally `internal/stdlib`, containing
module factories and explicit function descriptors. An engine session and an
extension initialization both obtain the same frozen bindings from it. Returned
collections belong to the invocation; no mutable cross-instance state is shared.

Descriptors provide parameter names, result descriptions, concise docs, and
examples. They feed the agent prompt and editor metadata, with full details
available on demand instead of appending every example to every prompt. Update
`api/openapi.yaml` before regenerating metadata clients with `make gen`.

Utilities are separate from `CapabilityName` and `spec.capabilities`. They have no
policy grant switch or per-call audit event, and they do not become Rego builtins.
Module names must not shadow `starlark.Universe`: use `hashlib`, not `hash`, which
is already a builtin and is rejected by `ValidatePredeclared`. New module names
become reserved predeclared bindings; release notes must warn about collisions
with scripts previously using those names.

## Resource model

Starlark step limits do not count all work performed inside a Go builtin. Every
new utility therefore enforces independent bounds before allocating output.

Proposed first-release ceilings, validated against the default child memory cap:

| Bound | Initial ceiling |
|---|---|
| Input bytes per call | 8 MiB |
| Serialized/text output bytes per call | 8 MiB |
| CSV rows / total cells | 10,000 / 100,000 |
| CSV columns / field bytes | 256 / 256 KiB |
| URL bytes / query pairs | 64 KiB / 1,024 |
| Regex pattern bytes / matches | 8 KiB / 10,000 |

These limits compose: passing one does not exempt the others. Account for the
materialized list/dict representation as well as input bytes; reject while
building rather than after constructing a huge result. Function options may
lower ceilings but never raise deployment limits. Do not implement a limit by
silently returning partial data.

Long loops check the execution context for cancellation. Extension initialization
must receive a bounded context too; step-limited module initialization alone is
not sufficient. Regex caches, if introduced, are bounded and never keyed by
unbounded tenant data. Prefer Go's existing standard library over new dependencies.

Base64 decoding returns an explicitly documented byte value rather than silently
coercing arbitrary bytes into UTF-8. Text-only CSV and URL functions reject byte
inputs until explicitly decoded. Do not introduce decompression in this tranche.

## Compatibility and security

- No process environment, filesystem path lookup, network, or host clock access.
- Errors must not echo raw inputs; utility calls can handle sensitive business data.
- A module name or URL parsing result is not an authorization decision.
- Keep runtime/library versions in the eventual EP-0001 replay manifest. Utility
  behavior changes can affect outputs and step counts.
- A future non-memory backend does not remove parsing memory costs. An uploaded
  file can be larger than the materialization limit and therefore require a later
  bounded streaming API; this proposal does not claim unbounded dataset support.

## Delivery and acceptance

1. Add the shared registry and metadata contract without changing `json`/`math`.
2. Implement URL and CSV with reference tests and documentation examples.
3. Wire instance globals, extension globals, prompt, completion, and SDK examples.
4. Add encoding/hash, then regex and datetime after their bounds are tested.

Acceptance includes:

- The same utility script works in a direct instance and a contrib extension.
- URL duplicate parameters, Unicode, malformed escapes, and cross-host resolution.
- CSV quoting, BOM, empty files, duplicate headers, malformed rows, formula cells,
  and deterministic formatting.
- Limit and cancellation tests, fuzzed parsers, and memory tests in a capped child.
- No changes to the capability catalog or Rego builtin allowlist, and no
  collisions with Starlark builtin names.
- Go checks, web metadata/completion tests, `make gen-check`, and SDK tests for
  examples that cross the public API.

## Open questions

- Final public names and exact return types for byte-oriented and datetime APIs.
- Whether deployment-specific ceilings need public configuration immediately or
  can begin as documented constants.
- Which additional pure utility is justified by a real contrib use case, rather
  than expanding the standard library speculatively.
