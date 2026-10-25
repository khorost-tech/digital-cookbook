"""Проверки оси 2."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_idem", os.path.join(HERE, "scripts", "analyze-idempotency.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Idempotency(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()
        self.by = {r["variant"]: r for r in self.rows}

    def test_controls(self):
        analyze.check(self.rows)

    def test_append_duplicates_on_rerun(self):
        r = self.by["append/rerun"]
        self.assertEqual(r["duplicates"], r["want_items"])

    def test_only_transaction_keeps_day_whole_during_crash(self):
        # после падения: транзакция откатилась — день целый; delete-batch — день потерян частично
        self.assertEqual(self.by["delete-insert/crash-reload"]["after_crash"]["missing"], 0)
        self.assertGreater(self.by["delete-batch/crash-reload"]["after_crash"]["missing"], 0)
        self.assertGreater(self.by["append/crash-reload"]["after_crash"]["duplicates"], 0)

    def test_idempotent_modes_converge(self):
        for m in ("delete-insert", "delete-batch", "merge"):
            for s in ("rerun", "crash-reload"):
                self.assertEqual(self.by["%s/%s" % (m, s)]["outcome"], "match", (m, s))

    def test_crash_that_did_not_happen_is_refused(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, runs=["success"] * 3) if r["variant"] == "merge/crash-reload" else r
                           for r in self.rows])


if __name__ == "__main__":
    unittest.main()
