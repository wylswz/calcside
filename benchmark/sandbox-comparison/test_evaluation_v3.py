"""Contract and fixture-oracle checks for the additional workload."""

import csv
import io
import json
import unittest
from collections import Counter
from decimal import Decimal

from evaluation_v3 import BANK, EXPECTED, LEDGER, SERIES, TASKS_B, summarize, validate


class AdditionalTaskTests(unittest.TestCase):
    def test_reference_values_against_inputs(self):
        records = [
            json.loads(line) for text in TASKS_B["triage"]["files"].values()
            for line in text.splitlines()
        ]
        errors = [r for r in records if r["level"] == "error"]
        self.assertEqual(dict(Counter(r["service"] for r in errors)), EXPECTED["triage"]["counts"])
        latest = sorted(errors, key=lambda r: (r["ts"], r["request_id"]), reverse=True)[0]["ts"]
        self.assertEqual(min(r["request_id"] for r in errors if r["ts"] == latest), "req-tie-a")
        ledger = {r["id"]: Decimal(r["amount"]) for r in csv.DictReader(io.StringIO(LEDGER))}
        bank = {r["id"]: (Decimal(r["amount"]), r["status"])
                for r in csv.DictReader(io.StringIO(BANK))}
        matched = [k for k in ledger if bank.get(k) == (ledger[k], "settled")]
        self.assertEqual(matched, ["TX01", "TX02", "TX06"])
        self.assertEqual(
            sum(bank[k][0] for k in bank if k not in matched)
            - sum(ledger[k] for k in ledger if k not in matched), Decimal("40.10"))
        frequencies = Counter(int(r["value"]) for r in csv.DictReader(io.StringIO(SERIES)))
        self.assertEqual(frequencies, Counter({v: 5 for v in range(1000)}))

    def test_valid_and_invalid_artifacts(self):
        for task, answer in EXPECTED.items():
            self.assertTrue(validate(task, json.dumps(answer))["ok"])
            self.assertFalse(validate(task, json.dumps({**answer, "extra": 1}))["ok"])
            self.assertFalse(validate(task, "[]")["ok"])
        bad = {**EXPECTED["triage"], "latest_error": "req-tie-z"}
        self.assertFalse(validate("triage", json.dumps(bad))["ok"])
        bad = {**EXPECTED["reconcile"], "unmatched_ledger": ["TX05", "TX04", "TX03"]}
        self.assertFalse(validate("reconcile", json.dumps(bad))["ok"])
        bad = {**EXPECTED["reconcile"], "matched_count": True}
        self.assertFalse(validate("reconcile", json.dumps(bad))["ok"])
        for wrong in (950, True, "949", 949.0):
            self.assertFalse(validate("series", json.dumps({**EXPECTED["series"], "p95": wrong}))["ok"])

    def test_summary_task_denominators(self):
        row = {"backend": "python", "task": "triage", "passed": True,
               "events": [], "cleanup_error": None}
        by_task = summarize([row])["python"]["by_task"]
        self.assertEqual(by_task["triage"]["attempts"], 1)
        self.assertEqual(by_task["reconcile"]["attempts"], 0)
        self.assertEqual(set(by_task), set(TASKS_B))


if __name__ == "__main__":
    unittest.main()
