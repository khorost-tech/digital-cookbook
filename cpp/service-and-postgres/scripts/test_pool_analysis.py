"""Проверки оси 2 — сколько соединений драйвер открывает НА САМОМ ДЕЛЕ.

НАПИСАНЫ ДО ПУЛА. На момент написания пула не было ни у одного из трёх
слоёв: проба показала, что соединение открывается на каждый запрос и
закрывается вместе с ним (см. expected.json, блок pool). Поэтому здесь
сначала падают все проверки разом — нет ни модуля `scripts/pool.py`, ни
фикстуры, — и это записанный шаг, а не случайность.

ГЛАВНАЯ ЗДЕСЬ — ПРОВЕРКА КОНТРОЛЯ. Пул на 1 против пула на 8, всё
остальное одинаково. Счётчик соединений обязан пойти за настройкой. Не
пошёл — мы меряем не пул, ось недействительна, таблица не печатается.
Это законный исход, и он обязан быть ЗАПИСАН, а не обойдён.

ВТОРАЯ ПО ВАЖНОСТИ — ПРОВЕРКА ТОГО, ОТКУДА БЕРЁТСЯ ЧИСЛО. Счётчик
соединений, взятый не из базы, а из настройки (или из ответа сервиса),
даёт тавтологию: «пул на 8 показал 8» тогда верно всегда и не значит
ничего. Поэтому сампер проверяется отдельно — он обязан спрашивать
`scripts.pg.connections` про ИМЯ ВАРИАНТА.
"""

import inspect
import io
import json
import os
import shutil
import sys
import unittest

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts import docker as dockerlib  # noqa: E402
from scripts import drogonpool  # noqa: E402
from scripts import pg  # noqa: E402
from scripts import pool  # noqa: E402

FIXTURE = os.path.join(STAND, "fixtures", "pool.jsonl")
VARIANTS = ("beast", "crow", "drogon")


def _row(variant="beast", pool_size=1, connections=1, concurrent=8,
         idle_after=None, **extra):
    """Придуманная строка замера — чтобы правило вывода можно было ронять.

    Правило вывода обязано проверяться на строках, которых не было ни в
    одном прогоне: на настоящей фикстуре контроль сходится, и испорченное
    правило зеленело бы вместе с ней.
    """
    row = {
        "kind": "pool",
        "variant": variant,
        "driver": pool.DRIVER,
        "pool_size": pool_size,
        "concurrent": concurrent,
        "workers": concurrent,
        "sleep_ms": pool.SLEEP_MS,
        "entry_binary_sha256": "0" * 64,
        "runs": 2,
        "connections_before": 0,
        "connections_by_run": [connections, connections],
        "connections_observed": connections,
        # ПО УМОЛЧАНИЮ ПРИДУМАННАЯ СТРОКА НЕ ТЕЧЁТ. Прежняя редакция
        # приравнивала остаток к числу соединений — и строка с большим
        # числом при малом пуле оказывалась ещё и «текущей». Проверки
        # контроля зеленели тогда из-за утечки, а не из-за контроля:
        # диверсия, разрешавшая печать при контроле, сошедшемся лишь у
        # одного варианта, прошла на этом зелёной.
        "connections_idle_after_by_run": (
            [min(connections, pool_size)] * 2 if idle_after is None
            else [idle_after, idle_after]),
        "connections_idle_after": (min(connections, pool_size)
                                   if idle_after is None else idle_after),
        "inflight_peak": concurrent,
        "connections_stable": True,
        "served": concurrent * 2,
        "not_served": 0,
    }
    row.update(extra)
    return row


def _pair(variant="beast", low=1, high=8, **extra):
    return [_row(variant=variant, pool_size=1, connections=low, **extra),
            _row(variant=variant, pool_size=8, connections=high, **extra)]


def _all_variants(low=1, high=8, **extra):
    rows = []
    for name in VARIANTS:
        rows += _pair(variant=name, low=low, high=high, **extra)
    return rows


