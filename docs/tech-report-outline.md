# Technical report: outline

Status: outline. Sections state what must be *established* and where the
evidence already lives in the tree, so writing is filling in, not deciding.

Target length: 10–12 pages, 5 figures, 1 comparison table. Audience: systems
and PL readers who know what an LLM agent is but have not built one, plus
security reviewers who will attack section 3.

Thesis: an agent's action space should be *code*, but the code must run where
effects are named rather than ambient. Starlark in a per-instance process with
a capability gate gives code-as-action without giving the model an operating
system; the notebook gives that process a durable, checkable program.

Contributions to claim, and only these:

- **C1** A capability gate that makes every effect an auditable, policy-checked
  call, armed per exec and revoked at teardown, with no ambient authority in the
  language (section 3).
- **C2** An execution architecture where the API tier never executes, one OS
  process per instance carries all state, and every hop speaks the same protocol
  (section 4).
- **C3** A notebook model in which order is semantics, staleness is derived
  rather than trusted, and restoration never executes anything (section 5).
- **C4** The observation that effects can be *derived from the audit record*
  rather than declared by the author or inferred by static analysis (5.7).

Not claimed: formal security proofs, model quality, or that Starlark is
sufficient for every workload.

## 1. Introduction and problem statement

1.1 The gap. A tool-calling agent has a fixed action space; a coding agent has
an operating system. The middle — arbitrary computation with named, bounded
effects — is what data analysis, API orchestration and report generation
actually need.
1.2 Three families of existing answers and the failure each cannot fix:
CodeAct-style in-process interpreters (no isolation), container/microVM shells
(ambient authority inside the box), and notebook kernels (state is a function
of click history). One paragraph each, one citation each.
1.3 Thesis and C1–C4.
1.4 Scope: what the report does not cover (model prompting, the console's
visual design, chart rendering).

## 2. System overview

2.1 Deployment shapes: single binary with embedded UI, API + workers, Compose,
Helm. One sentence each; no install instructions.
2.2 Figure 1 — the request path: client → API (`service/sandbox`, never
executes) → worker (`subproc.Supervisor`) → instance process (`instance.Manager`
→ `engine.Session`) → gate → capability. Annotate what each hop may and may not
do. Source: `docs/execution-routing.md`.
2.3 Vocabulary table: instance, exec, session, section, incarnation, gate,
capability, extension, policy, artifact. Fix these before section 3 uses them.
2.4 Code map: one table mapping subsystem → package, so a reader can check any
claim. `capability/`, `engine/`, `notebook/`, `runtime/subproc/`,
`service/access/`, `policy/`, `secrets/`.

## 3. Security model

The section a reviewer will attack. Every subsection ends with "what this does
not stop".

3.1 Assets: secret values; the host network and the private ranges behind it;
other tenants' instances and artifacts; the host filesystem; the integrity of
the audit record itself.

3.2 Adversaries, named separately because they have different powers:
A1 the model (authors untrusted code, may be steered by injected text);
A2 data the code reads (pages, tool output, uploaded files);
A3 the tenant's own user (holds valid credentials, may exceed intent);
A4 extension authors (code that composes capabilities);
A5 the worker operator or a compromised worker process.
State which of A1–A5 each control is aimed at; a control with no adversary is
decoration.

3.3 Figure 2 — trust boundaries and what crosses them: language boundary
(no I/O primitives), process boundary (cgroup, kill on overrun), protocol
boundary (HMAC, per-child key, config on stdin), secret boundary (placeholder
in, redaction out), authorization boundary (`service/access`).

3.4 The invariant: **no ambient authority**. Naming a resource is not authority
over it. Starlark's universe has no filesystem, network or process primitives;
extensions are Starlark, never native binaries; therefore the only way to cause
an effect is a capability op. Point at `capability.Gate`'s doc comment and at
`types.CapabilityName`'s closed set.

