"""README против фикстур: паспорт стенда пересчитывается заново.

README читают чаще, чем фикстуры, а числа в нём набраны руками. Любой
перезамер делает паспорт устаревшим МОЛЧА: устаревшая таблица выглядит
ровно как свежая.

В этом стенде почти всё счётное, поэтому и сверять просто: слова «поймал»
и «нет» в таблицах обязаны совпадать с кодами возврата в фикстурах, а
версии и хеш набора — с записанными условиями.
"""

import importlib.util
import json
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
README = ROOT / "README.md"


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts"
                                                  / path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def fixture(name):
    path = ROOT / "fixtures" / name
    return [json.loads(line) for line in
            path.read_text(encoding="utf-8").splitlines()
            if line.strip() and not line.startswith("#")]


def block_after(heading):
    text = README.read_text(encoding="utf-8")
    start = text.index(heading)
    fence = text.index("```", start)
    end = text.index("```", fence + 3)
    return text[fence + 3:end].strip().splitlines()


class TypingTable(unittest.TestCase):

    def setUp(self):
        path = ROOT / "fixtures" / "typing.txt"
        if not path.exists():
            self.skipTest("фикстуры нет")
        self.rows = {r["defect"]: r for r in fixture("typing.txt")}
        self.an = load("analyze_typing", "analyze-typing.py")

    def test_matrix_matches(self):
        lines = block_after("## Сценарий 1: кто ловит подсунутый дефект")
        checked = 0
        for line in lines[1:]:
            parts = line.split()
            defect = parts[0]
            if defect not in self.rows:
                continue
            words = parts[1:1 + len(self.an.ARMS)]
            for arm, word in zip(self.an.ARMS, words):
                cell = self.rows[defect]["arms"][arm]
                if defect == "none":
                    expected = "зел." if not cell["caught"] else "КРАСНОЕ"
                else:
                    expected = "поймал" if cell["caught"] else "нет"
                self.assertEqual(word, expected, f"{defect}/{arm}")
                checked += 1
        self.assertEqual(checked, len(self.rows) * len(self.an.ARMS))

    def test_state_count_matches(self):
        """Утверждение «у шести состояний шесть разных хешей» проверяемо.

        Совпади два хеша, это значило бы, что правка не применилась и два
        состояния измерены на одном коде.
        """
        hashes = {r["sha256_before"] for r in self.rows.values()}
        self.assertEqual(len(hashes), len(self.rows))
        self.assertEqual(len(self.rows), 6)
        self.assertIn("шести состояний шесть разных",
                      README.read_text(encoding="utf-8"))

    def test_quoted_diagnostics_are_real(self):
        """Выдержки в README взяты из фикстуры, а не сочинены."""
        text = README.read_text(encoding="utf-8")
        strict = self.rows["untyped-body"]["arms"]["mypy-strict"]["evidence"]
        pyright = self.rows["untyped-body"]["arms"]["pyright"]["evidence"]
        self.assertIn("no-untyped-def", text)
        self.assertIn("no-untyped-def", strict)
        self.assertIn('Operator "+" not supported', text)
        self.assertIn('Operator "+" not supported', pyright)


class DataTable(unittest.TestCase):

    def setUp(self):
        path = ROOT / "fixtures" / "data.txt"
        if not path.exists():
            self.skipTest("фикстуры нет")
        data = fixture("data.txt")
        self.runs = {(r["state"], r["arm"]): r
                     for r in data if r.get("kind") == "run"}
        self.env = next(r for r in data if r.get("kind") == "env")
        self.an = load("analyze_data", "analyze-data.py")

    def test_matrix_matches(self):
        lines = block_after("## Сценарий 2: кто не полез в лишние данные")
        checked = 0
        for line in lines[1:]:
            parts = line.split()
            arm = parts[0]
            if arm not in self.an.ARMS:
                continue
            control = self.runs[(self.an.CONTROL, arm)]["reached_answer"]
            locked = self.runs[(self.an.LOCKED, arm)]["reached_answer"]
            self.assertEqual(parts[1], "дошёл" if control else "упал", arm)
            expected = "дошёл" if locked else "отказ в доступе"
            self.assertEqual(" ".join(parts[2:2 + len(expected.split())]),
                             expected, arm)
            checked += 1
        self.assertEqual(checked, len(self.an.ARMS))

    def test_dataset_facts_match(self):
        text = README.read_text(encoding="utf-8")
        self.assertIn(self.env["dataset_sha256"], text)
        self.assertIn(f"{self.env['buckets']} разделов", text)
        self.assertIn(f"{self.env['rows']:,}".replace(",", " ") + " строк",
                      text)

    def test_versions_match(self):
        text = README.read_text(encoding="utf-8")
        versions = self.env["versions"]
        for name, key in (("Python", "python"), ("pandas", "pandas"),
                          ("polars", "polars"), ("pyarrow", "pyarrow")):
            self.assertIn(f"{name} {versions[key]}", text)

    def test_unprivileged_user_is_named(self):
        """Обещание «от непривилегированного пользователя» проверяемо."""
        self.assertNotEqual(self.env["unprivileged_user"], "root")
        self.assertIn("непривилегированного пользователя",
                      README.read_text(encoding="utf-8"))


class NoTimings(unittest.TestCase):
    """Обещание «времени тут нет» проверяется по самим фикстурам."""

    def test_fixtures_contain_no_seconds(self):
        for name in ("typing.txt", "data.txt"):
            path = ROOT / "fixtures" / name
            if not path.exists():
                self.skipTest("фикстуры нет")
            raw = path.read_text(encoding="utf-8")
            for marker in ("seconds", "elapsed", "duration", "\"ms\""):
                self.assertNotIn(marker, raw,
                                 f"{name}: в фикстуре нашлось время ({marker})")


if __name__ == "__main__":
    unittest.main(verbosity=2)
