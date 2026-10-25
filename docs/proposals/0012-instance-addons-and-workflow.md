# EP-0012: Instance add-ons and workflow

- **Status:** Implemented; agent help follow-up deferred
- **Scope:** Instance creation, boundary inspection, agent connection, supervision
- **Related:** EP-0008, EP-0009, EP-0010, EP-0011

## Problem

The create dialog exposes the storage shape rather than the user's task:
capabilities, extension configuration, policies, secrets, environment variables,
and JSON appear together. It requires knowledge before there is a reason to
learn it. Making that dialog larger does not fix the interaction.

The detail page has the opposite problem: uniformly small, pale sections obscure
the important anchors. Moving all actions into More reduces clutter but does not
explain how the boundary, agent work, and results relate.

The human assembles and approves the boundary; the agent works within it;
Calcside enforces and records it. The interface must expose that sequence.

## Decisions

### Add-on is an interaction, not another runtime abstraction

An add-on is a configurable item in the console. It maps to the existing spec,
not a new database entity or plugin execution system.

| Item | Function | Existing representation |
|---|---|---|
| Files | Grants a bounded virtual filesystem | `capabilities.fs` |
| Network | Grants HTTP access with explicit host/method settings | `capabilities.net` |
| An extension | Adds named Starlark operations using base capabilities | `capabilities.ext[alias]` |
| A policy | Constrains calls; never adds authority | `policies` |
| Variables and secrets | Supplies explicit inputs, with secret domain restrictions | `env`, `secrets` |
| Run limits | Bounds time, steps, output | `limits` |

Keep **Abilities**, **Rules**, and **Inputs** as plain section headings, not new
domain types. Cards state whether they grant access or constrain it. Do not make
every distinction another icon, colour, or product term.

MCP is a connection, not a capability. It belongs after creation and in an Agent
section, separate from the installed boundary. A key exists does not mean an
agent is connected: say "key ready" or "waiting for a run", never "connected"
without evidence.

### Creation is a workspace, not a modal

`/instances/new` provides space for progressive configuration:

1. Name and lifetime. Pure computation is available with no optional abilities.
2. Add only what the task needs. Files and Network cards explain their access;
   extensions come from the existing catalog. Selecting a card reveals its
   editor; unrelated editors remain closed.
3. A persistent review summarizes grants, constraints, inputs by name, and the
   next action: Create instance. It never exposes secret values.
4. Creation navigates to the new instance with Connect agent as the next action.

Offer Compute only and Files starting points; neither grants network access.
Templates are local draft presets, not persisted products. Changing a populated
draft must not silently discard work.

Use the same draft/editor for restricted key specifications, preserving their
vault-reference-only restriction. Advanced JSON remains available on explicit
request, not as the default right-hand panel. It is an alternate editing mode:
switching modes must not silently overwrite or drop fields.

The form must preserve an initial spec: extension aliases, source, sum, typed
config, network hosts/methods, labels, limits, policy nil-versus-empty semantics,
and secret refs/domain restrictions. Sanitized inline secrets cannot be cloned:
require a replacement or removal before creating, rather than submitting an
empty value. Unknown supported JSON fields must not disappear on round-trip.

Validation identifies the item to fix. Missing extension dependencies and
required secret references block submission, but installing an extension must
never silently grant Network or expand its hosts. Catalog failures must be
visible and retryable, not interpreted as an empty catalog.

### Detail page is a supervision workspace

Desktop layout:

```text
Instances / name                         status + remaining lifetime    More
---------------------------------------------------------------------------
Boundary (supporting)      Work (dominant)
Abilities                 One contextual next action, if needed
Rules                     Activity: code, outcome, capability calls
Inputs (names only)
                          Results
Agent                     Files when granted / State
Key ready / MCP URL
```

On narrow screens, show identity/status and next action first, followed by a
compact boundary summary, Activity, Results, then supporting configuration.
No page-level horizontal scroll or clipped primary action.

