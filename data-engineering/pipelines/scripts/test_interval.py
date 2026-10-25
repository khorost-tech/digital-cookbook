"""Проверки оси 3."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_interval", os.path.join(HERE, "scripts", "analyze-interval.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Interval(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()
        self.by = {r["variant"]: r for r in self.rows}

    def test_controls(self):
        analyze.check(self.rows)

    def test_now_picks_one_day(self):
        r = self.by["now"]
        self.assertEqual((r["distinct_picked"], r["many"], r["never"]), (1, 1, r["days"] - 1))

    def test_airflow3_default_schedule_takes_starting_day(self):
        # @daily в Airflow 3 по умолчанию: интервала нет, берётся день срабатывания
        t, i = self.by["scheduled-trigger"], self.by["scheduled-interval"]
        self.assertEqual((t["day_offset"], t["interval_hours"]), (0, 0))
        self.assertEqual((i["day_offset"], i["interval_hours"]), (-1, 24))

    def test_manual_trigger_interval_is_run_time(self):
        r = self.by["manual-trigger"]
        self.assertEqual((r["picked_logical"], r["picked_run_day"]), (False, True))

    def test_broken_control_is_refused(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, once=13, never=1) if r["variant"] == "interval" else r for r in self.rows])


if __name__ == "__main__":
    unittest.main()
