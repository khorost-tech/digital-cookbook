"""README — паспорт evidence, и он обязан совпадать с фикстурами.

Проверка перенесена из стенда python/web, где она появилась после того, как
README утверждал «315 замеров, 7 кругов» при 495 записях и 11 кругах в
файле. Такая ошибка не всплывает при чтении: числа выглядят правдоподобно,
и заметить их можно, только сверив с файлом.

Здесь она строже: сверяются все четыре фикстуры, а у сценария Alembic
дополнительно — число разобранных изменений схемы. Формулировки в README
разные («замеров», «записи»), поэтому регулярка принимает оба варианта, но
паспорт требуется у КАЖДОЙ фикстуры: пропущенный паспорт означал бы, что
проверка молчит именно там, где ошиблись.
"""

import json
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CLAIM = re.compile(
    r"`fixtures/(\w[\w-]*\.txt)`,\s*(\d+)\s*(?:замер\w*|запис\w+)"
    r"(?:,\s*(\d+)\s*круг\w*)?")
FIXTURES = ("layers.txt", "nplus1.txt", "lazy-async.txt", "alembic.txt")


def rows(path):
    return [json.loads(line) for line in
            path.read_text(encoding="utf-8").splitlines()
            if line.strip() and not line.startswith("#")]


class TestReadmeMatchesFixtures(unittest.TestCase):
    def setUp(self):
        self.readme = (ROOT / "README.md").read_text(encoding="utf-8")
        self.claims = {m[0]: m for m in CLAIM.findall(self.readme)}

    def test_every_fixture_has_a_passport(self):
        # Проверка на нуле совпадений всегда «проходит», поэтому отсутствие
        # паспорта — отдельный отказ, а не молчание.
        for name in FIXTURES:
            with self.subTest(fixture=name):
                self.assertIn(name, self.claims,
                              f"README не называет размерность {name}")

    def test_row_counts_match(self):
        for name, (_, said_rows, _said_rounds) in self.claims.items():
            path = ROOT / "fixtures" / name
            with self.subTest(fixture=name):
                self.assertTrue(path.exists(), f"{name} нет на месте")
                data = rows(path)
                self.assertEqual(len(data), int(said_rows),
                                 f"{name}: README обещает {said_rows}, "
                                 f"в файле {len(data)}")

    def test_round_counts_match_where_stated(self):
        for name, (_, _said_rows, said_rounds) in self.claims.items():
            if not said_rounds:
                continue
            data = rows(ROOT / "fixtures" / name)
            with self.subTest(fixture=name):
                got = len({r["round"] for r in data})
                self.assertEqual(got, int(said_rounds),
                                 f"{name}: README обещает {said_rounds} "
                                 f"кругов, в файле {got}")

    def test_alembic_case_count_matches(self):
        # У сценария Alembic вместо кругов — изменения схемы, и их число
        # тоже названо в README.
        said = re.search(r"(\d+)\s*изменени\w*\s*схемы", self.readme)
        self.assertIsNotNone(said, "README не называет число изменений схемы")
        data = rows(ROOT / "fixtures" / "alembic.txt")
        cases = {r["case"] for r in data}
        self.assertEqual(len(cases), int(said.group(1)),
                         f"README обещает {said.group(1)} изменений, "
                         f"в фикстуре {len(cases)}")

    def test_detects_a_wrong_number(self):
        # Предохранитель заводится вместе со случаем, который его роняет.
        name = "layers.txt"
        real = len(rows(ROOT / "fixtures" / name))
        broken = CLAIM.findall(f"`fixtures/{name}`, {real + 1} замеров, "
                               "11 кругов, дальше текст")
        self.assertEqual(len(broken), 1)
        self.assertNotEqual(int(broken[0][1]), real)


if __name__ == "__main__":
    unittest.main(verbosity=2)
