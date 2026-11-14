#!/usr/bin/env python3
"""Render presentation figures from FROZEN v2 summaries. Stdlib only.

Reads results/v2-agent/summary.json, results/v2-micro/micro.json and
security_evidence_v2.json. Never reads raw trial rows, never recomputes
rates or statistics, never runs the model or evaluation code. Unit
scaling (s -> ms) and layout are the only transformations applied.

Outputs results/figures-v2/*.svg. Run only after the official summaries
are final; raw summaries are never modified.
"""

import html
import json
from pathlib import Path

HERE = Path(__file__).resolve().parent
AGENT_SUMMARY = HERE / "results" / "v2-agent" / "summary.json"
MICRO_SUMMARY = HERE / "results" / "v2-micro" / "micro.json"
SECURITY_EVIDENCE = HERE / "security_evidence_v2.json"
OUT_DIR = HERE / "results" / "figures-v2"

FONT = "IBM Plex Sans, -apple-system, 'Segoe UI', sans-serif"
IVORY = "#FAF6EC"
PAPER = "#FFFDF8"
INK = "#2A241B"
INK_SOFT = "#6E6455"
TEAL = "#0F766E"
ORANGE = "#D97706"
TAUPE = "#B7A98C"
GRID = "#E3DAC6"


def esc(value):
    return html.escape(str(value), quote=True)


def el(tag, attrs, text=None):
    a = "".join(' %s="%s"' % (k, esc(v)) for k, v in attrs.items())
    if text is None:
        return "<%s%s/>" % (tag, a)
    return "<%s%s>%s</%s>" % (tag, a, esc(text), tag)


def txt(x, y, s, size=12, fill=INK, anchor="start", weight=None):
    attrs = {"x": x, "y": y, "font-size": size, "fill": fill,
             "text-anchor": anchor, "font-family": FONT}
    if weight:
        attrs["font-weight"] = weight
    return el("text", attrs, s)


def wrap_text(text, max_chars):
    """Greedy word wrap; always returns at least one line."""
    words = str(text).split()
    lines, cur = [], ""
    for w in words:
        cand = (cur + " " + w).strip()
        if len(cand) <= max_chars:
            cur = cand
        else:
            if cur:
                lines.append(cur)
            cur = w
    if cur:
        lines.append(cur)
    return lines or [""]


def svg_doc(width, height, body):
    return ('<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" '
            'viewBox="0 0 %d %d">\n'
            '<rect width="%d" height="%d" fill="%s"/>\n%s\n</svg>\n'
            % (width, height, width, height, width, height, IVORY, body))


def header(title, subtitle, width):
    """Title + word-wrapped subtitle; returns (markup, extra_height).

    subtitle may be a string or a list of paragraphs; each paragraph is
    wrapped independently so notes stay on their own lines.
    """
    max_chars = max(40, int((width - 48) / 6.4))
    paragraphs = [subtitle] if isinstance(subtitle, str) else subtitle
    lines = []
    for para in paragraphs:
        lines.extend(wrap_text(para, max_chars))
    parts = [txt(24, 30, title, size=17, weight="600")]
    for i, line in enumerate(lines):
        parts.append(txt(24, 48 + 14 * i, line, size=11.5, fill=INK_SOFT))
    return "\n".join(parts), 14 * max(0, len(lines) - 1)


def fmt_pct(rate):
    return "—" if rate is None else "%.0f%%" % (100 * rate)


# ------------------------------------------------- task artifact validation


