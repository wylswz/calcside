# EP-0019: Workspace default credentials for extensions

- **Status:** Superseded (design refined; not implemented)
- **Superseded by:** the per-consumer environment design accepted with
  EP-0017/EP-0018 — `spec.environment` extension bindings (named
  bindings of vault-secret references, `default` selected at execution)
  cover the same "which vault entry feeds this extension's secret
  field" need as an owned consumer scope rather than workspace-level
  recommendations. This document's workspace-default mechanism is not
  the plan going forward; keep it only as background.
- **Created:** 2026-11-03
- **Area:** store, API, console (Secrets, instance/application editor)
- **Related:** [EP-0003](0003-community-extensions.md) (extensions), [EP-0004](0004-extension-discovery-ui.md) (discovery UI), [EP-0010](0010-workspaces-and-access.md) (access), [EP-0013](0013-llm-providers.md) (LLM providers)

## Summary

Let a workspace record, once, which vault secret is the recommended
credential for an extension's secret config field — "`contrib/tavily`
`api_key` → vault `TAVILY_PROD`". The console uses the recommendation to
prefill the field when someone adds the extension. It is only an editor
default: the server never reads it while validating or running a spec,
and the resulting spec is exactly what the user saw and saved.

## Motivation

Every secret config field is configured per instance by picking a vault
entry. In practice the choice is fixed: the OpenAI extension always takes
the OpenAI key, Tavily always the Tavily key. The repetition is pure
friction, and it is also where mistakes happen (picking a staging key, or
a key for a different vendor).

Manifests already hint at a conventional name — all contrib extensions
declare `default: "{{secrets.<VENDOR>_API_KEY}}"` — but that only works if
an instance secret with exactly that name exists, and the console renders
it as an empty "Choose a secret". Vault names are chosen by people
(`OPENAI_PROD`, `openai_team_b`), so a naming convention is not a
reliable binding.

## Goals

- One workspace-level mapping `(extension, secret field) → vault secret`.
- Editable from both sides: the Secrets page ("recommend for…") and the
  extension side (inline in the instance/application editor).
- The recommendation only prefills the editor. Specs stay explicit and
  self-contained; changing a recommendation never affects an existing
  instance, key, application or saved draft.

## Non-goals

- Server-side resolution. Specs submitted through the API/SDK without a
  value behave exactly as today (manifest default, must name an instance
  secret).
- Recommendations for non-secret config fields (base URLs, limits).
  Possible later under the same table shape.
- Changing runtime secret semantics (injection, redaction, domain checks).

## Proposal

### Data model

New table `extension_secret_defaults`, both dialects, one migration per
`make migrate-new`:

| column | notes |
| --- | --- |
| `id` | `esd_…` |
| `workspace_id` | |
| `extension` | extension key, see below |
| `field` | secret config field name from the manifest |
| `secret_id` | vault secret id |
| `user_id`, `created_at`, `updated_at` | |

Unique `(workspace_id, extension, field)`. Store interface, `gormstore`
and `storetest` conformance are updated together; queries stay dialect
neutral. `DeleteSecret` removes its recommendations in the same
transaction (no reliance on FK cascade).

**Extension key.** The extension source without `@version`: local
`contrib/tavily`, remote `domain/group/name`. A recommendation survives
version upgrades. `internal/capability/ext` exposes the normalization so
the API and console agree. If a newer version drops or retypes the field,
the list response marks the recommendation `stale` and the editor
ignores it.

### API

`api/openapi.yaml` first, then `make gen`:

- `GET /api/v1/extension-secret-defaults` — list for the workspace, each
  with `id`, `extension`, `field`, `secret_id`, `secret_name`, `stale`.
- `PUT /api/v1/extension-secret-defaults` — upsert
  `{extension, field, secret_id}`. Validates that the extension is in the
  workspace catalog, that `field` is a `type: secret` field, and that the
  secret belongs to the same workspace.
- `DELETE /api/v1/extension-secret-defaults/{id}`.

The response carries names only, never values — the same information the
vault list already exposes. Access (`service/access`): reading follows
vault listing; writing follows secret writes. Bound keys (`ins_…`,
`app_…`) see nothing. Every new operation gets an access table entry
(`TestRouteCoverage`).

