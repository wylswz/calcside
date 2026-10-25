import copy
import json
import pathlib
import tempfile
import unittest

from report import OPERATIONS, render, valid_summary


def summary(scenario, modern=False):
    metrics = {
        "benchmark_iterations": {"count": 1},
        "benchmark_success": {"value": 1},
        "checks": {"passes": 2, "fails": 0},
        "http_req_failed{phase:benchmark}": {"value": 0},
        "http_reqs{phase:benchmark}": {"count": 3, "rate": 2.5},
    }
    for op in OPERATIONS[scenario]:
        metrics[f"http_req_duration{{name:{op},phase:benchmark}}"] = {
            "avg": 2, "med": 2, "p(95)": 3, "p(99)": 4, "max": 5,
        }
    if modern:
        for name, values in metrics.items():
            if "value" in values:
                values["rate"] = values.pop("value")
            metrics[name] = {"values": values}
    return {"metrics": metrics}


class ReportTests(unittest.TestCase):
    def test_legacy_and_modern_metrics(self):
        for modern in (False, True):
            for scenario in OPERATIONS:
                run = {"stage": "integration", "scenario": scenario}
                self.assertTrue(valid_summary(summary(scenario, modern), run, 2000))

    def test_missing_or_failed_metrics_never_pass(self):
        run = {"stage": "integration", "scenario": "exec"}
        base = summary("exec")
        self.assertFalse(valid_summary({}, run, 2000))
        cases = [
            ("benchmark_success", "value", 0.5),
            ("benchmark_iterations", "count", 0),
            ("benchmark_iterations", "count", 2),
            ("checks", "fails", 1),
            ("http_req_failed{phase:benchmark}", "value", 0.01),
            ("http_reqs{phase:benchmark}", "count", 0),
            ("http_req_duration{name:exec,phase:benchmark}", "p(95)", 2000),
            ("http_req_duration{name:exec,phase:benchmark}", "p(95)", float("nan")),
        ]
        for name, key, value in cases:
            candidate = copy.deepcopy(base)
            candidate["metrics"][name][key] = value
            self.assertFalse(valid_summary(candidate, run, 2000), (name, key, value))

    def test_complete_report_and_missing_artifact(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            data = {
                "backend": "kind", "started_at": "test", "revision": "test",
                "dirty": False, "database": "PostgreSQL", "routing": "remote",
                "workload": "test", "vus": "5", "duration": "30s",
                "p95_limit_ms": "2000", "runs": [],
            }
            for stage in ("integration", "performance"):
                for scenario in OPERATIONS:
                    filename = f"{stage}-{scenario}.json"
                    (root / filename).write_text(json.dumps(summary(scenario)))
                    data["runs"].append({
                        "stage": stage, "scenario": scenario,
                        "exit_code": 0, "summary": filename,
                    })
            (root / "runs.json").write_text(json.dumps(data))
            self.assertTrue(render(root))
            self.assertIn("| readFile | 2.00 | 2.00 | 3.00 | 4.00 | 5.00 |",
                          (root / "report.md").read_text())
            (root / "performance-read.json").unlink()
            self.assertFalse(render(root))
            self.assertIn("FAIL / INCOMPLETE", (root / "report.md").read_text())
            data["runs"].pop()
            (root / "runs.json").write_text(json.dumps(data))
            self.assertFalse(render(root))


if __name__ == "__main__":
    unittest.main()
