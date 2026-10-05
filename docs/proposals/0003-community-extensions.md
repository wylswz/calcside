# EP-0003: Community extensions in contrib and a metadata catalog

- **Status:** Draft
- **Created:** 2026-10-05
- **Area:** `contrib`, extension manifests/loader/catalog, packaging, API, CI
- **Related:** [EP-0002](0002-standard-library-utilities.md), [EP-0004](0004-extension-discovery-ui.md)

## Summary

Make extensions a maintained product surface rather than example assets. Add a
repository-level `contrib/` directory for community extensions and move the
existing Tavily extension to `contrib/tavily/`. Define validated presentation
metadata, safe packaged icons, contribution checks, and an indexed catalog that
can support hundreds of entries.

The runtime remains Starlark-only and opt-in. Being shipped, searchable, or
community-maintained does not grant an extension any capability or credential.
This proposal describes a future move; it does not move files by itself.

## Motivation and current state

- The only checked-in provider extension lives in `examples/capabilities/tavily`.
- `Makefile`, the LangChain example, extension docs, and a Compose comment refer
  to that path. Local copies under `docker/data` and `docker/worker-cache` are not
  tracked source and must not be migrated or deleted automatically.
- `CapabilityManifest` provides name, version, description, dependencies, ops,
  and config. Unknown fields are rejected by the YAML decoder.
- `factory.Available()` currently scans local roots and loads/compiles extension
  trees on each call. That work should not happen on every catalog search.
- Remote identifiers identify a repository root, not an arbitrary monorepo
  subdirectory. Moving Tavily into contrib does not magically make a nested git
  URL a valid install source.

## Goals

- One canonical home for community-maintained extension implementations.
- Names, descriptions, categories, tags, and icons suitable for a rich catalog.
- Safe default discovery and reproducible selection of a specific source tree.
- Deterministic, offline contribution checks without provider credentials.
- Preserve custom local and explicitly pinned remote extension sources.

## Non-goals

- A hosted marketplace, automatic installation from the internet, ratings, or
  popularity tracking.
- Native plugins, extension-to-extension dependencies, new secret access, or
  implicit capability grants.
- Arbitrary remote image fetching or rendering contributor HTML in the console.
- Changing the remote source grammar to support monorepo subdirectories in v1.

## Repository layout and contribution contract

```text
contrib/
  README.md
  tavily/
    capability.yaml
    main.star
    icon.png
    README.md
    testdata/
```

The contrib guide defines required docs, fixture conventions, ownership, licensing,
and a credential-free test harness. Per-extension docs include setup, supported
operations, required domains, secret names, bounded examples, known limitations,
and error behavior. Fixtures use synthetic data and mock HTTP services.

The move preserves Tavily's existing `search` signature and configuration. Adding
`extract` is a separate enhancement, not hidden inside a packaging change.

All files in a runtime extension tree remain subject to the existing 256-file,
1 MiB tree limit and regular-file restrictions. Fixtures that exceed that budget
belong outside the shipped tree. No new dependency is implied by this layout.

## Manifest additions

Add optional fields to `CapabilityManifest`; keep old manifests valid:

```yaml
name: tavily
version: 0.1.0
display_name: Tavily
description: Search the web and return structured source results.
category: research
tags: [search, web, sources]
icon: icon.png
license: MIT
maintainers: [calcside-contributors]
dependencies: [net]
```

This is a metadata excerpt; existing `ops` and `config` sections are retained.
The displayed maintainer and license must reflect actual contribution review.

- `name` remains the machine identifier; `display_name` is presentation only.
- Require nonempty description, category, license, and maintainer information for
  contrib entries in CI. They remain optional for preexisting custom extensions.
- Bound every string/list. Suggested limits: display name 80 characters,
  description 300, up to 12 tags of 32 characters, and an icon path of 128 bytes.
- Start with categories such as research, developer, productivity, data, and
  other. Category validation is versioned, not inferred from arbitrary HTML.
- `icon` is a relative regular PNG file inside the verified extension tree.
  No URLs, absolute paths, traversal, symlinks, SVG, or data URLs in v1.
- Missing metadata falls back to machine name, an uncategorized label, and a
  deterministic monogram icon. A broken optional image must not hide the entry.

Icons are limited to 32 KiB and 256 by 256 pixels. Validate dimensions before
full decoding and serve sanitized/re-encoded image bytes. Include icon bytes in
the tree's integrity sum. The HTTP icon endpoint uses an opaque catalog identity
and serves only the validated icon, never an arbitrary requested path.

## Discovery, identity, and API

Build an immutable catalog snapshot on startup and refresh it with bounded
concurrency and an explicit refresh interval. A refresh uses existing tree
validation, computes verified tree sums, and never executes an extension's
module initialization. Searches read the snapshot, not the filesystem or git.
Keep the last successful snapshot on refresh failure and expose staleness.
Compute metadata and integrity from the same bounded, immutable tree bytes that
are validated/exported. Hashing a live directory and reading different bytes
afterward is not a sufficient revision guarantee.

