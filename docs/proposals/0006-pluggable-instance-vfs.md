# EP-0006: A stable virtual filesystem contract with pluggable backends

- **Status:** Draft
- **Created:** 2026-10-05
- **Area:** fs capability, typed accessors, engine/session lifecycle, node assembly
- **Related:** [EP-0005](0005-instance-file-uploads.md), [EP-0002](0002-standard-library-utilities.md), [EP-0001](0001-record-replay.md)

## Summary

Keep the current in-memory filesystem as the default. Separate the product's
virtual filesystem semantics from its storage implementation, so future backends
could use a local directory, S3-compatible object storage, or another substrate
without changing scripts, upload APIs, policy metadata, or instance ownership.

The VFS is an application-defined file API, not POSIX. Missing permissions, file
descriptors, symlinks, hard links, mmap, or OS process integration are acceptable
and intentional. Do not switch to disk merely to obtain those features.

The first delivery extracts a tested backend contract and adapts memory storage.
It does not implement disk or S3, migrate existing files, or promise persistence.

## Motivation and current coupling

`internal/capability/fs/fs.go` currently combines:

- `/work` path normalization and public filesystem errors.
- A map of directory/file nodes, content bytes, and an injectable clock.
- Logical byte and file-count quotas.
- Starlark bindings, host-side accessors, and a concrete `Closer.V *VFS` handle.

`instance.Browse` reaches that concrete handle through the capability closer.
The closer also exposes `Files()`, which copies all contents for a placeholder
snapshot path. Implementing uploads directly against map fields or replacing all
of this with `os.*` would deepen storage-specific coupling.

An interface lets uploads and utilities remain useful today while keeping a
path to lower-resident-memory or remote storage if real workloads need it.

## Goals

- Preserve the existing `fs.*` script API and logical `/work` namespace.
- One authority for ownership, quota, path rules, policy, and audit.
- A common behavioral conformance suite, first exercised by the memory backend.
- Explicit byte, metadata, concurrency, atomicity, and lifecycle contracts.
- No assumption that a filesystem operation has a corresponding POSIX syscall.

## Non-goals

- Implementing a disk or S3 backend in this change.
- Host mounts, a shell, user-selected host directories, bucket browsing, or
  arbitrary access to deployment storage.
- POSIX compatibility, Unix permissions/owners, symlinks, file descriptors, mmap,
  watching, or cross-instance shared directories.
- Durable artifacts, restore-on-restart, live instance migration, or serializing
  Starlark globals.
- Exposing deployment storage credentials to scripts or instance specs.

## Layering

```text
Starlark fs.* / typed host accessor (browse, upload)
                       |
                Gate.Invoke
                       |
       VFS namespace, operation and quota contract
                       |
             per-instance storage backend
                       |
            memory (initial implementation)
            disk / S3 (future proposals)
```

The Gate continues to mediate every user-visible filesystem operation. Backend
helpers are private implementation details and cannot be invoked from Starlark or
public HTTP routes. A future storage adapter's network I/O is an implementation
of the granted `fs` authority, not a new grant of arbitrary `net` access.

Keep base capability identity `fs`; do not create separate `s3_fs` and `disk_fs`
script APIs. Pure utility modules consume values supplied by callers and do not
open backend files themselves.

## Backend-neutral contract

Define a narrow internal interface around the operations actually used by the
bindings/accessor: read, write/append, stat, list, walk, mkdir, delete, and close.
Names and Go signatures should be finalized by extracting the memory adapter,
not by imitating the entire Go `io/fs` or `os.File` surface.

Important contract properties:

- Methods accept `context.Context` so future I/O can honor cancellation/deadlines.
- Inputs are normalized virtual paths and byte values, not host paths or object
  keys. The VFS assigns a private namespace from trusted instance creation data.
- Reads and traversal accept effective byte/entry bounds; resource limits are
  checked before materializing an unbounded result.
- Mutations return a typed outcome including whether a visible commit is known to
  have happened. A transport timeout must not be mapped to definitely-not-written.
- Errors map to stable VFS kinds. Never expose buckets, host paths, storage
  credentials, or raw provider error bodies to scripts or clients.
- Mutable backend state is per instance. No process-global current directory or
  mutable file cache shared across tenants.
