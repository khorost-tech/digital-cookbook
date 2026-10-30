"""Проверки оси 4."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_exclude", os.path.join(HERE, "scripts", "analyze-exclude.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Exclude(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()
        self.by = {r["variant"]: r for r in self.rows}

    def test_controls(self):
        analyze.check(self.rows)

    def test_std_error_handling_hides_real_defects(self):
        # пресет прячет не только Close в defer, но и непроверенный os.Remove
        hidden = self.by["preset-std-error-handling"]["hidden_want"]
        self.assertIn("sample/errs/errs.go:14 errcheck", hidden)
        self.assertIn("excl/danger/danger.go:35 errcheck", hidden)
        self.assertEqual(len(hidden), 5)

    def test_generated_lax_wider_than_strict(self):
        lax, strict = self.by["generated-lax"]["hidden_want"], self.by["generated-strict"]["hidden_want"]
        self.assertEqual(strict, ["excl/gen/std.go:10 errcheck"])
        self.assertEqual(sorted(lax), ["excl/gen/auto.go:9 errcheck", "excl/gen/std.go:10 errcheck"])

    def test_broad_curated_hides_close_after_write(self):
        # прежние исключения курируемого конфига прячут Close после записи; узкие — ничего
        self.assertEqual(self.by["curated-broad"]["hidden_want"], ["excl/danger/danger.go:35 errcheck"])
        self.assertEqual(self.by["curated"]["hidden_want"], [])

    def test_path_rule_hides_only_tests(self):
        self.assertEqual(self.by["path-tests"]["hidden_want"], ["excl/svc/svc_test.go:9 errcheck"])

    def test_lost_finding_is_refused(self):
        # находка пропала, но не числится спрятанной — счёт не сходится с контролем
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, found=r["found"] - 1) if r["variant"] == "path-tests" else r for r in self.rows])

    def test_dirty_control_is_refused(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, hidden_want=["x:1 errcheck"]) if r["variant"] == "base" else r
                           for r in self.rows])


if __name__ == "__main__":
    unittest.main()
