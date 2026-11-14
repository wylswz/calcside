#!/usr/bin/env python3
"""Render presentation figures from the FROZEN v3 audit view.

Reads results/v3-agentic/report-data.json, produced by audit_v3.py, and,
when present, the frozen results/openai-sol-agentic/report-data.json for
supplementary model rows. Never reads raw trial rows, never recomputes
rates or statistics; layout and unit scaling (s -> ms) are the only
transformations. The frozen files are never modified.

Outputs results/figures-agentic/task-outcomes.svg and costs.svg.
"""

import json
from pathlib import Path

from render_v2 import (GRID, INK, INK_SOFT, ORANGE, TEAL,
                       el, fmt_pct, header, svg_doc, txt)

HERE = Path(__file__).resolve().parent
REPORT_DATA = HERE / "results" / "v3-agentic" / "report-data.json"
OUT_DIR = HERE / "results" / "figures-agentic"
SOL_DATA = HERE / "results" / "openai-sol-agentic" / "report-data.json"

NATIVE = "calcside_mcp"
NATIVE_TAG = "calcside_mcp · native MCP"
TASKS_B = ("triage", "reconcile", "series")


def conditions(data):
    """Uniform backends sorted, then the native-MCP row last."""
    names = sorted(data["summary"])
    uniform = [b for b in names if b != NATIVE]
    return uniform + ([NATIVE] if NATIVE in names else [])


def row_label(b):
    return NATIVE_TAG if b == NATIVE else b


def fig_task_outcomes(data, path):
    summary = data["summary"]
    normal = data.get("normal_termination") or {}
    conds = conditions(data)
    width = 1060
    name_w = 210
    plot_left = name_w
    plot_w = 560
    row_h = 62
    top = 92

    hm, extra = header(
        "Task outcomes — artifact validation and normal termination",
        ["results/v3-agentic · frozen audit view (report-data.json) · "
         "9 trials per condition · Wilson 95% on artifact bars only",
         "artifact = persisted artifact validates · normal termination = "
         "no setup/agent error",
         "calcside_mcp is the separate native-MCP integration "
         "(CallToolResult.isError semantics), not the uniform "
         "execute_code adapter"],
        width)
    top += extra
    height = top + row_h * len(conds) + 56
    grid_top = height + 10
    cell_h = 30
    grid_x, grid_w = name_w, width - 30 - name_w
    cell_w = grid_w / len(conds)
    height = grid_top + 30 + cell_h * (len(TASKS_B) + 1) + 40

    parts = [hm]
    y = top
    for b in conds:
        st = summary[b]
        tc = st["task_completion"]
        nt = normal.get(b) or {}
        cy = y + 20
        parts.append(txt(24, cy - 2, row_label(b), size=12,
                         weight="600"))
        # artifact bar (teal) with Wilson whisker
        rate = tc.get("rate")
        if rate is not None:
            w = plot_w * rate
            parts.append(el("rect", {"x": plot_left, "y": cy - 8,
                                     "width": w, "height": 15,
                                     "fill": TEAL}))
            wil = tc.get("wilson95") or [None, None]
            if wil[0] is not None:
                xlo = plot_left + plot_w * wil[0]
                xhi = plot_left + plot_w * wil[1]
                parts.append(el("line", {"x1": xlo, "y1": cy - 0.5,
                                         "x2": xhi, "y2": cy - 0.5,
                                         "stroke": INK, "stroke-width": 1.2}))
                for xx in (xlo, xhi):
                    parts.append(el("line", {"x1": xx, "y1": cy - 4,
                                             "x2": xx, "y2": cy + 3,
                                             "stroke": INK,
                                             "stroke-width": 1.2}))
        nrate = nt.get("rate")
        ny = cy + 24
        if nrate is not None:
            parts.append(el("rect", {"x": plot_left, "y": ny - 8,
                                     "width": plot_w * nrate, "height": 15,
                                     "fill": ORANGE}))
        txt_x = plot_left + plot_w + 10
        parts.append(txt(txt_x, cy + 3, "%s/%s · %s" % (
            tc.get("successes"), tc.get("attempts"), fmt_pct(rate)),
            size=10.5, weight="600"))
        parts.append(txt(txt_x, ny + 3, "%s/%s · %s" % (
            nt.get("successes"), nt.get("attempts"), fmt_pct(nrate)),
            size=10.5, weight="600", fill=INK_SOFT))
        parts.append(el("line", {"x1": 24, "y1": y + row_h - 8,
                                 "x2": width - 24, "y2": y + row_h - 8,
                                 "stroke": GRID, "stroke-width": 1}))
        y += row_h

    # by-task counts grid: task rows x condition columns
    parts.append(txt(24, grid_top + 18, "By task (artifact successes/attempts)",
                     size=12, weight="600"))
    gy = grid_top + 26
    parts.append(txt(grid_x, gy + cell_h - 8, "task", size=11,
                     fill=INK_SOFT))
    for i, b in enumerate(conds):
        parts.append(txt(grid_x + cell_w * (i + 0.5), gy + cell_h - 8,
                         b, size=9.5, fill=INK_SOFT, anchor="middle"))
    for ti, task in enumerate(TASKS_B):
        ry = gy + cell_h * (ti + 1)
        parts.append(txt(grid_x + 4, ry + cell_h - 8, task, size=11))
        for i, b in enumerate(conds):
            cell = summary[b].get("by_task", {}).get(task, {})
            parts.append(txt(grid_x + cell_w * (i + 0.5),
                             ry + cell_h - 8,
                             "%s/%s" % (cell.get("successes"),
                                        cell.get("attempts")),
                             size=10, anchor="middle"))

    ly = height - 26
    for color, lab, lx in ((TEAL, "artifact validated", 24),
                           (ORANGE, "normal termination", 210)):
        parts.append(el("rect", {"x": lx, "y": ly - 9, "width": 12,
                                 "height": 12, "fill": color}))
        parts.append(txt(lx + 16, ly + 1, lab, size=10.5, fill=INK_SOFT))
    path.write_text(svg_doc(width, height, "\n".join(parts)))


