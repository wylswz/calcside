# EP-0004: Searchable extension library and configuration UI

- **Status:** Draft
- **Created:** 2026-10-05
- **Area:** Web instance creation, extension catalog API, accessible UI components
- **Depends on:** [EP-0003](0003-community-extensions.md) for rich catalog metadata

## Summary

Replace the extension source dropdown in instance creation with a searchable,
visual extension library. Users browse cards with icons and useful descriptions,
filter a large catalog, inspect a detail panel, and configure selected extensions
without losing instance draft state.

The library is a picker for a particular instance, not a global installation or
permission-granting screen. Custom source entry remains available.

## Current experience and motivation

`ExtRowEditor` in `web/src/pages/Instances.tsx` renders every catalog entry as a
native `<select>` option. Source selection, alias, configuration, secret mapping,
and dependency hints are packed into the same small row. This is workable for a
few extensions but not for hundreds with similar names.

The replacement should answer four questions before the user adds an extension:

1. What does it do?
2. Who supplied it, and which version will run?
3. What capabilities and credentials does it need?
4. What configuration remains before this instance can be created?

## Goals

- Fast discovery across at least 1,000 synthetic catalog entries.
- Attractive, consistent cards with an icon, title, short description, category,
  and explicit selection/configuration state.
- Clear separation between discovery, configuration, and permission review.
- Keyboard, screen-reader, narrow-screen, loading, and error support.
- Preserve custom local/remote sources and advanced JSON spec editing.

## Non-goals

- A marketplace, favorites backend, popularity rankings, reviews, or payments.
- Automatically installing packages or broadening capabilities/policies.
- Redesigning the rest of the console or adding a UI framework for one picker.
- Displaying arbitrary contributor HTML, remote images, or executable examples.

## Interaction model

### Instance form

The Extensions section contains selected-extension cards and an **Add extensions**
action, replacing one source dropdown per row. Each selected card shows its icon,
display name, alias, version, readiness, and Configure/Remove actions.

A readiness summary distinguishes ready, missing configuration, missing secret,
missing base capability, and source unavailable. Do not collapse these into a
single red error. Removing an extension removes only its selection/config; it
must not revoke a vault secret, delete a shared secret row, or disable a base
capability another extension uses.

### Library surface

Open a wide modal or drawer from the create form. Keep the instance draft mounted.
Avoid stacking a second modal for details; use an in-surface detail pane instead.

```text
Add extensions                                      2 selected
[ Search extensions or operations...                    ]
All | Research | Developer | Productivity | Data

[icon] Tavily                 [icon] Another extension
Web search with source       Concise description of
results for research.        the task it supports.
Research  net                Data  fs
[View details] [Add]          [View details] [Add]

24 of 312 results                        [Load more]
```

Cards use the existing Inter/IBM Plex Mono typography, surface/border tokens,
accent color, and button conventions. Use consistent 40-pixel icon containers,
a clear title, a two-line description, restrained metadata, and generous spacing.
Use two or three columns on wide screens and a single column on narrow screens.
Do not use gradients, animation, or oversized logos to substitute for hierarchy.

Every extension gets an icon: use the verified same-origin image from EP-0003,
or a deterministic monogram fallback. Missing/broken icons retain their space so
cards do not jump. Icons next to a visible title are decorative for accessibility.

### Search and filtering

- Autofocus search on opening. Search names, descriptions, tags, and operation
  names, so a user can search for a task rather than know a provider name.
- Debounce requests briefly and use the existing React Query dependency. Query
  keys include search, category, and catalog revision. Late responses must not
  replace a newer query's results.
- Use EP-0003's paged API; do not download every package's configuration or icon
  before rendering the first page. Proposed page size is 24.
- Reset the cursor on query/filter change; preserve selected items independently
  of result pages. An entry leaving the search results does not deselect it.
- Prefer deterministic token/prefix matching initially. Do not introduce a vector
  service or fuzzy-search dependency before measuring real search failures.
- Provide distinct loading, no matches, empty catalog, disabled sources, stale
  catalog, and network error states. Keep the draft and selections on retry.

