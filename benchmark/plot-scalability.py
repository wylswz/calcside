#!/usr/bin/env python3
"""Render report figures from frozen results, without querying or benchmarking."""

import argparse
import html
import json
import pathlib
import tarfile


INK = "#203343"
MUTED = "#647583"
GRID = "#e3e9ec"
BLUE = "#2375ae"
TEAL = "#158577"
ORANGE = "#d97830"
RED = "#c74949"
GREY = "#8a98a4"


class SVG:
    def __init__(self, height, title, description):
        self.parts = [
            f'<svg xmlns="http://www.w3.org/2000/svg" width="1120" height="{height}" '
            f'viewBox="0 0 1120 {height}" role="img" aria-labelledby="title desc">',
            f"<title id=\"title\">{html.escape(title)}</title>",
            f"<desc id=\"desc\">{html.escape(description)}</desc>",
            '<rect width="1120" height="100%" fill="#ffffff"/>',
            '<style>text{font-family:"IBM Plex Sans","Helvetica Neue",sans-serif;'
            'fill:#203343} .title{font-family:Georgia,serif;font-size:29px}'
            '.note{font-size:13px;fill:#647583}.tick{font-size:14px;fill:#647583}'
            '.label{font-size:15px}.value{font-size:15px;font-weight:600}</style>',
        ]
        self.text(42, 44, title, "title")
        self.text(42, 72, description, "note")

    def add(self, element):
        self.parts.append(element)

    def text(self, x, y, text, cls="label", anchor="start", color=None):
        style = f' style="fill:{color}"' if color else ""
        self.add(f'<text x="{x:.2f}" y="{y:.2f}" class="{cls}" '
                 f'text-anchor="{anchor}"{style}>{html.escape(str(text))}</text>')

    def line(self, x1, y1, x2, y2, color=GRID, width=1, dash=None):
        dashed = f' stroke-dasharray="{dash}"' if dash else ""
        self.add(f'<line x1="{x1:.2f}" y1="{y1:.2f}" x2="{x2:.2f}" y2="{y2:.2f}" '
                 f'stroke="{color}" stroke-width="{width}"{dashed}/>')

    def rect(self, x, y, width, height, color, opacity=1):
        self.add(f'<rect x="{x:.2f}" y="{y:.2f}" width="{width:.2f}" '
                 f'height="{height:.2f}" fill="{color}" opacity="{opacity}"/>')

    def dot(self, x, y, color):
        self.add(f'<circle cx="{x:.2f}" cy="{y:.2f}" r="4.5" '
                 f'fill="{color}" stroke="white" stroke-width="1.5"/>')

    def legend(self, x, y, color, label):
        self.rect(x, y - 10, 13, 13, color)
        self.text(x + 21, y + 1, label, "tick")

    def save(self, path):
        path.write_text("\n".join(self.parts + ["</svg>"]) + "\n")


