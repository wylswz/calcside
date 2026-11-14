# Reading list

Papers worth reading against this codebase, grouped by the part of the system
they speak to. Each entry says what the paper does and what it would change or
confirm here, so the list stays useful after the first read.

Status note: the 2026 entries are preprints and have not been checked for peer
review — treat their numbers as the authors' claims. The older systems and PL
papers are safe to cite.

## Start here

**Tracekit Box: Running AI Agents in Micro-VMs Where Every Way Out Is Decided
and Signed** (2026) — <https://arxiv.org/html/2610.38458v1>

A job runs in a micro-VM with no network card; the only exits are a few doors on
the host (start a command, outbound connection, secret, model call, workspace
patch), and a signer the agent cannot reach decides and signs each one into a
single verifiable bundle. Secrets never enter the VM: the guest sees
placeholders that the host proxy swaps on requests to the secret's bound hosts.
This is the same shape as `{{secrets.NAME}}` + `Gate.Invoke` + the audit drain,
carried one step further — the run ends as an artifact anyone can verify
offline, where audit here is still an event stream. That closing move is the
part worth borrowing.

**Defeating Prompt Injections by Design (CaMeL)** (2025) —
<https://arxiv.org/abs/2503.18813>

Extracts control and data flow from the trusted query, attaches capabilities to
values, and enforces policy at tool calls, so untrusted data cannot steer the
program. Solves 77% of AgentDojo tasks with provable security against 84%
undefended. The Gate already makes every op pass through `Invoke`, but a policy
sees `net.post(url, body)` and not where `body` came from. This is the semantics
for attaching provenance to values and checking it in Rego.

**Runtime provenance refinement for notebooks** (Vizier, 2022) —
<https://doi.org/10.1145/3530800.3534535>

Refines a static provenance approximation at runtime — dependencies confirmed
or refuted as cells execute — with cells in isolated interpreters
communicating only through dataflow. EP-0022 and EP-0023 both note that static
`defines`/`uses` cannot see mutation; this is the serious answer to that
limitation, and the prerequisite for `run("stale")` ever being sound under
mutation.

## Execution model: code as the action

- **Executable Code Actions Elicit Better LLM Agents (CodeAct)**, ICML 2024 —
  <https://arxiv.org/abs/2402.01030>
  The premise of the project, across 17 models. Read the failure analysis
  rather than the headline: it says where code actions lose to JSON tool calls.

- **From Tool Orchestration to Code Execution: A Study of MCP Design Choices**
  (2026) — <https://arxiv.org/abs/2602.15945v1>
  Formalises context-coupled vs context-decoupled (CE-MCP) execution: code
  execution cuts tokens and latency and greatly expands the attack surface,
  with sixteen attack classes including exception-mediated code injection and
  unsafe capability synthesis. The argument for Starlark over Python, and a
  reminder that extension composition is the exposed edge.

- **Tool Attention Is All You Need** (2026) —
  <https://arxiv.org/pdf/2604.21816>
  The MCP "tools tax": 10k–60k tokens per turn in multi-server deployments.
  Intent–schema overlap plus lazy schema loading cuts per-turn tool tokens by
  95%. A way to put numbers on EP-0008's progressive disclosure.

## Isolation and process lifetime

- **Catalyzer: Sub-millisecond Startup for Serverless Computing**, ASPLOS 2020
  — <https://dl.acm.org/doi/10.1145/3373376.3378512>
  Restore from a checkpoint instead of booting, plus `sfork` to reuse a running
  sandbox's state. EP-0022 does the opposite on purpose (restoration is a fresh
  empty process, never a replay); the trade-off belongs in that proposal's
  alternatives section.

- **The True Cost of Containing: A gVisor Case Study**, HotCloud 2019 —
  <https://www.usenix.org/system/files/hotcloud19-paper-young.pdf>
  Measured cost of a userspace kernel on syscalls, network and file opens. Use
  it to justify interpreter-level isolation with a few capability exits over
  syscall sandboxing, instead of asserting it.