def fig_task_completion(summary, path):
    backends = sorted(summary)
    row_h = 46
    top = 76
    left, right = 250, 560
    plot_w = right - left
    tasks = sorted({t for b in summary.values() for t in b.get("by_task", {})})
    cell_w, cell_h = 108, 26
    width = 980

    hm, extra = header(
        "Task artifact validation",
        ["results/v2-agent · Wilson 95% interval "
         "(z=1.959963984540054) · descriptive only; tasks are clustered, "
         "not independent",
         "fetch validates public fields, not HTTP provenance"],
        width)
    top += extra
    height = top + row_h * len(backends) + 60
    grid_top = height + 8
    height = grid_top + 30 + cell_h * (len(tasks) + 1) + 40

    parts = [hm]
    y = top
    for b in backends:
        tc = summary[b]["task_completion"]
        rate = tc.get("rate")
        succ, att = tc.get("successes"), tc.get("attempts")
        wil = tc.get("wilson95") or [None, None]
        cy = y + 18
        parts.append(txt(24, cy + 4, b, size=12.5, weight="600"))
        parts.append(txt(24, cy + 18, "%s/%s" % (succ, att), size=10.5,
                         fill=INK_SOFT))
        if rate is not None:
            w = plot_w * rate
            parts.append(el("rect", {"x": left, "y": cy - 9, "width": w,
                                     "height": 18, "fill": TEAL}))
            if wil[0] is not None and wil[1] is not None:
                xlo, xhi = left + plot_w * wil[0], left + plot_w * wil[1]
                parts.append(el("line", {"x1": xlo, "y1": cy, "x2": xhi,
                                         "y2": cy, "stroke": INK,
                                         "stroke-width": 1.4}))
                for xx in (xlo, xhi):
                    parts.append(el("line", {"x1": xx, "y1": cy - 5,
                                             "x2": xx, "y2": cy + 5,
                                             "stroke": INK,
                                             "stroke-width": 1.4}))
        pct_x = left + plot_w + 12
        parts.append(txt(pct_x, cy + 4, fmt_pct(rate), size=12.5,
                         weight="600"))
        ci = ("[%.0f–%.0f%%]" % (100 * wil[0], 100 * wil[1])
              if wil[0] is not None else "")
        parts.append(txt(pct_x, cy + 18, ci, size=10, fill=INK_SOFT))
        parts.append(el("line", {"x1": 24, "y1": y + row_h - 8,
                                 "x2": width - 24, "y2": y + row_h - 8,
                                 "stroke": GRID, "stroke-width": 1}))
        y += row_h

    parts.append(txt(24, grid_top + 18, "By task (successes/attempts)",
                     size=12.5, weight="600"))
    gx, gy = 24, grid_top + 26
    parts.append(txt(gx, gy + cell_h - 8, "task", size=11, fill=INK_SOFT))
    for i, b in enumerate(backends):
        parts.append(txt(gx + cell_w * (i + 1) + cell_w / 2, gy + cell_h - 8,
                         b, size=10.5, fill=INK_SOFT, anchor="middle"))
    for ti, task in enumerate(tasks):
        ry = gy + cell_h * (ti + 1)
        parts.append(txt(gx + 4, ry + cell_h - 8, task, size=11))
        for i, b in enumerate(backends):
            cell = summary[b].get("by_task", {}).get(task, {})
            parts.append(txt(gx + cell_w * (i + 1) + cell_w / 2,
                             ry + cell_h - 8,
                             "%s/%s" % (cell.get("successes"),
                                        cell.get("attempts")),
                             size=10.5, anchor="middle"))
    path.write_text(svg_doc(width, height, "\n".join(parts)))


# ------------------------------------------------------------- warm latency


def fig_warm_latency(micro, path):
    rows = list(micro)  # every backend stays; failures render as N/A
    width = 980
    left, right = 210, 900
    plot_w = right - left
    top = 80
    row_h = 84
    height = top + row_h * len(rows) + 70
    y_max_ms = max(
        ((r.get("all_attempt_latency_s") or {}).get("p95") or 0) * 1000
        for r in rows) or 1
    y_max_ms *= 1.15

    hm, extra = header(
        "Warm round-trip latency — execute('print(\"ok\")')",
        "results/v2-micro · all attempts incl. errors · single session, "
        "30 samples + 1 excluded warmup per backend · p50/p95 in ms",
        width)
    top += extra
    height = top + row_h * len(rows) + 70

    parts = [hm]
    y = top
    for r in rows:
        b = r["backend"]
        dist = r.get("all_attempt_latency_s") or {}
        n = dist.get("n") or 0
        p50 = dist.get("p50")
        p95 = dist.get("p95")
        succ = r.get("success") or {}
        rate = succ.get("rate")
        cy = y + 22
        parts.append(txt(24, cy + 4, b, size=12.5, weight="600"))
        meta = ["n=%d" % n]
        if rate is not None:
            meta.append("%d/%d ok" % (succ.get("successes"),
                                      succ.get("attempts")))
        if r.get("setup_error"):
            meta.append("setup_error")
        if r.get("cleanup_error"):
            meta.append("cleanup_error")
        parts.append(txt(24, cy + 18, " · ".join(meta), size=10,
                         fill=INK_SOFT))
        usable = (not r.get("setup_error")) and n > 0 \
            and p50 is not None and p95 is not None
        for val, color, lab, dy in ((p50, TEAL, "p50", 0),
                                    (p95, ORANGE, "p95", 26)):
            bar_y = cy + dy - 7
            if usable:
                v = val * 1000
                w = plot_w * (v / y_max_ms) if y_max_ms else 0
                parts.append(el("rect", {"x": left, "y": bar_y,
                                         "width": max(w, 1), "height": 14,
                                         "fill": color}))
                parts.append(txt(left + max(w, 1) + 6, bar_y + 11,
                                 "%s %.2f ms" % (lab, v), size=10.5,
                                 fill=color))
            else:
                parts.append(txt(left, bar_y + 11, "%s N/A" % lab,
                                 size=10.5, fill=INK_SOFT))
        parts.append(el("line", {"x1": 24, "y1": y + row_h - 8,
                                 "x2": width - 24, "y2": y + row_h - 8,
                                 "stroke": GRID, "stroke-width": 1}))
        y += row_h

    ly = height - 26
    parts.append(el("rect", {"x": left, "y": ly - 9, "width": 12,
                             "height": 12, "fill": TEAL}))
    parts.append(txt(left + 16, ly + 1, "p50 (median, nearest-rank)",
                     size=10.5, fill=INK_SOFT))
    parts.append(el("rect", {"x": left + 190, "y": ly - 9, "width": 12,
                             "height": 12, "fill": ORANGE}))
    parts.append(txt(left + 206, ly + 1, "p95 (nearest-rank)",
                     size=10.5, fill=INK_SOFT))
    path.write_text(svg_doc(width, height, "\n".join(parts)))


