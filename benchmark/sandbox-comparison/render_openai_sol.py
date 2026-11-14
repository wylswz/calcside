#!/usr/bin/env python3
"""Render the frozen OpenAI-sol model figures.

Reads only results/openai-sol-agentic/report-data.json, produced by the
lead-owned audit_openai_sol.py (result["groups"]). Never reads raw
trial rows and never recomputes metrics; layout and unit scaling are
the only transformations.

Outputs results/figures-openai-sol/model-outcomes.svg and
model-comparison.svg (+ PNG via rsvg-convert when available).
"""

import json
import shutil
import subprocess
from pathlib import Path

from render_v2 import (GRID, INK, INK_SOFT, ORANGE, TAUPE, TEAL,
                       el, fmt_pct, header, svg_doc, txt)

HERE = Path(__file__).resolve().parent
REPORT_DATA = HERE / "results" / "openai-sol-agentic" / "report-data.json"
OUT_DIR = HERE / "results" / "figures-openai-sol"

INTERFACE_LABEL = {"calcside": "uniform execute_code",
                   "calcside_mcp": "native MCP"}


def ordered_groups(data):
    groups = list(data.get("groups") or [])
    return sorted(groups, key=lambda g: (g.get("model") or "",
                                         g.get("backend") or ""))


def fig_model_outcomes(data, path):
    """Artifact/termination bars plus loop latency, mirroring the set-B figure."""
    groups = ordered_groups(data)
    width = 1240
    label_x = 30
    plot_left, plot_right = 210, 600
    plot_w = plot_right - plot_left
    lat_left, lat_right = 690, 1210
    lat_w = lat_right - lat_left
    row_h = 70
    top = 132
    tasks = ("triage", "reconcile", "series")

    hm, extra = header(
        "Task outcomes and loop latency — gpt-6.1-sol vs deepseek-flash",
        ["Task set B · 9 trials per model/interface configuration · "
         "artifact = persisted artifact validates · normal termination = "
         "no setup/agent error",
         "Wilson 95% whiskers on artifact bars only; loop p50/p95 are "
         "nearest-rank over 9 trials, so p95 equals the largest observation",
         "Generation configurations differ between the two models — see "
         "PROTOCOL_OPENAI_SOL.md; this is not a matched-budget comparison"],
        width)
    top += extra
    grid_top = top + row_h * len(groups) + 34
    cell_h = 30
    height = grid_top + 30 + cell_h * (len(tasks) + 1) + 54

    parts = [hm]
    for px, title, unit in ((plot_left, "Artifact validation", "trials"),
                            (lat_left, "Agent loop latency", "seconds")):
        parts.append(txt(px, top - 44, title, size=12, weight="600"))
        parts.append(txt(px, top - 28, unit, size=10, fill=INK_SOFT))

    lat_max = max([(g["loop_latency_s"]["p95"] or 0) for g in groups] + [1.0]) * 1.1

    y = top
    for g in groups:
        cy = y + 26
        model = g.get("model") or "?"
        backend = g.get("backend") or "?"
        parts.append(txt(label_x, cy - 8, model, size=12, weight="600"))
        parts.append(txt(label_x, cy + 8,
                         INTERFACE_LABEL.get(backend, backend),
                         size=9.5, fill=INK_SOFT))

        rate = g["artifact_successes"] / g["trials"]
        parts.append(el("rect", {"x": plot_left, "y": cy - 10,
                                 "width": max(plot_w * rate, 1), "height": 15,
                                 "fill": TEAL}))
        lo, hi = g["by_task"][tasks[0]]["wilson95"]
        parts.append(el("line", {"x1": plot_left + plot_w * lo, "y1": cy - 2.5,
                                 "x2": plot_left + plot_w * hi, "y2": cy - 2.5,
                                 "stroke": INK, "stroke-width": 1.6}))
        nrate = g["normal_successes"] / g["trials"]
        parts.append(el("rect", {"x": plot_left, "y": cy + 12,
                                 "width": max(plot_w * nrate, 1), "height": 15,
                                 "fill": ORANGE}))
        parts.append(txt(plot_right + 8, cy - 1,
                         "%d/%d · %s" % (g["artifact_successes"], g["trials"],
                                         fmt_pct(rate)), size=10.5, weight="600"))
        parts.append(txt(plot_right + 8, cy + 23,
                         "%d/%d · %s" % (g["normal_successes"], g["trials"],
                                         fmt_pct(nrate)),
                         size=10.5, fill=INK_SOFT))

        dist = g["loop_latency_s"]
        for key, color, dy in (("p50", TEAL, -9), ("p95", ORANGE, 9)):
            value = dist.get(key)
            if value is None:
                continue
            w = max(lat_w * value / lat_max, 1)
            parts.append(el("rect", {"x": lat_left, "y": cy + dy - 5,
                                     "width": w, "height": 10, "fill": color}))
            parts.append(txt(lat_left + w + 7, cy + dy + 3.5,
                             "%s %.2f s" % (key, value), size=9.5,
                             fill=INK_SOFT))
        parts.append(el("line", {"x1": label_x, "y1": y + row_h - 8,
                                 "x2": width - 24, "y2": y + row_h - 8,
                                 "stroke": GRID, "stroke-width": 1}))
        y += row_h

    parts.append(txt(label_x, grid_top + 16,
                     "By task (artifact successes/attempts)",
                     size=12, weight="600"))
    gy = grid_top + 22
    grid_x = 210
    cell_w = (width - 24 - grid_x) / len(groups)
    parts.append(txt(grid_x, gy + cell_h - 8, "task", size=11, fill=INK_SOFT))
    for i, g in enumerate(groups):
        parts.append(txt(grid_x + cell_w * (i + 0.5), gy + cell_h - 8,
                         "%s · %s" % (g["model"], INTERFACE_LABEL.get(
                             g["backend"], g["backend"])),
                         size=9, fill=INK_SOFT, anchor="middle"))
    for ti, task in enumerate(tasks):
        ry = gy + cell_h * (ti + 1)
        parts.append(txt(grid_x + 4, ry + cell_h - 8, task, size=11))
        for i, g in enumerate(groups):
            cell = g["by_task"][task]
            parts.append(txt(grid_x + cell_w * (i + 0.5), ry + cell_h - 8,
                             "%d/%d" % (cell["successes"], cell["attempts"]),
                             size=10, anchor="middle"))

    ly = height - 26
    for color, lab, lx in ((TEAL, "artifact validated / p50", 30),
                           (ORANGE, "normal termination / p95", 250)):
        parts.append(el("rect", {"x": lx, "y": ly - 9, "width": 12,
                                 "height": 12, "fill": color}))
        parts.append(txt(lx + 16, ly + 1, lab, size=10, fill=INK_SOFT))
    path.write_text(svg_doc(width, height, "\n".join(parts)))
    rasterize(path)


