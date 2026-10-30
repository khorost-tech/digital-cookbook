"""Проверки оси 5."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_nolint", os.path.join(HERE, "scripts", "analyze-nolint.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Nolint(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()
        self.by = {r["variant"]: r for r in self.rows}

    def test_controls(self):
        analyze.check(self.rows)

    def test_bare_hides_second_defect_silently(self):
        r = self.by["strict/two-bare"]
        self.assertEqual(r["suppressed"], ["errcheck", "noctx"])
        # nolintlint говорит о форме директивы, но не о спрятанном втором дефекте
        self.assertEqual(r["nolintlint"], ["без имени линтера", "без причины"])

    def test_specific_keeps_other_linter(self):
        self.assertEqual(self.by["strict/two-specific"]["shown"], ["noctx"])

    def test_wrong_linter_does_not_suppress(self):
        r = self.by["strict/wrong-linter"]
        self.assertEqual((r["shown"], r["nolintlint"]), (["errcheck"], ["не используется"]))

    def test_with_reason_is_clean(self):
        self.assertEqual(self.by["strict/with-reason"]["nolintlint"], [])

    def test_marks_without_nolintlint_are_refused(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, nolintlint=["без причины"]) if r["variant"] == "plain/bare" else r
                           for r in self.rows])

    def test_broken_control_is_refused(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, shown=[]) if r["variant"] == "control/bare" else r for r in self.rows])


if __name__ == "__main__":
    unittest.main()