### Details and configuration

The detail pane shows:

- Full description, version, provenance, category/tags, and source/integrity data.
- Exported operations with signatures and concise docs.
- Required base capabilities and secret configuration fields.
- A clearly separate configuration section for alias, typed config, and vault
  references, reusing the current type-aware controls.

Adding an extension does not require configuring it immediately. The selected
card remains visibly incomplete until requirements are satisfied. Allow adding
the same source under another alias through an explicit duplicate action; normal
Add should not accidentally create duplicates. Alias conflicts block submission.

Keep custom source entry under an **Add custom source** action. Preserve source,
sum, alias, and manually entered config even if that source has no catalog entry.
Unrecognized sources are not rendered as trusted catalog cards.

## Permission and credential review

A missing `net` or `fs` dependency is an actionable warning, not permission to
enable it automatically. Offer a reviewed change to the instance draft, showing
exactly what will change. Never widen host allowlists to `*`, remove mandatory or
selected policies, or expand secret domains to satisfy an extension.

Secret selectors display names and required bindings, never secret values. The
backend still validates authorization and compatibility; UI warnings are not a
security boundary. A community badge describes distribution, not endorsement of
an extension's behavior.

On source/version change, do not silently reuse incompatible config. Ask before
discarding user-entered values, preserve matching fields when valid, and show a
reviewable diff of added/removed fields. Pin the selected source and sum in the
submitted spec, as defined in EP-0003.

## State and API contract

Selected state is keyed by a stable draft row ID, not array index, source name,
or position in the current result page. Store `catalog_id`, pinned source/sum,
alias, and edited config. Several rows may reference the same source.

Use the detail endpoint to refresh a selected entry not in the current page.
If that exact revision disappears, mark it unavailable and require reselection;
do not silently resolve to a newer tree. Preserve custom source data regardless.

Form-to-JSON and JSON-to-form round-trips must preserve extensions, explicit empty
policy lists, secret references, and user changes outside this section. Existing
inline-secret support must not be removed incidentally, and secrets must never
enter search parameters, URLs, telemetry, or catalog caches.

The implementation uses the existing React, React Query, Tailwind tokens, and
shared UI primitives. The project does not currently declare an icon, dialog,
virtualization, or browser-test library; adding one requires an explicit justified
choice rather than assuming it is installed.

## Accessibility and performance

- Search has a visible label; results/count/loading changes have an appropriate
  live announcement without reading every card on each keystroke.
- Modal/drawer traps focus, supports Escape, and returns focus to its trigger.
- Card actions are distinct buttons, not nested interactive elements. Selection
  and validation states are not conveyed by color alone.
- Support keyboard-only search, filtering, details, configuration, and removal.
- Only render loaded pages and lazy-load icons. Cap rendered results or introduce
  measured virtualization if repeated Load more becomes expensive.
- With 1,000 fixture entries, assert a bounded first page and no synchronous full
  catalog work on every keypress. Record interaction timings in a reproducible
  browser setup rather than promising a latency independent of network/hardware.

## Delivery and acceptance

1. Extract extension draft transformations into testable functions.
2. Build search/cards/detail/config flows against catalog fixtures and EP-0003.
3. Replace the dropdown while preserving custom-source and JSON workflows.
4. Verify keyboard behavior, narrow layouts, long text, missing images, stale
   search responses, and permission-review flows.

Automated tests cover duplicate aliases, multi-alias selection, category/search
pagination, source changes, unavailable revisions, configuration persistence,
secret references, and JSON round-trips. Run web typecheck/lint/build and the
existing Node tests. Interaction/accessibility checks require browser coverage;
use a manual acceptance matrix initially or explicitly add browser test tooling.
Do not claim those checks are covered by the current Node-only suite.

## Open questions

- Modal versus drawer within the existing instance-creation modal layout.
- Whether a later standalone browse page is useful outside instance creation.
- Whether future catalogs need multi-tag filtering in addition to category and
  text search. The initial data model should not preclude it.
