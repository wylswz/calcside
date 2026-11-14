# Task set B: multi-file agent workflows

Frozen before model calls. This extension uses new inputs and does not replace
task-set A results or historical source files.

- Three task types: enumerate/aggregate JSONL logs with timestamp tie-breaking;
  reconcile two ledgers with missing, mismatched and pending records;
  aggregate 5,000 CSV rows with an explicitly defined nearest-rank quantile.
  This is moderate-size batch processing, not a streaming or big-data test.
- Five uniform execute_code conditions and a separate native Calcside MCP
  condition, each task repeated three times: 54 planned fresh trials.
  All six conditions are interleaved by seed 20261114.
- Same deepseek-flash, temperature 0, 1,200 generated tokens per request,
  64 model / 96 tool calls, graph limit 512, sequential tools, no provider
  retries. Per-tool deadline and instance/key lifetimes remain as before.
- Original backend adapters are unchanged. Controller staging creates
  subdirectories with preparatory execs in chunks of at most 4,096 ASCII
  characters, then reads every fixture back exactly.
  Staging is included in setup time, not agent tool counts. Staging code
  introduces no fixture-valued globals; agent receives no reference answers.
- Record exact task text, input bytes/hashes, raw model/tool events and all
  artifacts. Both successful and failed trials remain in the denominator.
  Task inputs are fully published, including deterministic generator recipes.
- Artifact validation uses fixed independently checked reference values;
  correct artifacts and normal termination are reported separately.
  Native MCP errors use isError, uniform calls use adapter error fields.
- Keep set A and B, and uniform and MCP outcomes separately labelled.
  Report per-task 3-trial counts; do not claim statistically significant
  differences or general agent competence from this bounded task set.
- No security, OOM enforcement or streaming-memory claim follows from these
  benign tasks. Security probe evidence is independent of task outcomes.

An incomplete staging preflight is retained separately at
results/preflight-v3-staging with its original runner/protocol. It is not
merged into the 54-trial experiment under this fixed chunked-staging protocol.