- **Sandlock: Confining AI Agent Code with Unprivileged Linux Primitives**
  (2026) — <https://arxiv.org/html/2605.26298>
  Landlock + seccomp-bpf + seccomp user notification, no root, no cgroup, no
  image, ~5 ms startup, with HTTP-level access control and reversible filesystem
  effects. The closest reference if `subproc` ever needs real shell execution.

- **SandboxEscapeBench** (2026) — <https://arxiv.org/html/2603.02277v3>
  A CTF benchmark that measures how well models break out of Docker and
  Kubernetes, in a nested sandbox. Its threat-model checklist is worth running
  against the worker boundary.

## Authority, capability and audit

- **PACE: Provenance-Aware Capability Enforcement** (2026) —
  <https://arxiv.org/abs/2610.01349>
  At the last tool boundary, checks the concrete proposal, the authenticated
  request, the observed provenance and a hook that can still change the
  outcome. The same idea as the rule that hook-visible `Result`/`Call` metadata
  must be JSON-able and carry no file contents; its vocabulary is better.

- **Progent: Securing AI Agents with Privilege Control** (2025) —
  <https://arxiv.org/html/2504.11703>
  Symbolic policy over tool names and arguments, with an SMT check classifying
  every update as narrowing (automatic) or expansion (needs approval), so
  authority can only shrink unattended. Policies here are snapshotted at create;
  this is the security definition needed before an agent may ever request more
  authority at runtime.

- **AgentCap: Capability-Based Runtime Authorization** (2026) —
  <https://arxiv.org/abs/2611.00729>
  Short-lived, budget-bounded capability tokens whose delegation can only
  attenuate. Structurally the same as bound API keys (`ins_…`, `app_…`) with
  a key ceiling; the difference is that budget and lifetime attenuate too,
  which bound keys here do not.

- **Authority Is Not a String: A Capability-Scoped Harness for
  Prompt-Injection-Resistant Coding Agents** (2026) —
  <https://arxiv.org/pdf/2609.08371>
  The title states the rule `service/access` already follows: naming a resource
  is not authority over it. CapScope derives a task-wide ceiling from trusted
  input and gives each sub-agent typed capabilities, so an injection can request
  an action but cannot acquire the capability.

- **Capability Myths Demolished** (Miller, Yee, Shapiro, 2003) —
  <https://srl.cs.jhu.edu/pubs/SRL2003-02.pdf>
  Old, and the right citation for why services never compare user IDs and why
  invisible resources return `not_found`. Steadier than any 2026 agent paper for
  that argument.

## Notebooks and dependency-driven reruns

- **Build Systems à la Carte**, ICFP 2018 —
  <https://dl.acm.org/doi/10.1145/3236774>
  Separates scheduling from rebuilding strategy (dirty bit, verifying traces,
  constructive traces). `stale` is a dirty bit with a human trigger; `rerun:
  once` is a weakened constructive trace. It gives the vocabulary to write "why
  nothing is ever run automatically" as a design point rather than a product
  decision.

- **Dataflow Notebooks: Encoding and Tracking Dependencies of Cells**, TaPP 2017
  — <https://www.usenix.org/system/files/conference/tapp2017/tapp17_paper_koop.pdf>
  Persistent cell identifiers plus explicit dependencies and downstream updates.
  EP-0022's stable section names instead of positions is the same argument,
  made six years earlier.

- **From Agent Loops to Deterministic Graphs: Execution Lineage** (2026) —
  <https://arxiv.org/abs/2605.06365>
  DAG replay preserved upstream and unrelated branches exactly when an
  intermediate artifact changed, while loop baselines regenerated the whole
  product and imported unrelated context. The strongest evidence for sections
  over scratch execs.

- **NBRewind: Efficiently Reproducing Distributed Workflows in Notebook-based
  Systems** (2026) — <https://arxiv.org/html/2603.26965v1>
  An audit kernel doing incremental cell-level checkpointing and a repeat kernel
  doing partial re-execution. Addresses the cost side of "after restoration
  everything is `not_run` and the agent decides".

