"""README против фикстур: все числа паспорта пересчитываются заново.

ЗАЧЕМ. README — паспорт стенда, и читают его чаще, чем фикстуры. Числа в
нём набраны руками, а фикстуры переснимаются: любой перезамер делает
паспорт устаревшим МОЛЧА. Устаревшая таблица выглядит ровно как свежая.

Проверяется не «похоже», а совпадение с точностью, с которой число
напечатано. Если медиана уехала на сотую, тест обязан упасть: сотая — это
и есть тот разряд, который автор напечатал, значит он за него отвечает.
"""

import collections
import importlib.util
import json
import re
import statistics as st
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
README = ROOT / "README.md"


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def fixture(name):
    path = ROOT / "fixtures" / name
    return [json.loads(line) for line in
            path.read_text(encoding="utf-8").splitlines()
            if line.strip() and not line.startswith("#")]


def block_after(heading):
    """Первый блок кода после заголовка."""
    text = README.read_text(encoding="utf-8")
    start = text.index(heading)
    fence = text.index("```", start)
    end = text.index("```", fence + 3)
    return text[fence + 3:end].strip().splitlines()


MIB = 1024 * 1024


class InstallTable(unittest.TestCase):

    def setUp(self):
        rows = [r for r in fixture("install.txt") if "kind" not in r]
        self.cells = collections.defaultdict(list)
        for r in rows:
            self.cells[(r["tool"], r["state"])].append(r["seconds"])
        self.rows = rows

    def test_medians_match(self):
        lines = block_after("### Установка: время, с")
        checked = 0
        for line in lines[1:]:
            parts = line.split()
            if len(parts) != 4:
                continue
            tool, values = parts[0], [float(x) for x in parts[1:]]
            for state, printed in zip(("cold", "warm", "noop"), values):
                actual = st.median(self.cells[(tool, state)])
                # Сверяются НАПЕЧАТАННЫЕ значения, а не числа с плавающей
                # точкой. Медиана poetry/noop равна 0,805; разбор печатает
                # её как 0.81, и README обязан повторять именно это.
                # Сравнение с допуском спотыкалось ровно на половинках.
                self.assertEqual(
                    f"{printed:.2f}", f"{actual:.2f}",
                    f"{tool}/{state}: в README {printed}, в фикстуре "
                    f"{actual:.2f}")
                checked += 1
        self.assertEqual(checked, 9)

    def test_measurement_count_matches(self):
        text = README.read_text(encoding="utf-8")
        # Число согласуется с русской грамматикой (162 измерения,
        # 99 измерений), поэтому окончание в шаблоне не жёсткое.
        printed = int(re.search(r"fixtures/install\.txt\s+(\d+) измерени[йя]",
                                text).group(1))
        self.assertEqual(printed, len(self.rows))

    def test_rounds_count_matches(self):
        text = README.read_text(encoding="utf-8")
        printed = int(re.search(r"медиана по (\d+) кругам", text).group(1))
        self.assertEqual(printed, len({r["round"] for r in self.rows}))


class PypiArm(unittest.TestCase):

    def setUp(self):
        self.rows = [r for r in fixture("install.txt")
                     if r.get("kind") == "pypi"]

    def test_numbers_match(self):
        lines = block_after("#### Отдельное плечо: против настоящего PyPI")
        measured = collections.defaultdict(list)
        for r in self.rows:
            if r.get("measured"):
                measured[r["tool"]].append(r["seconds"])
        checked = 0
        for line in lines[1:]:
            tool = line.split()[0]
            if tool not in measured:
                continue
            numbers = [float(x) for x in re.findall(r"\d+\.\d+", line)]
            # Последнее число в строке — медиана локального прогона, она
            # проверяется таблицей выше. Остальные — повторы сетевого плеча.
            printed = sorted(numbers[:-1])
            actual = sorted(measured[tool])
            self.assertEqual(len(printed), len(actual), tool)
            for p, a in zip(printed, actual):
                self.assertEqual(f"{p:.1f}", f"{a:.1f}",
                                 f"{tool}: README {p}, фикстура {a}")
                checked += 1
        self.assertGreaterEqual(checked, 4)

    def test_unmeasured_tool_is_named(self):
        """Инструмент, который не измерить, обязан быть НАЗВАН.

        Пропуск без строки читается как «не успели», а не как «нельзя», и
        это разные утверждения.
        """
        skipped = [r["tool"] for r in self.rows if not r.get("measured")]
        self.assertTrue(skipped)
        text = README.read_text(encoding="utf-8")
        for tool in skipped:
            self.assertIn(tool, text)
            self.assertIn("не измеряется", text)