Each entry has an opaque `catalog_id` derived from canonical source identity and
tree revision. Names are not identities: two roots can contain `tavily`, and the
same source can be used under several instance aliases.

Extend `GET /api/v1/extensions` with opt-in `q`, `category`, `cursor`, and `limit`
parameters. The new UI always supplies a limit; preserve the legacy no-query
response and its `extensions`, `local_enabled`, and `remote_enabled` fields.
Add `catalog_id`, `sum`, `display_name`, `category`, `tags`, `icon_url`, and
server-derived provenance to entries, plus `total`, `next_cursor`, and
`catalog_revision` to paged responses. Full ops/config may remain on each bounded
page initially; do not embed icon bytes in the JSON response.

- Proposed page size: 24, hard maximum 100.
- Index name, display name, description, tags, and operation names. Prefer exact
  and prefix name matches, then other token matches; tie-break deterministically.
- Cursors bind query/filter and snapshot revision. A stale cursor returns a
  recoverable restart condition instead of silently skipping entries.
- Provide `GET /api/v1/extensions/{catalog_id}` for selected-entry lookup outside
  the current page and an icon subresource with same-origin, private caching and
  `nosniff`. An unavailable revision is reported, never substituted silently.
- Provenance such as bundled-community or operator-local is assigned by the
  server. A manifest cannot award itself a trusted or official badge.

Edit `api/openapi.yaml` first, then regenerate the strict server and web types.
The catalog remains a file-derived cache; no store tables are needed in v1.

## Loading and packaging

Initially contrib packages use the existing local-root mechanism:

- Development points `DEV_EXT_ROOTS` to `$(CURDIR)/contrib`.
- Release images include the contrib tree in a read-only application directory,
  separate from writable `/data/ext`. Packaging explicitly configures which
  roots are discoverable; standalone operators must opt in to a contrib root.
- Release archives, if produced, ship a matching contrib directory rather than
  fetching code on first startup.
- In split deployments the API owns local-source discovery. Workers continue to
  resolve the selected tree through the existing authenticated API callback;
  do not assume identical absolute paths on API and worker machines.
- Selecting a catalog entry writes its source and computed `h1:` sum into the
  instance spec. Validate against that same revision at creation. A local edit
  after selection must cause a clear integrity failure, not execute new code.
- Existing custom local sources may continue without a sum. Remote sources still
  require explicit `@version`, `h1:`, and server source allowlists.

Do not invent `github.com/.../contrib/tavily@...` syntax: the current parser cannot
resolve it. A later remote-subdirectory proposal can address that separately.

## Permission and trust model

Contrib is a distribution channel, not an elevated execution tier. Dependencies
still need explicit instance grants. Nested calls still pass through the Gate,
network allowlists, mandatory server policies, and secret domain restrictions.

An extension cannot request that the UI silently broaden `net.allow_hosts`,
change `spec.policies`, or bind a vault credential. Documentation and suggested
setup are untrusted input until reviewed by the operator/user. The current model
also exposes a granted base capability to the script; an extension wrapper is
not a narrower authority boundary by itself.

## Migration and delivery

1. Extend manifest validation and catalog metadata with old-manifest tests.
2. Move the tracked Tavily source into contrib and add its metadata/assets/docs.
3. Update `Makefile`, `examples/langchain-agent`, extension docs, Docker packaging,
   and the Compose setup reference. Keep the LangChain app under examples.
4. Add catalog snapshots, paging/search, safe icon delivery, and integrity pinning.
5. Connect the UI from EP-0004.

Old manifests remain valid on the new runtime, but old runtimes reject the
new metadata fields because parsing is strict. Upgrade API and worker runtimes
before deploying manifests that use them; do not weaken unknown-field validation
to mask a mixed-version rollout.

Existing live instances retain their loaded modules. Saved specs using the old
absolute example path need an explicit path update before creating a new instance;
do not add a symlink workaround to the extension loader. Existing operator copies
continue to work and are not rewritten by migration.

## Acceptance and verification

- Tavily loads from contrib in direct, remote-worker, and process-isolated modes.
- Mock-provider tests verify its current API, nested Gate audit, and secret rules.
- Old manifests still parse; unknown fields remain rejected.
- Malicious icon paths, decompression-sized images, missing icons, duplicate names,
  changed trees, and stale revisions have deterministic behavior.
- A 1,000-entry synthetic catalog does not recompile trees on search requests.
- Invalid entries do not break discovery for valid entries; operators can inspect
  bounded diagnostics without exposing credentials or file contents.
- CI validates contrib manifests, license/asset requirements, loadability, hashes,
  fixture tests, generated schema drift, and Docker inclusion.

## Open questions

- Exact maintainer policy and supported categories for community submissions.
- Whether an authenticated manual refresh endpoint is needed in addition to the
  refresh interval, especially for local development.
- Whether release packaging includes every reviewed contrib entry or a curated
  subset. Neither option automatically grants them to an instance.