def cost_rows(data, supplement):
    """v3 conditions first, then supplementary model rows for the same interfaces.

    Supplement rows come from the frozen OpenAI-sol view and cover only the
    two Calcside conditions; no other backend was rerun with that model.
    """
    summary = data["summary"]
    tokens = data.get("tokens_total") or {}
    rows = []
    for b in conditions(data):
        dist = (summary.get(b, {}).get("latency_s", {}) or {}).get("loop_s") or {}
        rows.append({"label": row_label(b), "dist": dist,
                     "tokens": tokens.get(b), "supplement": False})
    groups = (supplement or {}).get("groups") or []
    if groups:
        # The supplement view also carries the baseline model so its own
        # comparison is self-contained; drop it by matching the tokens the
        # v3 view already reports for the same condition.
        baseline = [g["model"] for g in groups
                    if g["backend"] == "calcside"
                    and g["tokens_total"] == tokens.get("calcside")]
        if len(baseline) != 1:
            raise SystemExit("cannot identify the baseline model in the "
                             "supplement view; refusing to guess")
        for group in groups:
            if group["model"] == baseline[0]:
                continue
            rows.append({
                "label": row_label(group["backend"]),
                "sub": group["model"],
                "dist": group.get("loop_latency_s") or {},
                "tokens": group.get("tokens_total"),
                "supplement": True,
            })
    return rows


