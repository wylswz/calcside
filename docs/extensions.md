# Developing extended capabilities

An **extended capability** is a directory of Starlark code — never a native binary — that composes base capabilities (`net`, `fs`, `io`) into higher-level ops. A `tavily` extension can turn `net.post` + JSON plumbing into a single `ext.tavily.search(query)` op.

Extensions run *inside* the instance's Gate: every op call is a gated `ext` op, and every base-capability call the extension makes internally goes through the Gate again, so host allowlists, secret injection/redaction, policies, and audit apply unchanged.

## Layout

Community extensions live under `contrib/`; Tavily is bundled in `contrib/tavily`.
Development uses this root by default. Container images include it at
`/opt/calcside/contrib`, alongside operator-managed `/data/ext`; Compose mounts
`contrib` read-only on the API, while workers resolve local trees through the API.

An optional `icon: icon.png` manifest field refers to a regular PNG within the
extension tree (32 KiB, at most 256 by 256 pixels). External URLs and traversal
are rejected. Catalog icons are decoded/re-encoded and served by authenticated
content-addressed endpoints; missing or invalid image bytes use the UI fallback.
The bundled Tavily icon is Tavily's official app icon (from tavily.com). Existing custom manifests
without icons remain valid; saved specs using absolute source paths need updating to `root-name/extension`.

```
myext/
├── capability.yaml    # manifest (required)
├── main.star          # entry point (required)
└── helpers.star       # optional; load("helpers.star")
```

Tree limits: at most 256 files and 1 MiB total, regular files only — symlinks, sockets, and other special files are rejected.

## `capability.yaml`

```yaml
name: tavily
version: 0.1.0
description: Tavily web search
dependencies: [net]           # base capabilities used; never other extensions
ops:
  - name: search              # must be a top-level def in main.star
    doc: Search the web; returns list of {title,url,content}
    params: [query, max_results]
config:
  - name: base_url
    type: string
    doc: Tavily API base URL
    default: https://api.tavily.com
  - name: api_key
    type: secret              # value must be a {{secrets.NAME}} reference
    doc: Tavily API key (secret reference)
    default: "{{secrets.TAVILY_API_KEY}}"
  - name: max_results
    type: int
    default: 5
```

| Field | Notes |
|-------|-------|
| `name`, `version`, `description` | Displayed by `GET /api/v1/extensions` and in instance prompts. |
| `dependencies` | Base capability names the module needs as globals: `net`, `fs`, `io`. Extensions cannot depend on `ext` or on other extensions. The instance must also grant each dependency. |
| `ops[].name` | Exported op. Must be a valid Starlark identifier and resolve to a top-level function in `main.star`. |
| `ops[].params` | Positional parameter names; used to label args in audit/policy records. |
| `config[].type` | `string`, `int`, `bool`, `string_list`, `string_map`, or `secret`. |
| `config[].default` | Applied when the instance doesn't override the key. |

Config keys, op names, and params must all be Starlark identifiers (`[A-Za-z_][A-Za-z0-9_]*`).

## `main.star`

At instance creation the module executes once per alias with these globals:

- `json`, `math` — pure modules.
- `config` — frozen dict of effective config values (manifest defaults + instance overrides). Read via `config["key"]`; `config.get("key", fallback)` works too.
- One global per declared dependency — e.g. `net` — the *same gated binding* the instance script sees.

The Gate is **not armed during init**: calling `net.get(...)` at module top level fails with `capability used outside its scope`. Do all side-effecting work inside op functions.

```python
# main.star
def search(query, max_results=None):
    if max_results == None:
        max_results = config["max_results"]
    r = net.post(
        url=config["base_url"] + "/search",
        body=json.encode({"query": query, "max_results": max_results}),
        headers={"Authorization": "Bearer " + config["api_key"]},
    )
    if r["status"] < 200 or r["status"] >= 300:
        fail("tavily search failed: status " + str(r["status"]))
    return json.decode(r["body"])["results"]
```

- `fail(msg)` aborts the op; the script sees it as a normal call error.
- `load("helpers.star")` loads another `.star` file relative to the extension root (no absolute paths, no `..` escapes). Each file executes once per alias and is cached.
- Op calls inherit the instance's step limit and exec cancellation.

## Config and secrets

- A `secret` config field must be a whole-string reference `{{secrets.NAME}}`. The referenced secret must exist in the instance spec (`spec.secrets`), or instance creation fails.
- The module only ever sees the *placeholder string* (`config["api_key"]` is literally `{{secrets.TAVILY_API_KEY}}`). The plaintext is injected by the `net` capability at send time, only to hosts on the secret's domain allowlist, and is scrubbed from responses, errors, and audit.
- Non-secret config fields **reject** `{{secrets.*}}` placeholders — credentials must be declared `type: secret`.
- Unknown config keys and type mismatches are rejected at instance creation.

## Granting an extension to an instance

