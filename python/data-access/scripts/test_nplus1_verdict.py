"""Предохранители разбора N+1 — вместе со случаями, которые их роняют.

Проверка «N+1 нет» бесполезна, пока не показано, что она падает на N+1.
Здесь подсовываются ровно те подделки, которые выглядят как успех:

* стратегия, обошедшаяся одним запросом, потому что ничего не загрузила;
* стратегия, у которой число запросов гуляет по кругам;
* selectinload, который на самом деле сходил в базу двести раз.
"""

import importlib.util
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location(
    "analyze_nplus1", ROOT / "scripts" / "analyze-nplus1.py")
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


def row(strategy, queries, rnd=1, books=200, reviews=1995, rating=5000):
    return {"scenario": "nplus1", "strategy": strategy, "round": rnd,
            "authors": 20, "queries": queries, "rows_from_server": 2215,
            "ms": 30.0, "books": books, "reviews": reviews,
            "rating_sum": rating}


def healthy(rounds=3):
    data = []
    for r in range(1, rounds + 1):
        data.append(row("lazy", 221, r))
        data.append(row("selectinload", 3, r))
        data.append(row("joinedload", 1, r))
    return data


class TestVerdict(unittest.TestCase):
    def test_healthy_fixture_passes(self):
        code, problems = mod.verdict(healthy())
        self.assertEqual(problems, [])
        self.assertEqual(code, 0)

    def test_strategy_that_loaded_nothing_is_caught(self):
        # Один запрос и ноль загруженного — идеальная строка в таблице и
        # полностью бессмысленная.
        data = healthy()
        data = [r for r in data if r["strategy"] != "joinedload"]
        for rnd in (1, 2, 3):
            data.append(row("joinedload", 1, rnd, books=0, reviews=0,
                            rating=0))
        code, problems = mod.verdict(data)
        self.assertEqual(code, 2)
        self.assertTrue(any("РАЗНОЕ" in p for p in problems), problems)

    def test_wandering_query_count_is_caught(self):
        data = healthy()
        data.append(row("selectinload", 4, 4))
        code, problems = mod.verdict(data)
        self.assertEqual(code, 2)
        self.assertTrue(any("гуляет" in p for p in problems), problems)

    def test_selectinload_that_is_actually_nplus1_is_caught(self):
        data = [r for r in healthy() if r["strategy"] != "selectinload"]
        for rnd in (1, 2, 3):
            data.append(row("selectinload", 221, rnd))
        code, problems = mod.verdict(data)
        self.assertEqual(code, 2)
        self.assertTrue(any("это N+1" in p for p in problems), problems)

    def test_lazy_is_allowed_to_be_nplus1(self):
        # Ленивая загрузка обязана давать N+1 — это её измеряемое свойство,
        # а не дефект замера. Порог к ней не применяется.
        code, problems = mod.verdict(healthy())
        self.assertEqual(problems, [])


if __name__ == "__main__":
    unittest.main(verbosity=2)
