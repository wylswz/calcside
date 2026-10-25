# contrib — bundled community extensions

Starlark-only extensions composing base capabilities. Each directory is a
self-contained extension: `capability.yaml`, `main.star`, `icon.png`, and this
README pattern. All network access goes through the instance's gated `net`
capability, so `allow_hosts`, method whitelists, secret domain allowlists,
policies, and audit apply unchanged.

## Catalog

| Extension | Service | Required secret | API host |
|---|---|---|---|
| `slack` | Slack messaging | `SLACK_BOT_TOKEN` | `slack.com` |
| `github` | GitHub repos/issues/PRs | `GITHUB_TOKEN` | `api.github.com` |
| `stripe` | Stripe payments | `STRIPE_SECRET_KEY` | `api.stripe.com` |
| `linear` | Linear issues (GraphQL) | `LINEAR_API_KEY` | `api.linear.app` |
| `notion` | Notion pages/databases | `NOTION_API_TOKEN` | `api.notion.com` |
| `sendgrid` | SendGrid email | `SENDGRID_API_KEY` | `api.sendgrid.com` |
| `airtable` | Airtable records | `AIRTABLE_API_KEY` | `api.airtable.com` |
| `upstash-redis` | Redis via Upstash REST | `UPSTASH_REDIS_TOKEN` | your `*.upstash.io` host |
| `upstash-qstash` | QStash messaging/schedules | `UPSTASH_QSTASH_TOKEN` | `qstash.upstash.io` |
| `neon` | Postgres via Neon HTTP SQL | `NEON_PASSWORD` | your `*.neon.tech` host |
| `qdrant` | Qdrant vector search | `QDRANT_API_KEY` | your Qdrant host |

### LLM providers (`interface: llm`)

| Extension | Backend | Required secret |
|---|---|---|
| `llm-openai` | OpenAI chat completions | `OPENAI_API_KEY` |
| `llm-anthropic` | Anthropic Messages | `ANTHROPIC_API_KEY` |
| `llm-gemini` | Gemini generateContent | `GEMINI_API_KEY` |
| `llm-openai-compat` | OpenAI-compatible (vLLM/Ollama/LiteLLM) | `LLM_API_KEY` |

Providers expose `respond`/`structured`/`score`/`choose`/`rank` and are
not used directly — a workspace registers them via
`POST /api/v1/llm-providers`, and an ext alias selects one with
`"llm": "<name>"`, gaining an `llm` global. See docs/extensions.md.

## Security conventions

- Auth credentials are always `type: secret` config fields — never op
  arguments — so values are injected at send time and redacted everywhere.
- Extensions depend only on `net`; none use `fs` or `io`.
- Ops build URLs on the configured host; there are no arbitrary-URL ops
  (except `upstash-qstash.publish`, whose destination is inherent to the
  service — the instance never fetches it).
- Grant the narrowest `net.allow_hosts` matching the API host. Ops marked
  "requires PATCH/PUT" need those methods added to `net.methods` — all other
  ops work with the default GET/POST.