class ControlOnTheFixture(unittest.TestCase):
    """Контроль на ТОМ ЗАМЕРЕ, на который будет ссылаться статья."""

    def setUp(self):
        if not os.path.exists(FIXTURE):
            self.fail("нет замера пула: %s" % FIXTURE)
        self.rows = pool.read(FIXTURE)

    def test_pool_knob_moves_the_counter(self):
        for variant in VARIANTS:
            one = pool.connections(self.rows, variant, pool_size=1)
            many = pool.connections(self.rows, variant, pool_size=8)
            self.assertLess(
                one, many,
                "%s: счётчик соединений не пошёл за размером пула "
                "(%d против %d) — мы меряем не пул" % (variant, one, many))

    def test_the_small_pool_is_a_ceiling_and_not_a_wish(self):
        # Пул на 1, показавший два соединения, — не пул: слот выдан
        # дважды разом. Это отдельное утверждение от «счётчик пошёл за
        # настройкой»: тот держится и при 2 против 8.
        for variant in VARIANTS:
            self.assertLessEqual(
                pool.connections(self.rows, variant, pool_size=1), 1,
                "%s: при пуле на 1 соединений больше одного" % variant)

    def test_both_settings_are_measured_with_the_same_everything_else(self):
        for variant in VARIANTS:
            low = pool.setting(self.rows, variant, 1)
            high = pool.setting(self.rows, variant, 8)
            for field in ("concurrent", "workers", "sleep_ms", "runs",
                          "driver", "entry_binary_sha256"):
                self.assertEqual(
                    low[field], high[field],
                    "%s: пара по размеру пула различается ещё и полем %s "
                    "(%r против %r) — контролем это уже не является"
                    % (variant, field, low[field], high[field]))

    def test_every_variant_is_measured_at_both_sizes(self):
        measured = {(row["variant"], row["pool_size"]) for row in self.rows}
        for variant in VARIANTS:
            for size in pool.SIZES:
                self.assertIn(
                    (variant, size), measured,
                    "%s: нет замера при пуле %d — сравнивать нечего"
                    % (variant, size))

    def test_the_axis_compares_layers_and_not_drivers(self):
        # Главное правило стенда: различаться обязан ТОЛЬКО слой HTTP.
        # Строка оси, снятая другим клиентом базы, сравнивала бы драйверы.
        for row in self.rows:
            self.assertEqual(
                row["driver"], pool.DRIVER,
                "%s: строка оси снята клиентом %r, а не общим для трёх "
                "слоёв" % (row["variant"], row["driver"]))

    def test_nothing_leaked_between_volleys(self):
        for row in self.rows:
            self.assertLessEqual(
                row["connections_idle_after"], row["pool_size"],
                "%s (пул %d): после залпа осталось %d простаивающих "
                "соединений — пул течёт, и следующий замер посчитает "
                "остаток" % (row["variant"], row["pool_size"],
                             row["connections_idle_after"]))

    def test_the_fixture_says_whether_it_may_be_published(self):
        verdict = pool.verdict(self.rows)
        self.assertIn("pool_publishable", verdict)
        self.assertEqual(
            verdict["pool_publishable"], True,
            "замер объявлен непечатным: %s" % json.dumps(
                verdict, ensure_ascii=False, sort_keys=True))


class TheFindingsOfTheAxis(unittest.TestCase):
    """ВЫВОДЫ, которые ось печатает, — сверенные с самой фикстурой.

    ЗАЧЕМ ОТДЕЛЬНАЯ ГРУППА. Контроль отвечает на вопрос «меряем ли мы
    пул». Он ничего не говорит о ДВУХ находках, которые печатает README:
    «у crow и drogon пул до потолка не дошёл» и «ожидание слота
    занимает рабочую единицу». Обе держатся на поле `inflight_peak`, и
    ревью показало, чего это стоило: три подмены прямо в фикстуре —
    подтянуть числа при пуле 8 к восьмёрке, объявить занятость восемь при
    семи соединениях, объявить занятость единицей при пуле на 1 — прошли
    зелёными, хотя каждая опровергает печатаемый вывод.
    """

    def setUp(self):
        if not os.path.exists(FIXTURE):
            self.fail("нет замера пула: %s" % FIXTURE)
        self.rows = pool.read(FIXTURE)

    def test_connections_never_exceed_what_was_in_flight(self):
        # Новое соединение пул открывает, только когда все открытые выданы,
        # а обработчиков разом не бывало больше пика. Строка,
        # где соединений больше, — подогнанное число, а не наблюдение.
        for row in self.rows:
            item = pool.reading(row)
            self.assertFalse(
                item["more_connections_than_requests"],
                "%s (пул %d): соединений %d при пике занятости %s — столько "
                "соединений взять было некому"
                % (row["variant"], row["pool_size"],
                   row["connections_observed"], row["inflight_peak"]))

    def test_every_high_row_says_what_it_hit(self):
        # Либо кончились слоты, либо кончились запросы. Строка, не
        # подходящая ни под одно, ничего не подтверждает — а таблица под
        # ней всё равно печаталась бы.
        for variant in VARIANTS:
            row = pool.setting(self.rows, variant, max(pool.SIZES))
            item = pool.reading(row)
            self.assertNotEqual(
                item["saturated_the_pool"], item["below_pool_at_inflight_peak"],
                "%s (пул %d): соединений %d, в полёте %s — строка не "
                "подтверждает ни «упёрлись в пул», ни «пул до потолка не дошёл»"
                % (variant, row["pool_size"], row["connections_observed"],
                   row["inflight_peak"]))

    def test_at_least_one_layer_hit_its_own_ceiling(self):
        # README говорит это находкой — значит за ней обязаны стоять
        # строки. Какие именно варианты, здесь не закрепляется: это
        # свойство прогона, и его держит сверка таблицы README.
        hit = [variant for variant in VARIANTS
               if pool.reading(
                   pool.setting(self.rows, variant, max(pool.SIZES))
               )["below_pool_at_inflight_peak"]]
        self.assertTrue(
            hit,
            "ни у одного варианта пул не остался недобранным, а README печатает это "
            "находкой")

    def test_the_small_pool_shows_the_queue_holding_units(self):
        # Вторая находка: при пуле на 1 в полёте больше одного — значит
        # ожидающие слота рабочую единицу удерживают.
        for variant in VARIANTS:
            row = pool.setting(self.rows, variant, min(pool.SIZES))
            item = pool.reading(row)
            self.assertTrue(
                item["queue_occupies_units"],
                "%s (пул %d): в полёте %s при %d соединениях — ожидание "
                "слота единицу не занимает, а README утверждает обратное"
                % (variant, row["pool_size"], row["inflight_peak"],
                   row["connections_observed"]))

    def test_the_number_is_stable_within_the_session_or_says_it_is_not(self):
        for row in self.rows:
            recomputed = (min(row["connections_by_run"])
                          == max(row["connections_by_run"]))
            self.assertEqual(
                row["connections_stable"], recomputed,
                "%s (пул %d): поле устойчивости разошлось с самими залпами "
                "%s" % (row["variant"], row["pool_size"],
                        row["connections_by_run"]))


