"""Проверки оси 3 (задача 4)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_scale", os.path.join(HERE, "scripts", "analyze-scale.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Scale(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()

    def test_controls(self):
        analyze.check(self.rows)

    def test_clients_spread_over_instances(self):
        for r in self.rows:
            if r["kind"] == "fanout":
                self.assertEqual(set(r["clients_per_instance"]), {analyze.A, analyze.B}, r["variant"])

    def test_all_clients_on_one_instance_is_refused(self):
        row = next(r for r in self.rows if r["kind"] == "fanout" and r["bus"] == "none")
        bad = dict(row, clients_per_instance={analyze.A: 20}, full_per_instance={analyze.A: 20},
                   clients_none=0, outcome="all")
        with self.assertRaises(SystemExit):
            analyze.check([bad])

    def test_without_bus_only_publishing_instance(self):
        for r in self.rows:
            if r["kind"] == "fanout" and r["bus"] == "none":
                self.assertEqual(r["outcome"], "partial", r["variant"])
                self.assertEqual(list(r["full_per_instance"]), [analyze.A], r["variant"])


if __name__ == "__main__":
    unittest.main()
