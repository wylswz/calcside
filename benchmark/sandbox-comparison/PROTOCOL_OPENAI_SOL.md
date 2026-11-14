# Supplement: gpt-6.1-sol on the two Calcside interfaces

Frozen before scored model calls. The earlier DeepSeek datasets remain intact.

## Scope and controls

- Two conditions: Calcside uniform execute_code and Calcside native MCP with
  server instructions and original seven schemas, not all five backends.
- Task set B is unchanged: triage, reconcile, series; three repetitions each,
  eighteen fresh trials. Use the original set-B manifest order filtered to
  calcside/calcside_mcp, preserving their relative order.
- Original task inputs, prompts, independent artifact validators, adapters,
  chunked staging and runtime bindings are reused. No extra advice about
  whole-file reads or token efficiency is added to the model instructions.
- 64 model calls, 96 tool calls, graph limit 512, sequential tools.
- Snapshot source hashes and the earlier baseline manifest/trials/report view.
  Record all failures, including setup/termination/provider failures.

## Provider configuration and interpretation

- Exact model gpt-6.1-sol, https://api.openai.com/v1, Responses API,
  reasoning_effort=medium, temperature omitted, no automatic retries,
  180-second model-request timeout.
- Maximum output 16,384 tokens per response, including reasoning. DeepSeek's
  earlier cap was 1,200, temperature 0 and a 90-second request timeout.
  This is an independent provider/model configuration replication, not a
  matched-output-budget causal model comparison. Both Sol interfaces use the
  same provider settings.
- Preserve provider usage_metadata verbatim per model event: input, output,
  cache-read and reasoning counters when supplied. Output includes reasoning;
  do not add reasoning again to totals. Missing counters stay unknown.
- Compare artifact and termination counts, tool outcomes, model requests,
  observed tokens and loop latency. Input usage sums repeated contexts and
  may include cache hits; it is not an invoice.
- Also record calls and trials in which native read_file returns series.csv
  exactly and completely. This narrow observable is not a detector for every
  possible data exposure through code outputs.
- No source or result changes to the DeepSeek experiments. Earlier provider
  validation failures are unscored setup evidence, not task trials.
