"""Предохранители разбора наборов тестов.

Замер про то, кто ловит дефекты, ломается тише всех остальных на стенде:
любая его поломка выглядит как содержательный результат. Красный контроль
читается как «строгий набор». Дефект, которого не заметил никто, — как
«все тесты бесполезны». Набор, не поймавший ничего, — как приговор
подходу. Поэтому каждый из этих случаев здесь проверяется отдельно, и
проверяется в обе стороны: срабатывает там, где должен, и молчит там, где
данные исправны.
"""

import importlib.util
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location(
    "analyze_tests", ROOT / "scripts" / "analyze-tests.py")
an = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(an)

DEFECTS = ("none", "null-filter", "like-case", "group-by", "n-plus-1")
# Кто кого ловит в исправном замере — та же картина, что в фикстуре.
CAUGHT = {
    "null-filter": ("sqlite", "postgres", "hypothesis"),
    "like-case": ("postgres", "hypothesis"),
    "group-by": ("postgres", "hypothesis"),
    "n-plus-1": ("mocks",),
}
# Плечи, которым положено поднимать контейнер. У моков и фейка его нет, и
# ноль стартов у них — норма, а не признак поломки.
WITH_DATABASE = ("postgres", "hypothesis")


def cell(defect, suite, caught, starts=None):
    return {
        "kind": "matrix", "defect": defect, "suite": suite,
        "tests": 3, "failed": 1 if caught else 0,
        "failed_names": ["test_x"] if caught else [],
        "caught": caught, "seconds": 1.0,
        "container_starts": starts if starts is not None else (
            1 if suite in WITH_DATABASE else 0),
    }


def healthy():
    matrix = {}
    for defect in DEFECTS:
        for suite in an.SUITES:
            caught = suite in CAUGHT.get(defect, ())
            matrix[(defect, suite)] = cell(defect, suite, caught)
    return matrix


class Verdict(unittest.TestCase):

    def test_healthy_matrix_passes(self):
        self.assertEqual(an.verdict(healthy()), [])

    def test_red_control_is_fatal(self):
        """Набор, красный на исправном коде, «ловит» что угодно."""
        matrix = healthy()
        matrix[("none", "postgres")] = cell("none", "postgres", True)
        problems = an.verdict(matrix)
        self.assertTrue(any("ИСПРАВНОЙ" in p for p in problems), problems)

    def test_missing_control_is_fatal(self):
        matrix = healthy()
        del matrix[("none", "sqlite")]
        problems = an.verdict(matrix)
        self.assertTrue(any("контрольного" in p for p in problems), problems)

    def test_defect_nobody_caught_is_fatal(self):
        """Никем не пойманный дефект — сообщение о замере, а не о тестах.

        Читается он ровно наоборот: «даже настоящая база не помогла». На
        деле это значит, что подсунуть ошибку не удалось — код с ней
        ведёт себя так же, как без неё.
        """
        matrix = healthy()
        for suite in an.SUITES:
            matrix[("like-case", suite)] = cell("like-case", suite, False)
        problems = an.verdict(matrix)
        self.assertTrue(any("не поймал НИКТО" in p for p in problems),
                        problems)

    def test_suite_catching_nothing_is_fatal(self):
        """Набор без единой поимки нельзя ни оправдать, ни осудить.

        Ровно этот случай и был у набора на моках, пока в перечень не
        добавили n-plus-1: три «нет» подряд выглядели приговором подходу,
        хотя означали лишь, что своего дефекта набор не встретил.
        """
        matrix = healthy()
        matrix[("n-plus-1", "mocks")] = cell("n-plus-1", "mocks", False)
        problems = an.verdict(matrix)
        self.assertTrue(any("ни одного дефекта" in p for p in problems),
                        problems)

    def test_database_that_never_started_is_fatal(self):
        """Зелёный интеграционный набор без базы — не результат."""
        matrix = healthy()
        matrix[("like-case", "postgres")] = cell(
            "like-case", "postgres", True, starts=0)
        problems = an.verdict(matrix)
        self.assertTrue(any("не стартовал" in p for p in problems), problems)

    def test_zero_starts_for_non_database_suites_is_normal(self):
        """У моков и фейка ноль стартов — это норма, а не дефект.

        Проверка обязана смотреть только на плечо с базой. Иначе она
        забракует любой исправный замер.
        """
        matrix = healthy()
        for defect in DEFECTS:
            for suite in ("mocks", "sqlite"):
                self.assertEqual(matrix[(defect, suite)]["container_starts"],
                                 0)
        self.assertEqual(an.verdict(matrix), [])


class RealFixture(unittest.TestCase):

    def setUp(self):
        self.fixture = ROOT / "fixtures" / "tests.txt"
        if not self.fixture.exists():
            self.skipTest("фикстуры нет")
        an.FIXTURE = self.fixture
        data = an.rows()
        self.matrix = {(r["defect"], r["suite"]): r
                       for r in data if r.get("kind") == "matrix"}
        self.costs = [r for r in data if r.get("kind") == "cost"]

    def test_fixture_passes(self):
        self.assertEqual(an.verdict(self.matrix), [])

    def test_every_suite_and_defect_present(self):
        for defect in DEFECTS:
            for suite in an.SUITES:
                self.assertIn((defect, suite), self.matrix)

    def test_fixture_survives_only_because_it_is_clean(self):
        spoiled = dict(self.matrix)
        spoiled[("none", "postgres")] = dict(
            spoiled[("none", "postgres")], failed=1, caught=True)
        self.assertNotEqual(an.verdict(spoiled), [])

    def test_cost_rows_carry_measured_starts(self):
        """Старты в строках цены — измеренные, а не проставленные.

        При области function их обязано быть столько же, сколько тестов:
        если бы приспособление молча переиспользовало контейнер, число
        осталось бы единицей, а вывод про «вдвое дороже» повис бы без
        объяснения.
        """
        fn = [r for r in self.costs
              if r["suite"] == "postgres" and r["scope"] == "function"]
        se = [r for r in self.costs
              if r["suite"] == "postgres" and r["scope"] == "session"]
        self.assertTrue(fn and se)
        for r in fn:
            self.assertEqual(r["container_starts"], r["tests"])
        for r in se:
            self.assertEqual(r["container_starts"], 1)