- The public accessor holds a filesystem interface, not `*VFS` or a raw backend.
  The engine/session closer owns release of that instance's filesystem handle.

Existing script bindings still convert read bytes into their existing Starlark
string representation. Public text preview is a separate bounded, UTF-8 checked,
secret-redacted operation as described in EP-0005. Do not confuse byte storage
with guaranteed text content or a permission to export raw secrets.

### Paths and directories

Preserve current slash-separated paths rooted at `/work`. Relative paths resolve
there; normalized paths outside it are rejected. Backend paths never escape to
scripts, audit, inspection, or spec JSON.

Directories are logical entries. `mkdir`, empty directories, parent creation,
listing, and recursive deletion must have defined behavior even when a storage
provider has only object keys. `list` returns direct children, and `walk` returns
bounded recursive entries in deterministic order. Do not pretend an object
store's raw prefix listing automatically matches these semantics.

Retain existing root deletion protection and file-versus-directory errors. Add
explicit path length/depth and directory-count ceilings so empty-directory trees
cannot evade file-byte quotas. Preserve `max_files` as the count of regular files;
introduce a separate bounded directory count rather than silently changing its
meaning. New limits and any compatibility impact must be documented.

Reject NUL and invalid namespace escapes consistently. A future disk adapter
must additionally handle platform path collisions safely; it may reject an
unsupported name but cannot alias one virtual file to another silently.

### Metadata and clocks

Expose the existing logical metadata shape: name, path, directory flag, byte size,
and mtime. Do not add inode numbers, POSIX owners, or provider storage identifiers.

Use an injected clock for logical mtimes, including on object storage. Do not make
script-visible time depend on provider upload time or a host filesystem timestamp.
Memory keeps its current wall-clock default until a deterministic clock is adopted.
This leaves EP-0001 a place to supply a per-exec clock later; it does not claim
that replay is already deterministic.

### Writes, append, and atomic visibility

A single file write publishes either the complete old value or the complete new
value. Quota/conflict/storage failures before commit leave the old value intact.
Append preserves the existing API, but may be implemented as bounded
read-modify-write; it is not a promise of a POSIX append syscall or a cheap remote
operation. Retain existing return counts and replacement behavior.

Logical recursive deletion removes the selected namespace entries as one visible
VFS mutation; physical reclamation may be delayed in a future remote backend.
This requires explicit metadata/commit semantics, not a loop of externally visible
partial deletes. A future backend that cannot meet this contract must not be
advertised as a transparent substitute.

Policy semantics remain distinct from storage atomicity: `Gate.Invoke` after hooks
run after the operation, and their rejection does not imply rollback. EP-0005
requires write receipts that preserve this distinction.

## Quotas and resource accounting

Preserve `fs.quota_bytes`, `max_files`, and `read_only` at the common VFS layer.
Today their defaults include a 64 MiB logical byte quota and 10,000 regular files.
The proposed refactor must not silently raise them or treat an S3 bucket's capacity
as the instance's quota.

- Count committed logical file bytes, not encoded HTTP size, allocated disk blocks,
  compressed storage size, or provider object versions.
- Overwrite charges `new_size - old_size`; append charges the added bytes.
- Serialize quota validation and visible mutation. Check both the applicable
  instance limit and deployment ceiling before committing.
- Release logical usage only after a confirmed mutation; ambiguous backend outcomes
  require reconciliation before admitting more writes, not optimistic accounting.
- Future upload reservations and staging have separate byte/concurrency limits.
  They cannot be invisible to all resource accounting just because they are not
  visible under `/work`.
- Deleting logical data does not necessarily mean remote staging or old object
  versions have been reclaimed. Track and bound cleanup backlog/provider storage
  separately in any future remote implementation.

A logical file quota is not a heap limit. Memory storage, read copies, CSV objects,
base64 envelopes, interpreter globals, and Go overhead share the child memory cap.
Keep bounded reads/uploads and the existing process/cgroup defenses. An S3 or disk
backend reduces resident storage but does not make parsing or `fs.read` free.
For in-process execution there is still no per-instance Go heap accounting.

