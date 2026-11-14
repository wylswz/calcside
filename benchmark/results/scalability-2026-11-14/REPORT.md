# Calcside Scalability Experiment

Date: 2026-11-14 UTC. Local kind; two API replicas; PostgreSQL; one to three workers.

## Conclusions

1. Persistent applications now scale through **pre-scheduled replica pools**.
   At 24 concurrent clients, successful throughput rises from 9.48 to 28.58/s
   for the 100 ms I/O workload (**3.02x**), and from 100.48 to 199.28/s for
   the CPU workload (**1.98x**). These are workload-specific local observations,
   not a claim of unlimited or universally linear scaling.
2. All **73,647 steady-state invocations** produce the exact expected result
   and a fresh invocation VFS. No steady-state invocation fails.
3. Both APIs actually serve traffic: **36,827 / 36,820** steady-state requests,
   with zero unattributed API responses. This does not rely on assuming an API
   Service port-forward distributes requests.
4. Two expansions under load complete without application errors or API
   replacement. Existing application replica IDs and the notebook's worker
   binding, epoch, globals and file contents remain unchanged.
5. Failure detection is not instantaneous. A verified 59.7-second worker-process
   freeze produces **24 client transport timeouts**. Healthy replicas remain
   eligible; the pool contracts after lease expiry and recovers after resume.
   No transparent invoke replay is introduced.
6. Overload is a real limit. At 120 I/O VUs on three workers, only **960/6,934**
   attempts have validated successful output. An HTTP 200 alone is not proof
   of successful execution: many overloaded durable runs have `unknown` status.

Implementation and invariants: [technical design](../../../docs/scalability.md).

## Environment and Isolation

| Component | Configuration |
|---|---|
| Host | macOS arm64; k6 runs on the host |
| Docker VM | 12 vCPUs; 12,599,218,176 bytes memory, about 11.7 GiB |
| Kubernetes | Single-node kind cluster `calcside-scalability` |
| API | Two pods, each limited to 2 CPU / 1 GiB |
| Workers | One, then two, then three pods; each limited to 1 CPU / 1 GiB |
| PostgreSQL | PostgreSQL 17; 2 CPU / 2 GiB limit; disposable test `emptyDir` |
| Traffic proxy | nginx; 1 CPU / 128 MiB; explicit two-API upstreams; retries disabled |
| I/O fixture | Python HTTP server; fixed 100 ms wait; 1 CPU / 256 MiB |
| k6 | 1.2.3 |
| kind / Helm | 0.29.0 / 3.19.0 |
| Application images | `ghcr.io/wylswz/calcside{,-migrate,-render}:scalability-local` |

Image IDs, exact resources, pod identities and tool versions are captured in
`environment-facts.json`. Source file hashes and the pre-commit revision/dirty
state are in the raw archive. All runs use the new pool implementation, including
the one-worker baseline; this is a worker-count comparison, not an old/new
software-version comparison.

The new cluster is isolated from the existing `kind` / `calcside-stress` clusters
and user Compose project, which are left untouched. It is **not** an isolated
physical host: all local clusters still share Docker and host resources.

During bootstrap, changing PostgreSQL resources recreated its test `emptyDir`
database before any workload fixtures existed. Migrations and generated
credentials were recreated before measurement. No subsequent PostgreSQL
replacement occurs during the experiment. This is also why the disposable
deployment is not a production persistence example.

## Method

- Closed-loop, constant VUs, no think time: workers 1/2/3; VUs 1/8/24;
  two 30-second replicates for each workload and topology: **36 timed cases**.
- Five-second warmups are separate from the matrix. Workload and VU ordering
  reverses in replicate two, but topology grows monotonically.
- CPU: sum `i*i` over `range(50000)`, exact answer `41665416675000`;
  write an invocation-local VFS marker and verify it did not exist on entry.
- I/O: the same fresh-VFS assertion plus an HTTP call with a controlled 100 ms
  server delay. The response contains the caller's worker pod IP.
