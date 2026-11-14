"""Usage subcounter preservation without provider or execution-service calls."""

import unittest
from types import SimpleNamespace

from langchain_core.messages import AIMessage

from run_openai_sol import DetailedRecorder
from run_v2 import BudgetExceeded


class UsageRecorderTests(unittest.TestCase):
    def test_subcounters_do_not_double_count_reasoning(self):
        usage = {
            "input_tokens": 10, "output_tokens": 6, "total_tokens": 16,
            "input_token_details": {"cache_read": 3},
            "output_token_details": {"reasoning": 2},
        }
        response = SimpleNamespace(result=[AIMessage(content="ok", usage_metadata=usage)])
        recorder = DetailedRecorder()
        self.assertIs(recorder.wrap_model_call(None, lambda req: response), response)
        event = recorder.events[0]
        self.assertEqual((event["input_tokens"], event["output_tokens"]), (10, 6))
        self.assertEqual(event["usage_metadata"], [usage])
        response.result[0].usage_metadata["input_token_details"]["cache_read"] = 99
        self.assertEqual(event["usage_metadata"][0]["input_token_details"]["cache_read"], 3)

    def test_budget_error_does_not_overwrite_previous_usage(self):
        recorder = DetailedRecorder()
        recorder.model_calls = 64
        previous = {"kind": "model", "index": 64, "usage_metadata": [{"input_tokens": 7}]}
        recorder.events.append(previous)
        with self.assertRaises(BudgetExceeded):
            recorder.wrap_model_call(None, lambda req: self.fail("handler must not run"))
        self.assertEqual(previous["usage_metadata"], [{"input_tokens": 7}])


if __name__ == "__main__":
    unittest.main()
