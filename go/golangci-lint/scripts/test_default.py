"""Проверки осей 1–2."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_default", os.path.join(HERE, "scripts", "analyze-default.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Default(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()
        self.by = {r["variant"]: r for r in self.rows}

    def test_controls(self):
        analyze.check(self.rows)

    def test_standard_misses_non_default_linters(self):
        # набор по умолчанию не видит линтеров вне своих пяти и выключенных анализаторов govet
        missed = self.by["standard"]["missed_by_linter"]
        for linter in ("bodyclose", "rowserrcheck", "sqlclosecheck", "errorlint", "gosec", "noctx",
                       "prealloc", "perfsprint", "gocritic"):
            self.assertIn(linter, missed, linter)
        self.assertEqual(missed.get("govet"), 2, "shadow и nilness выключены и внутри govet")
        self.assertEqual(self.by["standard"]["extra"], 0)

    def test_places_differ_from_diagnostics(self):
        # 20 ожидаемых диагностик на 18 местах; по умолчанию — 7 диагностик на 5 местах
        s = self.by["standard"]
        self.assertEqual((s["want"], s["places"]), (20, 18))
        self.assertEqual((s["found"], s["places_found"]), (7, 5))
        self.assertEqual((s["bug_places"], s["dead_places"], s["opt_places"]), (15, 1, 2))
        self.assertEqual((s["opt_places"], s["opt_found"]), (2, 0))
        self.assertEqual(self.by["curated"]["places_found"], 18)

    def test_all_is_mostly_outside_marks(self):
        a = self.by["all"]
        self.assertGreater(a["extra"], a["found"], "«всё» шумит больше, чем находит")
        self.assertEqual(a["missed_by_linter"].get("govet"), 2, "«всё» не включает анализаторы govet")

    def test_dirty_fixed_pair_is_refused(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, issues=1) if r["variant"] == "fixed-curated" else r for r in self.rows])

    def test_truncated_count_is_refused(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, lint_cmd=r["lint_cmd"].replace(" --uniq-by-line=false", ""))
                           for r in self.rows])


if __name__ == "__main__":
    unittest.main()
