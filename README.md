# calcside

A lightweight code-execution sandbox for AI agents, built on Starlark.

Inspired by [citron](https://github.com/mishudark/citron) and [marimo](https://marimo.io/)

Powered by [starlark-go](https://github.com/google/starlark-go)

## Repository layout

- `server/` — Go module (`calcside`): API + worker binaries (`cmd/`), shared execution code (`internal/`), example policies (`policies/`), Atlas config (`atlas.hcl`), and `webdist/` — the embed package the console build outputs into.
- `api/` — OpenAPI specs shared by all modules: the public API and worker protocol; generated Go/TS code comes from here.
- `contrib/` — bundled community extensions.
- `web/` — console frontend (React + Vite); `pnpm -C web build` writes into `server/webdist/dist`.
- `render/` — stateless Vega-Lite → PNG service for MCP chart images.
- `sdk/` — Python SDK (`sdk/python`, uv project).
- `charts/` — Helm chart plus `deploy-test.py` test-deploy helper.
- `docker/` — Dockerfiles, Compose stacks, env bootstrap.
- `benchmark/` — k6 API benchmark and release-gate runner/report.
- `docs/` — design docs (proposals/releases/articles are historical).
- `examples/` — sample agents and Starlark programs.
- `.github/` — CI and release workflows.

## Get started

```bash
./docker/init-env.sh && cd docker && docker compose up --build
```

Then open http://localhost:8080.

## Chart images

Notebook charts (`io.display` + `chart.*`) show interactively in the console
and, over MCP, as a one-line summary by default. Passing `images=true` to
`exec`, `run_sections` (single section) or `read_section` also returns a PNG
snapshot per chart — that needs the `calcside-render` service.

Compose starts it automatically on a private network; `make docker-env`
generates `CALCSIDE_RENDER_KEY` in `docker/.env`. For a manual deployment set
`CALCSIDE_RENDER_URL` (e.g. `http://render:8091`) and the same
`CALCSIDE_RENDER_KEY` on the API — the renderer refuses to start without a
key, listens on `RENDER_ADDR` (default `:8091`), and holds no state or
secrets. Without a renderer, `images=true` still works and reports the image
as unavailable; a renderer outage never fails an execution.