# ------------------------------------------------------------ agent latency


def fig_agent_latency(summary, path):
    backends = sorted(summary)
    width = 980
    left, right = 210, 900
    plot_w = right - left
    top = 92
    row_h = 84

    def mean_of(b, key):
        d = summary[b].get("latency_s", {}).get(key) or {}
        return d.get("mean")

    totals = {b: sum(mean_of(b, k) or 0
                     for k in ("model_s", "tool_s", "residual_s"))
              for b in backends}
    setups = {b: mean_of(b, "setup_s") for b in backends}
    x_max = max([t for t in totals.values() if t] +
                [v for v in setups.values() if v] + [1]) * 1.08

    hm, extra = header(
        "Agent loop latency — mean seconds per trial (stacked)",
        "results/v2-agent · latency_s means: model events + tool events + "
        "residual orchestration · setup shown separately, not in loop",
        width)
    top += extra
    height = top + row_h * len(backends) + 80

    parts = [hm]
    parts.append(txt(left, top - 14, "s", size=10, fill=INK_SOFT))
    y = top
    for b in backends:
        cy = y + 30
        parts.append(txt(24, cy + 4, b, size=12.5, weight="600"))
        segs = [("model_s", TEAL), ("tool_s", ORANGE),
                ("residual_s", TAUPE)]
        x = left
        for key, color in segs:
            v = mean_of(b, key) or 0
            w = plot_w * v / x_max
            if w > 0.5:
                parts.append(el("rect", {"x": x, "y": cy - 10,
                                         "width": w, "height": 20,
                                         "fill": color}))
            x += w
        parts.append(txt(x + 8, cy + 4, "%.2f s" % totals[b],
                         size=11.5, weight="600"))
        sv = setups[b] or 0
        sw = plot_w * sv / x_max
        parts.append(el("rect", {"x": left, "y": cy + 22,
                                 "width": max(sw, 1), "height": 8,
                                 "fill": PAPER, "stroke": INK_SOFT,
                                 "stroke-width": 0.8}))
        parts.append(txt(left, cy + 42,
                         "setup μ=%.2f s (outside loop)" % sv,
                         size=10, fill=INK_SOFT))
        parts.append(el("line", {"x1": 24, "y1": y + row_h - 6,
                                 "x2": width - 24, "y2": y + row_h - 6,
                                 "stroke": GRID, "stroke-width": 1}))
        y += row_h

    ly = height - 30
    for color, lab, lx in ((TEAL, "model events", left),
                           (ORANGE, "tool events", left + 130),
                           (TAUPE, "residual", left + 250)):
        parts.append(el("rect", {"x": lx, "y": ly - 9, "width": 12,
                                 "height": 12, "fill": color}))
        parts.append(txt(lx + 16, ly + 1, lab, size=10.5, fill=INK_SOFT))
    path.write_text(svg_doc(width, height, "\n".join(parts)))


# -------------------------------------------------------------- agent tokens


