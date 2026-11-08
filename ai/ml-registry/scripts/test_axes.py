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
        # положительный контроль — модель из реестра та же; отрицательный — нет алиаса, нет доверия
        self.assertTrue(a["registry_roundtrip_identical"])
        self.assertEqual((a["missing_alias"], a["log_without_trusted_types"]), ("RestException", "MlflowException"))
        self.assertEqual(a["default_artifact"], ["model.skops"])

    def test_five_parts(self):
        a = actual("five_parts")
        self.assertTrue(a["same_inputs"]["predictions_identical"])
        self.assertIn("MLmodel", a["same_inputs"]["differing_files"])   # хеш артефакта — не идентичность
        self.assertTrue(a["data_slice"]["flipped"] and a["data_slice"]["digest_differs"])
        self.assertTrue(a["feature_version"]["flipped"] and a["preprocessing"]["separate_flipped"])
        self.assertEqual(a["preprocessing"]["pipeline_flipped"], 0)

    def test_library_version(self):
        a = actual("library_version")
        self.assertEqual(sorted(a["by_version"]), ["1.6.1", "1.7.2", "1.8.0"])
        for v, r in a["by_version"].items():
            self.assertEqual(r["same_trust_list"], "error", v)
            self.assertEqual((r["outcome"], r["predictions_identical"], r["version_warning"]), ("loaded", True, True), v)
        self.assertTrue(a["artifact_pins_sklearn"])

    def test_registry(self):
        self.assertEqual(actual("registry"), {"alias_resolves": True, "stage_api_works": True, "stage_api_warning": ["FutureWarning"]})

    def test_stages(self):
        a = actual("stages")
        self.assertTrue(a["before_move_serves_v1"] and a["after_move_serves_v1"] and not a["after_move_serves_v2"])
        self.assertTrue(a["after_restart_serves_v2"] and a["version_endpoint_is_mlflow_version"])
        # сверка ловит расхождение и не «красная всегда»
        self.assertEqual(a["reconcile"], {"before_move": True, "after_move": False, "after_restart": True})
        self.assertFalse(a["old_version_alias_trace"])     # истории назначений в реестре нет

    def test_decision_policy(self):
        a = actual("decision_policy")
        self.assertTrue(a["same_model_other_threshold_flipped"] and a["threshold_from_older_window_differs"])

    def test_data_versioning(self):
        a = actual("data_versioning")
        self.assertTrue(a["versions_differ"] and a["only_labels_file_changed"])
        self.assertTrue(a["restored_bytes_identical"] and a["restored_model_identical"])
        self.assertTrue(a["dvc_stores_changed_file_whole_and_unchanged_once"] and a["git_with_dvc_under_1mb"])
        self.assertTrue(a["git_gc_smaller_than_dvc_cache"])

if __name__ == "__main__":
    unittest.main()
