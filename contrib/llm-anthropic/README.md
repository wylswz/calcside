# llm-anthropic

Anthropic provider for the `llm` interface. `structured` uses a forced
single tool call (`emit`) so the response is already parsed JSON.
Setup: key `sk-ant-...` as `ANTHROPIC_API_KEY` allowed for
`api.anthropic.com`; grant `net.allow_hosts: ["api.anthropic.com"]`.
`max_tokens` is required by the API and defaults to 4096.
