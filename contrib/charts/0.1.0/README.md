# charts

Ready-made Vega-Lite chart specs for `io.display`, built on the core
`chart` module (`chart.spec`, `chart.field`, `chart.infer_type`).

Every op returns a validated spec dict — the caller attaches it:

```python
spec = ext.charts.line(data, "day", "latency", {"zoom": True, "color": "service"})
io.display(spec, title="Latency by day")
```

## Ops

| Op | Params | Notes |
|---|---|---|
| `line` | `data, x, y, options` | point overlay on the line for tooltips |
| `area` | `data, x, y, options` | stacked area |
| `bar` | `data, x, y, options` | x defaults to nominal unless temporal |
| `scatter` | `data, x, y, options` | point mark; `size` option allowed |
| `histogram` | `data, field, options` | `bins` (default 30), binned x + count y |
| `heatmap` | `data, x, y, color, options` | rect mark, quantitative color |

`data` is a list of flat row dicts with scalar values (at most 5,000
rows); `x`, `y`, `field` and `color` are field names in those rows.
`options` is an optional dict; unknown keys are errors.

Shared options (per-op sets differ — see `capability.yaml`):

- `color` — field name for a colour encoding (`scatter`-only `size`
  does the same for point size).
- `title` — chart title.
- `x_type` / `y_type` — one of `quantitative`/`temporal`/`ordinal`/
  `nominal`; inferred from the first non-null value when omitted (bar
  x stays nominal unless the data looks temporal).
- `tooltip` (default `True`) — per-point tooltips.
- `zoom` (default `False`) — drag to pan, scroll to zoom in the console.
- `legend_filter` (default `False`; requires `color`) — click legend
  entries to filter.
- `width` (default `480`) / `height` (default `280`) — spec size in px.
- `bins` (histogram only, default `30`) — maximum x bins.

No base-capability dependencies and no secrets: specs are pure data.
Chart interactivity (zoom, legend filter) works in the console; over
MCP charts appear as one-line summaries.