- Every successful sample requires HTTP 200, no engine result error, the exact
  expected output, and `fresh == true`. Timeouts, HTTP errors and missing/invalid
  output are not counted as successful throughput.
- Fixture management uses an administrator session. Timed requests use an API
  key; keys intentionally cannot configure or publish applications.
- Host traffic enters a forwarded nginx pod. nginx balances each request across
  two API pod IPs and returns `X-Scalability-API`. Forwarding the API Service
  directly would normally pin one selected API pod.
- Successful requests/second divide validated completions by k6's observed
  duration, including graceful drain. Latencies are client-observed wall time.
  Tables show median replicate throughput and the **range of per-run p95s**,
  not a pooled percentile or a confidence interval.

Two repetitions are exploratory evidence, not a statistical-significance test.
Closed-loop clients reduce offered traffic while blocked; this is not an
open-loop arrival-rate capacity or queue-stability benchmark.

## Steady-State Results

No errors in any row. Throughput is successful invokes/s; p95 is milliseconds.
Parenthesized throughput values are the two-replicate minimum and maximum.

### CPU

| Workers | VUs | Median Throughput (Range) | Per-Run p95 Range | Speedup vs 1 Worker |
|---:|---:|---:|---:|---:|
| 1 | 1 | 63.45 (61.30-65.60) | 20-21 | 1.00x |
| 2 | 1 | 58.75 (58.01-59.49) | 21-22 | 0.93x |
| 3 | 1 | 56.13 (55.89-56.38) | 23-23 | 0.88x |
| 1 | 8 | 94.30 (94.02-94.59) | 100-101 | 1.00x |
| 2 | 8 | 157.32 (151.12-163.52) | 72-83 | 1.67x |
| 3 | 8 | 184.39 (178.63-190.16) | 63-67 | 1.96x |
| 1 | 24 | 100.48 (98.23-102.73) | 254-291.5 | 1.00x |
| 2 | 24 | 167.80 (160.67-174.93) | 205-278 | 1.67x |
| 3 | 24 | 199.28 (198.95-199.61) | 184-192 | 1.98x |

CPU scaling is useful but sublinear. With one VU there is no request parallelism
to unlock, and additional pool/membership work does not make an individual
invocation faster. At 24 VUs, three workers deliver about 66% of ideal 3x
scaling. This experiment does not isolate the limiting cause.

### Fixed-Latency I/O

| Workers | VUs | Median Throughput (Range) | Per-Run p95 Range | Speedup vs 1 Worker |
|---:|---:|---:|---:|---:|
| 1 | 1 | 8.41 (8.17-8.65) | 126-130 | 1.00x |
| 2 | 1 | 8.19 (8.14-8.23) | 131-134 | 0.97x |
| 3 | 1 | 8.16 (8.08-8.24) | 132-137 | 0.97x |
| 1 | 8 | 9.51 (9.51-9.52) | 852.4-853 | 1.00x |
| 2 | 8 | 19.05 (19.05-19.06) | 434-435 | 2.00x |
| 3 | 8 | 28.69 (28.65-28.74) | 322-325 | 3.02x |
| 1 | 24 | 9.48 (9.46-9.50) | 2552.6-2555 | 1.00x |
| 2 | 24 | 19.06 (19.03-19.08) | 1273-1278 | 2.01x |
| 3 | 24 | 28.58 (28.53-28.64) | 924.9-940 | 3.02x |

Each replica executes one invoke at a time. The delay workload makes that
serialization visible: approximately ten invokes/s per prepared replica before
protocol overhead. More workers provide additional prepared replicas and real
parallelism. This controlled fixture has no external service rate limit and
does not predict the behavior of a production downstream API.

Worker attribution at 24 I/O VUs, summed over both replicates:

| Workers | Worker 0 | Worker 1 | Worker 2 |
|---:|---:|---:|---:|
| 1 | 616 | 0 | 0 |
| 2 | 595 | 595 | 0 |
| 3 | 588 | 589 | 588 |

