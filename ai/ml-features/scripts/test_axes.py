import json, unittest
from pathlib import Path
from contract import Contract
ROOT = Path(__file__).resolve().parents[1]
CONTRACT = Contract(outcomes=("match", "mismatch"), success="match", fields=("kind", "variant", "probe", "outcome", "detail", "cmd", "actual"))
def rows():
    return {r["variant"]: r for r in CONTRACT.read_fixture(ROOT/"fixtures/checks.jsonl")}
def actual(key):
    return rows()[key]["actual"]
class Checks(unittest.TestCase):
    def test_contract_complete(self):
        r = rows()
        hyp = json.loads((ROOT/"expected.json").read_text())
        expected = dict(hyp["checks"])  # набор ключей — исходные проверки и добавленные пересмотрами revision*
        for k in sorted(k for k in hyp if k.startswith("revision")):
            expected.update(hyp[k].get("checks", {}))
        self.assertEqual(sorted(r), sorted(expected))
        for k, v in r.items():
            self.assertEqual(v["cmd"], "python3 scripts/run-checks.py " + k)
            self.assertEqual(v["outcome"], "match")

    def test_controls(self):
        # батч совпал с эталоном, а сравнение не «зелёное всегда» — сдвиг на единицу ловит
        self.assertEqual(actual("controls"), {"batch_vs_reference_mismatches": 0, "comparator_detects_injection": True, "postgres_major": 18})

    def test_skew(self):
        a = actual("skew")
        self.assertEqual(a["errors"], 0)            # отказ выглядит как успех: ни одной ошибки
        v = a["variants"]
        self.assertTrue(v["as_written"]["count_always_off_by_one"])
        self.assertIn("tx_count_24h", v["as_written"]["differing"])
        self.assertNotIn("tx_count_24h", v["fix_current"]["differing"])
        self.assertTrue(v["as_written"]["flipped"] and v["fix_current"]["flipped"])
        # положительный контроль: все три расхождения устранены — расхождений и переворотов нет
        self.assertEqual(v["all_fixed"], {"differing": [], "flipped": False})

    def test_feature_store(self):
        a = actual("feature_store")
        self.assertEqual(a["online_controls"], {"known_user_found": True, "unknown_user_null": True})
        self.assertTrue(a["inclusive_boundary"])
        self.assertTrue(a["own_row"]["tx_count_24h_differ_over_50pct"] and a["own_row"]["flipped"])
        s = a["shifted"]
        self.assertEqual((s["mismatch_outside_ttl_gap"], s["flipped_outside_ttl_gap"]), (0, 0))
        self.assertTrue(s["ttl_gap_cases_present"] and s["ttl_gap_cases_all_differ"])
        self.assertTrue(a["consistent_but_not_reference"])

    def test_materialization(self):
        self.assertEqual(actual("materialization"), {"stale_15m": True, "monotonic": True, "flipped_24h": True})

    def test_point_in_time(self):
        a = actual("point_in_time")
        self.assertTrue(a["event_val_above_label_val"] and a["event_prod_below_its_val"])
        self.assertTrue(a["label_val_prod_within_0_03"])
        self.assertEqual(a["feast_label_pit_vs_reference_mismatches"], 0)
        # утечки две: собственная метка и задержанное знание о прошлых платежах
        self.assertTrue(a["by_event_includes_own_label"])
        # «строго по предыдущим» — тот самый признак на всех строках, а не только на мошеннических
        self.assertEqual(a["prior_vs_reference_mismatches_all_rows"], 0)
        # без собственной метки задержанное знание валидацию не завысило — провал даёт собственная метка
        self.assertFalse(a["prior_val_above_label_val"])

    def test_one_path(self):
        self.assertEqual(actual("one_path"), {"mismatch_vs_batch": 0, "mismatch_train_vs_serve": 0})

if __name__ == "__main__":
    unittest.main()