def fig_agent_tokens(summary, path):
    backends = sorted(summary)
    width = 980
    left, right = 210, 900
    plot_w = right - left
    top = 92
    row_h = 56

    totals = {b: ((summary[b].get("input_tokens_observed") or 0),
                  (summary[b].get("output_tokens_observed") or 0))
              for b in backends}
    x_max = max([i + o for i, o in totals.values()] + [1]) * 1.08

    hm, extra = header(
        "Observed token usage — totals over all model events",
        "results/v2-agent · provider-reported input/output tokens summed; "
        "not normalized per trial · missing-usage events noted per backend",
        width)
    top += extra
    height = top + row_h * len(backends) + 80

    parts = [hm]
    parts.append(txt(left, top - 14, "tokens", size=10, fill=INK_SOFT))
    y = top
    for b in backends:
        cy = y + 22
        inp, outp = totals[b]
        parts.append(txt(24, cy + 4, b, size=12.5, weight="600"))
        parts.append(txt(24, cy + 17,
                         "in %s · out %s" % (format(inp, ","),
                                             format(outp, ",")),
                         size=10, fill=INK_SOFT))
        miss = summary[b].get("model_events_missing_usage") or 0
        if miss:
            parts.append(txt(24, cy + 30, "%d events missing usage"
                             % miss, size=9.5, fill=ORANGE))
        x = left
        for val, color in ((inp, TEAL), (outp, ORANGE)):
            w = plot_w * val / x_max
            if w > 0.5:
                parts.append(el("rect", {"x": x, "y": cy - 10,
                                         "width": w, "height": 20,
                                         "fill": color}))
            x += w
        parts.append(txt(x + 8, cy + 4, format(inp + outp, ","),
                         size=11.5, weight="600"))
        parts.append(el("line", {"x1": 24, "y1": y + row_h - 10,
                                 "x2": width - 24, "y2": y + row_h - 10,
                                 "stroke": GRID, "stroke-width": 1}))
        y += row_h

    ly = height - 30
    for color, lab, lx in ((TEAL, "input_tokens_observed", left),
                           (ORANGE, "output_tokens_observed", left + 190)):
        parts.append(el("rect", {"x": lx, "y": ly - 9, "width": 12,
                                 "height": 12, "fill": color}))
        parts.append(txt(lx + 16, ly + 1, lab, size=10.5, fill=INK_SOFT))
    path.write_text(svg_doc(width, height, "\n".join(parts)))


# ---------------------------------------------------------- security evidence


