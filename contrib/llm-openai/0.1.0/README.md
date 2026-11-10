# llm-openai

OpenAI provider extension (Responses API) for the `llm` capability. It declares `interface: llm`,
its default model catalogue in `capability.yaml`, and the provider
protocol ops `respond` and `structured`. Workspace providers select this
source and supply config (endpoint + secret reference). See
`docs/extensions.md`.
