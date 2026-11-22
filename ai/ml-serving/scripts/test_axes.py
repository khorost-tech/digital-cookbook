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
        self.assertTrue(a["service_matches_in_process"])
        self.assertEqual((a["wrong_width_status"], a["missing_field_status"]), (500, 400))

    def test_service_vs_library(self):
        self.assertTrue(actual("service_vs_library")["http_at_least_2x_in_process"])

    def test_cold_start(self):
        a = actual("cold_start")
        self.assertTrue(a["requests_before_ready_refused"] and a["rss_over_20x_artifact"])

    def test_batching(self):
        a = actual("batching")
        self.assertTrue(a["generator_ceiling_over_2x_best"])        # замер не упёрся в генератор
        self.assertTrue(a["c64_throughput_gain_over_1_5"] and a["c64_mean_batch_over_4"] and a["c1_no_gain"])
        self.assertTrue(a["c64_batching_rejects_some"] and a["without_batching_rejects_none"])
        self.assertIn("slo_100ms", a)

    def test_workers(self):
        a = actual("workers")
        self.assertTrue(a["generator_ceiling_over_2x_best"] and a["four_workers_over_2x"])

    def test_degradation(self):
        a = actual("degradation")
        # ноль вместо оценки: ни одной ошибки и ни одного флага — отказ выглядит как успех
        self.assertEqual(a["silent_zero"], {"visible_errors": 0, "flagged": 0})
        self.assertTrue(a["heuristic"]["catches_some"] and a["explicit_error"]["all_visible"])
        self.assertTrue(a["silent_mean_score_drop_over_90pct"])

if __name__ == "__main__":
    unittest.main()
