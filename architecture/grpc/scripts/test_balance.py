"""Проверки оси 7 (задача 8)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_balance", os.path.join(HERE, "scripts", "analyze-balance.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Balance(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()

    def test_controls(self):
        analyze.check(self.rows)

    def test_l4_pins_l7_spreads(self):
        for r in self.rows:
            if r["path"].endswith("-l4"):
                self.assertEqual(r["outcome"], "pinned", r["variant"])
            if r["path"].endswith("-l7") or r["path"] == "direct-round-robin":
                self.assertEqual(r["outcome"], "spread", r["variant"])

    def test_pinned_with_many_connections_is_refused(self):
        row = next(r for r in self.rows if r["outcome"] == "pinned")
        with self.assertRaises(SystemExit):
            analyze.check([dict(row, backend_conns={"grpc-bl-1": 2, "grpc-bl-2": 0, "grpc-bl-3": 0})])


if __name__ == "__main__":
    unittest.main()