- **Large-scale Evaluation of Notebook Checkpointing with AI Agents** (2025) —
  <https://arxiv.org/abs/2504.01377>
  Agents simulate data scientists over 1,000 exploration paths; checkpointing
  cuts redundant re-execution and duplicate variables. Useful empirical support
  for keeping in-memory state across revisions.

## Replay and durable state

- **Pollard: Governed Semantic Execution Trees** (2026) —
  <https://arxiv.org/html/2610.16145>
  Step identity commits to canonical input, kind, attempt and parent; strict
  replay returns a verified stored result without invoking the wrapped
  operation, and a transactional store coordinates reservations across workers.
  The structure EP-0001 wants: `exec_id` dedup covers a repeated request, not
  proof that a replay made no external call.

- **Deterministic Replay for AI Agent Systems (agrepl)** (2026) —
  <https://arxiv.org/abs/2607.16200>
  A MITM proxy records every external interaction and replays it with zero
  outbound network, with a noise-aware diff that sorts HTTP header divergence
  into signal and noise. The most implementable version of request-level replay.

- **Engineering Record and Replay for Deployability (rr)**, USENIX ATC 2017 —
  <https://arxiv.org/abs/1705.05937>
  The industrial answer to process-level deterministic replay. Read it to price
  that path for a Go runtime before promising it.

- **Restoring Uniqueness in MicroVM Snapshots** (2021) —
  <https://arxiv.org/abs/2102.12892>
  Snapshots carry secrets and must restore uniqueness (UUIDs, nonces);
  introduces `MADV_WIPEONSUSPEND` and VmGenId. The formal counterpart to
  re-resolving secret references on restoration, and to why `incarnation` has to
  be monotonic.

## Extensions and supply chain

- **Skill Constellations: Tracing the Supply Chain of Agent Skills on GitHub**
  (2026) — <https://arxiv.org/abs/2610.11169>
  2.19M skill adoptions; half of all SKILL.md files are verbatim copies, and a
  fix at the source rarely reaches the copies. Concludes platforms should
  distribute versioned references rather than copies — which is EP-0020, with
  numbers.

- **Formal Analysis and Supply Chain Security for Agentic AI Skills** (2026) —
  <https://arxiv.org/abs/2603.00195>
  Attack surface per phase (install, load, execute) and an integrity definition,
  `sha256(content(s,v)) = H(s,v)`, that matches the insert-then-compare
  transaction in `extension_versions`.

- **in-toto**, USENIX Security 2019 —
  <https://www.usenix.org/conference/usenixsecurity19/presentation/torres-arias>
  Signed provenance for each step of a supply chain. Audit here records runtime
  ops; this records build and publish. EP-0018's reviewable publish needs
  something like it to show the reviewed artifact is the one that runs.

## Streams, console and charts

- **Push-Pull Functional Reactive Programming**, Haskell 2009 —
  <https://dl.acm.org/doi/10.1145/1596638.1596643>
  Combines data- and demand-driven evaluation. Explains why a pull-only design
  re-evaluates per consumer, which is the background for EP-0021's single-exec
  iterators and explicit `.close()`.

- **Overseeing Agents Without Constant Oversight** (2026) —
  <https://arxiv.org/abs/2602.16844>
  Three user studies on action traces: detail that helps verification also
  overwhelms, and a better interface cut error-finding time without improving
  accuracy. A direct constraint on the boundary panel and the one-line-per-
  section outline.

- **Coding with "Enemy": Can Human Developers Detect AI Agent Sabotage?** (2026)
  — <https://arxiv.org/html/2606.05647v1>
  94% of developers missed sabotage, and 56% still accepted malicious code after
  a monitor warned them. Whatever can be decided deterministically should be;
  human review is not a security boundary.

- **Vega-Lite: A Grammar of Interactive Graphics**, TVCG 2017 —
  <https://idl.cs.washington.edu/papers/vega-lite/>
  The grammar `chart.*` is a subset of. Its layering (data, transform, encoding)
  is the test for whether the core constructors should grow transforms.
