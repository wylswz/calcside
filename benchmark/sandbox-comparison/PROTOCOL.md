# Sandbox comparison v2: measurement contract

This contract is fixed before the v2 model runs. The original JSON files are
pilot evidence, not v2 inputs. Do not overwrite them or discard failed trials.

## Experimental unit

- Five systems: host Python, Calcside, Docker, hosted E2B, and OpenShell with
  its installed compute driver. CodeAct is an action representation, not an
  isolation boundary. These systems are not interchangeable security layers.
- One fresh environment per (backend, task, repetition). Four benign tasks,
  three repetitions, 60 planned trials. A seeded shuffle interleaves backends.
- Same task text, tool name/schema, model, temperature, token and call budgets.
  Language/API documentation necessarily differs; compare complete systems,
  not a causal effect of isolation alone.
- DeepSeek `deepseek-flash`, temperature 0, 1,200 output tokens/model request,
  eight model requests and twelve tools maximum/trial. Provider retries are
  disabled. Model identity returned by the provider is retained where present.
- LangChain `create_agent`, one `execute_code(code: str)` tool.
  `AgentMiddleware.wrap_model_call` and `wrap_tool_call` record timed events.
  Local JSON traces only: no LangSmith dependency or automatic trace upload.
- Parallel tool calls disabled. Trials run sequentially. Each execution has
  a 30-second adapter deadline. Setup, verification and cleanup are timed
  separately from the agent loop. Timeout is not proof of process termination.
- Calcside grants api.github.com with the private-network policy selected.
  This host's fake-IP DNS requires the explicitly recorded server exemption
  198.18.0.0/15; this is not a zero-exemption deployment.
- OpenShell uses its Docker driver, default filesystem policy and a
  per-sandbox rule granting GET api.github.com:443 /repos/**. The gateway's
  default-deny egress would make fetch fail by construction, so it is not used
  for the benign task comparison. No global gateway policy is changed.
  Its writable workspace is /sandbox; other adapters use /work or a host temp
  directory. Control-plane setup includes policy application and file staging.

## Ground truth

The agent must create an artifact. After the loop, the controller reads it
through an adapter control path and validates its contents independently of
the model's final answer and console output. No evaluator tool is given to
the agent. This is functional validation, not proof of the algorithm used.

| Task | Input | Artifact and exact predicate |
|---|---|---|
| stats | x=1..100 CSV | result.json: count=100, mean=50.5, max=100; numeric types, not bool/string |
| join | employees and departments CSV | summary.json: exactly Engineering=210, Sales=90 |
| file | task text | squares.csv: header n,sq; exactly five ordered rows (n,n*n), n=1..5 |
| fetch | public GitHub repository endpoint | repo.json: full_name=python/cpython and private=false |

The fetch artifact alone cannot prove HTTP occurred: those public values can
be guessed. Calcside exec IDs permit a separate capability audit, but other
adapters do not have equivalent effect tracing. Report artifact success, not
"verified network tool success". GitHub/network failures remain in results.

## Metrics (never conflate these)

- Task completion: validated artifacts / all planned trials per backend.
  Setup failures, budget exhaustion, model failures and unreadable artifacts
  remain in the denominator. Also show counts, task-level cells and a 95%
  Wilson interval (z=1.959963984540054). This is descriptive: repeated tasks
  are clustered, not independent samples of real-world agent tasks.
- Tool execution success: execute_code calls returning no adapter error /
  attempted tool calls. A successful process can still write a wrong artifact.
  Policy denials are errors for these benign tasks, but correct security
  behavior in a probe whose requested effect is forbidden.
- First-tool success: first attempted tool call returned without error /
  trials with at least one attempted tool call. Not first-attempt task success.
- Recovery: trials with a tool error that eventually pass / trials with a
  tool error. Undefined when there are no such trials.
- Model reliability: successful middleware model events / attempted model
  events. No hidden retry. Missing token usage is unknown, not zero.
- Tokens: sum provider input/output usage on successful model responses.
  Report separately; include repeated context and tool schemas. Do not
  subtract a separately measured system-prompt baseline.
- Latency: setup; agent loop; sum of model events; sum of tool events;
  residual orchestration (= loop - model - tool); verification; cleanup.
  Wall-clock overhead and remote RPC are included in tool latency.
  Quantiles use nearest-rank ceil(p*n), raw unrounded seconds are saved.
  Small n does not support stable p95 or product-wide rankings.
- Failed model calls may have unknown provider billing. No dollar cost is
  inferred from tokens without an archived price schedule.

## Security evidence

Do not run new credential-exfiltration, host-key reads, metadata exploitation,
unbounded allocation or escape attacks. Historical probes are observations
under their recorded configuration, not a security certification.

- Reading guest /etc/passwd does not read the physical host's file.
- A missing SSH key is absence, not an enforced access-control decision.
- Loopback means a different host in each network namespace.
- ECONNREFUSED, routing failure and timeout do not demonstrate policy denial.
- HTTP 401 proves an HTTP response, not metadata/credential disclosure or
  endpoint identity. EACCES is consistent with enforcement but not attribution
  to a particular kernel mechanism without policy/enforcement logs.
- A 1 GiB allocation is not an escape; an excessive-repeat rejection does
  not establish a general memory bound. Mac host cgroups were not measured.
- Capability audit, LangChain tool telemetry, and kernel/network logs observe
  different boundaries; one cannot substitute for another.

## Reproduction and data handling

Use a dedicated local API/worker, never a production server. Preserve old DBs
and results. Use unique output directories and stop only resources created by
this run. API keys stay in the controller environment; local generated Python
does not inherit credentials. Traces contain synthetic task code/data only;
never log model request headers or controller environment variables.

Record commit, dirty-worktree status, dependency versions, platform, task
order, model configuration, adapter metadata and cleanup errors. A dirty
checkout means a commit SHA alone is insufficient to reproduce the run.
Freeze summaries to summary.json before rendering charts/report tables.
