# Scaling APIs, Workers, and Persistent Applications

Calcside has two execution models. Their routing strategies intentionally differ.

| Property | Ephemeral notebook | Persistent application |
|---|---|---|
| State between calls | Globals and VFS remain in an instance process | Fresh globals, modules and VFS for every invoke |
| Placement | One worker chosen at creation | One prepared replica per eligible live worker, capacity permitting |
| Routing | Persisted instance-to-worker binding | Per-application round robin among currently eligible ready replicas |
| Add a worker | Only new notebooks can be placed there | Existing enabled applications gain replicas through reconciliation |
| Remove a worker | Its notebook memory becomes unavailable; never silently reroute | Other healthy replicas continue to serve new invokes |
| In-flight operation | Remains on its original process | Remains on its selected replica; no transparent invoke retry |

"Persistent" describes the desired deployment and immutable release, not a
session whose interpreter memory survives an invocation. Do not use application
globals or `/work` as cross-invocation storage.

## Shared Authority and API Replicas

Use PostgreSQL for more than one API replica. SQLite is for single-API
deployments; the Helm chart rejects SQLite with multiple API replicas.

All APIs use the shared database for worker membership, instance bindings,
application desired generations and replica leases. Workers do not access SQL.
No Redis, DNS-based worker membership, consistent-hash request routing, or
distributed routing-cache invalidation is required.

Notebook route caches retain an immutable binding. A cold API resolves that same
binding from the store; adding application replicas or workers does not alter it.
Application invocations select a concrete replica ID first. Transport routing
then resolves that replica's worker under its lease and fencing tuple.

Round-robin counters are process-local, per persistent instance, and are not
ownership records. Different APIs can choose different eligible replicas.
There is no globally coordinated least-loaded queue or global round-robin
sequence. Correctness depends on shared leases and bindings, not identical
counter values or simultaneous membership observations.

Artifact-preview snapshots remain API-process-local. Sharing the routing store
does not make preview snapshots durable or available on every API; that is a
separate multi-API limitation.

## Worker Enrollment and Liveness

Workers register `node_id`, a process boot ID, serving capacity, and a reachable
`host:port` over the existing HMAC-signed control channel. Registration persists a
normalized endpoint and an incarnation in `serving_nodes`. The shared worker key
is also the enrollment boundary: address validation is syntactic, not protection
against a compromised trusted worker. Use a trusted control network.

The API no longer accepts `--workers` / `CALCSIDE_WORKERS` or a discovery-mode
switch. API configuration requires `--worker-key`; worker configuration requires
`--api-addr`, `--shared-key`, and `--advertise-addr`.
`CALCSIDE_WORKER_ADVERTISE_ADDR` is the corresponding environment variable.

Helm advertises the downward-API pod IP, rather than generating an address list
from the worker count. Compose and dev recipes set an explicit reachable endpoint.
Changing only `worker.replicaCount` changes neither the API pod template nor the
existing worker pod template.

- Default node heartbeat TTL: 45 seconds.
- Default replica lease: 30 seconds, never longer than the node heartbeat.
- Default worker sync interval: five seconds.
- `/healthz`: process liveness, independent of API contact.
- `/readyz`: registration and the last heartbeat sync succeeded; a sync error
  clears readiness until a subsequent successful sync.

Heartbeats are failure detectors, not instant failure notification. APIs compare
shared deadlines using their own clocks and database snapshots. A worker can
fail after a readiness check but before a request reaches it.

## Persistent Replica Reconciliation

For every enabled desired instance, the scheduler attempts to fill one replica
on each live registered worker, subject to its serving capacity.

1. Read registered live nodes and a page of desired instances.
2. Read the instance's unexpired nonterminal replicas.
3. Skip workers already holding an unexpired lease for that instance.
4. Claim missing placements transactionally; workers prepare assignments through
   the signed control channel and report readiness.
5. Workers renew only locally ready inventory under the complete fence.

Claims preserve the database lock order (node, then logical instance), node
capacity checks, expected-epoch CAS, and unique `(instance_id, epoch)` allocation.
Two API schedulers cannot allocate duplicate live placements for the same
logical instance on the same stable node.

The epoch is a monotonically allocated identifier, not a singleton-owner epoch.
A valid replica at epoch 1 remains eligible after another worker claims epoch 2.
`LastEpoch` is the allocation high-water mark. `ActiveReplicaID` and
`GetCurrentServingReplica` remain compatibility/diagnostic metadata and are
not request-routing authority.

An old unexpired lease still blocks a replacement on the **same node** across
boots, generations and releases. New boot registration does not shorten old
leases. Other workers may hold simultaneous independent leases. Historical boot
IDs cannot re-enroll after a new boot supersedes them.

Reconciliation scans at most 200 desired instances and makes at most 200 claim
attempts per pass. Placement uses the existing deterministic rendezvous
preference, rotated between passes so full prefix workers do not permanently
starve later candidates. The instance cursor advances only over processed
instances. Filling a large pool may take several passes.

Preparation failures retain bounded exponential backoff on the logical instance
(five seconds up to five minutes). That backoff delays new claims, not invokes
on healthy siblings. A duplicate READY report does not erase failure backoff.
Per-worker failure backoff and demand-based replica-count policies are not
implemented.

## Invocation Eligibility and Lifecycle Changes

An application can serve while at least one replica is ready. Each selected
candidate must match:

- Enabled desired instance, workspace, release and generation.
- READY state and an unexpired lease.
- A positive allocated epoch no higher than `LastEpoch`.
- Live node heartbeat with the same node boot and incarnation.