`capabilities.ext` is a map of `alias -> {source, sum, config}`:

```json
{
  "capabilities": {
    "net": {"allow_hosts": ["api.tavily.com"]},
    "ext": {
      "tavily": {
        "source": "contrib/tavily",
        "config": {"api_key": "{{secrets.TAVILY_API_KEY}}"}
      }
    }
  },
  "secrets": {
    "TAVILY_API_KEY": {"value": "tvly-...", "allowed_domains": ["api.tavily.com"]}
  }
}
```

- The **alias** is the script-visible name: `ext.tavily.search("q")`. It must be a Starlark identifier; several aliases may load the same source with different config.
- The extension's dependencies must also be granted (`net` above) — nested calls still enforce that capability's own restrictions (`allow_hosts`, methods, secret allowlists).

Send this spec to `POST /api/v1/instances`, or pass it to the Python SDK's `Client.create_instance`. Execute `print(ext.tavily.search("hello"))` via `POST /api/v1/instances/{id}/exec` or `Client.exec`.

## Sources and integrity

**Local** — `{root-name}/{extension}`, e.g. `contrib/tavily`. The server searches its `--ext-local-roots` for a directory whose basename matches `root-name`, then loads the named immediate subdirectory. With roots `/opt/calcside/contrib,/data/ext`, `contrib/tavily` resolves under the first root and `ext/myext` under the second. Development and containers use the same identifiers; clients never send host paths. The catalog returns these identifiers unchanged for use in specs.

Absolute paths, traversal, backslashes, and sources that escape their root after symlink evaluation are rejected. If multiple roots contain different trees for the same identifier, resolution fails as ambiguous rather than picking one silently. Repeated roots pointing at the same tree are harmless. Empty roots disable local sources entirely. `sum` is optional for local sources; remote version and sum requirements are unchanged.

**Remote** — `{domain}/{group}/{name}@{version}`, e.g. `github.com/acme/tools@v1.2.0`. The server shallow-fetches `https://{domain}/{group}/{name}.git` at `@version` (a tag-like ref or full commit SHA) using the git CLI with https-only transport, strips `.git`, and caches the tree under `--ext-cache-dir` (default: the user cache dir; override with `--ext-cache-dir`, timeout with `--ext-fetch-timeout`).

Remote sources require:

- `--ext-allow-sources` — comma-separated identifier prefixes (`github.com/acme` or `github.com`); empty disables remote sources.
- An `h1:` integrity sum — the Go dirhash over the extension tree (same hashing as `go mod download`). It is verified on **every** load, including cache hits.

To bootstrap the pin, create the instance without `sum`: validation fails with `ext: sum required for remote source; computed h1:...` — copy that value into the spec.

## Policy and audit surface

Extension ops produce gate records with `capability: "ext"` and `op: "<alias>.<op>"`:

- `args` are summarized using the declared `params` names; strings are truncated at 256 characters and non-JSON values appear as `<type>`. Don't pass secrets as plain op arguments — use `secret` config fields.
- `result.meta` carries `source`, `sum`, and the resolved `commit` for remote sources.
- Nested base-capability calls (the `net.post` inside `search`) produce their own `net` records, so policies can restrict either layer — e.g. deny `ext.tavily.*` entirely, or allow it but restrict which hosts it may reach.

`GET /api/v1/extensions` lists the extensions discoverable under the configured local roots.

## Development loop

From the repository root, run `make build` once for the embedded console. Save the spec above as `spec.json`, keeping `source: contrib/tavily` and supplying your own secret locally. Start the dev server, then run the API requests in another terminal. These examples use anonymous dev-mode auth; outside dev mode, include an `Authorization: Bearer` API key header.

```bash
# serve with local sources enabled
make serve-dev DEV_EXT_ROOTS="$(pwd)/contrib"

# check discovery
curl -fsS http://127.0.0.1:8787/api/v1/extensions | jq .

# create an instance (manifest/config errors surface here) and exec
instance_id=$(curl -fsS http://127.0.0.1:8787/api/v1/instances \
  -H 'Content-Type: application/json' -H 'X-Requested-With: calcside' \
  --data-binary @spec.json | jq -er '.instance.id')
curl -fsS "http://127.0.0.1:8787/api/v1/instances/$instance_id/exec" \
  -H 'Content-Type: application/json' -H 'X-Requested-With: calcside' \
  -d '{"code":"print(ext.tavily.search(\"hello\"))"}' | jq .

# inspect what the gate saw
curl -fsS "http://127.0.0.1:8787/api/v1/audit?instance_id=$instance_id" | jq .
curl -fsS -X DELETE "http://127.0.0.1:8787/api/v1/instances/$instance_id" \
  -H 'X-Requested-With: calcside'
```

Local sources are re-read on each instance create, so iterate by editing files and recreating the instance — no server restart needed.

## LLM (`contrib/llm`)

LLM access has two extension layers:

