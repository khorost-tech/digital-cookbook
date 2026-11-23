"""Проверки оси 7 (задача 5.3, изменения 05.10)."""

import importlib.util
import json
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location(
    "analyze_coverage", os.path.join(HERE, "scripts", "analyze-coverage.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Coverage(unittest.TestCase):
    def test_boundary_test_separates_variants(self):
        analyze.check_controls(analyze.read())

    def test_percentages_come_from_export(self):
        cov = analyze.pick(analyze.read(), "coverage", "on")
        self.assertEqual(cov["summary"], analyze.summary(cov["export"]))

    def test_main_suite_green_with_bug(self):
        # Предмет оси: основной набор зелёный, хотя ошибка в бинарнике.
        self.assertTrue(analyze.pick(analyze.read(), "coverage", "on")["suite_passed"])

    def test_summary_reads_percent_fields(self):
        doc = {"data": [{"files": [{"summary": {k: {"percent": 50.0} for k in
                                                ("lines", "branches", "mcdc", "regions")}}]}]}
        self.assertEqual(analyze.summary(json.dumps(doc))["mcdc"], 50.0)


if __name__ == "__main__":
    unittest.main()
