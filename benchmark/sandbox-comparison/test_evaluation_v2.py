"""Contract regressions: false positives here would silently inflate results."""

import unittest

from evaluation_v2 import distribution, rate, summarize, validate


class EvaluationTests(unittest.TestCase):
    def test_artifact_predicates(self):
        self.assertTrue(validate("stats", '{"count":100,"mean":50.5,"max":100}')["ok"])
        self.assertFalse(validate("stats", '{"count":"100","mean":50.5,"max":100}')["ok"])
        self.assertFalse(validate("stats", '{"count":100,"mean":50.5,"max":100,"x":1}')["ok"])
        self.assertTrue(validate("join", '{"Sales":90,"Engineering":210}')["ok"])
        self.assertFalse(validate("join", '{"Sales":true,"Engineering":210}')["ok"])
        self.assertFalse(validate("join", '{"Sales":90,"Engineering":NaN}')["ok"])
        self.assertTrue(validate("file", "n,sq\n1,1\n2,4\n3,9\n4,16\n5,25\n")["ok"])
        self.assertFalse(validate("file", "sum=55")["ok"])
        self.assertFalse(validate("file", "n,sq\n1,1\n2,4\n3,9\n4,16\n5,25\n6,36\n")["ok"])
        self.assertTrue(validate("fetch", '{"full_name":"python/cpython","private":false}')["ok"])
        self.assertFalse(validate("fetch", '{"full_name":"python/cpython","private":0}')["ok"])
        self.assertFalse(validate("fetch", "stars=123")["ok"])

    def test_denominators_and_recovery(self):
        rows = [
            {"backend": "example", "task": "stats", "passed": True, "events": [
                {"kind": "model", "ok": True, "duration_s": 2,
                 "input_tokens": 10, "output_tokens": 5},
                {"kind": "tool", "ok": False, "duration_s": 1, "error_kind": "runtime"},
                {"kind": "tool", "ok": True, "duration_s": 1},
            ]},
            {"backend": "example", "task": "stats", "passed": False, "events": []},
        ]
        summary = summarize(rows)["example"]
        self.assertEqual(summary["task_completion"]["rate"], 0.5)
        self.assertEqual(summary["tool_success"]["attempts"], 2)
        self.assertEqual(summary["tool_success"]["rate"], 0.5)
        self.assertEqual(summary["first_tool_success"]["rate"], 0)
        self.assertEqual(summary["recovery"]["rate"], 1)
        self.assertEqual(summary["input_tokens_observed"], 10)
        self.assertEqual(summary["tool_error_kinds"], {"runtime": 1})
        self.assertEqual(summary["latency_s"]["loop_s"]["n"], 0)

    def test_statistics(self):
        self.assertIsNone(rate(0, 0)["rate"])
        self.assertAlmostEqual(rate(12, 12)["wilson95"][0], 0.7575059933447592)
        self.assertEqual(distribution([1, 2, 3, 4])["p50"], 2)
        self.assertEqual(distribution([1, 2, 3, 4])["p95"], 4)


if __name__ == "__main__":
    unittest.main()
