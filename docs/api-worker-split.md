# API / worker split — implementation plan

Goal: run the API (control plane) and the execution tier (workers) as separate
processes that scale independently, without coupling core business logic to any
transport or job framework.

Phase 1 (done) separated the domain: `internal/runtime` is a pure, serializable
message contract; `*instance.Manager` satisfies `runtime.Runtime` and touches no
store; `service/sandbox` owns all persistence, vault resolution, policy
compilation, audit batch writes, and TTL expiry. This document covers what
remains to put a network boundary between them.

## Decisions already made

- No standalone gateway. API nodes are stateless peers and forward only
  execution-dependent ops (`create`, `exec`, `delete`, `files`, `prompt`) to the
  owning worker.
- No API↔API forwarding. Ever.
- Authoritative `instance_id → node_id` binding lives in Postgres, next to the
  instance lifecycle it is part of. Redis holds node liveness keys, route
  caches, and quota counters — never the source of truth.
- Live Starlark state (globals, VFS, decrypted secrets) is non-serializable, so
  instances are pinned to one worker for life. No consistent hashing; remapping
  means the instance is `lost`.
- Node identity is two-layered: `node_id` is **stable across restarts**
  (configured or persisted, not generated per boot); `boot_id` identifies one
  process incarnation. A restart keeps the node_id — its bindings still point
  at it — but the new boot_id tells the control plane every instance bound to
  it is dead.
- Asynq is only for genuinely async work (audit flush, `TouchAPIKey` batching,
  node-death reconciliation, session GC, ext prewarm). Never on the `exec` path.
- Workers never query the database. All context arrives inside the request.

## Phase 2 — placement & store schema  ✅ landed

Additive; no behaviour change yet.

1. `instances` += `node_id TEXT`, `lease_epoch INTEGER`. Migration +
   `storetest` conformance updates (AGENTS.md: interface, gormstore, suite).
2. `MarkRunningAsLost` → `MarkRunningAsLostForNode(nodeID)`; the global variant
   dies. Startup recovery becomes per-node.
3. `internal/placement` port:

   ```go
   type Registry interface {
       Bind(ctx, instanceID, nodeID string) (epoch int64, err error) // CAS insert
       Lookup(ctx, instanceID string) (node NodeRef, err error)      // node_id, addr, epoch
       Release(ctx, instanceID string, epoch int64) error            // epoch-guarded
       Heartbeat(ctx, nodeID, bootID, addr string, ttl time.Duration) error // liveness key
       Nodes(ctx) ([]NodeRef, error)                                 // live nodes
       CountForUser(ctx, userID string) (int, error)                 // cluster-wide quota
   }
   ```

   `placement/memory` for dev/tests; Postgres does `Bind`/`Release` (atomic CAS
   on `instances`); Redis does liveness + a short-TTL `instance→node` route
   cache in front of `Lookup`.
4. Create path: `sandbox.Create` persists the row, picks a live node
   (least-loaded by `CountForUser`-style scan of `Nodes()`), `Bind`s it, then
   calls `Runtime.Create` on that node. Failure before bind ⇒ delete row.

## Phase 3 — transport  ✅ landed (static node list; Redis liveness in P5)

1. `internal/runtime/remote`: `remote.Client` — a `Runtime` that resolves
   placement and calls the owning node over HTTP/JSON. The protocol is
   defined in `api/worker.openapi.yaml`; gin routes and the typed client
   are generated (`internal/runtime/remote/gen`), message bodies map via
   `x-go-type` straight onto the contract types so the two never drift.
2. `cmd/calcside-worker`: flags `--node-id` (or `--node-id-file` — generate
   once on first boot, persist, reuse), `--listen`, `--placement`,
   `--heartbeat-interval`. Resolution order: flag/env → persisted file →
   generate-and-persist. In k8s map this to the StatefulSet pod name; a
   random per-boot UUID would orphan every binding the node owned.
   Startup: generate `boot_id`, register node, start heartbeat loop, serve
   `Runtime` RPCs over `*instance.Manager`. SIGTERM: stop accepting `Create`,
   drain or mark instances, deregister, exit.
