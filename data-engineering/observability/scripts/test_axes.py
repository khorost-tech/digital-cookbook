"""Проверки осей стенда observability."""

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

    def test_lineage_mismatch_is_refused(self):
        rows = [dict(r, datasets=r["datasets"][1:]) if r["variant"] == "tables" else r for r in analyze.read("lineage")]
        with self.assertRaises(SystemExit):
            analyze.check("lineage", rows)

    def test_noisy_history_detector_is_refused(self):
        rows = [dict(r, fp_days=["2026-09-03"]) if r["variant"] == "volume-history" else r
                for r in analyze.read("monitors")]
        with self.assertRaises(SystemExit):
            analyze.check("monitors", rows)


class Lineage(unittest.TestCase):
    def test_table_and_column_blast_radius(self):
        d = by("lineage")
        self.assertEqual(len(d["tables"]["datasets"]), 6)
        self.assertIn("marts.status_counts", d["tables"]["datasets"])
        self.assertNotIn("marts.status_counts", d["columns"]["datasets"])
        self.assertEqual(len(d["columns"]["datasets"]), 5)

    def test_indirect_dependencies_invisible(self):
        # status в WHERE денежных витрин влияет на сумму, но в фасетах его нет
        c = by("lineage")["columns-status"]
        self.assertEqual(sorted(c["missed"]), ["marts.customer_ltv", "marts.daily_revenue", "marts.region_revenue"])
        self.assertIn("marts.status_counts", c["datasets"])

    def test_column_api_fails(self):
        self.assertEqual(by("lineage")["column-api"]["http_status"], 500)


class Monitors(unittest.TestCase):
    def test_static_thresholds(self):
        d = by("monitors")
        self.assertEqual((d["volume-static"]["tp"], d["volume-static"]["fp"]), (2, 4))
        self.assertEqual((d["distribution-static"]["tp"], d["distribution-static"]["fn"]), (0, 1))

    def test_no_constant_threshold_separates(self):
        # даже порог, подобранный задним числом, тревожит в выходной: провал
        # будня внутри нормы выходных
        b = by("monitors")["volume-static-best"]
        self.assertEqual(b["fn"], 0)
        self.assertGreaterEqual(b["fp"], 1)
        self.assertGreater(max(b["rows_truth"]), min(b["rows_fp"]))

    def test_history_thresholds(self):
        d = by("monitors")
        for v in ("volume-history", "freshness-static", "freshness-history", "schema-diff", "distribution-history"):
            self.assertEqual((d[v]["fp"], d[v]["fn"]), (0, 0), v)
            self.assertEqual(d[v]["outcome"], "match", v)

    def test_drift_becomes_normal(self):
        # затянувшийся сбой входит в историю: σ растёт, и детектор замолкает;
        # история без дней с тревогой ловит каждый день сбоя
        d = by("monitors")
        h, c = d["drift/distribution-history"], d["drift/distribution-history-clean"]
        self.assertGreater(h["fn"], len(h["truth"]) // 2)
        self.assertEqual((c["tp"], c["fn"], c["fp"]), (len(c["truth"]), 0, 0))
        self.assertEqual(d["distribution-history-clean"]["outcome"], "match")

    def test_late_load_raises_two_families(self):
        d = by("monitors")
        self.assertIn("2026-09-08", d["volume-history"]["alerts"])
        self.assertIn("2026-09-08", d["freshness-static"]["alerts"])


if __name__ == "__main__":
    unittest.main()
