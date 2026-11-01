"""Проверки осей стенда quality."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze", os.path.join(HERE, "scripts", "analyze.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


def by(axis):
    return {r["variant"]: r for r in analyze.read(axis)}


class Controls(unittest.TestCase):
    def test_controls(self):
        for a in analyze.AXES:
            analyze.check(a, analyze.read(a))

    def test_noisy_clean_batch_is_refused(self):
        rows = [dict(r, ge_caught=["rows:batch"]) if r["variant"] == "clean" else r for r in analyze.read("detect")]
        with self.assertRaises(SystemExit):
            analyze.check("detect", rows)

    def test_quarantine_mismatch_is_refused(self):
        rows = [dict(r, quarantined=0) if r["variant"] == "quarantine" else r for r in analyze.read("reaction")]
        with self.assertRaises(SystemExit):
            analyze.check("reaction", rows)


class Detect(unittest.TestCase):
    def test_complementary_coverage(self):
        d = by("detect")
        for v in ("nulls", "duplicates", "range", "enum"):
            self.assertTrue(d[v]["dbt_detected"] and d[v]["ge_detected"], v)
        for v in ("orphans", "stale"):  # только dbt
            self.assertEqual((d[v]["dbt_detected"], d[v]["ge_detected"]), (True, False), v)
        for v in ("shift", "volume"):  # только проверки партии GE
            self.assertEqual((d[v]["dbt_detected"], d[v]["ge_detected"]), (False, True), v)


class Reaction(unittest.TestCase):
    def test_modes(self):
        d = by("reaction")
        self.assertFalse(d["fail"]["mart_built"])
        self.assertEqual(d["warn"]["bad_in_mart"], d["warn"]["bad"])
        self.assertEqual((d["quarantine"]["bad_in_mart"], d["quarantine"]["good_missing"]), (0, 0))

    def test_null_flag_loses_rows(self):
        # трёхзначная логика: без coalesce строки с NULL не идут ни в витрину, ни в карантин
        d = by("reaction")
        n, f = d["split-naive"], d["split-fixed"]
        self.assertEqual(n["lost"], n["bad"])
        self.assertEqual(n["bad_quarantined"], 0)
        self.assertEqual((f["lost"], f["bad_quarantined"], f["quarantined"]), (0, f["bad"], f["bad"]))

    def test_split_conservation_is_checked(self):
        rows = [dict(r, to_quarantine=0) if r["variant"] == "split-fixed" else r for r in analyze.read("reaction")]
        with self.assertRaises(SystemExit):
            analyze.check("reaction", rows)

    def test_stop_keeps_previous_mart(self):
        # остановка поверх прошлой загрузки: витрина не пуста, а устарела
        k = by("reaction")["fail-kept"]
        self.assertEqual(k["prev_dbt_rc"], 0)
        self.assertNotEqual(k["dbt_rc"], 0)
        self.assertFalse(k["mart_rebuilt"])
        self.assertEqual(k["mart_orders"], k["prev_mart_orders"])


class Contract(unittest.TestCase):
    def test_contract_catches_schema_not_semantics(self):
        d = by("contract")
        self.assertFalse(d["numeric"]["contracted_built"])
        self.assertTrue(d["numeric"]["uncontracted_built"])
        self.assertFalse(d["dropped"]["downstream_built"])
        self.assertTrue(d["units"]["contracted_built"])
        self.assertTrue(d["units"]["ge_mean_failed"])


if __name__ == "__main__":
    unittest.main()
