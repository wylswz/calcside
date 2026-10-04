# AGENTS.md

## Commands
- Go checks: `go build ./... && go vet ./... && gofmt -l . && go test -race ./...`
- Web checks: `pnpm -C web typecheck && pnpm -C web lint && pnpm -C web build`
- `make gen` regenerates code from `api/openapi.yaml`; `make gen-check` fails if generated files drift.
- `make lint` runs gen-check + vet + gofmt + web typecheck/lint; `make build` builds web then `bin/calcside`, `bin/csctl`.
- `make dev`/`serve-dev` pass `--net-allow-cidrs=$(DEV_NET_ALLOW_CIDRS)` (default `198.18.0.0/15`) to exempt fake-ip proxy ranges from net's SSRF blocking; set `DEV_NET_ALLOW_CIDRS=` to disable.
- Dev: `make dev` starts the backend in dev mode (anonymous auth, no login) on :8787 plus the Vite dev server on :5173. `make serve-dev` runs the backend alone with the embedded UI.
- `make test-cgroup` runs the per-instance cgroup memory-limit test (`internal/node/subproc/cgroup_linux_test.go`) as root in a privileged throwaway Docker container; plain `go test` skips it unless `CALCSIDE_TEST_CGROUP=1`. `make test-cgroup-host` runs it on a Linux host via sudo with `-race`.
- CI (`.github/workflows/ci.yml`): `unit` (Go build/vet/gofmt/`test -race`), `integration-cgroup` (`make test-cgroup-host` + `make test-cgroup`), `integration-sdk` (`make sdk-test`). Actions are pinned by commit SHA.
- Python SDK (`sdk/python`, uv project): `make sdk-test` (pytest, builds + boots a dev-mode server), `make sdk-lint` (ruff). Not part of `make test`/`make lint`.

## Conventions
- No container / host process capabilities. New capabilities implement `capability.Factory` and must route every op through `Gate.Invoke` so hooks and audit see it.
- Result/Call metadata passed to hooks must be JSON-able and must never contain file contents or response bodies.
- User Rego policies run with a restricted capability set (see `internal/policy`); keep dangerous builtins out.
- Store changes: update the interface, the GORM impl (`internal/store/gormstore`), and `internal/store/storetest` conformance suite.
- API changes: edit `api/openapi.yaml` first, then `make gen`; never hand-edit generated files (`internal/api/gen`, `internal/client/gen`, `web/src/api/schema.ts`).
- Worker protocol changes: edit `api/worker.openapi.yaml`, then `make gen`. Worker-served ops generate into `internal/runtime/remote/gen`; `api-callback`-tagged ops (worker→API reverse calls, e.g. ext tree resolution) generate the API-side strict server into `internal/api/intgen` and the worker's client into `internal/runtime/remote/apiclient`. Request/response bodies map via `x-go-type` onto `internal/runtime` contract types — payloads change in the contract package, not the spec.
- Extensions (`ext` capability): Starlark-only (never native binaries); they compose base capabilities and every op must route through `Gate.Invoke` (nested base-cap calls inherit allowlists, secrets, hooks, audit). Remote sources require an explicit `@version` and an `h1:` sum; git CLI is required at runtime for fetches. Server opt-in flags: `--ext-allow-sources` / `--ext-local-roots` / `--ext-cache-dir`.
- Instance isolation (`--instance-isolation`, worker default `process`, `calcside serve` default `inproc`): in `process` mode `internal/node/subproc.Supervisor` gives each instance its own OS process by re-executing the current binary as `<bin> __instance` (config JSON on stdin, never argv/env) and forwards the worker protocol over a per-child unix socket via `remote.Direct`. Globals/VFS live in the child's memory — Starlark values (functions, bindings) are not serializable, so don't add globals snapshots. A child exits when its stdin closes. Subproc tests re-exec the test binary from `TestMain`. Design: `docs/execution-routing.md`. `--instance-memory-max` puts each child in its own cgroup v2 group (`internal/node/subproc/cgroup_linux.go`); without usable cgroup v2 it warns and runs uncapped.
- Pin dependency versions published at least 7 days ago.
- `web/dist/README.txt` is a committed placeholder so Go builds work without Node.