class TheReadingRule(unittest.TestCase):
    """Само правило «во что упёрлось» — на придуманных строках.

    ЗАЧЕМ ОТДЕЛЬНО ОТ ФИКСТУРЫ. На настоящем замере каждое поле правила
    имеет ОДНО значение, и правило, отвечающее этим значением всегда,
    зеленеет вместе с фикстурой: диверсия «очередь занимает единицы
    ВСЕГДА» прошла именно так. Спросить правило можно только там, где
    верный ответ — «нет».
    """

    def test_a_saturated_row_is_not_below_the_pool(self):
        item = pool.reading(_row(pool_size=8, connections=8, concurrent=8))
        self.assertTrue(item["saturated_the_pool"])
        self.assertFalse(item["below_pool_at_inflight_peak"])
        self.assertFalse(item["queue_occupies_units"],
                         "соединений столько же, сколько запросов в полёте — "
                         "очереди за слотом не было")

    def test_a_row_below_the_pool_says_so(self):
        row = _row(pool_size=8, connections=5, concurrent=8)
        row["inflight_peak"] = 5
        item = pool.reading(row)
        self.assertFalse(item["saturated_the_pool"])
        self.assertTrue(item["below_pool_at_inflight_peak"])

    def test_a_row_where_nobody_queued_says_so(self):
        # Ровно тот случай, которого нет в фикстуре: занятость равна
        # числу соединений. Правило обязано ответить «нет».
        row = _row(pool_size=1, connections=1, concurrent=8)
        row["inflight_peak"] = 1
        self.assertFalse(
            pool.reading(row)["queue_occupies_units"],
            "в полёте был один — очереди за слотом не было, а правило "
            "утверждает обратное")

    def test_a_row_with_no_occupancy_at_all_claims_nothing(self):
        row = _row(pool_size=8, connections=8, concurrent=8)
        row["inflight_peak"] = None
        item = pool.reading(row)
        self.assertFalse(item["below_pool_at_inflight_peak"])
        self.assertFalse(item["queue_occupies_units"])
        self.assertFalse(item["more_connections_than_requests"])

    def test_more_connections_than_requests_is_spotted(self):
        row = _row(pool_size=8, connections=8, concurrent=8)
        row["inflight_peak"] = 4
        self.assertTrue(pool.reading(row)["more_connections_than_requests"])


class MeasurementComputesWhatItWrites(unittest.TestCase):
    """Поля, которые НЕЛЬЗЯ уронить фикстурой: их пишет замер.

    Подмена в замере не трогает закоммиченную фикстуру, и проверка над
    фикстурой её не видит — она увидела бы ложь только в СЛЕДУЮЩЕМ
    прогоне. Здесь требуется, чтобы величина вычислялась из сырых чисел, а
    не объявлялась. Это проверка ТЕКСТА, и она слабее исполнения: она
    держит форму, а не ответ, — но форма тут единственное, что доступно
    без живого прогона.
    """

    def test_stability_is_computed_from_the_volleys(self):
        source = inspect.getsource(pool.measure)
        self.assertIn(
            "min(by_run) == max(by_run)", source,
            "устойчивость числа объявляется, а не считается по залпам")

    def test_the_probe_links_are_computed_from_the_list(self):
        # Поле «слинкована с libpqxx», объявленное ложью, поймать живым
        # прогоном нельзя, пока проба и правда не слинкована: замер и
        # ложь дают один ответ. Ловится только тем, что величина
        # ВЫЧИСЛЯЕТСЯ из снятого списка.
        source = inspect.getsource(drogonpool.measure)
        for library in ("libpq.so", "libpqxx"):
            self.assertIn(
                'any(name.startswith("%s") for name in linked)' % library,
                source,
                "поле про %s объявляется, а не считается по списку "
                "библиотек пробы" % library)

    def test_the_probe_verdict_is_read_from_the_process(self):
        # «Залп состоялся», объявленный истиной, живым прогоном не
        # поймать: в удачном прогоне замер и ложь совпадают. Поймать
        # можно только тем, что величина читается из вывода процесса.
        source = inspect.getsource(drogonpool.measure)
        self.assertIn('"PROBE DONE" in (proc.stdout or "")', source,
                      "проба объявляет свой залп состоявшимся, а не читает "
                      "это из вывода процесса")
        self.assertIn("proc.returncode", source,
                      "код возврата пробы не попадает в строку замера")

    def test_every_measuring_script_builds_the_image_it_measures(self):
        # Тег внутри демона несёт то, что собралось последним, — хоть бы и
        # из чужого рабочего дерева. Замер по тегу мерит тогда чужой
        # бинарник, а сверка сумм ловит это лишь когда суммы СЛУЧАЙНО
        # разошлись; обратный случай она не ловит вовсе.
        for name in ("run-variants.py", "run-pool.py", "run-db-wait.py"):
            with io.open(os.path.join(STAND, "scripts", name),
                         encoding="utf-8") as handle:
                text = handle.read()
            self.assertIn(
                "build_variant", text,
                "%s меряет образ по тегу, не пересобрав его" % name)