def fig_security_evidence(evidence, path):
    cols = evidence.get("columns") or []
    rows = evidence.get("rows") or []
    width = 1240
    left = 30
    probe_w = 230
    ncol = len(cols)
    cell_w = (width - left * 2 - probe_w) / max(ncol, 1)
    top = 92
    header_h = 40
    row_h = 34

    # Per-probe observation categories. They describe what a cell
    # observed, never a security rating; labels stay verbatim.
    CAT = {
        "enforced": {"strong": "#1E7A46", "soft": "#E2F2E8",
                     "gloss": "policy/VFS/step/memory-limit rejection"},
        "reached": {"strong": "#B3261E", "soft": "#F9E3E0",
                    "gloss": "reached host/external resource"},
        "guest": {"strong": "#2B5EA7", "soft": "#E4ECF7",
                  "gloss": "confined to guest filesystem"},
        "unknown": {"strong": "#6E6455", "soft": "#ECE7DC",
                    "gloss": "inconclusive observation"},
    }
    ENFORCED = {"VFS rejected", "policy denied", "EACCES",
                "step limit", "repeat rejected", "OOM enforced"}
    REACHED = {"host file read", "HTTP 200", "HTTP 401; no data",
               "host write"}
    GUEST = {"guest file read", "guest write"}

    def category(label):
        if label in ENFORCED:
            return "enforced"
        if label in REACHED:
            return "reached"
        if label in GUEST:
            return "guest"
        return "unknown"

    hm, extra = header(
        "Observed effects at execution boundaries",
        evidence.get("scope") or
        "Labels describe observations, not a security score.",
        width)
    top += extra

    # column headers
    hy = top + header_h - 10
    parts = [hm]
    parts.append(txt(left, hy, "probe", size=11, fill=INK_SOFT,
                     weight="600"))
    for i, c in enumerate(cols):
        parts.append(txt(left + probe_w + cell_w * i + cell_w / 2, hy,
                         c, size=10, fill=INK_SOFT, anchor="middle",
                         weight="600"))
    parts.append(el("line", {"x1": left, "y1": hy + 8, "x2": width - left,
                             "y2": hy + 8, "stroke": INK, "stroke-width": 1}))

    y = hy + 8
    for ri, row in enumerate(rows):
        ry = y + row_h - 9
        parts.append(txt(left, ry, row.get("probe", ""), size=11.5,
                         weight="600"))
        labels = row.get("labels") or []
        for i in range(ncol):
            lab = labels[i] if i < len(labels) else ""
            if not lab:
                continue
            st = CAT[category(lab)]
            cx = left + probe_w + cell_w * i
            parts.append(el("rect", {"x": cx + 3, "y": y + 3,
                                     "width": cell_w - 6,
                                     "height": row_h - 6, "rx": 5,
                                     "fill": st["soft"]}))
            group_w = 8 + 5 + len(lab) * 5.5
            gx = cx + cell_w / 2 - group_w / 2
            parts.append(el("rect", {"x": gx, "y": ry - 7, "width": 8,
                                     "height": 8, "rx": 1.5,
                                     "fill": st["strong"]}))
            parts.append(txt(gx + 13, ry, lab, size=10))
        y += row_h

    # legend: category chips with explicit semantics + non-guarantee notes
    ly = y + 24
    parts.append(txt(left, ly, "Cell color — per-probe observation "
                     "category (not a security rating):", size=11,
                     weight="600"))
    chip_y = ly + 18
    lx = float(left)
    for cat in ("enforced", "reached", "guest", "unknown"):
        st = CAT[cat]
        parts.append(el("rect", {"x": lx, "y": chip_y - 8, "width": 10,
                                 "height": 10, "rx": 2,
                                 "fill": st["strong"]}))
        nx = lx + 15
        parts.append(txt(nx, chip_y, cat, size=10.5, weight="600",
                         fill=st["strong"]))
        gx = nx + len(cat) * 6.6 + 8
        parts.append(txt(gx, chip_y, st["gloss"], size=10, fill=INK_SOFT))
        lx = gx + len(st["gloss"]) * 5.5 + 26

    note_y = chip_y + 20
    parts.append(txt(left, note_y,
                     "Color describes this probe only, not overall "
                     "security.", size=10.5, fill=INK_SOFT, weight="600"))
    parts.append(txt(left, note_y + 14,
                     "'repeat rejected' does not imply a general memory "
                     "bound — each label describes only its own probe.",
                     size=10, fill=INK_SOFT))
    parts.append(txt(left, note_y + 28,
                     "Allocation completion does not establish absence "
                     "of a memory limit.",
                     size=10, fill=INK_SOFT))

    # footer: additional observation (reached = red) + wrapped not_tested
    max_chars = max(40, int((width - 2 * left) / 6.2))
    fy = note_y + 14 + 28
    obs = evidence.get("additional_observation") or {}
    if obs:
        red = CAT["reached"]["strong"]
        parts.append(el("rect", {"x": left - 4, "y": fy - 9, "width": 10,
                                 "height": 10, "rx": 2, "fill": red}))
        prefix = "Additional observation: "
        parts.append(txt(left + 10, fy, prefix, size=11, weight="600"))
        pw = len(prefix) * 6.6
        parts.append(txt(left + 10 + pw, fy, obs.get("label", ""),
                         size=11, weight="600", fill=red))
        for i, line in enumerate(wrap_text(obs.get("limitation", ""),
                                           max_chars)):
            parts.append(txt(left, fy + 16 + 14 * i, line, size=10,
                             fill=INK_SOFT))
        fy += 16 + 14 * max(1, len(wrap_text(obs.get("limitation", ""),
                                              max_chars))) + 10
    nt = evidence.get("not_tested") or []
    if nt:
        nt_lines = wrap_text("Not tested: " + "; ".join(nt), max_chars)
        for i, line in enumerate(nt_lines):
            parts.append(txt(left, fy + 14 * i, line, size=10,
                             fill=INK_SOFT))
        fy += 14 * len(nt_lines)
    height = int(fy + 20)
    path.write_text(svg_doc(width, height, "\n".join(parts)))


def main():
    for p in (AGENT_SUMMARY, MICRO_SUMMARY, SECURITY_EVIDENCE):
        if not p.exists():
            raise SystemExit("missing input: %s" % p)
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    summary = json.loads(AGENT_SUMMARY.read_text())
    micro = json.loads(MICRO_SUMMARY.read_text())
    evidence = json.loads(SECURITY_EVIDENCE.read_text())
    fig_task_completion(summary, OUT_DIR / "task-completion.svg")
    fig_warm_latency(micro, OUT_DIR / "warm-latency.svg")
    fig_agent_latency(summary, OUT_DIR / "agent-latency.svg")
    fig_agent_tokens(summary, OUT_DIR / "agent-tokens.svg")
    fig_security_evidence(evidence, OUT_DIR / "security-evidence.svg")
    print("wrote", len(list(OUT_DIR.glob('*.svg'))), "figures to", OUT_DIR)


if __name__ == "__main__":
    main()
