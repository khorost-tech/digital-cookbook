"""Проверки оси 5."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_late", os.path.join(HERE, "scripts", "analyze-late.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Late(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()
        self.by = {r["variant"]: r for r in self.rows}

    def test_controls(self):
        analyze.check(self.rows)

    def test_window_must_cover_lateness(self):
        # опоздание 2 дня: окна в 1 день мало, 2 — хватает
        self.assertEqual(self.by["event-lookback1"]["orders_missing"], self.by["event-lookback1"]["late_orders"])
        self.assertEqual(self.by["event-lookback2"]["outcome"], "match")

    def test_incremental_mart_loses_without_window(self):
        self.assertEqual(self.by["ingest"]["outcome"], "match")
        self.assertEqual(self.by["ingest-inc"]["orders_missing"], self.by["ingest-inc"]["late_orders"])
        for v in ("ingest-inc-lookback", "ingest-inc-refresh"):
            self.assertEqual(self.by[v]["outcome"], "match", v)

    def test_late_not_staged_is_refused(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, orders_missing=0) if r["variant"] == "event" else r for r in self.rows])


if __name__ == "__main__":
    unittest.main()