def throughput(rows, output):
    svg = SVG(490, "More prepared replicas, more application throughput",
              "Successful invokes per second | Median of two 30-second runs | Independent panel y-axes")
    colors = {1: GREY, 8: BLUE, 24: ORANGE}
    for x, vus in zip((740, 850, 960), colors):
        svg.legend(x, 111, colors[vus], f"{vus} VU" + ("s" if vus > 1 else ""))
    for workload, left, maximum, ticks, label in [
        ("cpu", 90, 220, range(0, 201, 50), "CPU: sum of squares"),
        ("io", 650, 32, range(0, 31, 10), "I/O: fixed 100 ms delay"),
    ]:
        top, bottom, width = 157, 386, 380
        x = lambda workers: left + 18 + (workers - 1) * (width - 36) / 2
        y = lambda rate: bottom - rate / maximum * (bottom - top)
        svg.text(left, 114 if workload == "cpu" else 139, label, "value")
        if workload == "cpu":
            svg.text(left, 139, "Successful invokes / s", "note")
        for tick in ticks:
            svg.line(left, y(tick), left + width, y(tick))
            svg.text(left - 12, y(tick) + 5, tick, "tick", "end")
        for workers in (1, 2, 3):
            svg.text(x(workers), bottom + 25, workers, "tick", "middle")
        svg.text(left + width / 2, bottom + 49, "Registered workers", "tick", "middle")
        for vus, color in colors.items():
            series = sorted((r for r in rows if r["workload"] == workload and r["vus"] == vus),
                            key=lambda r: r["workers"])
            assert [r["workers"] for r in series] == [1, 2, 3]
            assert all(r["replicates"] == 2 and
                       r["rps_min"] <= r["rps_median"] <= r["rps_max"] for r in series)
            upper = [(x(r["workers"]), y(r["rps_max"])) for r in series]
            lower = [(x(r["workers"]), y(r["rps_min"])) for r in reversed(series)]
            points = " ".join(f"{px:.2f},{py:.2f}" for px, py in upper + lower)
            svg.add(f'<polygon points="{points}" fill="{color}" opacity="0.13"/>')
            previous = None
            for r in series:
                px, py = x(r["workers"]), y(r["rps_median"])
                if previous:
                    svg.line(*previous, px, py, color, 2.3)
                svg.line(px, y(r["rps_min"]), px, y(r["rps_max"]), color, 1.2)
                svg.dot(px, py, color)
                previous = (px, py)
            if vus == 24:
                svg.text(px + 11, py - 9, f'{series[-1]["rps_median"]:.2f}', "value", color=color)
    svg.text(42, 469, "Bands show observed min-max across two runs, not confidence intervals. "
             "Lines connect measured worker counts only.", "note")
    svg.save(output / "throughput.svg")


def speedup(rows, output):
    svg = SVG(430, "Scale-out gains depend on the workload",
              "24 concurrent clients | Speedup relative to each workload's one-worker median throughput")
    left, right, top, bottom = 90, 710, 126, 344
    x = lambda workers: left + 25 + (workers - 1) * (right - left - 50) / 2
    y = lambda ratio: bottom - ratio / 3.4 * (bottom - top)
    for tick in (0, 1, 2, 3):
        svg.line(left, y(tick), right, y(tick))
        svg.text(left - 12, y(tick) + 5, f"{tick}x", "tick", "end")
    for workers in (1, 2, 3):
        svg.text(x(workers), bottom + 25, workers, "tick", "middle")
    svg.text((left + right) / 2, bottom + 49, "Registered workers", "tick", "middle")
    svg.line(x(1), y(1), x(3), y(3), GREY, 2, "6 5")
    svg.legend(90, 105, GREY, "Ideal linear reference (not measured)")
    for workload, color, name, summary_y in [
        ("cpu", BLUE, "CPU", 273), ("io", ORANGE, "I/O", 170),
    ]:
        series = sorted((r for r in rows if r["workload"] == workload and r["vus"] == 24),
                        key=lambda r: r["workers"])
        assert [r["workers"] for r in series] == [1, 2, 3]
        assert series[0]["speedup_vs_1"] == 1
        previous = None
        for r in series:
            px, py = x(r["workers"]), y(r["speedup_vs_1"])
            if previous:
                svg.line(*previous, px, py, color, 2.8)
            svg.dot(px, py, color)
            previous = (px, py)
        svg.text(800, summary_y, f'{series[-1]["speedup_vs_1"]:.2f}x', "title", color=color)
        svg.text(800, summary_y + 27, f"{name}: three workers vs one", "tick")
    svg.text(42, 415, "Ratios use median throughput; two replicates do not establish statistical "
             "significance or superlinear scaling.", "note")
    svg.save(output / "speedup.svg")