def fig_costs(data, path, supplement=None):
    rows = cost_rows(data, supplement)
    width = 1240
    label_x = 30
    panel_a = (210, 645)   # loop_s p50/p95
    panel_b = (715, 1215)  # tokens_total
    row_h = 62
    top = 132
    has_supplement = any(r["supplement"] for r in rows)

    subtitles = [
        "results/v3-agentic · frozen audit view (report-data.json) · "
        "loop_s p50/p95 nearest-rank, seconds shown as ms · tokens are "
        "provider-observed totals, not normalized per trial",
        "calcside_mcp is the separate native-MCP integration",
    ]
    if has_supplement:
        subtitles.append(
            "Lower rows: gpt-6.1-sol replication of the same tasks on the two "
            "Calcside interfaces only (results/openai-sol-agentic, different "
            "generation configuration); the other backends were not rerun")
    hm, extra = header(
        "Cost and latency — loop seconds and observed tokens", subtitles, width)
    top += extra
    height = top + row_h * len(rows) + 60

    parts = [hm]
    for px, title, unit in ((panel_a[0], "Agent loop latency (p50 / p95)",
                             "ms"),
                            (panel_b[0], "Observed tokens (input + output)",
                             "tokens")):
        parts.append(txt(px, top - 44, title, size=12, weight="600"))
        parts.append(txt(px, top - 28, unit, size=10, fill=INK_SOFT))

    p95s, tok_max = [], 1
    for row in rows:
        v = row["dist"].get("p95")
        if v is not None:
            p95s.append(v * 1000.0)
        if row["tokens"]:
            tok_max = max(tok_max, row["tokens"])
    lat_max = max(p95s + [1.0]) * 1.15

    y = top
    previous_supplement = False
    for row in rows:
        if row["supplement"] and not previous_supplement:
            parts.append(el("line", {"x1": label_x, "y1": y + 2,
                                     "x2": width - 24, "y2": y + 2,
                                     "stroke": INK_SOFT, "stroke-width": 1,
                                     "stroke-dasharray": "4 3"}))
            y += 8
        previous_supplement = row["supplement"]
        cy = y + 24
        if row.get("sub"):
            parts.append(txt(label_x, cy - 2, row["label"], size=11.5,
                             weight="600"))
            parts.append(txt(label_x, cy + 12, row["sub"], size=9.5,
                             fill=INK_SOFT))
        else:
            parts.append(txt(label_x, cy + 4, row["label"], size=11.5,
                             weight="600"))
        dist = row["dist"]
        for val_s, color, dy, lab in (
                (dist.get("p50"), TEAL, -13, "p50"),
                (dist.get("p95"), ORANGE, 13, "p95")):
            if val_s is None:
                continue
            v = val_s * 1000.0
            w = (panel_a[1] - panel_a[0]) * v / lat_max
            parts.append(el("rect", {"x": panel_a[0], "y": cy + dy - 6,
                                     "width": max(w, 1), "height": 12,
                                     "fill": color}))
            parts.append(txt(panel_a[0] + max(w, 1) + 6, cy + dy + 4,
                             "%s %.2f ms" % (lab, v), size=9.5,
                             fill=INK_SOFT))
        n = dist.get("n")
        if n is not None:
            parts.append(txt(panel_a[1] - 8, cy - 16, "n=%s" % n, size=9,
                             fill=INK_SOFT, anchor="end"))
        tv = row["tokens"]
        if tv is None:
            parts.append(txt(panel_b[0], cy + 4, "N/A", size=10,
                             fill=INK_SOFT))
        else:
            w = (panel_b[1] - panel_b[0] - 95) * tv / tok_max
            parts.append(el("rect", {"x": panel_b[0], "y": cy - 8,
                                     "width": max(w, 1), "height": 16,
                                     "fill": TEAL, "opacity": 0.85}))
            parts.append(txt(panel_b[0] + max(w, 1) + 8, cy + 4,
                             "{:,}".format(tv), size=10.5, weight="600"))
        parts.append(el("line", {"x1": label_x, "y1": y + row_h - 8,
                                 "x2": width - 24, "y2": y + row_h - 8,
                                 "stroke": GRID, "stroke-width": 1}))
        y += row_h

    ly = height - 24
    for color, lab, lx in ((TEAL, "p50 / tokens", panel_a[0]),
                           (ORANGE, "p95", panel_a[0] + 110)):
        parts.append(el("rect", {"x": lx, "y": ly - 9, "width": 12,
                                 "height": 12, "fill": color}))
        parts.append(txt(lx + 16, ly + 1, lab, size=10, fill=INK_SOFT))
    path.write_text(svg_doc(width, height, "\n".join(parts)))


def main():
    if not REPORT_DATA.exists():
        raise SystemExit("missing input: %s (run audit_v3.py first)"
                         % REPORT_DATA)
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    data = json.loads(REPORT_DATA.read_text())
    supplement = (json.loads(SOL_DATA.read_text()) if SOL_DATA.exists() else None)
    fig_task_outcomes(data, OUT_DIR / "task-outcomes.svg")
    fig_costs(data, OUT_DIR / "costs.svg", supplement)
    print("wrote", len(list(OUT_DIR.glob('*.svg'))),
          "figures to", OUT_DIR)


if __name__ == "__main__":
    main()