Keep backend selection and deployment credentials operator-controlled. The first
release needs only the memory provider; it does not need a user-facing backend
selector or speculative bucket fields in `spec.capabilities.fs`.

## Concurrency and lifecycle

Session execution, typed console mutation, and teardown must share a documented
ordering boundary. The manager checks owner and epoch, serializes access, and
rechecks liveness before an upload or other mutation. Backend locks protect state
without rearming a Gate or reversing the session/lifecycle lock order.

Closing an instance first prevents new operations and cancels/waits for admitted
work, then releases its filesystem. The memory implementation discards its data.
Failed creation must close a partially allocated filesystem. Cleanup is
idempotent and bounded; an error is observable rather than silently becoming a
live resource leak.

A later persistent backend must define how delete, expiry, child crash, worker
restart, and abandoned staging reclaim its namespace. Boot/fencing generations
must prevent an old process or cleanup job from modifying a newer owner's files.
Process-local locking is not sufficient for stale distributed writers.

No database migration is required for the interface extraction. A later backend
that needs durable namespace metadata must separately specify its store interface,
SQLite/PostgreSQL migrations, conformance tests, and recovery semantics; do not
quietly make `store.Store` a blob store in this change.

## Future backend evaluation

| Backend | Why it might be useful | Additional obligations |
|---|---|---|
| Memory | Simple, fast, dependency-free, current behavior | Bound resident bytes and copies; data ends with the instance. |
| Disk | Lower resident file-storage pressure, local streaming | Private roots, traversal/race protection, no special files/links, staging limits, orphan cleanup, host disk capacity. |
| S3-compatible | Separate storage capacity from worker memory/local disk | Opaque per-instance prefixes, deployment credentials, logical directory metadata, conditional publication, fencing, request budgets, multipart/garbage cleanup. |

For S3, storing raw files under a prefix is not sufficient. A plausible design is
immutable content objects plus a versioned namespace manifest, with conditional
updates for visible publication and stale-writer rejection. Its latency and
metadata-size costs must be measured before choosing that design. S3-compatible
providers differ; certify the exact consistency/conditional-write behavior needed
instead of assuming every provider has identical guarantees.

Do not expose bucket names, endpoints, or arbitrary key prefixes to model-written
code. Backend credentials stay in trusted deployment configuration and must not
be enumerable through the instance's `secrets` binding. Built-in network policy
opt-outs must not become a way to reconfigure the storage endpoint.

Persistent file bytes alone do not restore globals, closures, loaded bindings,
policies, or secret state. Existing lost-instance semantics remain until a separate
recovery design is implemented. Do not label a backend durable instance storage
merely because objects survive a worker restart.

## Delivery and acceptance

1. Capture current observable behavior in a backend conformance suite.
2. Extract the interface and memory adapter; remove concrete storage assumptions
   from the fs closer, accessor, and instance browser.
3. Route EP-0005 uploads through that same contract.
4. Add another backend only in response to a measured workload and a dedicated
   proposal/test matrix. No backend is required to expose extra POSIX features.

Conformance tests cover relative/rooted paths, traversal, empty directories,
listing order, write/append counts, quota deltas, read-only mode, file/directory
conflicts, root protection, atomic replacement/deletion, cancellation, and cleanup.
Run typed accessor and script operations through the same policy/audit tests and
exercise direct, remote, and subprocess runtimes. Include concurrent upload/exec/
delete tests and bounded allocation tests under the child memory limit.

Do not implement backend export by calling the current whole-tree `Files()` copier
on large data. Snapshot/replay integration needs a bounded enumeration/streaming
contract with a separate authorization and retention design.

Use normal Go checks and relevant SDK tests; regenerate Wire if provider assembly
changes, never hand-edit generated injectors. Public/worker schema changes belong
to the specific features needing them, not an unnecessary schema change solely to
rename an internal storage type.

## Open questions

- The smallest interface that preserves current behavior without leaking the
  memory map model or overfitting a hypothetical object store.
- Default directory-count, path-depth, and per-read materialization limits.
- Whether a first non-memory workload actually needs S3, local disk, or only fewer
  memory copies. Keep the default until measurements justify a change.
- The metadata/commit model and retention policy for a future S3 backend. These
  must be resolved before claiming safe multi-worker access or recovery.
