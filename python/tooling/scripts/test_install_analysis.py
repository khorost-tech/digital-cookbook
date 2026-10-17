"""Предохранители разбора установки: на чём они срабатывают и на чём молчат.

Разбор объявляет замер негодным по трём признакам и, кроме того, решает,
какие клетки вообще подлежат суду по разбросу. Последнее — то место, где
ошибиться можно в обе стороны, и обе выглядят прилично:

  * применить порог 1,5x ко всем клеткам — и забраковать исправный замер
    из-за клетки на сотых долях секунды, где 0,01 против 0,03 даёт 3x;
  * не применять порог нигде — и пропустить настоящую нестабильность,
    потому что «ну там же быстро».

Поэтому здесь проверяется не только то, что предохранитель срабатывает,
но и то, что он молчит там, где обязан молчать. Проверка, которая только
падает, доказывает лишь строгость, а не правильность границы.
"""

import importlib.util
import itertools
import json
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location(
    "analyze_install", ROOT / "scripts" / "analyze-install.py")
an = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(an)


STATE_POSITION = {"cold": 0, "warm": 1, "noop": 2}


def row(tool, state, round_no, seconds, sha="a" * 16, packages=51,
        caches=None, position=None, tool_position=0):
    state_position = STATE_POSITION[state] if position is None else position
    return {
        "tool": tool, "state": state, "round": round_no,
        "tool_position": tool_position,
        "state_position": state_position,
        "global_position": tool_position * 3 + state_position,
        "seconds": seconds, "sha256": sha, "packages": packages,
        "bootstrap": [],
        "caches_before_mib": caches if caches is not None else {
            "uv": 0.0, "pip": 0.0, "poetry": 0.0},
    }


class SpreadFloor(unittest.TestCase):
    """Порог разброса и клетки, к которым он неприменим."""

    def test_slow_cell_over_limit_is_a_defect(self):
        """Медиана выше пола, отношение выше порога — это дефект."""
        wide, tiny = an.spread_report({("pip", "cold"): [10.0, 14.0, 20.0]})
        self.assertEqual([(t, s) for t, s, _ in wide], [("pip", "cold")])
        self.assertEqual(tiny, [])

    def test_fast_cell_over_limit_is_not_judged(self):
        """Те же 2x, но на сотых долях секунды — не дефект.

        Ровно случай uv/noop из фикстуры: 0,01–0,03 с. Если этот тест
        начнёт падать, разбор станет браковать годные замеры.
        """
        wide, tiny = an.spread_report({("uv", "noop"): [0.01, 0.016, 0.03]})
        self.assertEqual(wide, [])
        self.assertEqual([(t, s) for t, s, _, _ in tiny], [("uv", "noop")])

    def test_boundary_is_the_median_not_the_minimum(self):
        """Граница проводится по медиане клетки, а не по её минимуму.

        Клетка с минимумом 0,05 и медианой 3 с — медленная, и судить её
        нужно. Если бы граница шла по минимуму, такая клетка молча ушла
        бы в «не судится», унеся с собой настоящий выброс.
        """
        wide, tiny = an.spread_report({("poetry", "cold"): [0.05, 3.0, 3.2]})
        self.assertEqual([(t, s) for t, s, _ in wide], [("poetry", "cold")])
        self.assertEqual(tiny, [])

    def test_stable_slow_cell_is_silent(self):
        wide, tiny = an.spread_report({("pip", "warm"): [10.6, 11.3, 13.2]})
        self.assertEqual(wide, [])
        self.assertEqual(tiny, [])


