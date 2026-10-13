"""Предохранители разбора матрицы типов и отбора выдержек.

Замер про «кто что ловит» ломается тише всех: любая его поломка выглядит
как содержательный результат. Красное плечо читается как строгий
инструмент. Дефект, которого не заметил никто, — как «проверки
бесполезны». Плечо без единой поимки — как приговор подходу.

Поэтому каждый случай проверяется в обе стороны: предохранитель обязан
срабатывать там, где должен, и молчать там, где данные исправны.
"""

import importlib.util
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "bench"))

SPEC = importlib.util.spec_from_file_location(
    "analyze_typing", ROOT / "scripts" / "analyze-typing.py")
an = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(an)

import run_typing  # noqa: E402

# Наблюдение, совпадающее с замыслом стенда.
OBSERVED = {
    "none": (),
    "wrong-return": ("mypy", "mypy-strict", "pyright", "runtime"),
    "untyped-body": ("mypy-strict", "pyright"),
    "optional-unchecked": ("mypy", "mypy-strict", "pyright"),
    "external-data": ("pydantic",),
    "runtime-only": ("runtime",),
}


def row(defect, catchers, sha="abc123", sha_after=None, evidence="ошибка"):
    arms = {}
    for arm in an.ARMS:
        caught = arm in catchers
        arms[arm] = {
            "exit_code": 1 if caught else 0,
            "caught": caught,
            "evidence": evidence if caught else None,
        }
    return {"defect": defect, "arms": arms, "sha256_before": sha,
            "sha256_after": sha if sha_after is None else sha_after}


def healthy():
    return [row(defect, catchers, sha=f"sha{i}")
            for i, (defect, catchers) in enumerate(OBSERVED.items())]


class Verdict(unittest.TestCase):

    def test_healthy_matrix_passes(self):
        self.assertEqual(an.verdict(healthy()), [])

    def test_red_control_is_fatal(self):
        """Плечо, красное на исправном коде, «ловит» что угодно."""
        data = [r for r in healthy() if r["defect"] != "none"]
        data.insert(0, row("none", ("mypy",), sha="sha0"))
        problems = an.verdict(data)
        self.assertTrue(any("ИСПРАВНОМ" in p for p in problems), problems)

    def test_defect_nobody_caught_is_fatal(self):
        """Никем не пойманный дефект — сообщение о замере, а не о плечах."""
        data = [r for r in healthy() if r["defect"] != "runtime-only"]
        data.append(row("runtime-only", (), sha="sha5"))
        problems = an.verdict(data)
        self.assertTrue(any("не поймал НИКТО" in p for p in problems),
                        problems)

    def test_arm_catching_nothing_is_fatal(self):
        """Плечо без поимок нельзя ни оправдать, ни осудить.

        Убираем единственную поимку pydantic — и плечо превращается в
        столбец из «нет», который читатель прочтёт как «валидация не
        нужна».
        """
        data = [r for r in healthy() if r["defect"] != "external-data"]
        data.append(row("external-data", ("mypy",), sha="sha4"))
        problems = an.verdict(data)
        self.assertTrue(any("не поймало ни одного" in p for p in problems),
                        problems)

    def test_changed_sources_are_fatal(self):
        """Если проверяющий переписал код под собой, плечи смотрели разное."""
        data = healthy()
        data[1]["sha256_after"] = "другое"
        problems = an.verdict(data)
        self.assertTrue(any("изменились за прогон" in p for p in problems),
                        problems)

    def test_divergence_from_intent_is_fatal(self):
        """Расхождение с замыслом обязан разбирать человек.

        Стенд объявляет, кто какой дефект ловит. Если наблюдение разошлось,
        молчаливо подстроить ожидание — значит превратить проверку в
        зеркало: она станет подтверждать любые данные.
        """
        data = [r for r in healthy() if r["defect"] != "untyped-body"]
        data.append(row("untyped-body", ("mypy", "mypy-strict", "pyright"),
                        sha="sha2"))
        problems = an.verdict(data)
        self.assertTrue(any("ожидалось" in p for p in problems), problems)

    def test_catch_without_evidence_is_fatal(self):
        """Поимка без выдержки ничего не объясняет читателю."""
        data = [r for r in healthy() if r["defect"] != "external-data"]
        data.append(row("external-data", ("pydantic",), sha="sha4",
                        evidence=None))
        problems = an.verdict(data)
        self.assertTrue(any("выдержки из вывода нет" in p for p in problems),
                        problems)

    def test_incomplete_matrix_is_fatal(self):
        data = [r for r in healthy() if r["defect"] != "external-data"]
        problems = an.verdict(data)
        self.assertTrue(any("нет состояний" in p for p in problems), problems)