- **Provider extension**: a vendor adapter declaring `interface: llm`,
  a default `models` catalogue in `capability.yaml`, and `respond` /
  `structured` ops. Bundled providers are `contrib/llm-openai` (OpenAI
  Responses API), `contrib/llm-anthropic`, `contrib/llm-gemini` and
  `contrib/llm-openai-compat` (Chat Completions for
  vLLM/Ollama/LiteLLM). HTTP errors include a bounded, secret-redacted
  response body to make provider 400s diagnosable.
- **Capability extension**: `contrib/llm` consumes every workspace
  provider and exposes one script surface. Provider and model are call
  parameters, so code can route tasks by complexity or cost:

```python
ext.llm.providers()                                      # -> [provider names]
ext.llm.models(provider)                                 # -> [model ids]
ext.llm.respond(provider, messages, model=None, options=None)
#   -> {text, finish_reason, usage: {input_tokens, output_tokens}}
ext.llm.structured(provider, messages, schema, model=None)
#   -> parsed value conforming to a JSON Schema dict
ext.llm.score(provider, content, criteria, scale=1.0, model=None)   # -> {score, rationale}
ext.llm.choose(provider, instruction, options, model=None)          # -> {index, option, rationale}
ext.llm.rank(provider, instruction, items, model=None)              # -> {order, rationale}
```

Each configured provider also exposes typed model constants, so call
sites can avoid stringly-typed model ids:

```python
ext.llm.openai.models.GPT_6_LUNA        # == "gpt-6-luna"
ext.llm.anthropic.models.CLAUDE_OPUS_5_5 # == "claude-opus-5-5"

ext.llm.respond("openai", messages,
                model=ext.llm.openai.models.GPT_6_LUNA)
```

Constants uppercase the model id, replace non-alphanumerics with `_`, and
prefix `M_` when the id starts with a digit. `ext.llm.<provider>.name`
is the provider name and `.models` is a frozen constant namespace.
Dot access requires an identifier-safe provider name; use
`getattr(ext.llm, "<name>")` otherwise. Provider names must not collide
with the llm ops (`respond`, `structured`, `score`, `choose`, `rank`,
`providers`, `models`).

Provider extensions are not shown in the instance extension picker;
instances only add the `llm` capability ext.

### Provider protocol

The capability consumer declares `capability: llm` in `capability.yaml`;
the API uses that metadata (never the source path) to decide which ext
aliases receive the workspace provider snapshot.

A provider extension must declare:

```yaml
interface: llm
models: [model-a, model-b]   # default catalogue; may be empty for custom endpoints
ops:
  - {name: respond, params: [messages, model, options]}
  - {name: structured, params: [messages, schema, model]}
```

`contrib/llm` receives a registry of provider protocol objects:

```python
provider.list_models()                  # -> [model ids from provider metadata]
provider.respond(messages, model, options)
provider.structured(messages, schema, model)
```

`list_models` is synthesized from the provider manifest; no model ids
are hardcoded in the server or console. Provider `config` fields
(`base_url`, secret-referenced `api_key`, ...) are declared by the
provider extension and validated on every provider-registry write.

### Workspace provider registry

`POST /api/v1/llm-providers` stores a named provider per workspace:

```json
{
  "name": "my-openai",
  "source": "contrib/llm-openai",
  "models": ["gpt-6.1-sol", "gpt-6-luna"],
  "config": {"api_key": "{{secrets.OPENAI_API_KEY}}"}
}
```

- `source` must load and declare `interface: llm`.
- `models` defaults to the provider extension's metadata catalogue; it
  can be edited. `openai_compatible` has no default models, so its list
  must be supplied explicitly.
- `config` is validated against the provider manifest; secret fields
  must reference existing workspace vault secrets. The console lists
  provider extensions and their model defaults under **Workspace → LLM
  providers**.

### Using providers in code

The API tier snapshots the workspace registry into each `llm` ext alias;
scripts select a provider and model per call:

```json
{
  "capabilities": {
    "ext": {
      "llm": {"source": "contrib/llm"}
    }
  }
}
```

```python
# cheap path
summary = ext.llm.respond("fast", [{"role": "user", "content": text}],
                          model="gpt-6-luna")

# hard reasoning task on a different provider
verdict = ext.llm.choose("reasoning", "Which plan is safest?", plans,
                         model="claude-opus-5-5")
```

- `provider` must be a configured workspace provider. `model` must be
  in that provider's catalogue; omitted uses the first model.
- The model catalogue is validation only — it never chooses a model for
  you.
- LLM ops are gated: audit records `ext.llm.<op>` with the provider and
  model arguments, nested provider `net` calls, and no prompt or
  completion text.
- Provider secret fields are injected by `net`. The API automatically
  installs each referenced workspace vault secret into the instance spec
  when the `llm` ext is added, so callers do not repeat them under
  `spec.secrets`.
- Unknown providers and models fail at call time. Provider-registry
  writes fail earlier on a bad source, config or model list.