CPU invocations deliberately omit the extra worker-identity network call.
Their successful counts are measured, but worker attribution is not inferred
from the I/O counters.

## Scaling While Requests Continue

Both transitions run 12 I/O VUs for 80 seconds, requesting expansion at second 20.

| Transition | Valid Attempts | Errors | Successful Throughput over Whole Transition |
|---|---:|---:|---:|
| 1 to 2 workers | 1,336/1,336 | 0 | 16.47/s |
| 2 to 3 workers | 2,115/2,115 | 0 | 26.27/s |

The whole-transition rates mix two topologies; they are not steady-state
capacity estimates. The first five-second sampling observations of both
applications' full new pools occur approximately 3.2 seconds and 1.8 seconds
after their respective scale requests. These are sampling observations, not
precise scheduling/preparation latency measurements.

Two applications produce 2, then 4, then 6 ready replica rows. All existing
`(instance_id, node_id) -> replica_id` pairs survive each addition. Both API pod
UIDs remain unchanged with restart delta zero. The original worker, PostgreSQL,
renderer and test fixture pod identities also remain unchanged during the full
run.

## Notebook and Multi-API Correctness

The notebook is created and seeded through direct API A before expansion.
After expansion, both API A and initially cold API B read:

```text
4242
stable-notebook
```

The persisted tuple remains
`ins_p9SLx65HKDIDaBdtU5ZOad8h -> calcside-worker-0, lease_epoch=1`.
It is checked again after the verified worker-2 freeze/recovery. This demonstrates
preserved globals, VFS and shared binding resolution for this surviving notebook.
It does **not** demonstrate recovering memory after its own worker is destroyed.

The integration suite separately verifies two-worker concurrent invocation
overlap, third-worker addition with old replica IDs intact, new-generation
output after republishing, and disabled-pool rejection through both API handlers.

## Verified Worker Failure and Recovery

The accepted fault experiment is `verified-fault/`, not the original full-run
pause case. It signals the worker-2 process from the parent kind-node PID
namespace and verifies `/proc/<pid>/status` reports **T (stopped)**.

- Effective pause: `14:19:01.633Z`; resume: `14:20:01.347Z`; duration **59.714 s**.
- The forwarding/nodeagent process is frozen; child engine processes are not
  deliberately frozen. This is a controlled worker-process stall, not a pod
  crash, host failure or network partition.
- Across the 120-second case: **2,361/2,385** validated completions;
  **24 transport timeouts**; no additional HTTP or invalid-output failures.
- Maximum observed latency: **15,004 ms**, matching the configured 15-second
  client timeout. Overall p95 is 684.8 ms and p99 about 3,019 ms; neither
  percentile should obscure the 24 individual timeouts.
- The first sampled two-ready-replica pool occurs about **30.7 s** after pause,
  consistent with the default 30-second replica lease. Heartbeat TTL is 45 s.
- Request-start cohorts after lease expiry use only the two healthy workers;
  five-second cohorts contain roughly 96 successful attempts. Three-worker
  cohorts return after resume, at roughly 144 successful attempts per five seconds.
- The first sampled three-replica pool occurs about **1.8 s** after resume.
  Samples are five seconds apart; this is not an exact recovery SLO.
- Separate post-recovery case: **876/876** successful, **28.77/s**, p95 **518 ms**.
  Both API identities and notebook state/binding still match.

Trace bins group requests by **start time**, not completion time. Some bins
have no starts because all twelve closed-loop clients are blocked on timeouts.
Do not interpret those gaps as a measurement of zero server capacity.

Requests may select a just-failed worker until shared lease/heartbeat deadlines
exclude it. This is expected detection delay. There is no cross-worker replay,
and a failed transport does not prove that an external side effect did not occur.

## Overload and Unknown Outcomes

At 120 I/O VUs, three replicas can hold only one active plus 32 queued invokes
each. Round robin is not queue-depth-aware.