class WhatTheReadmeClaims(unittest.TestCase):
    """README и фикстура обязаны говорить одно и то же.

    ЗАЧЕМ. Пересъём оси 1 сдвинул живость drogon (обращений 15, ответов
    14), а README в том же правленом абзаце продолжал утверждать, что
    маршрут живости ответил при КАЖДОМ обращении у всех трёх. Ни одна
    проверка это поле не читала, и расхождение прошло молча. Текст стенда
    — такая же поверхность, как код и фикстура: читатель статьи увидит
    именно его.
    """

    def setUp(self):
        with io.open(os.path.join(STAND, "README.md"),
                     encoding="utf-8") as handle:
            self.readme = handle.read()

    def _table(self, header):
        """Строки таблицы README, идущей сразу за названным заголовком."""
        self.assertIn(header, self.readme,
                      "в README нет таблицы с заголовком %r" % header)
        start = self.readme.index(header)
        rows = {}
        for line in self.readme[start:].splitlines():
            if not line.startswith("|"):
                if rows:
                    break
                continue
            cells = [cell.strip() for cell in line.strip("|").split("|")]
            if cells[0] in VARIANTS:
                rows[cells[0]] = cells[1:]
        return rows

    def test_the_pool_table_matches_the_fixture(self):
        if not os.path.exists(FIXTURE):
            self.fail("нет замера пула: %s" % FIXTURE)
        rows = pool.read(FIXTURE)
        printed = self._table("| вариант | пул 1 | пул 8 |")
        self.assertEqual(sorted(printed), sorted(VARIANTS),
                         "в таблице README не все варианты: %s" % printed)
        for variant, cells in printed.items():
            low, high, inflight = cells[0], cells[1], cells[2]
            self.assertEqual(
                int(low), pool.connections(rows, variant, min(pool.SIZES)),
                "%s: README печатает при малом пуле не то, что в фикстуре"
                % variant)
            self.assertEqual(
                int(high), pool.connections(rows, variant, max(pool.SIZES)),
                "%s: README печатает при большом пуле не то, что в фикстуре"
                % variant)
            self.assertEqual(
                int(inflight),
                pool.setting(rows, variant, max(pool.SIZES))["inflight_peak"],
                "%s: README печатает занятость не из фикстуры" % variant)

    def test_the_liveness_table_matches_the_fixture(self):
        path = os.path.join(STAND, "fixtures", "variants.jsonl")
        if not os.path.exists(path):
            self.fail("нет замера вариантов: %s" % path)
        measured = {}
        with io.open(path, encoding="utf-8") as handle:
            for line in handle:
                if line.strip().startswith("{"):
                    row = json.loads(line)
                    measured[row["variant"]] = row["liveness"]
        printed = self._table("| вариант | обращений к живости |")
        self.assertEqual(sorted(printed), sorted(VARIANTS),
                         "в таблице живости README не все варианты")
        for variant, cells in printed.items():
            asks, answers = int(cells[0]), int(cells[1])
            self.assertEqual(
                (asks, answers),
                (measured[variant]["asks"], measured[variant]["answers"]),
                "%s: README печатает живость не из фикстуры (%d из %d против "
                "%d из %d)" % (variant, answers, asks,
                               measured[variant]["answers"],
                               measured[variant]["asks"]))

    def test_the_readme_does_not_promise_more_liveness_than_measured(self):
        # Отдельная проверка от таблицы: числа можно напечатать верно и
        # рядом написать вывод, которого из них не следует. Ровно это и
        # случилось: «не отказывает и не виснет — держится у всех трёх».
        path = os.path.join(STAND, "fixtures", "variants.jsonl")
        shaky = []
        with io.open(path, encoding="utf-8") as handle:
            for line in handle:
                if line.strip().startswith("{"):
                    row = json.loads(line)
                    if not row["liveness"]["every_call_got_a_value"]:
                        shaky.append(row["variant"])
        if shaky:
            self.assertNotIn(
                "держится у всех трёх", self.readme,
                "у %s не каждое обращение к живости получило ответ, а README "
                "утверждает, что держится у всех трёх" % ", ".join(shaky))


