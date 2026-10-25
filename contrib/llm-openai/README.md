# llm-openai

OpenAI provider for the `llm` interface. Ops: `respond`, `structured`
(native `response_format: json_schema`), `score`, `choose`, `rank`.
Setup: key `sk-...` as `OPENAI_API_KEY` allowed for `api.openai.com`;
grant `net.allow_hosts: ["api.openai.com"]`. Set `json_schema: false`
for prompt-and-parse fallback.
