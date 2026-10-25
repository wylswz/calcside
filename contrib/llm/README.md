# llm

LLM capability extension. It consumes a workspace provider extension and
exposes `respond`, `structured`, `score`, `choose`, `rank`. Model
catalogues and vendor wire formats live in provider extensions such as
`contrib/llm-openai`.

Configure providers per workspace (`POST /api/v1/llm-providers`) and add
the capability ext with no provider selection:

```json
{"source": "contrib/llm"}
```

Scripts choose per call:

```python
ext.llm.respond("openai", messages,
                model=ext.llm.openai.models.GPT_6_1_SOL)
```

Model constants are generated from the provider's declared catalogue:
uppercase, non-alphanumerics become `_`, digits get an `M_` prefix.
Provider names that are not valid identifiers need
`getattr(ext.llm, "my-openai")`.

Provider exts are hidden from instance creation; only this capability
ext is added. The provider protocol is `list_models()`, `respond(...)`
and `structured(...)`. See `docs/extensions.md`.
