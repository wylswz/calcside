# EP-0008: MCP, restricted API keys, and on-demand utility docs

- **Status:** Implemented
- **Area:** API keys (store, auth, service), sandbox service, new MCP endpoint,
  stdlib metadata, agent prompt, web Keys page

## Summary

1. **Restricted API keys.** A key may carry an instance spec. A key with a
   spec can create instances only from that spec and can act only on the
   instances it created. Keys without a spec keep today's behaviour and are
   shown as full access.
2. **MCP endpoint.** `POST /mcp` serves the Model Context Protocol over
   streamable HTTP, authenticated with a restricted API key. Its tools wrap
   the existing sandbox service, so MCP calls are checked, audited and
   recorded exactly like API calls.
3. **On-demand utility docs.** The agent prompt lists utility modules with a
   one-line summary each. Full signatures come from a pure Starlark builtin,
   `help(name)`, available in every integration (MCP, SDK, LangChain).
4. **Setup guide.** After creating a key, the console shows the MCP URL and
   copy-ready client configurations.

## Problem

### API keys have their owner's full power

An agent receives an API key. With it, today it can:

- create an instance with any spec, including `"policies": []`, which drops
  `builtin.block_private_network`, and `net` with no `allow_hosts`;
- reference any of the owner's vault secrets by name and allow them for any
  host;
- read, run code in, or delete every instance of the owner.

Only key, secret and policy management already require a console session.
An agent should get exactly the authority its task needs, decided by the
human in advance. This is the same principle as capabilities inside an
instance, applied one level up.

### Connecting an agent needs custom code

Agents connect through the Python SDK or hand-written tool wrappers. MCP
clients (Claude Code, Cursor, VS Code, and agent frameworks) can connect to a
remote MCP server with a URL and a bearer token. Calcside has no MCP server.

### The prompt grows with the standard library

The agent prompt lists every utility function with its full description:
about 100 lines after the numeric modules landed, most of them irrelevant to
a given task. That costs context on every turn and dilutes the instructions
that matter (Starlark is not Python, persistence, secrets, errors).

## Design

### Words

No new nouns. A key is **full access** or **restricted**; a restricted key
has an **instance spec** (the existing `InstanceSpec`).

### Restricted API keys

**Data.** `store.APIKey` gains:

- `Spec json.RawMessage` — normalized `InstanceSpec`; empty means full access.
- `MaxInstances int` — live instances this key may hold at once; 0 means only
  the per-user limit applies. Required to be 1..`maxPerUser` for restricted
  keys.

`store.Instance` gains `KeyID string`: the key that created it, empty when
created from a console session or a full-access key. Migration adds
`api_keys.spec`, `api_keys.max_instances` and `instances.key_id` (indexed).

**Creation.** `POST /api/v1/keys` accepts optional `spec` and
`max_instances`. The spec is validated with the same
`capability.NormalizeSpec` and policy/secret resolution checks used by
instance creation (resolution without starting an instance), so a key cannot
hold a spec that would later fail. Vault secrets are stored as references,
never values; inline secret values are rejected in key specs because they
would have to be persisted.

**Authorization.** `auth.Principal` and `service.Actor` gain `KeyID` and
`KeySpec` (nil for full access). Rules, enforced in `service/sandbox`:

