"""Ready-made Vega-Lite chart specs for io.display, built on chart.*.

Every op returns a validated spec dict; attach it with
io.display(spec, title?)."""

_XY_OPTS = ["color", "title", "x_type", "y_type", "tooltip", "zoom", "legend_filter", "width", "height"]


def _opts(options, allowed):
    """Return the options dict, failing on non-dict or unknown keys."""
    if options == None:
        return {}
    if type(options) != "dict":
        fail("charts: options must be a dict")
    for k in options:
        if k not in allowed:
            fail("charts: unknown option '%s'" % k)
    return options


def _opt(options, key, default=None):
    v = options.get(key, None)
    if v == None:
        return default
    return v


def _field_type(data, name, explicit, bar_x=False):
    """Explicit type wins; else infer. Bar x stays nominal unless the
    data looks temporal."""
    if explicit != None:
        return explicit
    t = chart.infer_type(data, name)
    if bar_x and t != "temporal":
        return "nominal"
    return t


def _xy(mark_type, data, x, y, options, has_size):
    allowed = _XY_OPTS + (["size"] if has_size else [])
    opts = _opts(options, allowed)
    color = _opt(opts, "color")
    size = _opt(opts, "size") if has_size else None
    title = _opt(opts, "title")
    tooltip = _opt(opts, "tooltip", True)
    zoom = _opt(opts, "zoom", False)
    legend_filter = _opt(opts, "legend_filter", False)
    width = _opt(opts, "width")
    height = _opt(opts, "height")

    encoding = {
        "x": chart.field(x, _field_type(data, x, _opt(opts, "x_type"), mark_type == "bar")),
        "y": chart.field(y, _field_type(data, y, _opt(opts, "y_type"))),
    }
    if color != None:
        encoding["color"] = chart.field(color, chart.infer_type(data, color))
    if size != None:
        encoding["size"] = chart.field(size, chart.infer_type(data, size))

    params = []
    if zoom:
        params.append({"name": "zoom", "select": "interval", "bind": "scales"})
    if legend_filter:
        if color == None:
            fail("charts: legend_filter requires color")
        params.append({"name": "legend", "select": {"type": "point", "fields": [color]}, "bind": "legend"})
        encoding["opacity"] = {"condition": {"param": "legend", "value": 1}, "value": 0.2}

    if tooltip:
        mark = {"type": mark_type, "tooltip": True}
        if mark_type == "line":
            mark["point"] = True
    else:
        mark = mark_type

    return chart.spec(mark, data=data, encoding=encoding,
                      params=params if params else None,
                      title=title, width=width, height=height)


def line(data, x, y, options=None):
    """Line chart spec for io.display."""
    return _xy("line", data, x, y, options, False)


def area(data, x, y, options=None):
    """Area chart spec for io.display."""
    return _xy("area", data, x, y, options, False)


def bar(data, x, y, options=None):
    """Bar chart spec for io.display."""
    return _xy("bar", data, x, y, options, False)


def scatter(data, x, y, options=None):
    """Scatter (point) chart spec for io.display; options may add size."""
    return _xy("point", data, x, y, options, True)


def histogram(data, field, options=None):
    """Histogram spec for io.display."""
    opts = _opts(options, ["bins", "color", "title", "tooltip", "width", "height"])
    bins = _opt(opts, "bins", 30)
    if type(bins) != "int":
        fail("charts: bins must be an int")
    color = _opt(opts, "color")
    title = _opt(opts, "title")
    tooltip = _opt(opts, "tooltip", True)
    width = _opt(opts, "width")
    height = _opt(opts, "height")

    encoding = {
        "x": chart.field(field, "quantitative", bin={"maxbins": bins}),
        "y": chart.field(field, "quantitative", aggregate="count"),
    }
    if color != None:
        encoding["color"] = chart.field(color, chart.infer_type(data, color))

    mark = {"type": "bar", "tooltip": True} if tooltip else "bar"
    return chart.spec(mark, data=data, encoding=encoding,
                      title=title, width=width, height=height)


def heatmap(data, x, y, color, options=None):
    """Heatmap (rect) spec for io.display; color is quantitative."""
    opts = _opts(options, ["title", "x_type", "y_type", "tooltip", "width", "height"])
    title = _opt(opts, "title")
    tooltip = _opt(opts, "tooltip", True)
    width = _opt(opts, "width")
    height = _opt(opts, "height")

    encoding = {
        "x": chart.field(x, _field_type(data, x, _opt(opts, "x_type"))),
        "y": chart.field(y, _field_type(data, y, _opt(opts, "y_type"))),
        "color": chart.field(color, "quantitative"),
    }
    mark = {"type": "rect", "tooltip": True} if tooltip else "rect"
    return chart.spec(mark, data=data, encoding=encoding,
                      title=title, width=width, height=height)
