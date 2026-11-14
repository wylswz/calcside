# Supplementary track: Calcside native MCP and official prompt

This protocol is fixed before model calls in this track. It does not change
or replace the already-running uniform-interface v2 experiment.

## What changes

- Same four task texts, input files, independent artifact validators and three
  repetitions (12 new trials), model and 8-model/12-tool budgets as v2.
- Actual bound-instance Streamable HTTP MCP endpoint, not the v2 execute_code
  HTTP adapter. Controller creates/stages a fresh instance and temporary
  bound API key; agent uses server tools discovered through tools/list.
- MCP initialize.instructions is the server-generated official instance
  prompt. Preserve it verbatim, its hash, initialize result and original
  tool JSON schemas in each trial. No handcrafted Starlark language prompt.
- A minimal controller instruction specifies task completion, workspace and
  budgets; its exact text is stored in the manifest.
- LangChain StructuredTool bridges the MCP tool schemas without renaming,
  removing tools, rewriting code, substituting sections or trimming results.
  Text/content and raw MCP CallToolResult are preserved.
- Official exec requires a notebook section. The model chooses sections and
  whether to use notebook/read_section/add_section/run_sections/file tools;
  no controller-written solution or automatic repair is supplied.
- LangGraph max_concurrency=1 enforces sequential tool execution even if the
  provider returns multiple tool proposals despite parallel_tool_calls=false.
- Calls to list tools/initialize and key setup belong to setup, not agent
  tool counts. File verification and final Notebook capture are controller
  observations, not model-visible feedback. A temporary bound key is revoked
  and the instance deleted in finally.

## Observability and scoring

Same artifact task predicate and model/token/timing definitions as v2.
Native tool success means MCP CallToolResult.isError is not true; this is a
protocol-level signal, not a guarantee every nested/section operation
succeeded. Preserve raw result for inspection. Budget exhaustion and setup
failures remain visible; correct artifacts can pass even when final agent
termination has an error. No failure is discarded or rerun for the main table.

This is a distinct end-to-end configuration, not a prompt-only A/B test:
the transport, tool surface, schema size, output format and Notebook workflow
all change together. Compare functional outcomes and full context costs, but
do not attribute a difference solely to the prompt or MCP overhead. Native
MCP tool-call success and uniform execute_code runtime success have different
denominators/semantics; label them separately.

No new claim about prompt-injection resistance, secret security, audit
completeness or DAG speedup follows from passing these benign tasks.
This track is run later, not interleaved with uniform v2, so provider load
and cache changes are additional confounders.
