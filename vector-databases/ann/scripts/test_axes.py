"""Проверки фикстур стенда vector-databases/ann: каждая гипотеза — своя проверка.

Без Docker: читаются только fixtures/*.jsonl. Каждую проверку роняет своя
диверсия (scripts/sabotage.py).
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.common import EXPECTED, read  # noqa: E402


class Brute(unittest.TestCase):
    def setUp(self):
        self.d = read("brute")

    def test_control_matches_ground_truth(self):
        c = self.d["control"]
        self.assertEqual(c["gt_agree"], c["queries"])

    def test_linear_in_n(self):
        self.assertTrue(self.d["scaling"]["linear"])
        ns = [p["dists_per_query"] for p in self.d["scaling"]["points"]]
        self.assertEqual(ns, [p["n"] for p in self.d["scaling"]["points"]])


class HNSW(unittest.TestCase):
    def setUp(self):
        self.d = read("hnsw")

    def test_recall_monotonic(self):
        for v in ("m16-efc200", "m8-efc200", "m32-efc200", "m16-efc100"):
            self.assertTrue(self.d[v]["recall_monotonic"], v)

    def test_reaches_target(self):
        r = self.d["m16-efc200"]
        self.assertTrue(r["reaches_target"])
        self.assertLessEqual(r["ef_reaching_target~"], EXPECTED["hnsw"]["ef_target_max"])

    def test_links_within_bound(self):
        for v in ("m16-efc200", "m8-efc200", "m32-efc200", "m16-efc100"):
            self.assertTrue(self.d[v]["link_within"], v)
            self.assertLessEqual(self.d[v]["link_bytes~"], self.d[v]["link_bound"], v)

    def test_few_unreachable(self):
        for v in ("m16-efc200", "m8-efc200", "m32-efc200", "m16-efc100"):
            self.assertTrue(self.d[v]["unreachable_ok"], v)

    def test_m_and_efc(self):
        c = self.d["compare"]
        self.assertTrue(c["recall_grows_with_m"])
        self.assertTrue(c["build_grows_with_m"])
        self.assertTrue(c["efc100_builds_faster"])
        self.assertTrue(c["efc100_not_better"])


class IVF(unittest.TestCase):
    def setUp(self):
        self.d = read("ivf")

    def test_control_scans_all(self):
        c = self.d["control"]
        self.assertEqual(c["recall_at_10"], 1)
        self.assertTrue(c["scanned_all"])

    def test_monotonic(self):
        for v in ("nlist723", "nlist2892", "skewed723"):
            self.assertTrue(self.d[v]["recall_monotonic"], v)

    def test_finer_lists_cheaper(self):
        self.assertTrue(self.d["compare"]["finer_cheaper_at_090"])

    def test_skewed_training(self):
        self.assertTrue(self.d["compare"]["skewed_lists_uneven"])
        self.assertTrue(self.d["compare"]["skewed_scans_more"])


class Quant(unittest.TestCase):
    def setUp(self):
        self.d = read("quant")

    def test_code_sizes(self):
        for v in ("sq8", "pq48", "pq96"):
            self.assertTrue(self.d[v]["code_size_ok"], v)
        self.assertEqual(self.d["pq48"]["code_bytes"], self.d["pq48"]["n"] * 48)

    def test_sq8_raw(self):
        self.assertTrue(self.d["sq8"]["raw_ok"])

    def test_pq_loses_then_rescore_recovers(self):
        self.assertTrue(self.d["pq48"]["raw_loses_much"])
        self.assertTrue(self.d["pq48"]["rescored_ok"])
        self.assertTrue(self.d["pq96"]["rescored_ok"])

    def test_finer_pq_more_accurate(self):
        self.assertTrue(self.d["compare"]["finer_more_accurate"])


class Metric(unittest.TestCase):
    def test_metrics(self):
        r = read("metric")["all"]
        self.assertTrue(r["normalized_agree"])
        self.assertTrue(r["scaled_dot_differs"])


class Crosscheck(unittest.TestCase):
    def setUp(self):
        self.d = read("crosscheck")

    def test_faiss_within_tolerance(self):
        for v in ("faiss-hnsw", "faiss-ivf", "faiss-pq", "faiss-sq8"):
            self.assertTrue(self.d[v]["within"], v)
            self.assertLessEqual(self.d[v]["max_gap~"], self.d[v]["tolerance"], v)

    def test_jvector(self):
        j = self.d["jvector"]
        self.assertTrue(j["recall_monotonic"])
        self.assertTrue(j["reaches_min"])


class Outcomes(unittest.TestCase):
    def test_all_match(self):
        for axis in ("brute", "hnsw", "ivf", "quant", "metric", "crosscheck"):
            for v, r in read(axis).items():
                self.assertEqual(r["outcome"], "match", "%s/%s: %s" % (axis, v, r["detail"]))


if __name__ == "__main__":
    unittest.main()