- **Create.** A restricted actor's request body may set only `labels` and
  `ttl_seconds` (at most the spec's TTL). Anything else is `forbidden`. The
  instance is created from the key's spec with the given labels merged
  under the spec's labels (spec labels win) and `KeyID` set.
- **Access.** `owned()` additionally requires `in.KeyID == a.KeyID` for a
  restricted actor; otherwise `not found`, as for another user's instance.
- **Lists.** The instance list is filtered to the key's instances
  (`store.ListInstances` takes an `InstanceFilter{UserID, Status, KeyID}`
  instead of positional arguments). Executions are reached through their
  instance, so `GetExecution` checks the instance with `owned()`. The audit
  list requires `instance_id` or `exec_id` for a restricted actor and checks
  that instance; without either it is `forbidden`.
- **Limit.** Counting live instances by `KeyID` before creation.
- **Everything else is session-only or read-only.** Restricted keys may not
  create, update or delete library policies: a key spec can name a library
  policy, and editing it before creating an instance would weaken the spec.
  Keys and the vault are already session-only. Listing policies and the
  extension catalog stays allowed.

Vault secret references are resolved at instance creation, as today, so
rotating a secret affects later instances; deleting it makes creation fail.
Revoking a key does not stop its instances; they expire by TTL, and the owner
can delete them.

One helper, `service.Actor.Restricted() bool`, keeps checks readable; the
spec itself is passed to `Create`, not re-read in each method.

### MCP endpoint

**Transport.** `POST /mcp` uses the official Go SDK
(`github.com/modelcontextprotocol/go-sdk`, a release at least 7 days old)
with `StreamableHTTPOptions{Stateless: true}`. The 2026-07-28 protocol is
stateless, so no MCP session state is kept and every tool call names its
instance. Request bodies are bounded (`MaxRequestBodyBytes` = code limit +
headroom).

**Authentication.** Bearer restricted API key only. Full-access keys and
cookies are rejected with 401/403: the MCP endpoint should not be a way to
use broad authority, and cookie-free auth means no CSRF or DNS rebinding
exposure. The handler is mounted on the Gin engine through the same auth
middleware as `/api/v1`, and receives `Request.Context()`.

**Package.** `cmd/calcside/internal/mcp` (API-only). It depends on the
sandbox service interface and the prompt package, never on HTTP handlers or
generated API types. Each tool is a thin adapter: decode arguments, call the
service with the actor from the context, map service errors to MCP tool
errors (`isError: true` with the service message).

**Tools.**

| Tool | Arguments | Result |
|---|---|---|
| `create_instance` | `labels?` | `instance_id`, `expires_at`, and the instance's agent prompt |
| `exec` | `instance_id`, `code` | output, or error type and message |
| `list_files` | `instance_id`, `path?` | entries |
| `read_file` | `instance_id`, `path` | redacted content |
| `delete_instance` | `instance_id` | — |

`list_files` and `read_file` are listed only when the key's spec grants
`fs`. The server `instructions` field carries the general part of the
prompt; `create_instance` returns the instance-specific part, which is the
existing prompt renderer with tool names unprefixed.

### On-demand utility docs

**Metadata.** `internal/stdlib` gains a one-line description per module,
kept next to the function table (`moduleDocs`, one entry per module; a test
fails if a function's module has none). `Symbols()` already emits a
`namespace` symbol per module with a generic detail; it now carries the
module's line in `Doc`. The prompt reads namespace symbols, completion shows
the same text, and no new accessor is added. `json` and `math` keep their
existing prompt line.

**`help(name)`.** A pure builtin added with the utility modules (so it works
in scripts and extensions):

- `help()` → module list with one-liners.
- `help("stats")` → that module's functions, one signature line each.
- `help("stats.ols")` → full signature and description.

It covers utility modules only. Capability ops stay fully listed in the
prompt (short and instance-specific), and keeping them out means `help`
needs no session state and lives entirely in `internal/stdlib`.

It returns a string (the agent prints it), output is bounded by the
metadata size, and unknown names are an error listing close matches.

**Prompt.** The "Pure utilities" section becomes one line per module plus:
"Call `print(help("module"))` for signatures before first use." Golden files
change accordingly. Capability sections stay complete: they are short and
specific to the instance.

### Console

- **Keys page.** The create form gets an **Access** choice: *Restricted*
  (default) with the instance spec form (shared with New instance; extract
  it into one component) and max instances, or *Full access* with a warning
  that the key can create any instance and use every instance and vault
  secret reference. Each key row shows its access and, for restricted keys,
  the allowed summary.
- **After creation**, besides the secret shown once: the MCP URL
  (`{console origin}/mcp`) and tabs with configurations for Claude Code
  (`claude mcp add --transport http …`), Cursor/VS Code JSON, and a generic
  `Authorization: Bearer` header. Snippets use the real key, shown only in
  that dialog.
- Runs made through MCP or the API by a restricted key appear as **agent**
  runs (EP-0009), with the key name.

## Refactoring check

- **Layering.** MCP is another transport over the sandbox service, like the
  REST handlers; no business logic in the adapter. Authorization lives in
  the service, so REST and MCP cannot diverge.
- **Shared spec handling.** Key creation and instance creation call one
  `sandbox.ValidateSpec(ctx, actor, raw)` that runs normalization, policy
  selection and secret resolution without placing an instance; `Create`
  uses it too.
- **Small interfaces.** `Actor` grows by two fields; the MCP package sees
  only the service methods it calls.
- **One spec form** in the web app instead of two copies.
- **Metadata in one place.** Module one-liners live in the stdlib registry
  next to the functions; prompt, `help` and completion read from it.

## Security

- Restricted keys reduce what a leaked key can do to: create instances from
  one fixed spec (bounded by `MaxInstances` and TTL), and use those
  instances.
- A key spec never contains secret values; the vault stays session-only.
- MCP accepts only restricted keys, bounded bodies, no cookies.
- `help` is pure and reads static metadata; it reveals nothing about the
  host or other instances.
- Existing keys keep working as full access; the console marks them so
  owners can replace them.

## Tests

- Service: restricted create ignores/rejects spec fields; access to another
  key's or console instance is `not found`; list filtering; `MaxInstances`;
  full-access behaviour unchanged.
- Key creation: spec validation errors; inline secret rejected; vault refs
  stored as refs.
- MCP: tool listing depends on `fs`; tool round trip against an in-process
  server (create → exec → read_file → delete); full-access key and cookie
  rejected; oversized body rejected; exec audit and execution record carry
  `via = api_key`.
- stdlib: `help` outputs for no argument, module, function, unknown name;
  every function belongs to a documented module.
- Prompt goldens; web unit tests for snippet rendering.

## Out of scope

- OAuth for MCP clients.
- Per-key scopes beyond the spec (e.g. read-only keys).
- Updating a key's spec after creation (create a new key instead).