def hyp_row(defect, shrink, examples, caught, counterexample="Failing test "
            "case: ...", max_examples=100):
    return {"kind": "hypothesis", "defect": defect, "shrink": shrink,
            "examples": examples, "caught": caught,
            "counterexample": counterexample if (shrink and caught) else None,
            "max_examples": max_examples, "seconds": 7.0}


def healthy_hyp():
    rows = [hyp_row("none", False, 100, False),
            hyp_row("none", True, 100, False)]
    for defect, plain, shrunk in (("null-filter", 4, 77),
                                  ("like-case", 6, 45),
                                  ("group-by", 2, 12)):
        rows.append(hyp_row(defect, False, plain, True))
        rows.append(hyp_row(defect, True, shrunk, True))
    rows.append(hyp_row("n-plus-1", False, 100, False))
    rows.append(hyp_row("n-plus-1", True, 100, False))
    return rows


class Hypothesis(unittest.TestCase):
    """Проверки property-based плеча."""

    def test_healthy_rows_pass(self):
        self.assertEqual(an.verdict_hypothesis(healthy(), healthy_hyp()), [])

    def test_catch_without_counterexample_is_fatal(self):
        """Ровно тот случай, который однажды проехал незамеченным.

        Разбор искал в выводе фразу, которой там нет, и на каждую поимку
        возвращал None. Поимки, счётчики и время при этом выглядели
        безупречно — не хватало только того, ради чего набор и нужен.
        """
        rows = [r for r in healthy_hyp()
                if not (r["defect"] == "like-case" and r["shrink"])]
        rows.append(hyp_row("like-case", True, 45, True,
                            counterexample=None))
        rows[-1]["counterexample"] = None
        problems = an.verdict_hypothesis(healthy(), rows)
        self.assertTrue(any("контрпримера нет" in p for p in problems),
                        problems)

    def test_control_stopping_early_is_fatal(self):
        """«Контроль прошёл» — это «прошёл на СТОЛЬКИХ примерах».

        Если генератор выдохся на двадцатом примере, зелёный контроль
        говорит о вдесятеро меньшей работе, чем указано в настройках, а
        читатель об этом не узнает.
        """
        rows = [r for r in healthy_hyp() if r["defect"] != "none"]
        rows += [hyp_row("none", False, 20, False),
                 hyp_row("none", True, 20, False)]
        problems = an.verdict_hypothesis(healthy(), rows)
        self.assertTrue(any("раньше" in p for p in problems), problems)

    def test_shrink_run_shorter_than_plain_is_fatal(self):
        """Сжатие не может начаться раньше поимки.

        Если прогон со сжатием выполнил МЕНЬШЕ примеров, чем прогон без
        него, значит это не два режима одного поиска, а два разных
        замера, и сравнивать их числа нельзя.
        """
        rows = [r for r in healthy_hyp()
                if not (r["defect"] == "group-by" and r["shrink"])]
        rows.append(hyp_row("group-by", True, 1, True))
        problems = an.verdict_hypothesis(healthy(), rows)
        self.assertTrue(any("несопоставимы" in p for p in problems), problems)

    def test_no_hypothesis_rows_is_not_a_defect(self):
        """Фикстура без property-based плеча разбирается как прежде."""
        self.assertEqual(an.verdict_hypothesis(healthy(), []), [])


class RealFixtureHypothesis(unittest.TestCase):

    def setUp(self):
        fixture = ROOT / "fixtures" / "tests.txt"
        if not fixture.exists():
            self.skipTest("фикстуры нет")
        an.FIXTURE = fixture
        data = an.rows()
        self.hyp = [r for r in data if r.get("kind") == "hypothesis"]
        self.matrix = {(r["defect"], r["suite"]): r
                       for r in data if r.get("kind") == "matrix"}

    def test_fixture_passes(self):
        self.assertEqual(an.verdict(self.matrix, self.hyp), [])

    def test_every_catch_has_a_counterexample(self):
        by = {(r["defect"], r["shrink"]): r for r in self.hyp}
        caught = [d for (d, sh), r in by.items() if not sh and r["caught"]]
        self.assertTrue(caught)
        for defect in caught:
            text = by[(defect, True)]["counterexample"]
            self.assertTrue(text and "Failing test case" in text, defect)

    def test_shrinking_costs_more_examples_than_finding(self):
        by = {(r["defect"], r["shrink"]): r for r in self.hyp}
        for (defect, shrink), row in by.items():
            if shrink or not row["caught"]:
                continue
            self.assertGreater(by[(defect, True)]["examples"], row["examples"],
                               defect)


if __name__ == "__main__":
    unittest.main(verbosity=2)
