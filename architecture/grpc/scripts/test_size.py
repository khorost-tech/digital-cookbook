"""Проверки оси 9 (задача 9)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_size", os.path.join(HERE, "scripts", "analyze-size.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Size(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()

    def test_controls(self):
        analyze.check(self.rows)

    def test_default_limit_rejects_and_raised_delivers(self):
        for r in self.rows:
            if r["name"] == "5mb":
                self.assertEqual(r["outcome"], "rejected", r["variant"])
            if r["name"] == "5mb-raised":
                self.assertEqual(r["outcome"], "delivered", r["variant"])


if __name__ == "__main__":
    unittest.main()
