# EP-0024: Notebook charts — interactive console views and image snapshots for agents

- **Status:** Phase 1 implemented (core constructors, `contrib/charts`,
  `io.display`, storage, REST/MCP text, console); Phase 2 (renderer,
  MCP images) not started
- **Created:** 2026-11-13
- **Area:** new pure `chart` stdlib module (spec constructors),
  `contrib/charts` extension (chart types), `io` capability (`io.display`),
  shared spec validator (`internal/chart`), worker contract
  (`ExecResult.displays`), store (one migration), OpenAPI, MCP tools,
  agent prompt, console, a new optional `calcside-render` service
  (image, Compose service, Helm values, release workflow).
- **Related:** [EP-0007](0007-artifact-preview-and-download.md)
  (artifact preview; static HTML/SVG subset, preview origins),
  [EP-0022](0022-instance-notebooks.md) (outputs stay off the
  outline), [EP-0023](0023-notebook-dag.md) (section runs),
  [docs/interaction-stability.md](../interaction-stability.md)

## Summary

A section can display charts. Starlark builds a declarative
**Vega-Lite subset** spec from data with core constructors (`chart.spec`, `chart.field`),
or with ready-made chart types from the `contrib/charts` extension,
and emits it with `io.display(spec)`. The spec is validated against an
allowlist, secret-redacted, and stored with the execution, just like
printed output. Two renderers read that same spec:

- **Console:** `vega-embed` renders it as an interactive chart
  (tooltips, zoom/pan, brush, legend filtering) under the section's
  output.
- **MCP:** an optional stateless `calcside-render` service (Node: Vega,
  then resvg) turns the stored spec into a PNG. `exec`/`read_section`
  return it as `ImageContent`, so multimodal agents can see what the
  human sees.

Interactivity is for humans; agents get a static snapshot of the same
spec. Without a configured renderer everything works except MCP
images, which fall back to a text summary of the spec.

## Problem

- Humans supervising agents (EP-0009) read numbers slowly. A notebook
  with no visual output is hard to review at a glance.
- Agents cannot see shape: outliers, distribution form, trend breaks,
  or a broken chart in a deliverable. `stats`/`series` answer precise
  questions, but only once the agent knows what to ask.
- The workarounds are poor. Hand-written SVG in `/work` only renders
  through the EP-0007 static subset and is never interactive, and MCP
  `read_file` returns it as JSON text, which a model cannot look at.

## Goals

- One spec format, two renderers (interactive in the browser, PNG for
  MCP) that use the same rendering library, so what the agent sees
  matches what the human sees.
- Specs are data: bounded JSON that crosses the API/worker/subprocess
  boundaries, is stored with the execution, and is redacted the same
  way output is.
- Untrusted specs never get code execution, network egress, or
  navigation in the console origin.
- Charts are produced by re-running sections, never trusted as stored
  truth (EP-0022 R-rules); they stay off the outline.

## Non-goals

- Arbitrary HTML/JS widgets, `ipywidgets`-style kernel callbacks, or
  interactions that re-execute Starlark.
- Images, maps/geoshapes, external data, or user-authored Vega
  expressions in v1 (see *Spec subset*).
- Streaming charts during an exec, or charts in REST/SDK clients beyond
  returning the stored spec.
- Durable chart storage beyond the execution record, or chart export
  as files (agents can still `fs.write(json.encode(spec))`).

## Starlark surface

```python
rows = [{"day": d, "latency_ms": v} for d, v in samples]
# core constructors: always available
io.display(chart.spec("line", data=rows, title="p95 latency",
    encoding={"x": chart.field("day", "temporal"),
              "y": chart.field("latency_ms", "quantitative")},
    params=[{"name": "zoom", "select": "interval", "bind": "scales"}]))
# ready-made chart types from the charts extension, when granted
io.display(ext.charts.histogram(rows, "latency_ms", {"bins": 40}))
```

The spec is the core data structure; chart *types* are recipes on top
of it and live in an extension.