3. API↔worker auth: shared secret in config; every request carries
   `X-Calcside-Node` + HMAC-SHA256 signature over (method, path, body, ts);
   worker rejects unsigned/stale requests. mTLS can replace it later without
   touching `Runtime`. Workers bind to a private interface; never exposed
   publicly.
4. Defence in depth on the worker: verify `req.Owner.UserID` equals the bound
   owner and `req.Epoch` equals the live binding epoch before touching the
   instance.
5. `remote` failure handling: 30s route cache, invalidated on transport error
   or `ErrStaleEpoch`; connection pool + per-node concurrency cap; API ctx
   deadline = worker exec timeout + margin so cancellation propagates.
6. Local ext sources live only on the API tier's filesystem. Workers carry
   no mount of it; `capext.Options.LocalResolver` resolves local identifiers
   over `GET /internal/v1/ext/tree` (same HMAC scheme) into the worker's own
   cache dir, keyed by path and invalidated by the tree's h1 sum.

## Phase 4 — idempotency & fencing  ✅ landed (worker-side dedup + epoch check)

`net` calls inject secrets into real external APIs; a retried `exec` is a
security event, not just a correctness bug.

1. `ExecRequest.ExecID` is generated once per user request and survives API
   retries. Worker keeps `exec_id → result` in memory (TTL ~10 min): duplicate
   delivery returns the stored result.
2. `Create` is naturally idempotent via the `instances` PK + `Bind` CAS.
   `Delete` is idempotent by definition. Reads may retry freely.
3. `lease_epoch` increments on every `Bind`; requests carry it and the worker
   rejects stale epochs (`ErrStaleEpoch`) — this is what stops a zombie node
   (GC pause, partition heal) from executing against an instance it lost.
4. Timeout rule: if the API↔worker call times out, the API returns the known
   `exec_id` state as unknown/retryable rather than fabricating a failure —
   the worker may still be running it.

## Phase 5 — shared lifecycle state

1. Keepalive becomes an API-side write: renew the Redis TTL key + `UPDATE
   instances SET expires_at`. No worker hop for the highest-frequency endpoint.
2. `sandbox.ExpireDue` (already API-side) is the only authority flipping
   running→expired; it `Release`s placement so stale workers reject late
   traffic. Worker `Reap` stays memory-only.
3. `MaxInstancesPerUser` enforcement moves to `CountForUser` over live
   bindings — correct across N API nodes.
4. Node death: liveness key expires ⇒ reconciler job runs
   `MarkRunningAsLostForNode` + releases that node's bindings.
5. Node restart: heartbeat value carries `boot_id`. The reconciler sees a live
   node whose incarnation changed ⇒ all instances still bound to that
   `node_id` are marked lost and released immediately — deterministic, instead
   of waiting for sessions to time out. Because the node_id is stable, the
   same worker can take new `Bind`s right away; only its old in-memory state
   is gone.

## Phase 6 — async jobs & caching

1. `internal/jobs` port (`Dispatcher`/`Handler`) + `jobs/asynq` adapter; asynq
   types appear only inside the adapter.
2. First jobs: audit batch flush, `TouchAPIKey` throttle→batch, node-death
   reconciler, session/API-key GC.
3. `internal/cache` port + `cache/redis`: `auth.Resolve` caches
   `hash(token)→Principal` for ~30–60s with a revocation set for deletes;
   session lookups same. This removes the per-request write (`TouchAPIKey`)
   from the hot path — the single biggest DB win before any worker exists.

## Phase 7 — verification

- Serialization round-trip for every contract message (done, `contract_test.go`).
- Worker integration test booting `Manager` + transport with no DB handle —
  proving the no-store boundary holds end to end.
- Fencing: stale-epoch request rejected; zombie node cannot exec.
- Idempotency: duplicated `exec_id` returns the same result; create/delete
  retries safe.
- Full gate: `make lint`, `make build`, `go test -race ./...`, web checks.

## Open questions

- **Placement choice**: API picks the node then `Bind` (chosen above — simple,
  one hop) vs worker-side CAS claim. Revisit only if create contention shows up.
- **Route cache TTL**: start at 30s; invalidation on error matters more than TTL.
- **Streaming exec output**: not planned; if needed later it is a new `Runtime`
  method, not a gateway.
