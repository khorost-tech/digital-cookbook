"""Проверки оси 4."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_backfill", os.path.join(HERE, "scripts", "analyze-backfill.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Backfill(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()
        self.by = {r["variant"]: r for r in self.rows}

    def test_controls(self):
        analyze.check(self.rows)

    def test_repeat_needs_reprocess(self):
        self.assertEqual(self.by["repeat-none"]["runs"], 0)
        self.assertEqual(self.by["repeat-completed"]["runs"], self.by["repeat-completed"]["days"])

    def test_depends_on_past_stops_after_failure(self):
        free, ordered = self.by["fail-free"], self.by["fail-ordered"]
        self.assertEqual((free["success"], free["failed"], free["not_run"]), (13, 1, 0))
        self.assertEqual((ordered["success"], ordered["failed"], ordered["not_run"]), (4, 1, 9))

    def test_refusal_does_not_depend_on_history(self):
        # Airflow 3.3.2 отказывает DAG с depends_on_past без --reprocess-behavior
        # и на чистом диапазоне, а не только когда дата уже обработана
        for v in ("ordered-default", "ordered-clean"):
            self.assertNotEqual(self.by[v]["cli_rc"], 0, v)
            self.assertIn("InvalidReprocessBehavior", self.by[v]["cli_error"], v)
        self.assertEqual(self.by["ordered-clean"]["runs"], 0)

    def test_refusal_is_checked(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, cli_rc=0) if r["variant"] == "backwards-ordered" else r for r in self.rows])


if __name__ == "__main__":
    unittest.main()
