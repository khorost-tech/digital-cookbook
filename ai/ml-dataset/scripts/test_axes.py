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
        a = actual("controls")
        self.assertEqual((a["users"], a["days"]), (2000, 120))
        self.assertTrue(a["deterministic"] and a["user_time_strictly_increasing"])
        self.assertEqual(a["schemes"], ["card_testing", "noise", "normal", "takeover"])
        # сдвиги заложены с нужного дня, разметка с задержкой
        self.assertTrue(a["card_testing_from_day_105_only"] and a["digital_segment_from_day_100_only"])
        self.assertEqual(a["legit_label_delay_days"], [60.0])
        self.assertTrue(a["fraud_label_delay_within_10_60"] and a["fraud_share_before_105_in_0_3_to_1_5pct"])

    def test_reference(self):
        # эталон совпал с перебором, а сломанное окно перебор ловит — сверка не «зелёная всегда»
        self.assertEqual(actual("reference"), {"sampled": 2000, "mismatches": 0, "broken_window_detected": True})

    def test_labels(self):
        a = actual("labels")
        self.assertTrue(a["prod_labeled_share_below_1pct"] and a["labeled_share_falls_after_day_60"])
        self.assertEqual(a["prod_legit_labels_known"], 0)

    def test_model(self):
        a = actual("model")
        self.assertTrue(a["deterministic"] and a["val_flag_share_about_1pct"] and a["val_auc_above_0_8"])
        self.assertEqual(len(a["features"]), 7)

if __name__ == "__main__":
    unittest.main()
