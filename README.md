# calcside

A lightweight code-execution sandbox for AI agents, built on Starlark. There are no containers and no host processes: **capabilities are the security boundary**. Inspired by [mishudark/citron](https://github.com/mishudark/citron).

## Concepts

- **User**: signs in with Google SSO (OIDC). A dev login is available with `--dev-login`.
- **API Key**: `cs_...`. Only its sha256 hash is stored. Keys can only be managed from a logged-in session; an API key cannot mint new keys.
- **Instance**: a long-lived Starlark interpreter with its own in-memory VFS and persistent globals. It uses a sliding idle TTL; an exec or keepalive renews it. Lifecycle: create / exec / delete / expire.
- **Capability**: the globals an instance is granted. A capability that isn't granted simply doesn't exist in the script.
  - `fs`: in-memory VFS rooted at `/work`, with a quota, a file limit, and a read-only option. Path escapes are rejected.
  - `net`: HTTP with a host allowlist (exact, `*.suffix`, or `host:port`). Self-resolved DNS, IP pinning, blocking of internal addresses, and redirect re-checks. Private addresses are rejected by default (`--net-allow-private`).
  - `io`: `print` / `io.println`, with bounded output.
  - `json` and `math` are pure modules and are always available.
- **Env**: `spec.env` is a map of non-sensitive `SCREAMING_SNAKE` values readable via the frozen `env` dict (`env.get("X")`, `env["X"]`, `env.keys()`).
- **Secret**: a value scripts can never read. Use `{{secrets.NAME}}` in net URLs, header values, or bodies — the net capability injects the plaintext at send time only when the target host matches the secret's domain allowlist (https required unless `--secrets-allow-http`), and scrubs it from responses, errors, audit, and policy input. The `secrets` global exposes only `secrets.names()`. Secrets are either per-instance **inline** (`value` + `allowed_domains` in the spec, memory only, never persisted — stored spec shows `"inline": true`) or **vault refs** (`{"ref": "NAME", "allowed_domains": [narrowed]}`) resolved at creation from the user vault. Vault secrets are encrypted at rest with `--secret-key` (AES-256-GCM, base64 32-byte key), write-only via the session-only `/api/v1/secrets` endpoints, and instance refs may only *narrow* the vault allowlist. Response redaction is best-effort defense in depth — it catches raw, base64, URL- and JSON-escaped reflections, not arbitrary server-side transforms — so a secret's `allowed_domains` must only list hosts trusted with it.
- **Hook**: every op of every capability goes through the instance's Gate: `before hooks -> op -> after hooks -> audit`. Once an exec ends, the Gate is disarmed; once the instance is deleted, it is revoked. Any capability reference that leaks out of an exec then fails with `out_of_scope`.
- **Policy**: OPA/Rego, `package calcside.hooks`, `deny contains msg if {...}`.
  - Global policies come from `--policy-dir`.
  - User policies are compiled and evaluated separately. They run under a restricted builtin set (no `http.send`, `opa.runtime`, `net.lookup_ip_addr`, `print`, or `trace`).
  - Policy snapshots are taken when an instance is created. Evaluation errors or timeouts count as deny (fail closed).

Policy input:

```json
{"phase": "before|after", "user": {"id", "email"}, "instance": {"id", "labels"},
 "exec_id": "...", "capability": "net", "op": "get",
 "args": {"method", "url", "host", "port", "scheme", "secrets": ["NAME"]},
 "result": {"error": null, "meta": {"status": 200, "bytes": 123}}}
```

`args.url` is the placeholder template (e.g. containing `{{secrets.NAME}}`), never an expanded value; `args.secrets` lists the referenced secret names. `result` is only present in the after phase. It never contains file contents or response bodies. See `policies/examples/`.

## Quick start

```bash
make build                 # web console + bin/calcside + bin/csctl
make dev                   # serve with --dev-login and example policies on :8080
bin/csctl login --server http://localhost:8080 --api-key cs_...
bin/csctl run --fs -c 'fs.write("a.txt", "hi"); print(fs.read("a.txt"))'
```

Instance spec (`POST /api/v1/instances`):

```json
{"ttl_seconds": 900, "labels": {"team": "x"},
 "capabilities": {"fs": {"quota_bytes": 67108864}, "net": {"allow_hosts": ["api.github.com"]}, "io": {}},
 "limits": {"exec_timeout_ms": 30000, "max_steps": 10000000, "max_output_bytes": 1048576}}
```

## Security model and known limits

- The Starlark language has no I/O. All side effects go through capabilities, and every capability call is gated, policy-checked, and audited.
- CPU is bounded by `max_steps` and the exec timeout. Memory is bounded per call by the fs quota and the net response cap. Starlark itself has no per-exec memory accounting, so a process-wide heap watchdog (`--exec-memory-limit`) cancels all running execs when the limit is exceeded. That is a coarse guard, not per-tenant isolation. For stronger isolation, spread instances across multiple processes or nodes.
- Instance state lives only in memory. After a restart, instances are marked `lost`. The `Snapshotter` interface is reserved (currently a Noop).

## Layout

`cmd/calcside` server, `cmd/csctl` CLI, `internal/{capability,engine,instance,policy,audit,store,auth,api,client}`, `web/` console (React + Vite, embedded with go:embed).