3.5 The enforcement stack, one paragraph each, each naming its file:
(a) language — no I/O, step limit, output cap, inspection budget
(`engine/engine.go`: `maxInspectWork`, depth 64, oversized values omitted
before `String()` so a capped child cannot be OOM-killed);
(b) process — one OS process per instance, cgroup v2 memory max, kill at
`MaxExecTimeout + 30s`, node caps on instances and concurrent execs;
(c) gate — armed only for one exec, `ErrOutOfScope` for anything that outlives
it, `Revoke` on delete, `Before`/`After` hooks, one `Record` per call;
(d) capability limits — net allowlist and response cap, fs quota, secret
domains;
(e) secrets — see 3.6;
(f) policy — see 3.7;
(g) authorization — see 3.9.

3.6 Secret handling, end to end: `{{secrets.NAME}}` placeholders in code;
injection only into requests to the secret's bound hosts; `secrets.Set` never
leaves the package except inside an outbound request; every encoding
(url-encoded, base64, JSON-escaped) scrubbed from bodies, headers, errors and
audit; streaming responses redacted incrementally with a bounded pending suffix
(`secrets/redactor.go`). Say plainly that the decrypted value lives in the
instance process's memory, and what that implies for A5.

3.7 Policy: Rego hook over the normalized `Call`; `deny` is a partial set;
deny on non-empty set, evaluation error or timeout — fail closed; user policies
compiled without `http.send`, `opa.runtime`, `net.lookup_ip_addr`, `trace`,
`print`; server policies always apply, user library policies only when named,
snapshotted at create; `builtin.block_private_network` is opt-in and enforced at
dial time against every DNS result, not at parse time
(`runtime/policies.go`, `policy/policy.go`, `capability/net`).

3.8 Audit as the evidence channel: every gated call, allow and deny, with
normalized JSON-able args and metadata; by rule, no file contents and no
response bodies; reasons are redacted; records outlive the process and are
flushed before store close. Then the honest limit: the audit is a stream, not a
signed artifact — a reader cannot verify offline that a run did only what the
record says. Frame the fix (per-run signed bundle) as future work, not as done.

3.9 Authorization, kept separate from capability policy: roles ∩ key ceiling ∩
shares, decided in one place (`service/access`); invisible resources return
`not_found` before `forbidden`; bound keys hold one prefixed resource id and die
with it; `TestRouteCoverage` fails when an operation lacks a table entry. Note
that this is authority over *instances*, not over effects — section 3.4 covers
effects.

3.10 Residual risk, stated without hedging:
- A5 sees every instance on the node and their secrets in memory.
- Interpreter and Go runtime bugs are the real escape surface; the language
  boundary removes the classic ones, not all of them.
- Resource exhaustion is contained (memory cgroup, step and output caps) but CPU
  time is bounded only by wall-clock timeout.
- Extension supply chain rests on version+sum immutability, not on review.
- A1 can still exfiltrate anything a granted capability can carry; the model
  constrains *where*, not *whether*.

3.11 How the model is tested today: gate unit tests, contract tests, the
cgroup memory test that runs full inspection after a 64 MiB allocation, and
route coverage. Say what is not tested (no escape suite, no fuzzing of the
policy hook) — this is where an evaluation section 6.4 should land later.

## 4. Execution model

4.1 Three answers, described fairly before being compared:
(a) CodeAct — the model writes Python, an in-process interpreter runs it;
(b) Linux sandbox — the model writes shell, a container/microVM runs it;
(c) this system — the model writes Starlark, a per-instance process runs it and
every effect is a capability call.
For each: what the action space is, where state lives, what the audit sees.

4.2 Table 1 — the comparison, with a column per dimension: expressiveness;
isolation unit; authority model; audit granularity; state lifetime;
startup cost; determinism; blast radius of a bad action; token cost per action;
extension story. Fill cells with facts, and mark the two cells where this design
is strictly worse (library ecosystem; raw performance).

