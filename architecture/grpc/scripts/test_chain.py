"""Проверки оси 4 (задача 5)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_chain", os.path.join(HERE, "scripts", "analyze-chain.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Chain(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()

    def test_controls(self):
        analyze.check(self.rows)

    def test_recover_place_decides_if_metrics_see_panic(self):
        rows = [r for r in self.rows if r["kind"] == "panic-metrics"]
        self.assertEqual(len(rows), 8)
        for r in rows:
            self.assertEqual(r["client_code"], "Internal", r["variant"])
            seen = r["metrics_by_code"].get("Internal", 0)
            self.assertEqual(seen, 1 if r["place"] == "recover-inner" else 0, r["variant"])

    def test_order_decides_what_metrics_see(self):
        for r in self.rows:
            if r["kind"] != "chain":
                continue
            if r["order"] == "auth-first":
                self.assertEqual(r["metrics_calls"], analyze.N, r["variant"])
            else:
                self.assertEqual(r["metrics_calls"], 2 * analyze.N, r["variant"])
                self.assertEqual(r["metrics_by_code"].get("Unauthenticated"), analyze.N, r["variant"])

    def test_metrics_codes_same_in_every_pair(self):
        # Метрики по кодам — то, что попадает на дашборд; имена кодов у Go и Java
        # обязаны совпадать (у Java сначала было «Ok» вместо «OK»).
        for order in ("auth-first", "metrics-first"):
            seen = {tuple(sorted(r["metrics_by_code"].items())) for r in self.rows
                    if r["kind"] == "chain" and r["order"] == order}
            self.assertEqual(len(seen), 1, order)

    def test_auth_leak_is_refused(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(next(r for r in self.rows if r["kind"] == "chain"), handler_calls=2 * analyze.N)])


if __name__ == "__main__":
    unittest.main()