- **`chart` (core, pure utility, no grant):** constructors and checks
  only — `spec(mark, data, encoding, transform, params, title, width,
  height)` (adds `$schema`, default size and font), `field(name, type,
  ...)` for channel definitions, `infer_type(rows, field)`,
  `layer`/`hconcat`/`vconcat(*specs)`, `validate(spec)` (canonical dict
  or an error naming the JSON path) and `summary(spec)`. Agents may
  also edit the dict or write a spec by hand.
- **`contrib/charts` extension (Starlark-only, no base
  dependencies):** `line`, `area`, `bar`, `scatter`, `histogram`,
  `heatmap`, each returning a spec built from the core constructors.
  Options cover `color`, `size`, field types, titles, `tooltip`,
  `zoom` (scale-bound interval) and `legend_filter`; brushes and other
  interval selections are written as raw `params`. Chart recipes
  version and evolve independently of the server (EP-0020), and more
  can be contributed without touching the validator. The extension's
  op audit records only argument shapes, never row data.
- **`io.display(spec, title=None)` (io capability):** validates the
  spec (failures are script errors with a line number), then appends it
  to the exec's display buffer. It routes through `Gate.Invoke`. Hook
  and audit metadata are `{mark, rows, bytes}` only, never data values
  (AGENTS.md: metadata never carries contents).
- Validation lives in `internal/chart` (pure Go, no Starlark), shared
  by the child process, the API tier (re-validated before persisting
  and before serving or rendering), and tests.

## Spec subset (allowlist)

Validation is an **allowlist** over the Vega-Lite JSON tree. Any key
not listed is rejected with its JSON path:

- **Top level:** `$schema` (Vega-Lite v6 only), `title`,
  `description`, `width`, `height`, `autosize`, `padding`, `data`,
  `mark`, `encoding`, `transform`, `params`, `layer`, `hconcat`,
  `vconcat`, `facet`, `resolve`, and `config` (only `axis`, `legend`,
  `view`, `range`, `font` sizing and colour keys).
- **Data:** `data.values` only, inline. `url`, `name`, `sequence`,
  `graticule`, and top-level `datasets` are rejected. Every spec is
  self-contained.
- **Marks:** `line`, `area`, `bar`, `point`, `circle`, `square`, `tick`,
  `rect`, `rule`, `text`, `boxplot`, `errorbar`, `errorband`. `image`
  and `geoshape` are rejected.
- **Encodings:** `x`, `y`, `x2`, `y2`, `color`, `opacity`, `size`,
  `shape`, `strokeDash`, `text`, `tooltip`, `detail`, `order`, `row`,
  `column`, `facet`. `href` and `url` are rejected.
- **Transforms:** `aggregate`, `bin`, `timeUnit`, `fold`, `stack`,
  `window`, `joinaggregate`, `density`, `regression`, `loess`,
  `quantile`, and `filter` with a structured field or `{param}`
  predicate only.
- **Params:** `select: point | interval`, optional `bind: "scales"` or
  `bind: "legend"`. No input-widget bindings and no `expr`/`value`
  expressions.
- **No user-authored expression strings anywhere:** `calculate`, string
  `filter`, `test`, `expr`, signal expressions, and `format` strings
  outside a fixed set of d3 number/time formats. Agents compute derived
  fields in Starlark, which is where they already compute them.
  Vega-Lite's own compiled expressions still run, through the CSP-safe
  interpreter (below).

