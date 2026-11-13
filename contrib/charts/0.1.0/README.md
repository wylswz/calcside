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

Shared options: `color`, `title`, `x_type`, `y_type`, `tooltip` (default
true), `zoom` (interval param bound to scales), `legend_filter`
(requires `color`; point param bound to legend + opacity condition),
`width`, `height`. Types are `quantitative`/`temporal`/`ordinal`/
`nominal` and are inferred from the first non-null value when omitted.
Unknown option keys are errors.

No base-capability dependencies and no secrets: specs are pure data.
Chart interactivity (zoom, legend filter) works in the console; over
MCP charts appear as one-line summaries.
