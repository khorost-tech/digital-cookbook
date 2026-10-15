"""README — паспорт evidence, и он обязан совпадать с фикстурами.

Проверка появилась не на пустом месте: README утверждал «315 замеров, 7
кругов», когда в `fixtures/endpoints.txt` лежали 495 записей и 11 кругов.
Ниже по тексту тот же README уже говорил о медиане по одиннадцати кругам —
то есть расхождение было даже не с файлом, а с самим собой.

Такая ошибка не всплывает при чтении: числа выглядят правдоподобно, и
заметить их можно, только сверив с файлом. Поэтому сверка автоматическая.
"""

import json
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
# «`fixtures/endpoints.txt`, 495 замеров, 11 кругов, ...»
CLAIM = re.compile(r"`fixtures/(\w+\.txt)`,\s*(\d+)\s*замер\w*,\s*(\d+)\s*круг\w*")


def rows(path):
    return [json.loads(line) for line in
            path.read_text(encoding="utf-8").splitlines()
            if line.strip() and not line.startswith("#")]


class TestReadmeMatchesFixtures(unittest.TestCase):
    def setUp(self):
        self.readme = (ROOT / "README.md").read_text(encoding="utf-8")
        self.claims = CLAIM.findall(self.readme)

    def test_readme_states_sizes_at_all(self):
        # Если формулировку перепишут так, что размерность исчезнет, молчать
        # об этом нельзя: проверка на нуле совпадений всегда «проходит».
        self.assertGreaterEqual(len(self.claims), 3,
                                "README перестал называть размерность наборов")

    def test_each_claim_matches_its_fixture(self):
        for name, said_rows, said_rounds in self.claims:
            path = ROOT / "fixtures" / name
            with self.subTest(fixture=name):
                self.assertTrue(path.exists(), f"{name} нет на месте")
                data = rows(path)
                self.assertEqual(len(data), int(said_rows),
                                 f"{name}: README обещает {said_rows} замеров, "
                                 f"в файле {len(data)}")
                rounds = len({r["round"] for r in data})
                self.assertEqual(rounds, int(said_rounds),
                                 f"{name}: README обещает {said_rounds} кругов, "
                                 f"в файле {rounds}")

    def test_detects_a_wrong_number(self):
        # Предохранитель заводится вместе со случаем, который его роняет:
        # подменяем число в тексте и убеждаемся, что сверка это ловит.
        name, said_rows, said_rounds = self.claims[0]
        data = rows(ROOT / "fixtures" / name)
        self.assertNotEqual(len(data), int(said_rows) + 1)
        broken = CLAIM.findall(
            f"`fixtures/{name}`, {int(said_rows) + 1} замеров, "
            f"{said_rounds} кругов, дальше текст")
        self.assertEqual(len(broken), 1)
        self.assertNotEqual(int(broken[0][1]), len(data))


if __name__ == "__main__":
    unittest.main(verbosity=2)