def fault_timeline(fault, facts, events, output):
    case = next(c for c in fault["cases"] if c["name"] == "pause-w3-io-v12-verified")
    trace = fault["traces"][case["name"]]
    stop = next(e["unix_ms"] for e in events if e["name"] == "worker_paused")
    resume = next(e["unix_ms"] for e in events if e["name"] == "worker_resumed")
    stop_s, resume_s = [(t - trace["started_ms"]) / 1000 for t in (stop, resume)]
    assert 0 < stop_s < resume_s < 120
    bins = {r["start_s"]: r for r in trace["bins_5s"]}
    assert sum(r["attempts"] for r in bins.values()) == case["attempts"] == trace["requests"]
    assert sum(r["completed"] for r in bins.values()) == case["completed"]
    assert sum(r["status_counts"].get("0", 0) for r in bins.values()) == case["errors"]["transport"]
    svg = SVG(510, "Worker stall: detection, contraction, recovery",
              "5-second request-start cohorts | 12 closed-loop clients | Empty bins mean no new requests started")
    colors = (BLUE, TEAL, ORANGE)
    for i, color in enumerate(colors):
        svg.legend(410 + i * 145, 110, color, f"Worker {i}")
    svg.legend(845, 110, RED, "Timed out")
    left, width, top, bottom = 90, 960, 163, 390
    x = lambda seconds: left + seconds / 120 * width
    y = lambda count: bottom - count / 180 * (bottom - top)
    svg.rect(x(stop_s), top, x(resume_s) - x(stop_s), bottom - top, "#fff2e7")
    svg.text(left, 139, "Invocations per start cohort", "note")
    for tick in (0, 50, 100, 150):
        svg.line(left, y(tick), left + width, y(tick))
        svg.text(left - 12, y(tick) + 5, tick, "tick", "end")
    for start in range(0, 120, 5):
        row = bins.get(start)
        if row is None:
            continue
        by_worker = [0, 0, 0]
        for ip, count in row["worker_counts"].items():
            by_worker[case["worker_ips"][ip]] += count
        assert sum(by_worker) == row["completed"]
        timed_out = row["status_counts"].get("0", 0)
        assert row["completed"] + timed_out == row["attempts"]
        total = 0
        for count, color in zip(by_worker + [timed_out], [*colors, RED]):
            svg.rect(x(start) + 3, y(total + count), width / 24 - 6, y(total) - y(total + count), color)
            total += count
    for seconds, label in ((stop_s, f"STOP {stop_s:.1f} s"), (resume_s, f"CONT {resume_s:.1f} s")):
        svg.line(x(seconds), top - 8, x(seconds), bottom, INK, 1.2, "4 4")
        svg.text(x(seconds) + 7, top - 10, label, "value")
    for seconds in range(0, 121, 20):
        svg.text(x(seconds), bottom + 24, seconds, "tick", "middle")
    svg.text(left + width / 2, bottom + 47, "Seconds since first request start", "tick", "middle")
    svg.text(42, 469, f'{case["completed"]:,}/{case["attempts"]:,} valid completions; '
             f'{case["errors"]["transport"]} timeouts. Colors attribute successes to their actual worker.', "note")
    svg.text(42, 491, "First sampled pool contraction: "
             f'{facts["first_two_replica_pool_sample_after_pause_s"]:.1f} s after STOP; recovery: '
             f'{facts["first_three_replica_pool_sample_after_resume_s"]:.1f} s after CONT '
             "(5-second sampling; not exact SLOs).", "note")
    svg.save(output / "worker-stall.svg")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("results", type=pathlib.Path)
    args = parser.parse_args()
    root = args.results
    steady = json.loads((root / "steady-analysis.json").read_text())
    fault = json.loads((root / "fault-analysis.json").read_text())
    facts = json.loads((root / "report-facts.json").read_text())
    with tarfile.open(root / "raw-results.tar.gz", "r:gz") as archive:
        with archive.extractfile("verified-fault/events.json") as source:
            events = json.load(source)
    output = root / "charts"
    output.mkdir(exist_ok=True)
    throughput(steady["steady"], output)
    speedup(steady["steady"], output)
    fault_timeline(fault, facts["fault"], events, output)


if __name__ == "__main__":
    main()
