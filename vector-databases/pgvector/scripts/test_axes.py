"""Проверки фикстур стенда vector-databases/pgvector: каждая гипотеза — своя проверка.

Без Docker: читаются только fixtures/*.jsonl. Каждую проверку роняет своя
диверсия (scripts/sabotage.py).
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.common import read  # noqa: E402


def pick(row, b, mode):
    return next(p for p in row["points"] if p["bucket_lt"] == b and p["mode"] == mode)


class Exact(unittest.TestCase):
    def test_control(self):
        c = read("exact")["control"]
        self.assertTrue(c["only_near_ties"])
        self.assertEqual(c["gt_agree"] + c["near_tie"], c["queries"])


class HNSW(unittest.TestCase):
    def setUp(self):
        self.d = read("hnsw")

    def test_build_fits_memory(self):
        self.assertFalse(self.d["mwm2gb"]["spilled"])

    def test_recall(self):
        r = self.d["mwm2gb"]
        self.assertTrue(r["recall_monotonic"])
        self.assertTrue(r["near_ann"])
        self.assertTrue(r["uses_index"])

    def test_index_holds_vectors(self):
        self.assertTrue(self.d["mwm2gb"]["index_holds_vectors"])

    def test_small_memory_spills(self):
        self.assertTrue(self.d["mwm64mb"]["spilled"])
        self.assertTrue(self.d["compare"]["spill_slower"])
        self.assertTrue(self.d["compare"]["same_recall"])


class IVFFlat(unittest.TestCase):
    def test_ivfflat(self):
        r = read("ivfflat")["lists723"]
        self.assertTrue(r["recall_monotonic"])
        self.assertTrue(r["near_ann"])


class Ops(unittest.TestCase):
    def test_only_ip_uses_index(self):
        r = read("ops")["all"]
        self.assertTrue(r["only_ip_uses_index"])
        self.assertTrue(r["ops"]["ip"]["uses_index"])
        self.assertTrue(r["others_10x_slower"])


class Filter(unittest.TestCase):
    def setUp(self):
        self.d = read("filter")

    def test_post_filter_shortens(self):
        r = self.d["hnsw"]
        self.assertTrue(r["off_full_at_50"])
        self.assertTrue(r["off_short_when_selective"])
        self.assertLess(pick(r, 10, "off")["rows_per_query~"], 10)

    def test_iterative_fills(self):
        self.assertTrue(self.d["hnsw"]["iterative_full"])
        self.assertTrue(self.d["hnsw"]["always_hnsw"])

    def test_generic_plan_trap(self):
        r = self.d["hnsw-btree"]
        self.assertTrue(r["custom_plan_btree"])
        self.assertTrue(r["generic_plan_hnsw"])
        self.assertTrue(r["generic_plan_wins"])

    def test_btree_for_tiny_filter(self):
        r = self.d["hnsw-btree-custom"]
        self.assertTrue(r["btree_at_01pct"])
        self.assertTrue(r["exact_at_01pct"])
        self.assertTrue(r["hnsw_at_50pct"])


class Quant(unittest.TestCase):
    def setUp(self):
        self.d = read("quant")

    def test_halfvec(self):
        r = self.d["halfvec"]
        self.assertTrue(r["smaller_15x"])
        self.assertTrue(r["same_recall"])

    def test_sign_code_loses(self):
        h = self.d["hamming"]
        self.assertTrue(h["sign_code_loses"])
        self.assertGreater(h["sign_skewed_dims"], 0)
        r = self.d["binary"]
        self.assertTrue(r["smaller_3x"])
        self.assertTrue(r["sign_code_loses"])
        self.assertTrue(r["graph_not_better_than_exact"])

    def test_centering_fixes(self):
        h = self.d["hamming"]
        self.assertTrue(h["centering_fixes"])
        self.assertEqual(h["centered_skewed_dims"], 0)
        r = self.d["binary-centered"]
        self.assertTrue(r["rescored_1000_ok"])
        self.assertTrue(r["beats_plain"])
        self.assertTrue(r["same_size"])


class Java(unittest.TestCase):
    def test_identical(self):
        r = read("java")["jdbc"]
        self.assertTrue(r["identical"])
        self.assertEqual(r["same_order"], r["queries"])


class Outcomes(unittest.TestCase):
    def test_all_match(self):
        for axis in ("exact", "hnsw", "ivfflat", "ops", "filter", "quant", "java"):
            for v, r in read(axis).items():
                self.assertEqual(r["outcome"], "match", "%s/%s: %s" % (axis, v, r["detail"]))


if __name__ == "__main__":
    unittest.main()
