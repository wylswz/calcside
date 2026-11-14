# calcside

A lightweight code-execution sandbox for AI agents, built on Starlark.

Inspired by [citron](https://github.com/mishudark/citron) and [marimo](https://marimo.io/)

Powered by [starlark-go](https://github.com/google/starlark-go)

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