**Limits (fixed ceilings, like EP-0007's):**

| Limit | Value |
|---|---|
| Displays per exec | 8 |
| Spec size | 256 KiB |
| Total displays per exec | 1 MiB |
| Rows per spec (all `values` combined) | 5,000 |
| Tree depth | 32 |
| String length | 1 KiB |
| Canvas size | 2,000 × 2,000 px |

Exceeding a limit is a script error, never silent truncation. A
truncated chart would look plausible and mislead both the human and the
agent.

## Redaction and storage

- The node scrubs known secret values from every string in a spec
  (field names, values, titles) before it leaves the instance, the same
  way `Output` is scrubbed. This must happen **before** any rendering:
  once text is rasterized into a PNG it can no longer be redacted. This
  is why Starlark never produces image bytes.
- `runtime.ExecResult` gains `displays: [{kind: "vega-lite", title,
  spec}]`, with `spec` as raw JSON. It flows worker → API like `output`.
- Store: one migration adds a detail-only `displays` column (JSON text)
  to `executions`, served only by the execution detail endpoint, like
  `Output`. GORM row structs and the storetest conformance suite are
  updated with it. The list view carries only `display_count`.
- Outline: unchanged. The last-run line may show `· 2 charts`, but
  never chart contents (EP-0022 keeps outputs off the outline).

## Console rendering (interactive)

- A lazily loaded `ChartView` chunk uses `vega-embed` configured with
  `ast: true` plus `vega-interpreter` (no `new Function`/eval, so it
  works under the console CSP), and a loader that rejects every
  `load`/`sanitize` call. The actions menu is limited to "Save as PNG"
  and "View spec"; there is no "Open in editor" (it would post the spec
  off-origin).
- These defences sit on top of the server-side allowlist; the console
  re-validates nothing itself and trusts only specs served by the
  API. Because a validated spec contains no URLs, expressions, or HTML,
  inline rendering on the console origin does not conflict with the
  "HTML only on preview origins" rule: it is data rendered by a
  reviewed library, not agent markup.
- Placement: under a section's output in the notebook Code view and on
  execution detail.
- Interaction stability:
  - **S:** the chart's box is reserved from the spec's
    `width`/`height` before the chunk loads, so loading does not shift
    layout. Hover and brush effects are paint-only inside the canvas.
  - **C:** charts are keyed by spec hash, so a poll that returns the
    same exec keeps the user's zoom and brush. A new exec replaces the
    chart without resetting page scroll.

  Update the status table in `docs/interaction-stability.md`.
- Fonts: the spec `config` defaults to IBM Plex Sans, which the
  console already ships and the renderer embeds, so text measurement
  matches between the two renderers.

## MCP rendering (`calcside-render`)

**Service.** A stateless Node 22 HTTP service in `render/`, with pinned
`vega`, `vega-lite`, `vega-interpreter` and `@resvg/resvg-js`:

- `POST /render` takes `{spec, scale}` and returns `image/png`. It
  compiles with Vega-Lite, renders with `view.toSVG()` (no
  node-canvas, interpreter expressions, deny-all loader), then
  rasterizes with resvg using only embedded fonts and no system font
  or file access.
- `GET /healthz`.
- Bounded: request body ≤ 1.5 MiB, a 5 s render timeout per request
  (the worker is killed and recycled on timeout), a concurrency cap,
  and a pixel cap after `scale`.
- Requests are authenticated with a shared key (`CALCSIDE_RENDER_KEY`,
  generated like the worker key). It needs no egress and holds no
  secrets.

**Who calls it.** The **API tier**, on demand, from stored
(already-validated, already-redacted) specs. Rendering is a
presentation of frozen data, not code execution, so the "API never
executes code" rule holds. Rendering stays off the worker hot path,
and a renderer outage cannot fail an exec. Configuration is
`--render-url` / `CALCSIDE_RENDER_URL` plus the key. When unset, MCP
falls back to text.

**MCP surface.**

- `exec`, `run_sections` (the target section only), and
  `read_section(include_output)` append one `ImageContent` (PNG) per
  display after the text block. The text gets a line such as
  `displayed: "p95 latency" (line, 365 rows) [image 1]`.
- New optional tool argument `images` (default set by the pre-Phase-2
  measurement, see *Decisions*). `images=false` returns only the text line, for
  token-sensitive loops.
- Renderer failure or no renderer: the text line plus a compact
  summary per display (mark, fields, row count, per-field min/max
  computed from `values`). The result is never `IsError`, because the
  exec itself succeeded.
- The scale is chosen so the longest side is ≤ 1,024 px, which keeps
  image token cost predictable.

**Deployment.**

- `docker/Dockerfile.render` builds image
  `ghcr.io/wylswz/calcside-render`, multi-arch, published by
  `release.yml`.
- Compose adds a private `render` service; the API reaches it over the
  compose network.
- Helm: `render.enabled` (default `true`) adds a Deployment and
  Service, plus a NetworkPolicy that allows ingress only from API pods
  and no egress. The key goes in the existing Secret.
  `make helm-lint` renders both the enabled and disabled variants.
- The release gate (`benchmark/run-release.py`) adds one integration
  case: a section that displays a chart, and MCP `exec` returning a
  PNG.

## Agent prompt

The prompt adds one paragraph: build specs with `chart.spec`/`chart.field`
(or `ext.charts.*` when granted), attach them with `io.display`, use a chart to look for *shape* and `stats` for
numbers, and pass `images=false` when no visual check is needed. The
MCP instructions golden is updated.

## Phasing

1. **Spec and console.** `internal/chart` validator, `chart` module,
   `io.display`, worker contract, migration, OpenAPI, console
   `ChartView`. MCP returns the text summary. This phase adds no new
   service.
2. **Renderer.** `calcside-render`, API client, MCP `ImageContent`,
   Compose/Helm/release wiring, release-gate case.
3. **Later, on evidence.** Reviewed expressions (`calculate`/`filter`)
   behind the interpreter, input-widget bindings, cross-chart linked
   selection, and map marks with bundled geodata.

## Alternatives rejected

- **Starlark emits SVG/PNG bytes.** This cannot be redacted after
  rasterization, it is unbounded and opaque to validation, and it gives
  no interactivity.
- **ECharts options.** Formatters and tooltips are JS functions, and a
  JSON-only option set loses much of the library. Its declarative
  interaction model is also weaker than Vega-Lite params.
- **Go-native static renderer for a subset.** Two renderers means two
  pictures of the same spec, and the subset grows into a charting
  library to maintain.
- **QuickJS/resvg in wasm inside Go.** This avoids a service, but it
  carries open risk around ES compatibility and text measurement. It
  can be revisited if operating the sidecar proves costly; the
  `/render` contract would not change.
- **Headless browser.** Highest fidelity, but heavy, slow, and a large
  attack surface for a spec we already constrain.
- **Rendering on the worker.** It ties renderer availability to exec
  success and redoes work for every exec. Rendering from stored specs
  only when a client asks is cheaper and keeps image output consistent
  with what the console shows.

## Tests

- `internal/chart`: allowlist table, covering every rejected key
  (`data.url`, `image`, `href`, `calculate`, `expr`, widget `bind`)
  with its JSON path, the limits, depth, and the redaction walk.
- `stdlib`: `chart.*` constructors validate, `infer_type`, and `help`
  text.
- `contrib/charts`: every op returns a valid spec; unknown or invalid
  options fail.
- `io`: display buffer bounds, per-exec reset, and Gate/audit metadata
  that excludes data.
- Store/storetest: `displays` round trip on both dialects, plus
  `TestMigrationDirsInLockstep`.
- `service/sandbox` / `mcp`:
  - displays survive section runs;
  - `read_section(include_output)` returns them;
  - `ImageContent` is returned when a stub renderer is configured;
  - text fallback on renderer error or no renderer;
  - `images=false`.
- `render/`: golden PNG dimensions and non-blank pixels, spec URL and
  image rejection (defence in depth), timeout recycling, and auth.
- Web: `ChartView` unit tests (loader denies, interpreter configured),
  the S/C invariants in `interactionStability.test.ts`, a Playwright
  case (display, hover tooltip, zoom kept across a poll), and the
  visual sweep.

## Decisions

- **No user-authored expressions in v1.** `calculate`, string `filter`,
  `test`, `expr` and signal expressions are rejected; agents derive
  fields in Starlark. Reviewed expressions are Phase 3 work.
- **Inline rendering on the console origin.** Validated specs carry no
  URLs, expressions or HTML, so they are data rendered by a reviewed
  library — not agent markup — and need no preview-origin iframe.
- **`images` default is decided by measurement.** Before Phase 2 ships,
  run a small task set (anomaly detection, checking a report chart,
  distribution shape) with and without images; the default follows the
  result.

## Open questions

- Should the 5,000-row ceiling be configurable per deployment? EP-0007
  chose fixed ceilings first.
