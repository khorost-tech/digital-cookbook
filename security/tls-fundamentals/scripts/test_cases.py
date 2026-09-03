"""Каждый случай ломает в цепочке доверия ровно одну вещь.

Если случай ломает две, исход перестаёт что-либо доказывать: отказ
клиента будет объясняться двумя причинами сразу, и различить их нечем.
Ровно на этом уже спотыкались в стенде форматов сериализации, где
матрица несколько кругов подряд меряла не то, что заявляла.

Отдельно проверяется контрольный случай: цепочка целая, ломать в ней
нечего. Без него отказ на сломанном случае ничего не значит — может,
у нас просто не работает соединение.
"""

import io
import json
import os
import unittest

ROOT = "pki/"

CASES = ["valid", "no_intermediate", "expired", "not_yet_valid", "wrong_san", "self_signed", "other_ca", "revoked", "deep_valid", "deep_wrong_order"]


def _read_text(path):
    with io.open(path, encoding="utf-8") as handle:
        return handle.read()


def load():
    return json.loads(_read_text(ROOT + "cases.json"))


class CaseSet(unittest.TestCase):
    def test_all_nine_cases_present(self):
        self.assertEqual([c["case"] for c in load()], CASES)

    def test_control_case_breaks_nothing(self):
        control = [c for c in load() if c["case"] == "valid"][0]
        self.assertEqual(control["breaks"], "")

    def test_every_broken_case_names_its_break(self):
        for case in load():
            if not case["control"]:
                continue
            self.assertTrue(case["breaks"].strip(),
                            "%s не называет, что сломано" % case["case"])

    def test_every_broken_case_names_its_control(self):
        """Сломанный случай обязан назвать, с чем его сравнивают.

        Контролей два. У глубокой ветки другая длина пути, и сравнивать
        её с основным контролем значило бы сравнивать сразу по двум
        признакам — ровно та ошибка, из-за которой прежний случай о
        порядке мерил не то, что заявлял.
        """
        controls = {c["case"] for c in load() if not c["control"]}
        self.assertEqual(controls, {"valid", "deep_valid"})
        for case in load():
            if not case["control"]:
                continue
            self.assertIn(case["control"], controls,
                          "%s ссылается на несуществующий контроль %r"
                          % (case["case"], case["control"]))

    def test_control_breaks_nothing(self):
        for case in load():
            if case["control"]:
                continue
            self.assertEqual(case["breaks"], "",
                             "%s объявлен контролем, но что-то ломает"
                             % case["case"])

    def test_every_case_has_key_and_certificate(self):
        for case in load():
            for field in ("server_cert", "server_key"):
                self.assertTrue(case.get(field),
                                "%s: не задано поле %s" % (case["case"], field))
            self.assertIsInstance(case.get("chain"), list,
                                  "%s: chain должен быть списком" % case["case"])

    def test_files_exist_after_generation(self):
        for case in load():
            names = [case["server_cert"], case["server_key"]] + case["chain"]
            for name in names:
                self.assertTrue(os.path.exists(ROOT + name),
                                ROOT + name + " не выпущен — "
                                "запусти pki/make-certs.sh")


if __name__ == "__main__":
    unittest.main()