def fig_model_comparison(data, path):
    groups = ordered_groups(data)
    width = 1240
    label_x = 30
    panel_a = (210, 830)    # token stack: input teal + output orange
    panel_b = (880, 1210)   # model_calls
    bar_a_w = 380           # leaves ~240 px for value labels
    bar_b_w = 240           # leaves ~90 px for value labels
    row_h = 80
    top = 132

    usage_note = data.get("usage_note") or (
        "Input includes cached context; output already includes reasoning.")
    hm, extra = header(
        "Model comparison — observed tokens and model calls",
        ["Task set B · 9 trials per model/interface configuration",
         "deepseek-flash vs gpt-6.1-sol generation configurations differ "
         "(Responses API, reasoning_effort medium, 16,384-token output "
         "cap incl. reasoning, temperature omitted vs temperature 0, "
         "1,200-token cap) — not an intrinsic MCP-overhead measure",
         usage_note],
        width)
    top += extra
    height = top + row_h * len(groups) + 60

    parts = [hm]
    for px, title, unit in ((panel_a[0], "Observed tokens", "tokens"),
                            (panel_b[0], "Model calls", "calls")):
        parts.append(txt(px, top - 44, title, size=12, weight="600"))
        parts.append(txt(px, top - 28, unit, size=10, fill=INK_SOFT))

    tok_max = max([g["tokens_total"] for g in groups] + [1])
    call_max = max([g.get("model_calls") or 0 for g in groups] + [1])

    y = top
    for g in groups:
        cy = y + 56
        model = g.get("model") or "?"
        backend = g.get("backend") or "?"
        parts.append(txt(label_x, cy - 22, model, size=12, weight="600"))
        parts.append(txt(label_x, cy - 8,
                         INTERFACE_LABEL.get(backend, backend),
                         size=9.5, fill=INK_SOFT))

        # panel A: stacked input/output token bar
        inp = g.get("input_tokens") or 0
        out = g.get("output_tokens") or 0
        total = g["tokens_total"]
        w_in = bar_a_w * inp / tok_max
        w_out = bar_a_w * out / tok_max
        sx = panel_a[0]
        if w_in > 0:
            parts.append(el("rect", {"x": sx, "y": cy - 11,
                                     "width": w_in, "height": 22,
                                     "fill": TEAL}))
        if w_out > 0:
            parts.append(el("rect", {"x": sx + w_in, "y": cy - 11,
                                     "width": w_out, "height": 22,
                                     "fill": ORANGE}))
        stack_end = sx + w_in + w_out
        lx = stack_end + 8
        parts.append(txt(lx, cy - 1, "{:,} tokens".format(total),
                         size=10.5, weight="600"))
        parts.append(txt(lx, cy + 13, "in {:,} · out {:,}".format(inp, out),
                         size=9, fill=INK_SOFT))

        # panel B: model_calls bar
        calls = g.get("model_calls")
        if calls is not None:
            w = bar_b_w * calls / call_max
            parts.append(el("rect", {"x": panel_b[0], "y": cy - 7,
                                     "width": max(w, 1), "height": 14,
                                     "fill": TAUPE}))
            parts.append(txt(panel_b[0] + max(w, 1) + 8, cy + 4,
                             "{:,} calls".format(calls), size=10.5,
                             weight="600"))
        parts.append(el("line", {"x1": label_x, "y1": y + row_h - 8,
                                 "x2": width - 24, "y2": y + row_h - 8,
                                 "stroke": GRID, "stroke-width": 1}))
        y += row_h

    ly = height - 24
    for color, lab, lx in ((TEAL, "input tokens (incl. cache reads)",
                            panel_a[0]),
                           (ORANGE, "output tokens (incl. reasoning)",
                            panel_a[0] + 250)):
        parts.append(el("rect", {"x": lx, "y": ly - 9, "width": 12,
                                 "height": 12, "fill": color}))
        parts.append(txt(lx + 16, ly + 1, lab, size=10, fill=INK_SOFT))
    path.write_text(svg_doc(width, height, "\n".join(parts)))
    rasterize(path)


def rasterize(path):
    rsvg = shutil.which("rsvg-convert")
    if not rsvg:
        print("rsvg-convert not found; PNG not written for", path.name)
        return
    subprocess.run([rsvg, "-z", "2", "-o",
                    str(path.with_suffix(".png")), str(path)], check=True)


def main():
    if not REPORT_DATA.exists():
        raise SystemExit("missing input: %s (run audit_openai_sol.py "
                         "first)" % REPORT_DATA)
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    data = json.loads(REPORT_DATA.read_text())
    fig_model_outcomes(data, OUT_DIR / "model-outcomes.svg")
    fig_model_comparison(data, OUT_DIR / "model-comparison.svg")
    print("wrote", OUT_DIR / "model-outcomes.svg", "and model-comparison.svg")


if __name__ == "__main__":
    main()