The sandbox, worker, and application services do not read this table.

### Console

**Prefill on add.** When an extension is added in the instance or
application editor and a non-stale recommendation exists for a secret
field, the editor fills the draft as if the user had picked that vault
entry: `config[field] = "{{secrets.N}}"` plus an Inputs entry
`{name: N, kind: vault, ref: N}` unless the draft already defines `N`.
This is the same path as today's manual vault pick (`onUseVault`), so the
added entry has no `allowed_domains` and inherits the vault allowlist.
The field shows `N` with a "workspace default" hint; the user can change
it like any other value.

**No silent edits.** Prefill happens only at add time. Extensions already
in a draft (opened from an existing instance, key or application) are
never modified; an empty field with a recommendation shows a "Use
workspace default (`N`)" action instead. If recommendations are still
loading when the extension is added, the same action appears once they
arrive rather than changing the field under the user (C/P invariants in
`docs/interaction-stability.md`).

**Secrets page.** Each secret row shows chips "Default for
`tavily · api_key`". A "Recommend for…" action opens the shared dropdown
primitive listing catalog extensions' secret fields (icon, name, field
doc). Fields already recommending another secret show the current
holder; replacing uses `Confirm` (F2). Deleting a secret lists its
recommendations in the existing delete confirmation.

**Suggestion heuristic.** When a vault secret's name equals a manifest
default (`TAVILY_API_KEY`) and that field has no recommendation, the row
offers a one-click "Recommend for tavily". Never applied automatically.

**From the editor.** When the user picks a vault secret for a field with
no recommendation, a "Make this the workspace default" checkbox records
it on save.

All new controls follow `docs/interaction-stability.md` (S1 paint-only
states, S4 `SwapLabel`, P2 shared primitives).

## Edge cases

- **Vault unavailable / no `--secret-key`:** no recommendations are
  shown; the editor behaves as today.
- **Recommended secret deleted:** recommendation removed in the same
  transaction; drafts already prefilled keep their explicit reference and
  fail validation like any reference to a missing vault secret.
- **Two aliases of the same extension:** both are prefilled; a single
  Inputs entry `N`.
- **Draft already defines `N` inline:** the existing entry is kept; only
  the config reference is filled.
- **Remote extension not in catalog:** cannot be recommended in v1 (the
  field cannot be validated).
- **API/SDK callers:** unaffected; they can read the list to build their
  own defaults.

## Alternatives considered

- **Resolve recommendations into the spec server-side** (like
  `resolveLLMProviders`). Saves API/SDK users a field, but makes the
  submitted spec differ from the stored one and adds a hidden input to
  validation. Rejected: a default should be visible where it is chosen.
- **Rely on manifest default names, auto-pick matching vault secrets.**
  No new state, but depends on people naming vault entries after vendor
  conventions and cannot express "use `OPENAI_PROD`". Kept only as the
  suggestion heuristic above.
- **Tags on vault secrets (`ext: tavily`).** Equivalent data, but a
  many-to-many shape without the uniqueness that makes "the default"
  well defined.
- **Browser-local preference.** No server change, but per-user and
  per-browser; the point is a shared workspace choice.

## Follow-ups

- Prefill the LLM provider form (`Providers` page) from recommendations
  on `contrib/llm-*` provider extensions.
- Recommendations for non-secret fields (e.g. a self-hosted `base_url`).
- SDK helper to list recommendations.

## Plan

1. Store: migration (sqlite + postgres), interface, `gormstore`,
   `storetest`, `DeleteSecret` cleanup.
2. API + access: OpenAPI, `make gen`, service with catalog/field
   validation, access entries, tests.
3. Console: editor prefill on add and "Use workspace default" action;
   Secrets page chips, "Recommend for…", suggestions; "Make this the
   workspace default" checkbox. `pnpm -C web test` covers prefill (add vs
   existing draft, existing `N`, stale entries).
4. Docs: `docs/extensions.md`, AGENTS.md convention line ("extension
   secret defaults are console-only; never resolved server-side").
