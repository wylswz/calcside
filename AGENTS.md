# AGENTS.md

## Commands
- Go checks: `go build ./... && go vet ./... && gofmt -l . && go test -race ./...`
- Web checks: `pnpm -C web typecheck && pnpm -C web lint && pnpm -C web build`
- `make lint` runs vet + gofmt + web typecheck/lint; `make build` builds web then `bin/calcside`, `bin/csctl`.
- Dev server: `make dev` (or `bin/calcside serve --addr :8085 --dev-login --policy-dir policies/examples` if :8080 is taken). Vite dev (`pnpm -C web dev`) proxies /api and /auth to :8080.

## Conventions
- No container / host process capabilities. New capabilities implement `capability.Factory` and must route every op through `Gate.Invoke` so hooks and audit see it.
- Result/Call metadata passed to hooks must be JSON-able and must never contain file contents or response bodies.
- User Rego policies run with a restricted capability set (see `internal/policy`); keep dangerous builtins out.
- Store changes: update the interface, the sqlite impl + migration, and `internal/store/storetest` conformance suite.
- Pin dependency versions published at least 7 days ago.
- `web/dist/README.txt` is a committed placeholder so Go builds work without Node.
