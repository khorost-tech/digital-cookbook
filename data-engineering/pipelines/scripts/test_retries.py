"""Проверки оси 6."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_retries", os.path.join(HERE, "scripts", "analyze-retries.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Retries(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()
        self.by = {r["variant"]: r for r in self.rows}

    def test_controls(self):
        analyze.check(self.rows)

    def test_retries_bound(self):
        self.assertEqual((self.by["r3-k2"]["task_state"], self.by["r3-k2"]["try_number"]), ("success", 3))
        self.assertEqual((self.by["r1-k2"]["task_state"], self.by["r1-k2"]["try_number"]), ("failed", 2))

    def test_retry_of_non_idempotent_task_duplicates(self):
        self.assertGreater(self.by["r3-append-k1"]["duplicates"], 0)
        self.assertEqual(self.by["r3-delete-insert-k1"]["outcome"], "match")

    def test_retries_by_runner_are_refused(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, try_number=1) if r["variant"] == "r3-k2" else r for r in self.rows])


if __name__ == "__main__":
    unittest.main()
