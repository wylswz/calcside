# Native MCP high-budget replication

Requested after the first native run: the account has sufficient credit and
the eight-model-call harness cap should not cause premature termination.
BudgetExceeded previously meant a local harness limit, not exhausted credit.

- Repeat all four native tasks, three fresh trials each, same seeded task
  order, model, temperature, official server prompt/tools, policies,
  artifact validator and single-call output limit (1,200 tokens).
- Increase per-trial model calls 8 -> 64, tools 12 -> 96, graph recursion
  40 -> 512. Change only the controller budget sentence accordingly.
  Provider retries remain disabled, tools remain sequential. Existing
  runtime/key TTL and per-request timeouts stay unchanged.
- Use the original native trial function with explicit runtime dependency
  bindings in run_native_highbudget.py; do not alter archived source files.
  Record wrapper/base hashes and exact limits. No original result is replaced.
- Twelve new trials in results/v2-native-mcp-highbudget. Save all errors,
  including any remaining cap, API, timeout or infrastructure failures.
- Report artifact validity and normal agent termination separately. Compare
  task-matched records, but this is not deterministic replay: new instances,
  model nondeterminism, caches and GitHub quota can change the execution.
- Passing a public-field fetch artifact still does not prove HTTP provenance.
  MCP isError semantics and other limitations remain as in the native protocol.
- Read final raw rows to check actual model/tool counts and whether any new
  limit was hit; do not claim higher budgets caused an improvement solely
  from a small later sample.