Preparing, terminal, expired, old-generation, old-release and stale-boot replicas
are never load-balancing candidates. Status polling does not advance invocation
round robin. A healthy sibling keeps the application ready; a valid preparing
sibling reports preparation rather than being hidden by an older failure
diagnostic.

Every replica still admits one active engine call plus 32 queued calls. More
prepared replicas increase execution parallelism and total queue capacity, but
request-level round robin does not inspect queue depth. Overload can produce
admission errors even when another replica's queue has spare capacity.

Disabling, deleting or publishing a replacement release changes the desired
generation/enablement. That invalidates the entire old pool for new routing and
renewal; workers drain or delete obsolete assignments through normal sync.
This is not a zero-downtime rolling-release mechanism. A new generation may
temporarily have no ready replicas while old leases drain and new preparation
completes.

In-flight invokes keep their selected replica and lease snapshot. A timeout or
transport failure can leave the external side-effect outcome unknown. There is
no transparent reroute/replay, exactly-once execution guarantee, or restoration
of notebook memory after worker loss.

## Capacity and Practical Limits

Worker capacity, worker CPU/memory, child-process overhead, application count,
database transaction throughput, network latency, and downstream services all
limit scaling. Every application targets every eligible worker: memory/process
cost grows approximately with applications multiplied by workers, not just with
request volume.

Adding API replicas increases control-plane capacity, not the execution
parallelism of one notebook. Adding prepared application replicas enables
parallel invokes, but does not imply linear speedup for every workload.
Multiple workers on one kind node still share one Docker VM and physical host.

Future independent improvements include demand-based replica policies,
queue-aware balancing, per-node preparation backoff, shared artifact-preview
snapshots, and a staged generation rollout. None is required for the current
shared-store ownership and fencing guarantees.

## Reproducing the Local Experiment

The experiment requires a **dedicated disposable** kind deployment. It scales
workers and temporarily sends SIGSTOP/SIGCONT to worker 2. Do not run it against
a shared environment. It leaves its notebooks, applications, database, cluster,
and port-forwards intact for inspection.

1. Build API/worker, migrate and render images using their Dockerfiles. Deploy
   with `benchmark/deploy-release.py kind STATE_DIR`, an unused
   `TEST_KIND_CLUSTER` / `TEST_KIND_PORT`, `TEST_API_REPLICAS=2`, and the chosen
   `TEST_IMAGE_TAG`.
2. Before creating fixtures, apply `benchmark/scalability-values.yaml` as a Helm
   overlay with `--reuse-values`. Use PostgreSQL; the measured run assigns
   PostgreSQL requests `250m/256Mi` and limits `2 CPU/2Gi`.
3. Replace `__API_UPSTREAMS__` in `benchmark/scalability-fixtures.yaml` with two
   `server <api-pod-ip>:8080;` entries and apply it to namespace `calcside`.
   The proxy disables retries and exposes the actual API upstream in a response
   header. Unlike port-forwarding the API Service directly, it balances
   individual requests across both APIs. Record fixture image IDs.
4. Forward the proxy and both API pods. In private `STATE_DIR/connection.json`,
   retain `api_key`, set `base_url` to the proxy, add sorted-pod `api_urls`, and
   set `delay_url` to `http://scalability-delay:8080/delay`.
5. Run and analyze:

```sh
python3 benchmark/scalability.py --state STATE_DIR --output NEW_RESULT_DIR \
  --confirm-isolated --duration 30 --repeats 2
python3 benchmark/analyze-scalability.py NEW_RESULT_DIR \
  --output NEW_RESULT_DIR/analysis.json
```

The runner creates two applications and one notebook. CPU invokes sum squares
over `range(50000)`; the exact answer is `41665416675000`. I/O invokes wait
100 ms on a local HTTP fixture which returns the caller's worker pod IP.
Both workloads assert a fresh VFS on every invocation. Measurements use
closed-loop constant VUs, no think time, worker counts 1/2/3, VUs 1/8/24, two
repeats, five-second warmups, and reversed workload/VU order on repeat two.
Fixture creation/publishing uses a bootstrap-admin session; timed invocations use
the generated API key. API keys intentionally cap at Run and cannot publish
applications. The state directory must therefore retain `credentials.json` as
well as `connection.json`; neither belongs in committed results.

Two under-load expansions, a bounded-queue overload case, a reversible worker
pause, recovery, and notebook binding/state checks supplement the steady matrix.
The notebook is seeded through API A and first read through cold API B after
worker expansion. Ready pools are observed with the same generation/lease/node
fences used by request routing, not the legacy active pointer.

Successful throughput divides semantically valid completion count by k6's
observed test duration, including graceful drain. Report errors separately from
HTTP status, because an HTTP 200 can contain an engine error. Latency summaries
include all attempts and successful invokes separately. Aggregate tables show
median replicate throughput and the **range of per-run p95s**, never an invented
pooled percentile. Two repeats do not establish statistical significance.

`cases.json`, k6 summaries/logs, `events.json`, five-second kubelet samples,
source hashes, and before/after pod inventories are the source artifacts.
The analyzer reads only that captured data and does not rerun the experiment.
Keep credentials outside the repository and review artifacts before committing.

### Rendering Report Figures

The report embeds self-contained SVG figures for throughput, normalized speedup,
and the verified worker-stall timeline. Regenerate them from the committed,
frozen result captures without querying a server or rerunning the experiment:

```sh
python3 benchmark/plot-scalability.py benchmark/results/scalability-2026-11-14
```

The renderer uses only Python's standard library. Throughput bands represent the
observed two-run range, not confidence intervals. Timeline bars count requests
by their start cohort rather than completion time.