4.3 Where each wins. Argue, do not list: CodeAct wins on libraries and on
nothing else; the sandbox wins on tool familiarity and pays with ambient
authority and command-level audit; this design wins on audit and containment and
pays with a smaller language. Name the workload each is right for.

4.4 Why four hops. API never executes (so a compromised API cannot run code);
worker owns placement and process lifetime but no Starlark state; child owns all
state; `Manager` re-checks owner and epoch because it trusts no caller. The
same `runtime.Runtime` interface at every hop is what makes the path a router
rather than four implementations.

4.5 Lifecycle: create → exec → interrupt → restore → delete; TTL renewal; the
per-instance operation lease that serializes exec/run/save/restore; `exec_id`
dedup; lease epoch fencing so a stale worker cannot act for a restarted node.
Keep this to one figure (Figure 3) and one paragraph of prose.

4.6 Limits and why each exists: steps (infinite loops), output bytes (context
flooding), fs quota (disk), net response bytes (memory and context), inspection
budget (OOM before `String()`), memory cgroup (the interpreter's own
allocation), exec timeout (wall clock). Each limit should name the failure it
prevents, not just its value.

4.7 Determinism: within an incarnation, a program is a function of its globals
and its capability responses; across incarnations it is not, because state is
gone and external responses change. Therefore the unit of reproducibility is the
*notebook*, not the process — forward-reference 5.8. Say that record/replay is
not implemented.

4.8 Costs to measure, with the baseline named: cold start of a child vs an
in-process interpreter; per-call gate overhead vs a direct function call;
memory per idle instance; tokens per action vs a JSON tool call. No numbers in
this report until measured; the harness is `benchmark/api.js` and the release
gate.

4.9 Alternatives rejected, one line each with the reason: in-process Python
(no containment), WASM per exec (no persistent Starlark values, FFI surface),
container per exec (cold start, ambient authority), shell with seccomp
(expression and audit granularity).

## 5. Notebook model

5.1 The problem, in the agent's terms: a notebook file is not the program; the
kernel's memory is; stored outputs are not evidence; and the agent's one
verification move — re-run — is itself a mutation. Cite the numbers already
collected in EP-0022 rather than re-arguing.

5.2 Objects and states. Table: section fields (`name`, `description`, `code`,
`rerun`, derived `defines`/`uses`, observed `effects`, `depends_on`, last-run
facts), plus instance-level `incarnation`, `notebook_rev`, `run_seq`, `drift`.
Figure 4: the state machine — `not_run` → `fresh` / `failed` → `stale`, with the
transitions that cause each edge.

5.3 Static analysis: parse only, same dialect flags as the engine so analysis
and execution never disagree; `uses` deliberately conservative (a read before
the binding counts as external, deferred bodies resolve against guaranteed
defines only); builtins, stdlib, `env`, `secrets`, `print`, `help`, `interrupt`
and capability names are never uses. State the two things it cannot see:
mutation, and anything behind a dynamic name.

5.4 Graph: an edge is provider → consumer for each name; diagnostics for
duplicate defines, missing providers, cycles; the graph is rebuilt from saved
code on every call, never stored.

5.5 Planning: closure = targets + transitive providers; a blocking diagnostic in
the closure stops everything; order is topological over the closure; targets
always run, non-fresh upstreams run, a fresh node runs when a direct provider
runs in this plan, the rest reuse; `once` sections are skipped by bulk runs
after a success and named explicitly otherwise.

5.6 Staleness to a fixed point: base rules (no run this incarnation → `not_run`;
current-code error → `failed`; code hash changed → `stale`; else `fresh`), then
propagation — a fresh section becomes stale when a direct provider is not fresh
or ran after it. This is the part most notebook systems get wrong; show the
algorithm (`computeStates`) and why page position is irrelevant.

