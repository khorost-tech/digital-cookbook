"""Проверки оси 8."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_semantics",
                                               os.path.join(HERE, "scripts", "analyze-semantics.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Semantics(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()

    def test_controls(self):
        analyze.check(self.rows)

    def test_leak_only_on_early_return(self):
        by = {r["variant"]: r["in_use"] for r in self.rows if "in_use" in r}
        self.assertEqual(by, {"TestDrainedRowsReleaseConnection": 0, "TestSampleFirstHoldsConnection": 1,
                              "TestFixedFirstReleasesConnection": 0})

    def test_fix_that_changes_contract_is_refused(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, fixed_null=0) if r["variant"] == "TestIDsEmptyContract" else r for r in self.rows])

    def test_drained_leak_is_refused(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, in_use=1) if r["variant"] == "TestDrainedRowsReleaseConnection" else r
                           for r in self.rows])


if __name__ == "__main__":
    unittest.main()