class ControlRule(unittest.TestCase):
    """Само правило вывода — на придуманных строках, где его можно уронить."""

    def test_control_fails_when_the_counter_does_not_move(self):
        verdict = pool.verdict(_all_variants(low=4, high=4))
        self.assertFalse(verdict["pool_publishable"])
        self.assertFalse(verdict["by_variant"]["beast"]["knob_moves_counter"])

    def test_control_fails_when_the_counter_moves_the_wrong_way(self):
        verdict = pool.verdict(_all_variants(low=8, high=1))
        self.assertFalse(verdict["pool_publishable"])

    def test_control_fails_for_one_variant_out_of_three(self):
        # «У двух сошлось» — не контроль: печатается таблица целиком.
        rows = _pair("beast") + _pair("crow") + _pair("drogon", low=4, high=4)
        verdict = pool.verdict(rows)
        self.assertFalse(
            verdict["pool_publishable"],
            "контроль не сошёлся у drogon, а таблица объявлена печатной")

    def test_control_fails_when_the_small_pool_opened_more_than_its_size(self):
        # Счётчик пошёл за настройкой (2 < 8), но пул на 1 выдал два
        # соединения разом — значит слот не ограничивает ничего.
        verdict = pool.verdict(_all_variants(low=2, high=8))
        self.assertTrue(verdict["by_variant"]["beast"]["knob_moves_counter"])
        self.assertFalse(verdict["by_variant"]["beast"]["low_within_pool"])
        self.assertFalse(verdict["pool_publishable"])

    def test_a_leak_makes_the_axis_unpublishable(self):
        verdict = pool.verdict(_all_variants(idle_after=9))
        self.assertFalse(verdict["pool_publishable"])
        self.assertFalse(verdict["no_leak"])

    def test_the_leak_is_measured_against_the_pool_and_not_the_volley(self):
        # РАЗЛИЧАЮЩИЙ СЛУЧАЙ. Остаток ровно в размер залпа: правило,
        # сверяющее его с числом ОДНОВРЕМЕННЫХ ЗАПРОСОВ, признало бы это
        # нормой, а правило, сверяющее с числом СЛОТОВ, — утечкой. Прежняя
        # проверка брала остаток больше обоих чисел и оба правила
        # проходила одинаково, поэтому подмена одного на другое ничего не
        # роняла.
        rows = _pair("beast") + _pair("crow") + _pair("drogon")
        for row in rows:
            if row["pool_size"] == 1:
                row["connections_idle_after"] = row["concurrent"]
                row["connections_idle_after_by_run"] = [row["concurrent"]] * 2
        verdict = pool.verdict(rows)
        self.assertFalse(
            verdict["no_leak"],
            "пул на один слот оставил восемь простаивающих соединений, и это "
            "не названо утечкой")
        self.assertFalse(verdict["pool_publishable"])

    def test_more_connections_than_requests_stops_publication(self):
        # Соединений больше, чем запросов бывало в полёте, взять было
        # некому: это подогнанное число.
        # ЧИСЛА ПОДОБРАНЫ ТАК, ЧТОБЫ БОЛЬШЕ НИЧЕГО НЕ НАРУШАЛОСЬ. Прежняя
        # редакция брала соединений больше размера пула — и строку
        # роняла другая защита (потолок), а запрет на невозможное число
        # оставался непроверенным: диверсия, убравшая его из условия
        # печати, прошла зелёной.
        rows = _all_variants()
        rows[1]["inflight_peak"] = 4
        verdict = pool.verdict(rows)
        self.assertTrue(verdict["by_variant"]["beast"]["knob_moves_counter"])
        self.assertTrue(verdict["by_variant"]["beast"]["high_within_pool"])
        self.assertEqual(verdict["rows_with_impossible_numbers"], ["beast@8"])
        self.assertFalse(verdict["pool_publishable"])

    def test_a_row_without_the_before_reading_is_refused(self):
        # «До залпа ничего не было открыто» на ОТСУТСТВУЮЩЕМ поле
        # проходило зелёным — проверка отсутствия на пустых данных.
        rows = _all_variants()
        del rows[0]["connections_before"]
        with self.assertRaises(KeyError):
            pool.verdict(rows)

    def test_the_axis_says_whether_every_number_held_across_volleys(self):
        rows = _all_variants()
        rows[3]["connections_stable"] = False
        self.assertFalse(
            pool.verdict(rows)["every_number_stable_within_the_session"])
        self.assertTrue(
            pool.verdict(_all_variants())[
                "every_number_stable_within_the_session"])

    def test_unserved_probes_make_the_axis_unpublishable(self):
        # Соединений мало потому, что запросы не дошли, — это не пул.
        verdict = pool.verdict(_all_variants(not_served=3))
        self.assertFalse(verdict["pool_publishable"])

    def test_settings_with_different_run_counts_are_refused(self):
        # Больший максимум добывается бо́льшим числом залпов, а не
        # подключённой ручкой. Тот же довод, что у контроля оси 1.
        # Поле `runs` при этом НЕ трогается: его разъезд ловит другая
        # проверка — «пара различается ещё чем-то», — и сдвинь я оба, обе
        # диверсии краснели бы от одной защиты, а вторая оставалась бы
        # непроверенной. Ровно так эта проверка и зеленела сначала.
        rows = _pair("beast")
        rows[1]["connections_by_run"] = [8, 8, 8]
        with self.assertRaises(ValueError):
            pool.verdict(rows + _pair("crow") + _pair("drogon"))

    def test_a_pair_that_differs_in_anything_else_is_refused(self):
        # Контроль — пара, различающаяся ТОЛЬКО размером пула. Настройка,
        # поехавшая заодно, делает из него сравнение двух разных замеров,
        # и разницу объясняли бы две причины сразу.
        for field, value in (("concurrent", 16), ("workers", 2),
                             ("sleep_ms", 50), ("driver", "libpq"),
                             ("entry_binary_sha256", "f" * 64)):
            rows = _pair("beast") + _pair("crow") + _pair("drogon")
            rows[1][field] = value
            with self.assertRaises(ValueError, msg="поле %s" % field):
                pool.verdict(rows)

    def test_the_table_refuses_to_print_when_the_control_failed(self):
        # Запрет живёт в модуле, а не в печатающем сценарии: сценарий —
        # самое удобное место обойти его, не тронув ни одной проверки.
        with self.assertRaises(ValueError):
            pool.table(_all_variants(low=4, high=4))

    def test_the_table_prints_no_word_about_speed(self):
        lines = " ".join(pool.table(_all_variants()))
        for forbidden in ("быстр", "медленн", "RPS", "секунд", "мс",
                          "задержк", "пропускн"):
            self.assertNotIn(
                forbidden, lines,
                "в таблице оси 2 появилось слово о скорости: %s" % forbidden)

    def test_a_missing_setting_is_refused_and_not_defaulted(self):
        with self.assertRaises(KeyError):
            pool.connections(_pair("beast"), "beast", pool_size=4)

    def test_published_number_is_the_worst_run_not_the_luckiest(self):
        # Утверждение «при пуле на 1 соединение было одно» обязано
        # выдерживать КАЖДЫЙ залп: печатается наибольшее наблюдённое.
        row = _row(pool_size=1, connections=1)
        row["connections_by_run"] = [1, 1, 3]
        self.assertEqual(pool.observed(row["connections_by_run"]), 3)

    def test_an_empty_run_list_is_refused(self):
        with self.assertRaises(ValueError):
            pool.observed([])