| Outcome | Attempts |
|---|---:|
| Exact validated successful output | 960 |
| Non-200 HTTP response | 210 |
| HTTP 200 without the required successful output | 5,764 |
| Total | 6,934 |

Validated throughput stays **28.64/s**, while success ratio falls to about
**13.8%**. The overload case permits failures intentionally; its zero k6 exit
code is **not** a zero-error claim.

The captured local database diagnostic corroborates 5,764 `unknown` runs in the
overload creation-time window. The existing durable-run completion path marks
invocation errors or missing results unknown and can return an HTTP-200 run view
without successful output. These are **not known-successful executions**, nor
are they all proven-not-executed rejections. Clients must inspect `run_status`,
the engine result, and the expected output; blind retries are unsafe.

The diagnostic window also contains seven running and 960 succeeded rows.
Database creation-time cohorts are not identical to client request/response
cohorts, so those SQL rows must not be added up as a second HTTP request count.
The exact query and output are preserved with the raw results.

Potential follow-up: distinguish provably rejected admission from ambiguous
transport outcomes and expose clearer overload responses. That behavior is
reported here, not silently changed or counted as successful scaling.

## Resources and Limits of Interpretation

There are 296 full-run resource samples and zero sampler errors. Suite-wide
maximum sampled interval CPU and working set:

| Container | CPU Cores | Working Set |
|---|---:|---:|
| API A / B | 0.339 / 0.322 | 32.3 / 31.2 MiB |
| Worker 0 / 1 / 2 | 1.020 / 1.032 / 0.878 | 58.1 / 47.1 / 46.3 MiB |
| PostgreSQL | 1.865 | 200.3 MiB |

These are sampled interval maxima over the suite, not simultaneous peaks or
per-case averages. Short CPU excursions around a one-core quota are possible.
PostgreSQL approaches its two-core limit and earlier workers approach one core.
Shared database and host costs are plausible constraints on CPU scaling, but
no profile or controlled database-resource comparison establishes causality.

This experiment does not prove API-only speedup, multi-node linear scaling,
infinite worker capacity, crash-safe notebook memory, exactly-once side effects,
zero-downtime generation changes, or preview-snapshot sharing between APIs.
The application-per-worker policy costs approximately O(applications x workers)
processes/memory; preparation backoff, bounded reconciliation, queue limits,
database writes and downstream quotas remain real constraints.

## Verification, Artifacts and Reproduction

The implementation passes Go build/vet/race tests, real PostgreSQL store
conformance/migration tests, and eleven Python SDK client smoke tests. Additional
pool/fencing/scheduler/notebook integration tests pass after review fixes.
Registration checks include Helm lint/render, Wire checks, and web
typecheck/lint. No new browser-suite or cgroup-specific stress run is claimed.

- `steady-analysis.json`: every full-run summary and the 18 aggregate matrix rows.
- `fault-analysis.json`: accepted verified-pause and recovery summaries.
- `report-facts.json`: expansion, replica-ID preservation, fault sampling and
  pod identity evidence.
- `environment-facts.json`: sanitized runtime/image/resource configuration.
- `raw-results.tar.gz`: original summaries, traces, samples, source hashes,
  fixture code/IDs, notebook checks and the overload diagnostic.
- `SHA256SUMS`: integrity checks for packaged data.

Reproduction commands and credentials handling are in
[docs/scalability.md](../../../docs/scalability.md); workload, analyzer, resource
overlay and Kubernetes fixtures live in `benchmark/`. Credentials, API keys,
Helm secret values and private connection files are not committed.

Excluded setup observations are retained or disclosed rather than promoted to
evidence: initial fixture creation with a Run-only API key returned 403 and was
corrected to session-based management; the original same-namespace SIGSTOP of
PID 1 was ineffective; the first corrected fault attempt used the wrong
container name and aborted before signaling. The verified fault-only rerun
reuses captured fixture IDs and inputs and checks the effective stopped state.
The original steady/expansion measurements were not rerun to hide these issues.

The dedicated test cluster and port-forwards remain running for inspection.
