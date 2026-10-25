# EP-0013: LLM providers with a unified response / structured / judge interface

- **Status:** Partially implemented: provider extensions (`interface: llm`) declare model metadata and the `respond`/`structured` protocol; the `contrib/llm` capability extension consumes all workspace providers, with provider/model chosen in code. A native Go `llm` capability remains future work.
- **Area:** New `llm` capability, instance spec, provider adapters, audit
- **Related:** EP-0003 (extensions), EP-0008 (restricted keys, gate audit)

## Problem

LLM calls are different from tool integrations. The SaaS/infrastructure
extensions in `contrib/` map onto service-specific REST APIs, but every major
model vendor exposes the *same* conceptual interface — chat in, text or
structured data out — behind different wire formats (OpenAI chat completions /
responses, Anthropic messages, Gemini generateContent, plus the
OpenAI-compatible servers used by vLLM, Ollama, LiteLLM and Azure gateways).

Today a script could reach these APIs with `net.post`, but that has no shared
contract: response shapes differ, token usage is unobservable to audit and
policies, structured output requires per-provider hand-rolled prompting and
parsing, and nothing enforces per-model output limits. Per-provider Starlark
extensions would just copy this fragility into `contrib/`, and Starlark cannot
implement streaming, accurate usage accounting, or schema-validated decoding
reliably.

The interface also goes beyond chat. Agents need **discriminative** calls —
judge-style operations that score content against a rubric, choose between
candidate options, or rank items. These are the same completion under a
constrained contract, and they deserve first-class ops rather than each script
re-implementing "reply with only a number" prompts that parse unreliably.

## Implementation note

The shipped shape splits the interface from the vendor adapters:
provider extensions declare `interface: llm`, their model catalogue in
`capability.yaml`, and the `respond`/`structured` protocol. The `llm`
capability extension consumes every configured provider and exposes the
higher-level ops. Workspaces register providers through
`/api/v1/llm-providers` (`{name, source, models, config}`); instances add
only the `llm` capability ext and choose provider/model per call.
Provider extensions are hidden from instance creation. No vendor model
ids are hardcoded in the server or console. See `docs/extensions.md`.

## Proposal

Introduce an `llm` base capability with a **provider → models** model:

- A **provider** is a named adapter configuration: `type`, `base_url`, and a
  secret-referenced `api_key`.
- A **model** is a named alias bound to one provider plus a concrete model id
  and caps. Scripts address models by alias (`"smart"`, `"cheap"`,
  `"judge"`), never by raw provider ids, so operators can retarget aliases
  without touching code.

```json
"capabilities": {
  "llm": {
    "providers": {
      "openai":    {"type": "openai",    "api_key": "{{secrets.OPENAI_API_KEY}}"},
      "anthropic": {"type": "anthropic", "api_key": "{{secrets.ANTHROPIC_API_KEY}}"},
      "local":     {"type": "openai_compatible", "base_url": "https://llm.internal/v1"}
    },
    "models": {
      "smart": {"provider": "openai",    "model": "gpt-5.2",              "max_output_tokens": 8192},
      "cheap": {"provider": "openai",    "model": "gpt-5.2-mini",         "max_output_tokens": 2048},
      "judge": {"provider": "anthropic", "model": "claude-sonnet-4-6",    "max_output_tokens": 1024}
    }
  }
}
```

### Script surface

```python
llm.respond(model, messages, options=None)
#   messages: [{"role": "system"|"user"|"assistant", "content": "..."}]
#   options:  {temperature, max_output_tokens, seed, stop}
#   -> {text, finish_reason, usage: {input_tokens, output_tokens}}

llm.structured(model, messages, schema)
#   schema: JSON Schema dict
#   -> parsed dict/list validated against schema in Go, never the raw text

llm.score(model, content, criteria, scale=1.0)
#   -> {score, rationale}; score normalized to [0, scale]

llm.choose(model, instruction, options)
#   options: list of strings (or {label, description})
#   -> {index, option, rationale}

llm.rank(model, instruction, items)
#   -> {order: [indices], rationale}
```

The judge ops (`score`/`choose`/`rank`) are deterministic wrappers over
`structured` with fixed rubric schemas: `choose` always returns an index into
the caller's list rather than free text the script must fuzzy-match, and
`score` is always numeric and bounded. Because the contract is fixed, they can
later migrate to provider-native features (e.g. constrained decoding) without
changing call sites.

### Provider adapters

Each adapter translates the unified contract to one wire format. v1:

| type | Wire format | Structured output |
|---|---|---|
| `openai` | Chat Completions | `response_format: json_schema`; fallback prompt+validate |
| `anthropic` | Messages | forced single-tool use returning JSON |
| `gemini` | generateContent | `responseSchema` |
| `openai_compatible` | Chat Completions | `json_schema` if advertised, else prompt+validate |

Fallback path everywhere: append the schema to the prompt, parse the response
as JSON, validate in Go, retry once with a repair instruction, then fail —
never return unvalidated text as structured data.

### Safety and isolation

- HTTP egress reuses the `net` capability's vetted dialer: DNS in-process,
  vetted-IP pinning, `builtin.block_private_network` enforcement, and the
  instance's `allow_hosts`. LLM traffic cannot bypass network policy.
- `api_key` is a `{{secrets.NAME}}` reference injected at send time under the
  secret's domain allowlist — identical mechanics to `net`, including
  redaction in errors and audit.
- `base_url` defaults to the vendor endpoint and is spec-configurable (for
  gateways); secrets still only flow to their allowlisted hosts.
- Instance isolation is unchanged: config and credentials live in the
  instance spec, adapter state is per-instance, nothing crosses instances.
- Audit `result.meta` carries `{provider, model, input_tokens,
  output_tokens, finish_reason, latency_ms}` — never prompt or completion
  content, consistent with the "no bodies in metadata" rule. Per-op args
  record the model alias and message count, not message text.
- `max_output_tokens` per model caps spend and runaway generation;
  requests have bounded size and count toward exec limits.

### Non-goals (v1)

- Streaming responses (adds thread/cancellation complexity; non-streaming
  fits the request/response exec model).
- Tool/function-calling loops — orchestration belongs to the agent layer
  above exec, not the capability.
- Embeddings, image/audio modalities, token-count estimation, response
  caching, and cross-provider failover — all natural later phases.

### Alternatives considered

- **Plain extensions per provider.** Rejected for v1: no streaming, no Go-side
  schema validation, token usage invisible to audit, and each ext re-wires
  auth/dial logic in Starlark. A `contrib/openai` shim remains possible for
  exotic providers not worth a native adapter.
- **A single generic `llm.request` passthrough.** Rejected: it leaks each
  vendor's response shape into scripts and gives audit nothing to record.
- **Free-form `judge` prompts.** Rejected: unconstrained text output cannot be
  consumed reliably; fixed schemas make judge ops dependable primitives.

## Phases

1. `llm` capability + validation + `openai`/`anthropic`/`openai_compatible`
   adapters, `respond` + `structured`.
2. `gemini` adapter; `score`/`choose`/`rank` judge ops.
3. Embeddings op; streaming; usage budgets surfaced to policies.