class TheNumberComesFromTheDatabase(unittest.TestCase):
    """Тавтология ловится здесь: число обязано приезжать ИЗ БАЗЫ.

    Счётчик, взятый из размера пула или из ответа сервиса, подтвердил бы
    контроль всегда — и не сказал бы ничего.
    """

    def test_sampler_asks_the_database_about_this_variant(self):
        asked = []
        original = pg.connections
        pg.connections = lambda name, **kwargs: (asked.append(name) or 7)
        try:
            sampler = pool.sampler_for("crow")
            self.assertEqual(sampler(), 7)
        finally:
            pg.connections = original
        self.assertEqual(asked, ["crow"],
                         "сампер спросил базу не про тот вариант: %s" % asked)

    def test_the_drogon_probe_counts_its_own_connections_in_the_database(self):
        # Число собственного пула подменяется настройкой так же легко:
        # «пул на 8 показал 8» верно всегда, если восьмёрку взяли из
        # аргумента. Здесь спрашивается, КОГО спрашивает проба.
        asked = []
        original = pg.connections
        pg.connections = lambda name, **kwargs: (asked.append(name) or 3)
        try:
            self.assertEqual(drogonpool.sampler()(), 3)
        finally:
            pg.connections = original
        self.assertEqual(asked, [drogonpool.APPLICATION_NAME])

    def test_sampler_does_not_invent_a_number_when_the_database_refuses(self):
        original = pg.connections

        def refuse(name, **kwargs):
            raise RuntimeError("база не ответила числом")

        pg.connections = refuse
        try:
            with self.assertRaises(RuntimeError):
                pool.sampler_for("crow")()
        finally:
            pg.connections = original