Ended instances show reviewable history and an explicit Create similar action.
That action opens a draft for a **new** instance: globals and files are not
copied. Do not call it restore, resume, or install.

List groups are Needs attention, Live, and Ended. No instance appears twice;
only live instances with failures/denials or imminent expiry enter Needs
attention. Historical denied runs on an ended instance remain historical.
Status has redundant text and colour in every list, including shared instances.

### Visual construction serves interaction

- Use strong neutral dividers to group functional regions, not a border around
  every sentence. The Work region is visibly dominant.
- Name/status, the next action, and section headings have distinct type scales.
  Supporting IDs, timestamps, and raw configuration stay secondary.
- Blue identifies actionable focus; green means live; yellow means expiry or
  failure; red means denial. Do not assign red/yellow/blue to unrelated zones.
- State signals retain their existing shapes. Cards do not invent a second
  shape vocabulary whose meaning users must memorize.
- Create and Connect are anchors at different stages, never competing filled
  buttons in a flat toolbar. Advanced configuration is discoverable but quiet.
- Respect the existing typography and authentication/artifact design system.
  No downloaded fonts, decorative geometry, or animation needed for meaning.

## Refactoring Review

Replace the monolithic form with a typed draft/serialization module and small
editors for abilities, extensions, inputs, and rules. Keep pure conversion and
validation separate from React/query state. Reuse the extension manifest field
editor and secret handling rather than introducing another implementation.
Keep the exported form handle small and typed.

Move creation out of `pages/Instances.tsx` so detail does not import a list page.
Boundary summaries should use the same spec interpretation as creation review.
Workspace-scoped catalogs must use the selected workspace and workspace-aware
query keys; changing workspace must not reuse another workspace's data.

No API or worker changes are needed for this interaction. Authorization remains
in `service/access`; UI hiding is not authorization. Do not query or create keys
for a viewer merely to decide what guidance to show.

## Installation Lifecycle

Add-ons are installed **only during configuration, before creation**. After
creation, the human enables MCP access and connects an agent. The runtime
boundary stays fixed; MCP connection does not install or widen capabilities.

This is the intended lifecycle, not a temporary limitation awaiting hot updates.
There is no running-installation roadmap in this proposal. Instances display
their actual immutable boundary and offer Create similar to start another
configuration. No silent restart, state loss, or store-only update is allowed.

The existing Create-time capability bindings, policy snapshots, and worker
context already match this lifecycle. Preserve them rather than adding an
unnecessary update protocol. Serving and environments remain scoped to EP-0011.

## Deferred: Agent Progressive Disclosure

Requested follow-up, **not implemented in this stage**:

- One discoverable `help` primitive for stdlib, installed capabilities, and
  extension modules/functions, extending the existing stdlib help surface.
- A concise initial prompt explaining the exploration mechanism instead of
  embedding the complete function catalog.
- Bounded, deterministic documentation, suggestions, and dependency guidance.
  Discovery must not expose unavailable capabilities, secret values, private
  sources, host paths, or authority the instance does not possess.
- Keep prompt, completion metadata, extension manifests, and help consistent.

## Verification

Pure tests cover empty defaults, complete initial-spec round-trip, typed extension
config, policies omitted versus `[]`, secret clone failures, key-mode inline
rejection, explicit dependency validation, and no implicit network installation.
Include these tests and existing next-step tests in the actual `pnpm test` command.

Browser tests cover minimal creation, adding/removing Files and Network, extension
configuration, inline-secret clone validation, advanced editing, Create similar,
workspace scoping, connect guidance, and mobile overflow/keyboard use. Preserve
artifact isolation/consent tests. Screenshots must assert seeded state before
capture; backend restarts invalidate in-memory instances.

Run web typecheck/lint/tests/build and the browser suite. Backend repairs are
verified separately; do not run Go builds concurrently with web builds.