5.7 Effects from audit. The author does not declare effects and the parser
cannot infer them, but the last run's audit already recorded which capability
ops executed; that set is stored per section and shown in the outline, and it is
what makes `rerun: once` a decision the agent can make rather than guess. This
is C4 — give it its own subsection and an example (`net.post` appears, so the
outline suggests marking the section `once`).

5.8 Restoration: a dead process is replaced by an empty one; every section
derives `not_run`; nothing executes; stored references (secret, policy, module)
are re-resolved and a dead reference leaves the instance `lost` with the
notebook readable. `run("all")` is the property the agent can verify. Contrast
with snapshot restore (Catalyzer) and say why state reuse is declined here.

5.9 Why no reactivity, in two constraints: sections have external effects, and
the consumer has a token budget. Reactivity would re-fire `net.post` and would
spend the budget on results nobody asked for. The design answer is "mark stale,
never run".

5.10 The outline as the interface: one line per section, state and description
and effects, code and output on demand; the whole-notebook view costs tokens
proportional to sections, not to code. Tie to the tool-surface argument in
EP-0022 (few tools, stable names, no indices).

5.11 Open problems: mutation-aware dependencies (refinement, not parsing);
checkpointing to make re-execution cheaper; replay that proves no external call;
concurrent human and agent edits under `notebook_rev`; classifying effects as
safe or not without asking the author.

## 6. Evaluation plan

6.1 Questions: does the smaller language cost task success? is per-instance
process overhead acceptable? does the notebook model beat scratch execs and a
Jupyter MCP server on the same tasks? does the security model hold under attack?

6.2 Workloads and baselines: data-analysis tasks (the seismicity notebook is
the existing example), API orchestration, report generation; baselines are
scratch-only, a Jupyter MCP server, and an in-process Python interpreter.

6.3 Metrics: task success, tokens per task, p50/p99 exec latency, cold start,
memory per instance, number of external calls, and the fraction of runs where
the reported state matched reality.

6.4 Adversarial: escape attempts from granted capabilities (SSRF, redirect to
private range, secret echo in all encodings, quota and step exhaustion, a
goroutine outliving the exec), plus injected instructions in tool output. Report
attack success rate, not anecdotes.

6.5 Threats to validity: one model family, synthetic tasks, no formal proof, and
the fact that the harness runs in dev mode by default.

## 7. Limitations and open problems

One paragraph each, cross-cutting rather than per-section: worker trust; secrets
in process memory; audit not being verifiable offline; no replay; mutation
tracking; CPU time bounded only by timeout; extension review resting on sums.

## 8. Related work

Grouped, three to five sentences per group, no exhaustive list: code-as-action
and MCP design; capability and information-flow defences for agents; sandbox
mechanisms; notebooks and incremental computation; record/replay.
`docs/reading-list.md` is the long form; cite from it.

## 9. Conclusion

Restate C1–C4 in one sentence each, then the one claim worth remembering: the
interesting boundary is not "sandbox or not" but "which effects have names".

## Appendices

- A. Glossary (instance, exec, session, section, incarnation, gate, capability,
  extension, policy, artifact, drift).
- B. Section state machine and the plan table (target / upstream not fresh /
  provider runs / fresh / once already succeeded).
- C. One policy example end to end (`deny_secret_writes`): Rego, the `Call` it
  sees, the audit record it produces, the error the script gets.
- D. Reproduction: `make dev`, `make benchmark-smoke`, the release gate; what
  each proves and what it does not.
- E. Figure list: request path, trust boundaries, lifecycle, section state
  machine, notebook outline as seen by the agent.

## Writing notes

- Every claim in 3 and 4 should be checkable against a file path; if a claim has
  no file, it is an intention and belongs in 5.11 or 7.
- Do not quote limits without the failure they prevent (4.6).
- Prefer one worked example (a section that calls `net.post` with a secret and
  is later marked `once`) carried through 3.6, 5.7 and appendix C over three
  unrelated examples.