class TheKnobIsPassedAtLaunch(unittest.TestCase):
    """Размер пула приходит ПРИ ЗАПУСКЕ и не запечён в образ."""

    def test_run_command_passes_the_pool_size(self):
        cmd = dockerlib.run_variant_command("crow", workers=4, pool=8)
        self.assertIn("STAND_POOL=8", " ".join(cmd))

    def test_pool_size_is_a_knob_like_the_others(self):
        self.assertIn("STAND_POOL", dockerlib.KNOBS)

    def test_environment_cannot_override_the_pool_size(self):
        with self.assertRaises(ValueError):
            dockerlib.run_variant_command("crow", workers=4, pool=8,
                                          env={"STAND_POOL": "подмена"})

    def test_a_pool_size_that_is_not_a_count_is_refused(self):
        for bad in (0, -1, "восемь", None, True):
            with self.assertRaises((ValueError, TypeError)):
                dockerlib.run_variant_command("crow", workers=4, pool=bad)

    def test_the_pool_for_the_other_axes_is_not_below_a_volley(self):
        # Обоснование этой связи написано в комментарии к константе, а
        # проверки не было: пул, опущенный ниже числа проб, сделал бы
        # оси 1 и 4 замером ПУЛА, а не слоя, и молча — их фикстуры про
        # размер пула ничего не утверждают.
        import scripts.dbwait as dbwait
        self.assertGreaterEqual(
            dockerlib.POOL_FOR_OTHER_AXES,
            max(dockerlib.PROBE_COUNT, dbwait.PROBES),
            "пул для остальных осей ниже их залпа: ожидание упиралось бы в "
            "слот, а не в рабочую единицу")

    def test_the_volley_barrier_has_a_deadline(self):
        # Барьер без срока вешает замер, а повисший прогон неотличим от
        # долгого. Стенд сам про это пишет в реестре диверсий.
        self.assertIsInstance(pool.BARRIER_TIMEOUT_S, float)
        self.assertGreater(pool.BARRIER_TIMEOUT_S, 0)
        source = inspect.getsource(pool.volley)
        self.assertIn("timeout=BARRIER_TIMEOUT_S", source,
                      "залп ставит барьер без срока")

    def test_the_axis_one_rows_say_at_what_pool_they_were_taken(self):
        # Размер пула — такое же условие замера, как число рабочих
        # единиц. Замер, переставший его писать, молча потерял бы
        # условие: фикстура выглядела бы полной.
        path = os.path.join(STAND, "fixtures", "variants.jsonl")
        if not os.path.exists(path):
            self.fail("нет замера вариантов: %s" % path)
        with io.open(path, encoding="utf-8") as handle:
            for line in handle:
                if not line.strip().startswith("{"):
                    continue
                row = json.loads(line)
                self.assertIn(
                    "pool", row,
                    "%s: строка оси 1 не говорит, при каком размере пула "
                    "снята" % row["variant"])
                self.assertEqual(row["pool"], dockerlib.POOL_FOR_OTHER_AXES)

    def test_the_size_is_not_baked_into_any_image(self):
        for name in VARIANTS:
            path = os.path.join(STAND, "images",
                                dockerlib.variants()[name]["dockerfile"])
            with io.open(path, encoding="utf-8") as handle:
                text = handle.read()
            self.assertNotIn(
                "STAND_POOL", text,
                "%s: размер пула упомянут в образе — запечённое значение "
                "склеило бы две настройки в одну строку фикстуры" % name)


class TheAsymmetryIsMeasuredAndNotDeclared(unittest.TestCase):
    """«У Drogon пул свой» — утверждение, и у него обязан быть замер.

    Спека предвидела асимметрию: у Drogon пул свой, у Beast и Crow его
    нет. Проба до прогона показала, что в стенде пула не было НИ У КОГО,
    и в саму ось собственный пул drogon не берётся: он ходит в базу через
    libpq самого фреймворка, а два других слоя — через libpqxx, и ось
    сравнивала бы драйверы. Но раз утверждение делается — оно замеряется.
    """

    def setUp(self):
        if not os.path.exists(FIXTURE):
            self.fail("нет замера пула: %s" % FIXTURE)
        self.rows = pool.read(FIXTURE, kind="drogon-own-pool")

    def test_the_framework_pool_was_actually_run(self):
        self.assertTrue(
            self.rows,
            "строки kind=drogon-own-pool нет: асимметрия объявлена, но не "
            "замерена")
        for row in self.rows:
            self.assertNotEqual(
                row["driver"], pool.DRIVER,
                "замер собственного пула drogon снят тем же клиентом, что и "
                "ось, — тогда он не про собственный пул")
            self.assertFalse(
                row["used_in_axis"],
                "собственный пул drogon помечен как участник оси: ось "
                "сравнивала бы драйверы, а не слои")

    def test_the_framework_probe_links_another_database_client(self):
        # ОСНОВАНИЕ ИСКЛЮЧЕНИЯ — ЗАМЕР, А НЕ ЯРЛЫК. Строка не входит в ось
        # потому, что ходит в базу ДРУГИМ клиентом; до сих пор это
        # держалось на константе в коде, и диверсия, дописавшая -lpqxx в
        # команду сборки пробы, проходила зелёной. Теперь список
        # библиотек снимается с собранного бинарника.
        for row in self.rows:
            self.assertTrue(
                row["probe_links"],
                "у пробы собственного пула не снят список библиотек — "
                "«другой клиент базы» остаётся словами")
            self.assertTrue(
                row["links_libpq"],
                "проба собственного пула не слинкована с libpq: тогда она "
                "ходит в базу не тем, чем объявлено")
            self.assertFalse(
                row["links_libpqxx"],
                "проба собственного пула слинкована с libpqxx — тем же "
                "клиентом, что и ось; тогда исключать её из оси незачем, а "
                "объяснение неверно")
            # ВЫВОДЫ ПЕРЕСЧИТЫВАЮТСЯ ИЗ СПИСКА. Иначе «libpqxx нет» —
            # снова ярлык, только теперь рядом со списком, который его
            # опровергает.
            self.assertEqual(
                row["links_libpq"],
                any(name.startswith("libpq.so") for name in row["probe_links"]),
                "поле про libpq разошлось со списком библиотек пробы")
            self.assertEqual(
                row["links_libpqxx"],
                any(name.startswith("libpqxx") for name in row["probe_links"]),
                "поле про libpqxx разошлось со списком библиотек пробы")

    def test_the_framework_probe_actually_ran(self):
        # Строка, объявляющая залп состоявшимся не глядя на процесс, —
        # это объявление, а не замер.
        for row in self.rows:
            self.assertEqual(
                row["probe_exit_code"], 0,
                "проба собственного пула завершилась с кодом %s, а её число "
                "всё равно напечатано" % row["probe_exit_code"])
            self.assertTrue(
                row["probe_finished_every_query"],
                "проба собственного пула не довела свой залп до конца")

    def test_the_framework_pool_number_follows_its_own_setting(self):
        by_size = {row["pool_size"]: row["connections_observed"]
                   for row in self.rows}
        self.assertGreaterEqual(len(by_size), 2,
                                "у собственного пула замерен один размер: "
                                "подключённость ручки этим не показать")
        low, high = min(by_size), max(by_size)
        self.assertLess(
            by_size[low], by_size[high],
            "собственный пул drogon не пошёл за своей настройкой (%d против "
            "%d)" % (by_size[low], by_size[high]))


