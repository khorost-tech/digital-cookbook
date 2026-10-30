"""Проверки оси 7."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_custom", os.path.join(HERE, "scripts", "analyze-custom.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Custom(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()
        self.by = {r["variant"]: r for r in self.rows}

    def test_controls(self):
        analyze.check(self.rows)

    def test_ctxloss_catches_all(self):
        s = self.by["custom"]["linters"]["ctxloss"]
        self.assertEqual((s["missed"], s["false"]), ([], []))
        self.assertEqual(len(s["caught"]), 5)

    def test_contextcheck_misses_unpassed_root(self):
        # корневой контекст, который не передан дальше, contextcheck не видит
        self.assertEqual(self.by["custom"]["linters"]["contextcheck"]["missed"], ["Enqueue", "Poll"])

    def test_noctx_is_about_other_thing(self):
        self.assertEqual(self.by["custom"]["linters"]["noctx"]["caught"], [])

    def test_false_positive_is_refused(self):
        bad = dict(self.by["custom"])
        bad["linters"] = dict(bad["linters"], ctxloss=dict(bad["linters"]["ctxloss"], false=["Warmup"]))
        with self.assertRaises(SystemExit):
            analyze.check([bad if r["variant"] == "custom" else r for r in self.rows])


if __name__ == "__main__":
    unittest.main()
