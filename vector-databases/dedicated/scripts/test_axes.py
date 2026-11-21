"""Проверки фикстур стенда vector-databases/dedicated: каждая гипотеза — своя проверка.

Без Docker: читаются только fixtures/*.jsonl. Каждую проверку роняет своя
диверсия (scripts/sabotage.py).
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.common import read  # noqa: E402

ENGINES = ("qdrant", "milvus", "weaviate")


class Control(unittest.TestCase):
    def test_qdrant_exact(self):
        r = read("qdrant")["exact"]
        self.assertTrue(r["only_near_ties"])
        self.assertEqual(r["gt_agree"] + r["near_tie"], r["queries"])


class Load(unittest.TestCase):
    def test_all_rows(self):
        for e in ENGINES:
            self.assertEqual(read(e)["load"]["rows"], 522931, e)


class HNSW(unittest.TestCase):
    def test_monotonic(self):
        for e in ENGINES:
            self.assertTrue(read(e)["hnsw"]["recall_monotonic"], e)

    def test_near_pgvector(self):
        for e in ENGINES:
            self.assertTrue(read(e)["hnsw"]["near_pgvector"], e)


class Filter(unittest.TestCase):
    def test_full_and_accurate(self):
        for e in ENGINES:
            r = read(e)["filter"]
            self.assertTrue(r["always_full"], e)
            self.assertTrue(r["recall_min_ok"], e)

    def test_weaviate_acorn_loses_at_10(self):
        self.assertTrue(read("weaviate")["filter"]["acorn_loses_at_10"])


class Java(unittest.TestCase):
    def test_identical(self):
        for e in ENGINES:
            r = read(e)["java"]
            self.assertTrue(r["identical"], e)
            self.assertEqual(r["same_order"], r["queries"], e)


class Quant(unittest.TestCase):
    def test_qdrant_int8(self):
        r = read("qdrant")["quant"]
        self.assertTrue(r["codes_within_tolerance"])
        self.assertTrue(r["rescore_within_001"])


class Fresh(unittest.TestCase):
    def test_qdrant_weaviate_see_at_once(self):
        for e in ("qdrant", "weaviate"):
            self.assertTrue(read(e)["fresh"]["all_at_once"], e)

    def test_qdrant_unindexed_slower(self):
        self.assertTrue(read("qdrant")["fresh"]["unindexed_slower"])

    def test_milvus_bounded_and_strong(self):
        d = read("milvus")
        self.assertTrue(d["fresh"]["all_at_once"])
        self.assertTrue(d["fresh-strong"]["all_at_once"])

    def test_weaviate_sweeping(self):
        r = read("weaviate")["filter-sweeping"]
        self.assertTrue(r["sweeping_better_at_10"])
        self.assertTrue(r["always_full"])


class Outcomes(unittest.TestCase):
    def test_all_match(self):
        for e in ENGINES:
            for v, r in read(e).items():
                self.assertEqual(r["outcome"], "match", "%s/%s: %s" % (e, v, r["detail"]))


if __name__ == "__main__":
    unittest.main()