class LivePoolMeasurement(unittest.TestCase):
    """Тот же контроль, но сейчас и на живых образах.

    Пропуск здесь — не зелёный: без докера проверка объявляет себя
    пропущенной. Запускать: STAND_WITH_DOCKER=1.
    """

    def setUp(self):
        if os.environ.get("STAND_WITH_DOCKER") != "1":
            self.skipTest("замер требует докера: STAND_WITH_DOCKER=1")
        if shutil.which("docker") is None:
            self.skipTest("docker не найден")

    def test_a_fresh_framework_probe_proves_its_own_client(self):
        # Чем проба СОБРАЛАСЬ, видно только если её собрать: команда
        # сборки живёт в python-файле, и подмена в ней закоммиченную
        # фикстуру не трогает — ложь приехала бы только со следующим
        # прогоном. Здесь она приезжает сразу.
        dockerlib.build_variant("drogon")
        database = pg.start(statements=False)
        try:
            row = drogonpool.measure(1, concurrent=2)
        finally:
            database.stop()
        self.assertTrue(row["probe_links"], "список библиотек пробы пуст")
        self.assertEqual(
            row["links_libpqxx"],
            any(name.startswith("libpqxx") for name in row["probe_links"]),
            "поле про libpqxx разошлось со списком библиотек")
        self.assertFalse(
            row["links_libpqxx"],
            "свежая проба собственного пула слинкована с libpqxx — тем же "
            "клиентом, что и ось: основание исключать её из оси исчезло")
        self.assertTrue(row["links_libpq"])
        self.assertEqual(
            row["probe_finished_every_query"],
            "PROBE DONE" in row["probe_output"],
            "проба объявляет свой залп состоявшимся, не глядя на процесс")

    def test_a_fresh_axis_one_row_says_at_what_pool_it_was_taken(self):
        # Условие замера, исчезнувшее из строки, не видно по
        # закоммиченной фикстуре: она-то поле несёт.
        dockerlib.build_variant("beast")
        # Настройки — те же, что у боевого замера по нижней и верхней
        # точке: вывод занятости у него привязан к верхней, и набор без
        # неё не собрать.
        row = dockerlib.measure_variant(
            "beast", runs=1,
            settings=(dockerlib.LOW_WORKERS, dockerlib.HIGH_WORKERS))
        self.assertIn(
            "pool", row,
            "свежая строка оси 1 не говорит, при каком размере пула снята")
        self.assertEqual(row["pool"], dockerlib.POOL_FOR_OTHER_AXES)

    def test_a_fresh_pair_still_moves_the_counter(self):
        name = "beast"
        dockerlib.build_variant(name)
        database = pg.start(statements=False)
        try:
            fresh = [pool.measure(name, pool_size=size, runs=1)
                     for size in pool.SIZES]
        finally:
            database.stop()
        one = pool.connections(fresh, name, pool_size=1)
        many = pool.connections(fresh, name, pool_size=8)
        self.assertLessEqual(
            one, 1, "%s: свежий замер при пуле на 1 открыл %d соединений"
            % (name, one))
        self.assertLess(
            one, many,
            "%s: свежий счётчик не пошёл за размером пула (%d против %d)"
            % (name, one, many))


if __name__ == "__main__":
    unittest.main()