class Evidence(unittest.TestCase):
    """Отбор выдержки: что именно попадает читателю в глаза."""

    def test_traceback_gives_the_exception_not_the_word(self):
        """У падения значима ПОСЛЕДНЯЯ строка, а не первая.

        Первая версия отбора возвращала «Traceback (most recent call
        last):» — формально выдержка есть, читать нечего.
        """
        tail = [
            "Traceback (most recent call last):",
            '  File "/work/scenario.py", line 40, in main',
            "    value = orders.total(order)",
            "TypeError: unsupported operand type(s) for +: 'Decimal' and 'float'",
        ]
        self.assertTrue(
            run_typing.evidence(tail).startswith("TypeError:"),
            run_typing.evidence(tail))

    def test_analyzer_header_is_skipped(self):
        """У pyright первая строка — имя файла, а не диагностика."""
        tail = [
            "/work/orders.py",
            "  /work/orders.py:40:12 - error: Type \"float\" is not assignable",
            "1 error, 0 warnings, 0 informations",
        ]
        got = run_typing.evidence(tail)
        self.assertIn("error:", got)
        self.assertNotEqual(got, "/work/orders.py")

    def test_summary_lines_are_skipped(self):
        tail = ["Found 1 error in 1 file (checked 1 source file)"]
        self.assertIsNone(run_typing.evidence(tail))

    def test_pydantic_validation_error_is_picked(self):
        tail = [
            "Traceback (most recent call last):",
            "pydantic_core._pydantic_core.ValidationError: 1 validation error",
            "express",
            "  Input should be a valid boolean [type=bool_type]",
            "    For further information visit https://errors.pydantic.dev/",
        ]
        got = run_typing.evidence(tail)
        self.assertIn("ValidationError", got)
        self.assertFalse(got.startswith("http"), got)

    def test_plain_message_survives(self):
        """Сообщение сценария о нарушенном инварианте — само по себе выдержка."""
        tail = ["НАРУШЕН ИНВАРИАНТ: заказ 3: итог -490.00 отрицателен"]
        self.assertEqual(run_typing.evidence(tail), tail[0])


class RealFixture(unittest.TestCase):

    def setUp(self):
        fixture = ROOT / "fixtures" / "typing.txt"
        if not fixture.exists():
            self.skipTest("фикстуры нет")
        an.FIXTURE = fixture
        self.data = an.rows()

    def test_fixture_passes(self):
        self.assertEqual(an.verdict(self.data), [])

    def test_every_state_has_its_own_hash(self):
        """Шесть состояний кода — шесть разных исходников.

        Совпади два хеша, это значило бы, что правка не применилась и два
        состояния измерены на одном коде.
        """
        hashes = [r["sha256_before"] for r in self.data]
        self.assertEqual(len(set(hashes)), len(hashes))

    def test_fixture_survives_only_because_it_is_clean(self):
        spoiled = [dict(r) for r in self.data]
        spoiled[0] = row("none", ("runtime",))
        self.assertNotEqual(an.verdict(spoiled), [])

    def test_evidence_is_never_noise(self):
        """В настоящей фикстуре нет выдержек-пустышек."""
        for r in self.data:
            for arm, cell in r["arms"].items():
                if not cell["caught"]:
                    continue
                text = cell["evidence"]
                self.assertTrue(text, f"{r['defect']}/{arm}")
                self.assertFalse(text.startswith("Traceback"),
                                 f"{r['defect']}/{arm}: {text}")
                self.assertFalse(text.endswith(".py"),
                                 f"{r['defect']}/{arm}: {text}")


if __name__ == "__main__":
    unittest.main(verbosity=2)
