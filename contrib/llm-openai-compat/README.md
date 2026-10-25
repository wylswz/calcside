# llm-openai-compat

Generic OpenAI-compatible provider (vLLM, Ollama, LiteLLM, internal
gateways). `base_url` and `model` are required. `json_schema: true`
sends `response_format`; the default prompt-and-parse path works
everywhere and verifies the result locally. Setup: key as
`LLM_API_KEY` allowed for the endpoint host.
