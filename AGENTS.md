# AGENTS.md

## Commands
- Go checks: `go build ./... && go vet ./... && gofmt -l . && go test -race ./...`
- Web checks: `pnpm -C web typecheck && pnpm -C web lint && pnpm -C web build`
- `make gen` regenerates code from `api/openapi.yaml`; `make gen-check` fails if generated files drift.
- `make lint` runs gen-check + vet + gofmt + web typecheck/lint; `make build` builds web then `bin/calcside`, `bin/csctl`.
- `make dev`/`serve-dev` pass `--net-allow-cidrs=$(DEV_NET_ALLOW_CIDRS)` (default `198.18.0.0/15`) to exempt fake-ip proxy ranges from net's SSRF blocking; set `DEV_NET_ALLOW_CIDRS=` to disable.
- Dev: `make dev` starts the backend in dev mode (anonymous auth, no login) on :8787 plus the Vite dev server on :5173. `make serve-dev` runs the backend alone with the embedded UI.
- Python SDK (`sdk/python`, uv project): `make sdk-test` (pytest, builds + boots a dev-mode server), `make sdk-lint` (ruff). Not part of `make test`/`make lint`.

## Conventions
- No container / host process capabilities. New capabilities implement `capability.Factory` and must route every op through `Gate.Invoke` so hooks and audit see it.
- Result/Call metadata passed to hooks must be JSON-able and must never contain file contents or response bodies.
- User Rego policies run with a restricted capability set (see `internal/policy`); keep dangerous builtins out.
- Store changes: update the interface, the GORM impl (`internal/store/gormstore`), and `internal/store/storetest` conformance suite.
- API changes: edit `api/openapi.yaml` first, then `make gen`; never hand-edit generated files (`internal/api/gen`, `internal/client/gen`, `web/src/api/schema.ts`).
- Extensions (`ext` capability): Starlark-only (never native binaries); they compose base capabilities and every op must route through `Gate.Invoke` (nested base-cap calls inherit allowlists, secrets, hooks, audit). Remote sources require an explicit `@version` and an `h1:` sum; git CLI is required at runtime for fetches. Server opt-in flags: `--ext-allow-sources` / `--ext-local-roots` / `--ext-cache-dir`.
- Pin dependency versions published at least 7 days ago.
- `web/dist/README.txt` is a committed placeholder so Go builds work without Node.
