"""Предохранители разбора чтения.

Сценарий доказывает отрицание — «движок НЕ читал лишнего», — а такие
доказательства ломаются особенно тихо. Плечо, упавшее по своей причине,
выглядит как плечо, полезшее в закрытые разделы. Плечо, отбросившее
лишнее вместе с нужным, выглядит как самое расторопное.

Поэтому проверки идут в обе стороны: срабатывают там, где должны, и
молчат там, где данные исправны.
"""

import importlib.util
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location(
    "analyze_data", ROOT / "scripts" / "analyze-data.py")
an = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(an)

ANSWER = "одинаковый-ответ"
PRUNERS = ("pandas-filters", "polars-lazy")


def row(state, arm, reached, sha=ANSWER, error_type=None, locked=None):
    if locked is None:
        locked = [] if state == an.CONTROL else list(range(5, 20))
    return {
        "kind": "run", "state": state, "arm": arm,
        "reached_answer": reached,
        "result_sha256": sha if reached else None,
        "error_type": None if reached else (error_type or an.ACCESS_ERROR),
        "error": None if reached else "Permission denied",
        "groups": 4 if reached else 0,
        "locked_buckets": locked,
    }


def healthy():
    runs = [row(an.CONTROL, arm, True) for arm in an.ARMS]
    runs += [row(an.LOCKED, arm, arm in PRUNERS) for arm in an.ARMS]
    return runs


class Verdict(unittest.TestCase):

    def test_healthy_passes(self):
        self.assertEqual(an.verdict(healthy()), [])

    def test_failing_control_is_fatal(self):
        """Плечо, падающее и при доступных разделах, ничего не доказывает."""
        runs = [r for r in healthy()
                if not (r["state"] == an.CONTROL and r["arm"] == "polars-lazy")]
        runs.append(row(an.CONTROL, "polars-lazy", False))
        problems = an.verdict(runs)
        self.assertTrue(any("не дошли до ответа" in p for p in problems),
                        problems)

    def test_disagreeing_control_is_fatal(self):
        """Разные ответы в контроле — плечи решают разные задачи."""
        runs = [r for r in healthy()
                if not (r["state"] == an.CONTROL and r["arm"] == "pandas-full")]
        runs.append(row(an.CONTROL, "pandas-full", True, sha="другой"))
        problems = an.verdict(runs)
        self.assertTrue(any("РАЗНЫЕ" in p for p in problems), problems)

    def test_pruner_changing_the_answer_is_fatal(self):
        """ГЛАВНАЯ проверка сценария.

        Плечо отбросило разделы и ответило иначе. Внешне это выглядит как
        самый расторопный участник: дошёл там, где другие упали. На деле
        он потерял данные, и без сверки ответа это прошло бы за успех.
        """
        runs = [r for r in healthy()
                if not (r["state"] == an.LOCKED and r["arm"] == "polars-lazy")]
        runs.append(row(an.LOCKED, "polars-lazy", True, sha="урезанный"))
        problems = an.verdict(runs)
        self.assertTrue(any("ответил ИНАЧЕ" in p for p in problems), problems)

    def test_wrong_error_kind_is_fatal(self):
        """Падение не по доступу не доказывает чтения закрытого.

        Если плечо упало, скажем, на разборе схемы, из этого не следует
        ничего о том, куда оно лезло. Считать такое падение
        доказательством — значит принимать чужую поломку за результат.
        """
        runs = [r for r in healthy()
                if not (r["state"] == an.LOCKED and r["arm"] == "pandas-full")]
        runs.append(row(an.LOCKED, "pandas-full", False,
                        error_type="ArrowInvalid"))
        problems = an.verdict(runs)
        self.assertTrue(any("не с отказом в доступе" in p for p in problems),
                        problems)

    def test_nobody_prunes_is_fatal(self):
        runs = [r for r in healthy() if r["state"] != an.LOCKED]
        runs += [row(an.LOCKED, arm, False) for arm in an.ARMS]
        problems = an.verdict(runs)
        self.assertTrue(any("НИ ОДНО плечо" in p for p in problems), problems)

    def test_everybody_prunes_is_fatal(self):
        """Если дошли все, сценарий ничего не различает."""
        runs = [r for r in healthy() if r["state"] != an.LOCKED]
        runs += [row(an.LOCKED, arm, True) for arm in an.ARMS]
        problems = an.verdict(runs)
        self.assertTrue(any("дошли ВСЕ" in p for p in problems), problems)

    def test_locked_control_is_fatal(self):
        """Контроль с закрытыми разделами — уже не контроль."""
        runs = [r for r in healthy() if r["state"] != an.CONTROL]
        runs += [row(an.CONTROL, arm, True, locked=[7]) for arm in an.ARMS]
        problems = an.verdict(runs)
        self.assertTrue(any("тогда это не контроль" in p for p in problems),
                        problems)

    def test_nothing_locked_is_fatal(self):
        runs = [r for r in healthy() if r["state"] != an.LOCKED]
        runs += [row(an.LOCKED, arm, arm in PRUNERS, locked=[])
                 for arm in an.ARMS]
        problems = an.verdict(runs)
        self.assertTrue(any("не закрыт ни один раздел" in p for p in problems),
                        problems)

    def test_incomplete_matrix_is_fatal(self):
        runs = [r for r in healthy() if r["arm"] != "polars-eager"]
        problems = an.verdict(runs)
        self.assertTrue(any("нет клеток" in p for p in problems), problems)


class RealFixture(unittest.TestCase):

    def setUp(self):
        fixture = ROOT / "fixtures" / "data.txt"
        if not fixture.exists():
            self.skipTest("фикстуры нет")
        an.FIXTURE = fixture
        data = an.rows()
        self.runs = [r for r in data if r.get("kind") == "run"]
        self.env = next(r for r in data if r.get("kind") == "env")

    def test_fixture_passes(self):
        self.assertEqual(an.verdict(self.runs), [])

    def test_pruners_and_readers_both_present(self):
        by = {(r["state"], r["arm"]): r for r in self.runs}
        pruned = [a for a in an.ARMS if by[(an.LOCKED, a)]["reached_answer"]]
        read = [a for a in an.ARMS if not by[(an.LOCKED, a)]["reached_answer"]]
        self.assertTrue(pruned and read, (pruned, read))

    def test_arms_ran_unprivileged(self):
        """Под root права не действуют, и замер стал бы бессмысленным."""
        self.assertNotEqual(self.env["unprivileged_user"], "root")

    def test_fixture_survives_only_because_it_is_clean(self):
        spoiled = [dict(r) for r in self.runs]
        for r in spoiled:
            if r["state"] == an.LOCKED and r["arm"] == "polars-lazy":
                r["result_sha256"] = "испорчено"
        self.assertNotEqual(an.verdict(spoiled), [])


if __name__ == "__main__":
    unittest.main(verbosity=2)
