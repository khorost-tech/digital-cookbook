import json, unittest
from pathlib import Path
from contract import Contract
ROOT = Path(__file__).resolve().parents[1]
CONTRACT = Contract(outcomes=("match", "mismatch"), success="match", fields=("kind", "variant", "probe", "outcome", "detail", "cmd", "actual"))
APPS = {"go-nethttp", "go-gin", "rust-axum", "rust-actix", "py-flask", "py-fastapi", "node-express", "node-nestjs", "java-javalin",
        "java-spring", "cs-minimal", "cs-mvc"}
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

    def test_control(self):
        a = actual("control")                                                   # все двенадцать приложений работают
        self.assertEqual(set(a["valid_post"].values()), {201})
        self.assertEqual(set(a["valid_get"].values()), {200})
        self.assertEqual(set(a["valid_post_echoes_name"].values()), {True})       # и отвечают по контракту
        self.assertEqual(set(a["valid_get_returns_id"].values()), {True})
        self.assertTrue(a["javalin_typed_valid_get"])

    def test_routing(self):
        a = actual("routing")
        self.assertEqual(set(a["unknown_path"].values()), {404})                 # контроль: неизвестный путь
        self.assertGreater(len(set(a["wrong_method"].values())), 1)              # неверный метод — по-разному
        self.assertGreater(len(set(a["trailing_slash"].values())), 2)
        self.assertEqual(set(a["wrong_method"]), APPS)
        self.assertEqual(a["javalin_typed_bad_param"], 400)                      # штатный валидатор Javalin — 400, не 500
        for app, kept in a["post_redirect_body_kept"].items():                   # повтор POST проверяется только у перенаправивших
            self.assertEqual(kept is None, not 300 <= a["post_trailing_slash"][app] < 400)

    def test_validation(self):
        a = actual("validation")
        self.assertIn(201, a["missing_name"].values())                           # кто-то принял тело без обязательного поля
        self.assertGreater(len(set(a["missing_name"].values())), 2)
        self.assertIn(201, a["trailing_garbage"].values())                       # и тело с мусором после JSON

    def test_limits(self):
        by = actual("limits")["by_size"]
        self.assertGreater(len(by), 5)
        self.assertIn(201, by[max(by, key=int)].values())                        # самое большое тело кто-то принял
        self.assertIn(201, by[min(by, key=int)].values())                        # самое маленькое — контроль: принимает каждый
        self.assertEqual(set(by[min(by, key=int)].values()), {201})
        self.assertIn(413, by[max(by, key=int)].values())

    def test_errors(self):
        a = actual("errors")
        self.assertTrue(a["detector_controls_ok"])
        self.assertIn(500, a["boom_status"].values())
        for app, code in a["boom_status"].items():                               # нет ответа — значит, известен класс обрыва
            self.assertEqual(a["boom_transport"][app] == "ok", code != 0)
        self.assertEqual(set(a["boom_leaks_message_or_stack"]), APPS)
        self.assertEqual(set(a["server_discloses_software"]), APPS)

    def test_footprint(self):
        a = actual("footprint")
        self.assertTrue(a["go_pair_within_2x"])
        self.assertTrue(a["java_full_starts_over_1_5x_slower"])

if __name__ == "__main__":
    unittest.main()