def balanced(seconds=None, rounds=18):
    """План замера, как его строит сценарий: два независимых порядка.

    Сдвиг порядка инструментов берётся от (r-1) % 3, перестановка порядка
    состояний — от (r-1) // 3 по всем шести перестановкам. За 18 кругов
    каждая пара встречается ровно раз.
    """
    tools = ("uv", "pip", "poetry")
    orders = list(itertools.permutations(("cold", "warm", "noop")))
    data = []
    for r in range(1, rounds + 1):
        shift = (r - 1) % len(tools)
        states = orders[((r - 1) // len(tools)) % len(orders)]
        rotated = [tools[(shift + i) % len(tools)] for i in range(len(tools))]
        for tool_position, tool in enumerate(rotated):
            for state_position, state in enumerate(states):
                value = (seconds or {}).get((tool, state),
                                            1.0 + state_position / 10)
                data.append(row(tool, state, r, value,
                                position=state_position,
                                tool_position=tool_position))
    return data


class Verdict(unittest.TestCase):
    """Признаки негодного замера."""

    def test_clean_data_has_no_problems(self):
        data = balanced()
        self.assertEqual(an.verdict(data), [])

    def test_missing_position_is_fatal(self):
        """Фикстура без позиции — снята схемой, где порядок не ротировался.

        В таких данных эффект кэша неотделим от эффекта позиции, и разбор
        обязан отказаться, а не посчитать разности как ни в чём не бывало.
        """
        data = balanced()
        del data[0]["tool_position"]
        problems = an.verdict(data)
        self.assertTrue(any("обеих позиций" in p for p in problems), problems)

    def test_fixed_order_is_fatal(self):
        """Прежняя схема замера обязана отвергаться этим разбором.

        До ревью состояния шли строго cold, warm, noop, и каждое всегда
        занимало одну и ту же позицию. Разность cold − warm в таких данных
        включает эффект позиции, отделить его нечем — и вердикт обязан
        сказать это прямо, а не посчитать разности как ни в чём не бывало.
        """
        data = []
        for r in range(1, 7):
            for tool_position, tool in enumerate(("uv", "pip")):
                for position, state in enumerate(("cold", "warm", "noop")):
                    data.append(row(tool, state, r, 1.0 + position / 10,
                                    position=position,
                                    tool_position=tool_position))
        problems = an.verdict(data)
        self.assertTrue(any("неравномерно" in p for p in problems), problems)

    def test_locked_factors_are_fatal(self):
        """ТОТ САМЫЙ дефект, который нашло ревью.

        Позиции обе записаны и обе сбалансированы, но у каждого
        инструмента позиция состояния однозначно задаёт позицию
        инструмента: порядок состояний брался со смещением на номер
        инструмента. Тогда разложение времени по одному фактору в
        точности совпадает с разложением по другому, и приписать
        наблюдение какому-то одному нельзя — а таблица «по позициям»
        выглядит совершенно обычно.
        """
        tools = ("uv", "pip", "poetry")
        states = ("cold", "warm", "noop")
        data = []
        for r in range(1, 10):
            shift = (r - 1) % 3
            for tool_position in range(3):
                idx = (shift + tool_position) % 3
                order = [states[(shift + idx + i) % 3] for i in range(3)]
                for state_position, state in enumerate(order):
                    data.append(row(tools[idx], state, r, 1.0,
                                    position=state_position,
                                    tool_position=tool_position))
        problems = an.verdict(data)
        self.assertTrue(any("не разведены" in p for p in problems), problems)

    def test_independent_factors_pass(self):
        """План с двумя независимыми ключами обязан проходить."""
        self.assertEqual(an.verdict(balanced()), [])

    def test_balanced_rotation_passes(self):
        self.assertEqual(an.verdict(balanced()), [])

    def test_diverging_hashes_are_fatal(self):
        """Разные наборы — сравнивать нельзя: быстрее тот, кто сделал меньше."""
        data = [row("uv", "cold", 1, 2.9),
                row("pip", "cold", 1, 12.4, sha="b" * 16)]
        problems = an.verdict(data)
        self.assertTrue(any("РАЗНОЕ" in p for p in problems), problems)

    def test_dirty_cold_run_is_fatal(self):
        """Холодный прогон с непустым кэшем — это тёплый под чужим именем."""
        data = [row("pip", "cold", 1, 4.0,
                    caches={"uv": 0.0, "pip": 180.0, "poetry": 0.0})]
        problems = an.verdict(data)
        self.assertTrue(any("непустым" in p for p in problems), problems)

    def test_foreign_cache_does_not_trigger(self):
        """Чужой кэш холодности плеча не отменяет.

        Плечо pip меряет pip. Полный кэш uv в этот момент — норма: его
        никто и не обязан чистить, состояния заданы для инструмента,
        который сейчас измеряется. Если бы проверка смотрела на все кэши
        сразу, годный замер объявлялся бы негодным начиная со второго
        круга — то есть почти всегда.
        """
        data = balanced()
        for r in data:
            if r["state"] == "cold" and r["tool"] == "pip":
                r["caches_before_mib"] = {"uv": 900.0, "pip": 0.0,
                                          "poetry": 400.0}
        self.assertEqual(an.verdict(data), [])

    def test_old_fixture_is_rejected(self):
        stale = row("uv", "cold", 1, 2.9)
        del stale["sha256"]
        problems = an.verdict([stale])
        self.assertTrue(any("другой версией" in p for p in problems), problems)


class Paired(unittest.TestCase):
    """Покруговые сопоставления: отношение и выигрыш кэша."""

    def test_ratio_is_taken_inside_a_round(self):
        """Отношение считается в круге, а не между медианами.

        Круг 1 медленный целиком, круг 2 быстрый целиком. Отношение в
        обоих кругах ровно 4x, и покруговый счёт это видит. Счёт «медиана
        pip делить на медиану uv» дал бы то же число случайно — поэтому
        круги здесь подобраны так, чтобы неверный способ ошибался.
        """
        data = [row("uv", "cold", 1, 4.0), row("pip", "cold", 1, 16.0),
                row("uv", "cold", 2, 1.0), row("pip", "cold", 2, 4.0)]
        self.assertEqual(an.paired(data, "pip", "cold"), [4.0, 4.0])

    def test_round_without_a_pair_is_dropped(self):
        data = [row("uv", "cold", 1, 4.0), row("pip", "cold", 1, 16.0),
                row("pip", "cold", 2, 4.0)]
        self.assertEqual(an.paired(data, "pip", "cold"), [4.0])

    def test_cache_gain_is_cold_minus_warm(self):
        data = [row("uv", "cold", 1, 3.0), row("uv", "warm", 1, 0.6),
                row("uv", "cold", 2, 2.8), row("uv", "warm", 2, 0.6)]
        self.assertEqual([round(x, 2) for x in an.cache_gain(data, "uv")],
                         [2.2, 2.4])

    def test_cache_gain_keeps_negative_rounds(self):
        """Круг, где тёплый оказался МЕДЛЕННЕЕ холодного, не выбрасывается.

        Именно на таких кругах и держится знаковый счёт: выкинув их,
        получим «11 из 11» при любых данных.
        """
        data = [row("pip", "cold", 1, 11.0), row("pip", "warm", 1, 12.0)]
        self.assertEqual(an.cache_gain(data, "pip"), [-1.0])


class RealFixture(unittest.TestCase):
    """Разбор на настоящей фикстуре — и на ней же, испорченной."""

    def setUp(self):
        self.fixture = ROOT / "fixtures" / "install.txt"
        if not self.fixture.exists():
            self.skipTest("фикстуры нет")
        an.FIXTURE = self.fixture
        self.data = an.rows()

    def test_fixture_passes(self):
        self.assertEqual(an.verdict(self.data), [])
        self.assertGreaterEqual(len(self.data), 27)

    def test_fixture_survives_only_because_it_is_clean(self):
        """Порча одной строки обязана валить вердикт.

        Проверка на «данные годны» ничего не стоит, если она проходит и
        на негодных. Здесь портится ровно одна строка из девяноста
        девяти — меньше, чем округление, — и вердикт обязан измениться.
        """
        spoiled = [dict(r) for r in self.data]
        spoiled[0] = dict(spoiled[0], sha256="0" * 16)
        self.assertNotEqual(an.verdict(spoiled), [])


class PositionAndSpread(unittest.TestCase):
    """Разброс судится по тому, меняет ли он ПОРЯДОК инструментов."""

    def test_wide_cell_with_unanimous_ordering_passes(self):
        """Клетка с четырёхкратным разбросом, но порядок единогласный.

        Случай uv/warm: значения гуляют вчетверо, а uv в КАЖДОМ круге
        быстрее обоих соперников. Ранжировать по такой клетке можно, и
        блокировать замер из-за её ширины значило бы останавливаться на
        числе, которое ни на один вывод не влияет.
        """
        data = balanced({("uv", "warm"): 0.45, ("pip", "warm"): 2.77,
                         ("poetry", "warm"): 2.90})
        for r in data:
            if (r["tool"], r["state"]) == ("uv", "warm") and r["round"] % 2:
                r["seconds"] = 1.80
        self.assertEqual(an.verdict(data), [])

    def test_single_outlier_does_not_break_the_ordering(self):
        """Один выброс из восемнадцати не отменяет порядок.

        ЭТО ОШИБКА ПРЕДЫДУЩЕЙ ВЕРСИИ ПРОВЕРКИ. Она сравнивала размахи, а
        размах — статистика по худшему одиночному чтению: у pip/noop два
        значения из восемнадцати заехали в область Poetry, и замер был
        объявлен негодным, хотя pip быстрее в семнадцати кругах.
        """
        data = balanced({("uv", "noop"): 0.01, ("pip", "noop"): 0.55,
                         ("poetry", "noop"): 0.75})
        for r in data:
            if (r["tool"], r["state"]) == ("pip", "noop") and r["round"] == 5:
                r["seconds"] = 0.99
        self.assertEqual(an.verdict(data), [])

    def test_coin_flip_ordering_is_fatal(self):
        """Порядок, неотличимый от монетки, публиковать нельзя.

        Здесь два инструмента чередуются: в половине кругов быстрее один,
        в половине другой. Медианы при этом дадут какой-то порядок, и он
        будет выдумкой.
        """
        data = balanced({("uv", "warm"): 1.00, ("pip", "warm"): 1.00,
                         ("poetry", "warm"): 2.90})
        for r in data:
            if r["state"] != "warm":
                continue
            if r["tool"] == "uv":
                r["seconds"] = 1.0 if r["round"] % 2 else 1.2
            elif r["tool"] == "pip":
                r["seconds"] = 1.2 if r["round"] % 2 else 1.0
        problems = an.verdict(data)
        self.assertTrue(any("не установлен" in p for p in problems), problems)
        self.assertTrue(any("монетки" in p for p in problems), problems)

    def test_sign_test_matches_hand_computation(self):
        """Точный биномиальный тест — сверка с известными значениями."""
        self.assertAlmostEqual(an.sign_test(18, 18), 2 / 2 ** 18)
        self.assertAlmostEqual(an.sign_test(9, 18), 1.0)
        self.assertLess(an.sign_test(17, 18), 0.001)
        self.assertGreater(an.sign_test(11, 18), 0.05)

    def test_position_split_is_reported(self):
        data = balanced()
        for r in data:
            if (r["tool"], r["state"]) == ("uv", "warm"):
                r["seconds"] = 1.7 if r["state_position"] == 0 else 0.5
        groups = an.by_position(data, "uv", "warm")
        self.assertEqual(sorted(groups), [0, 1, 2])
        self.assertEqual(an.med(groups[0]), 1.7)


class OrderSplit(unittest.TestCase):
    """Выигрыш кэша, посчитанный отдельно по относительному порядку."""

    def test_split_selects_the_right_rounds(self):
        data = balanced()
        for r in data:
            if r["tool"] == "uv":
                r["seconds"] = {"cold": 3.0, "warm": 1.0, "noop": 0.1}[
                    r["state"]]
        every = an.cache_gain(data, "uv")
        cold_first = an.cache_gain(data, "uv", "cold-first")
        warm_first = an.cache_gain(data, "uv", "warm-first")
        self.assertEqual(len(every), len(cold_first) + len(warm_first))
        self.assertTrue(cold_first and warm_first,
                        "оба порядка обязаны встречаться — иначе ротации нет")
        self.assertTrue(all(abs(x - 2.0) < 1e-9 for x in every))

    def test_positional_artefact_would_show_up(self):
        """Если разность создаётся позицией, разделение это покажет.

        Здесь время зависит ТОЛЬКО от позиции, а не от состояния. Тогда в
        кругах, где холодный шёл раньше, разность положительна, а где
        позже — отрицательна. Именно этого и не могла показать прежняя
        схема с фиксированным порядком.
        """
        data = balanced()
        for r in data:
            r["seconds"] = 3.0 - r["state_position"]
        cold_first = an.cache_gain(data, "uv", "cold-first")
        warm_first = an.cache_gain(data, "uv", "warm-first")
        self.assertTrue(all(x > 0 for x in cold_first), cold_first)
        self.assertTrue(all(x < 0 for x in warm_first), warm_first)


if __name__ == "__main__":
    unittest.main(verbosity=2)