class ImagesTable(unittest.TestCase):

    def setUp(self):
        self.runs = [r for r in fixture("images.txt")
                     if r.get("kind") == "run"]

    def test_sizes_and_boot_match(self):
        lines = block_after("### Образы")
        boots = collections.defaultdict(list)
        statics = {}
        for r in self.runs:
            boots[r["strategy"]].append(r["boot_seconds"])
            statics[r["strategy"]] = r
        checked = 0
        for line in lines[1:]:
            parts = line.split()
            if len(parts) != 6:
                continue
            name = parts[0]
            self.assertIn(name, statics, f"в фикстуре нет стратегии {name}")
            m = statics[name]
            expected = [
                m["compressed_bytes"] / MIB,
                m["unpacked_bytes"] / MIB,
                m["fattest_layer_bytes"] / MIB,
            ]
            for printed, actual in zip([float(x) for x in parts[1:4]],
                                       expected):
                self.assertAlmostEqual(printed, actual, places=1, msg=name)
            self.assertEqual(int(parts[4]), m["layers"], name)
            self.assertAlmostEqual(float(parts[5]),
                                   st.median(boots[name]), places=2, msg=name)
            checked += 1
        self.assertEqual(checked, len(statics))

    def test_bytecode_claim_matches_probe(self):
        """Утверждение про 252 файла обязано совпадать с пробой."""
        env = next(r for r in fixture("images.txt") if r.get("kind") == "env")
        added = env["bytecode"]["uv"]["pyc_new"]
        text = README.read_text(encoding="utf-8")
        self.assertIn(f"дописывает {added} файла", text)


class TestsMatrix(unittest.TestCase):

    def setUp(self):
        self.data = fixture("tests.txt")
        self.matrix = {(r["defect"], r["suite"]): r
                       for r in self.data if r.get("kind") == "matrix"}

    def test_matrix_matches(self):
        lines = block_after("### Тесты: кто ловит подсунутый дефект")
        suites = ("mocks", "sqlite", "postgres", "hypothesis")
        checked = 0
        for line in lines[1:]:
            parts = line.split()
            defect = parts[0]
            if (defect, "mocks") not in self.matrix:
                continue
            for suite, word in zip(suites, parts[1:5]):
                row = self.matrix[(defect, suite)]
                if defect == "none":
                    self.assertEqual(word, "зел.", f"{defect}/{suite}")
                    self.assertEqual(row["failed"], 0)
                else:
                    expected = "поймал" if row["caught"] else "нет"
                    self.assertEqual(word, expected, f"{defect}/{suite}")
                checked += 1
        self.assertEqual(checked, len(self.matrix))

    def test_examples_table_matches(self):
        lines = block_after("### Property-based: примеров до ошибки")
        by = {(r["defect"], r["shrink"]): r
              for r in self.data if r.get("kind") == "hypothesis"}
        checked = 0
        for line in lines[1:]:
            parts = line.split()
            if len(parts) != 4:
                continue
            defect = parts[0]
            if (defect, False) not in by:
                continue
            self.assertEqual(int(parts[1]), by[(defect, False)]["examples"],
                             defect)
            self.assertEqual(int(parts[2]), by[(defect, True)]["examples"],
                             defect)
            expected = "да" if by[(defect, False)]["caught"] else "нет"
            self.assertEqual(parts[3], expected, defect)
            checked += 1
        self.assertEqual(checked, len(by) // 2)

    def test_cost_table_matches(self):
        lines = block_after("### Цена прогона")
        costs = collections.defaultdict(list)
        starts = {}
        for r in self.data:
            if r.get("kind") != "cost":
                continue
            key = (r["suite"], r["scope"])
            costs[key].append(r["seconds"])
            starts[key] = r["container_starts"]
        titles = {"моки": "mocks", "фейк": "sqlite", "база": "postgres"}
        checked = 0
        for line in lines[1:]:
            parts = line.split()
            if len(parts) < 4:
                continue
            suite = titles.get(parts[0])
            if suite is None:
                continue
            # Область ищется по всей строке: у плеча с базой название
            # состоит из двух слов, и позиционный разбор ловил «(Test...)».
            scope = next((p for p in parts if p in ("function", "session")),
                         "—")
            key = (suite, scope)
            self.assertIn(key, costs)
            self.assertEqual(f"{float(parts[-2]):.1f}",
                             f"{st.median(costs[key]):.1f}", str(key))
            self.assertEqual(int(parts[-1]), starts[key], str(key))
            checked += 1
        self.assertEqual(checked, len(costs))

    def test_counterexample_is_quoted_verbatim(self):
        """Контрпример в README обязан совпадать с записанным в фикстуре.

        Пересказать его своими словами — значит предъявить читателю не то,
        что напечатал hypothesis. Сверяется содержательная часть: данные и
        поисковый запрос; строка с адресом базы содержит случайный порт и
        в README сокращена многоточием.
        """
        text = README.read_text(encoding="utf-8")
        row = next(r for r in self.data
                   if r.get("kind") == "hypothesis" and r["shrink"]
                   and r["defect"] == "like-case")
        payload = next(line.strip() for line in
                       row["counterexample"].splitlines()
                       if line.strip().startswith("data="))
        self.assertIn(payload, text)


class Versions(unittest.TestCase):

    def test_versions_are_present(self):
        text = README.read_text(encoding="utf-8")
        for version in ("Python 3.14.7", "uv 0.12.5", "Poetry 2.3.1",
                        "pip 26.0", "PostgreSQL 18.4", "SQLAlchemy 2.0.52",
                        "pytest 9.1.1", "hypothesis 6.165.10",
                        "testcontainers 4.15.0", "Docker 29.7.2"):
            self.assertIn(version, text)

    def test_docker_version_matches_fixture(self):
        env = next(r for r in fixture("images.txt") if r.get("kind") == "env")
        text = README.read_text(encoding="utf-8")
        self.assertIn(f"Docker {env['store']['server']}", text)


if __name__ == "__main__":
    unittest.main(verbosity=2)
