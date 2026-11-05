"""Проверки оси 3 — параметр против литерала, как его видно СНАРУЖИ.

НАПИСАНЫ ДО РЕАЛИЗАЦИИ и на своём коммите падают целиком: модуля
`scripts/prepared.py` ещё нет, фикстуры тоже. Это записанный шаг, а не
случайность.

ЧТО ЗДЕСЬ ГЛАВНОЕ. Ось снята не тем, чем её задумывали. Прежняя
постановка — счёт строк в `pg_prepared_statements` — отозвана: чужая
сессия видит там ноль ВСЕГДА, и отрицательный контроль прошёл бы
одинаково у обоих плеч, подтверждая устройство вопроса, а не устройство
драйвера. Поэтому признак читается из `pg_stat_activity`: текст живого
запроса виден целиком, и параметр в нём отличается от подставленной
величины.

ТРИ ЛОВУШКИ, ПОД КОТОРЫЕ ЗДЕСЬ ПОДЛОЖЕНЫ ПРОВЕРКИ:

* КОНТРОЛЬ НА ПУСТЫХ ДАННЫХ. «У литерального плеча параметризованных
  вызовов ноль» проходит и тогда, когда снять не удалось НИ ОДНОГО
  текста. Ноль наблюдений неотличим от нуля параметров, если не
  потребовать наблюдений отдельно;
* ТАВТОЛОГИЯ. Разбор, заглянувший в объявленный режим плеча
  (`sql_mode`), подтвердил бы себя всегда. Признак обязан читаться из
  ТЕКСТА;
* ПОДСТРОКА ЧЕРЕЗ `LIKE`. В `LIKE` подчёркивание — метасимвол, и
  `pg_sleep` ловил бы чужие строки. Это замерено задачей 3, и число
  вызовов обязано читаться через `position`.
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
from scripts import pg  # noqa: E402
from scripts import prepared  # noqa: E402

FIXTURE = os.path.join(STAND, "fixtures", "prepared.jsonl")
PARAM_ARMS = ("beast", "crow", "drogon")
CONTROL = prepared.CONTROL

PARAM_TEXT = "SELECT pg_sleep($1)"
LITERAL_TEXT = "SELECT pg_sleep(1.500)"


def _row(variant="beast", sql_mode="param", texts=None, requests=4,
         calls_delta=None, **extra):
    """Придуманная строка замера — чтобы правило вывода можно было ронять.

    На настоящей фикстуре контроль сходится, и испорченное правило
    зеленело бы вместе с ней. Поэтому правило проверяется на строках,
    которых не было ни в одном прогоне.
    """
    if texts is None:
        texts = [PARAM_TEXT if sql_mode == "param" else LITERAL_TEXT] * 3
    counts = {"parameterized": 0, "literal": 0, "other": 0}
    for text in texts:
        counts[prepared.classify(text)] += 1
    row = {
        "kind": "prepared",
        "variant": variant,
        "sql_mode": sql_mode,
        "driver": prepared.DRIVER,
        "requests": requests,
        "served": requests,
        "not_served": 0,
        "workers": prepared.WORKERS,
        "pool_size": prepared.POOL,
        "sleep_ms": prepared.SLEEP_MS,
        "calls_fragment": prepared.FRAGMENT,
        "calls_before": 0,
        "calls_after": requests if calls_delta is None else calls_delta,
        "calls_delta": requests if calls_delta is None else calls_delta,
        "entry_binary_sha256": "0" * 64,
        # Признак «тот же клиент базы» есть только у контрольной строки:
        # он снимается с бинарника ПРОБЫ (ldd), а у варианта клиент
        # известен из образа.
        "links_libpqxx": variant == prepared.CONTROL,
        "probe_links": (["libpqxx-7.10.so", "libpq.so.5", "libc.so.6"]
                        if variant == prepared.CONTROL else []),
        "texts_sampled": len(texts),
        "distinct_texts": sorted(set(texts)),
        "parameterized_texts": counts["parameterized"],
        "literal_texts": counts["literal"],
        "other_texts": counts["other"],
    }
    row.update(extra)
    return row


def _four_arms(**extra):
    rows = [_row(variant=name, sql_mode="param", **extra)
            for name in PARAM_ARMS]
    rows.append(_row(variant=CONTROL, sql_mode="literal", **extra))
    return rows


class TheClassification(unittest.TestCase):
    """Признак читается из ТЕКСТА и ниоткуда больше."""

    def test_the_parameter_marker_makes_it_parameterized(self):
        self.assertEqual(prepared.classify("SELECT pg_sleep($1)"),
                         "parameterized")

    def test_a_pasted_value_makes_it_literal(self):
        self.assertEqual(prepared.classify("SELECT pg_sleep(1.500)"),
                         "literal")

    def test_a_text_with_both_is_neither(self):
        # Текст, где есть и параметр, и число, ни о чём не говорит: такую
        # строку нельзя молча засчитать ни одному плечу.
        self.assertEqual(prepared.classify("SELECT pg_sleep($1), pg_sleep(2)"),
                         "other")

    def test_a_foreign_query_is_neither(self):
        self.assertEqual(prepared.classify("SELECT 1"), "other")

    def test_classification_sees_only_the_text(self):
        # Тавтология ловится здесь: у разбора не должно быть доступа ни к
        # объявленному режиму плеча, ни к строке замера целиком.
        names = list(inspect.signature(prepared.classify).parameters)
        self.assertEqual(names, ["text"],
                         "разбор принимает не только текст — значит, может "
                         "подтвердить сам себя")


class TheNegativeControl(unittest.TestCase):
    """Без него один лишь факт «текст такой-то» не доказывает ничего."""

    def test_the_literal_arm_shows_no_parameterized_call(self):
        rows = _four_arms()
        self.assertEqual(
            prepared.parameterized(rows, CONTROL), 0,
            "у плеча с литеральным запросом нашёлся параметризованный "
            "вызов — признак различает не то, что мы думаем")
        self.assertTrue(prepared.verdict(rows)["prepared_publishable"])

    def test_a_parameterized_text_in_the_control_arm_breaks_the_axis(self):
        rows = _four_arms()
        rows[-1] = _row(variant=CONTROL, sql_mode="literal",
                        texts=[LITERAL_TEXT, PARAM_TEXT])
        decision = prepared.verdict(rows)
        self.assertFalse(
            decision["control_shows_only_literal"],
            "контрольное плечо показало параметр, а разбор этого не заметил")
        self.assertFalse(decision["prepared_publishable"])

    def test_the_control_arm_declared_literal_but_running_parameterized(self):
        # Плечо, ТАЙНО получившее параметризованный текст (например,
        # переменная режима не доехала до контейнера), объявлено
        # литеральным и выглядит настроенным. Разбор обязан смотреть на
        # текст, а не на объявление.
        rows = _four_arms()
        rows[-1] = _row(variant=CONTROL, sql_mode="literal",
                        texts=[PARAM_TEXT, PARAM_TEXT])
        self.assertFalse(prepared.verdict(rows)["prepared_publishable"])

    def test_a_literal_text_in_a_parameterized_arm_breaks_the_axis(self):
        rows = _four_arms()
        rows[0] = _row(variant=PARAM_ARMS[0], sql_mode="param",
                       texts=[PARAM_TEXT, LITERAL_TEXT])
        decision = prepared.verdict(rows)
        self.assertFalse(decision["parameterized_arms_show_only_the_parameter"])
        self.assertFalse(decision["prepared_publishable"])

    def test_a_missing_control_arm_is_a_refusal(self):
        rows = [_row(variant=name) for name in PARAM_ARMS]
        with self.assertRaises(KeyError):
            prepared.verdict(rows)


class TheControlNeedsData(unittest.TestCase):
    """Ноль наблюдений — не ноль параметров."""

    def test_an_arm_with_no_texts_is_not_publishable(self):
        rows = _four_arms()
        rows[-1] = _row(variant=CONTROL, sql_mode="literal", texts=[])
        decision = prepared.verdict(rows)
        self.assertEqual(prepared.parameterized(rows, CONTROL), 0,
                         "на пустых данных счёт параметров и так ноль")
        self.assertFalse(
            decision["every_arm_was_seen_at_work"],
            "плечо, у которого не снято ни одного текста, прошло как "
            "подтверждение — это проверка отсутствия на пустых данных")
        self.assertFalse(decision["prepared_publishable"])

    def test_an_arm_that_never_reached_the_database_is_not_publishable(self):
        rows = _four_arms()
        rows[1] = _row(variant=PARAM_ARMS[1], calls_delta=0)
        decision = prepared.verdict(rows)
        self.assertFalse(decision["every_arm_reached_the_database"])
        self.assertFalse(decision["prepared_publishable"])

    def test_a_call_count_that_misses_the_requests_is_recorded(self):
        # Не запрет печати, а оговорка: расхождение не делает текст
        # неверным, но запрещает называть прирост числом вызовов плеча.
        rows = _four_arms()
        rows[1] = _row(variant=PARAM_ARMS[1], calls_delta=9)
        self.assertFalse(prepared.verdict(rows)["calls_match_requests_everywhere"])


class TheControlUsesTheSameDatabaseClient(unittest.TestCase):
    """Контроль обязан различаться текстом запроса, а не драйвером."""

    def test_the_client_flag_is_computed_from_the_snapped_libraries(self):
        # Флаг, выставленный константой, — ярлык, а не замер: диверсия,
        # заменившая вычисление на True, прошла зелёной именно так.
        source = inspect.getsource(prepared.measure_control)
        self.assertIn('name.startswith("libpqxx") for name in linked', source,
                      "признак клиента базы не вычисляется из снятого ldd")

    def test_the_flag_must_agree_with_the_snapped_libraries(self):
        # Строка, объявившая клиента без библиотеки в списке, — подделка
        # ровно того же рода.
        rows = _four_arms()
        rows[-1] = _row(variant=CONTROL, sql_mode="literal",
                        links_libpqxx=True, probe_links=["libc.so.6"])
        decision = prepared.verdict(rows)
        self.assertFalse(decision["control_uses_the_same_database_client"])
        self.assertFalse(decision["prepared_publishable"])

    def test_a_control_with_another_database_client_breaks_the_control(self):
        # Проба, собравшаяся другим клиентом, сравнивала бы драйверы, а не
        # тексты. Признак снимается с бинарника (`ldd`), а не объявляется:
        # ровно на этом ревью оси 2 поймало пробу собственного пула —
        # ярлык в коде остался прежним, а линковка уехала.
        rows = _four_arms()
        rows[-1] = _row(variant=CONTROL, sql_mode="literal",
                        links_libpqxx=False)
        decision = prepared.verdict(rows)
        self.assertFalse(decision["control_uses_the_same_database_client"])
        self.assertFalse(decision["prepared_publishable"])

    def test_a_second_driver_breaks_the_comparison(self):
        rows = _four_arms()
        rows[0] = _row(variant=PARAM_ARMS[0], driver="libpq")
        self.assertFalse(prepared.verdict(rows)["prepared_publishable"])


class TheTableRefuses(unittest.TestCase):
    """Запрет живёт в модуле, а не в печатающем сценарии."""

    def test_the_table_is_not_printed_when_the_control_fails(self):
        rows = _four_arms()
        rows[-1] = _row(variant=CONTROL, sql_mode="literal",
                        texts=[PARAM_TEXT])
        with self.assertRaises(ValueError):
            prepared.table(rows)

    def test_the_table_says_nothing_about_speed(self):
        for line in prepared.table(_four_arms()):
            for word in ("мс", "секунд", "быстр", "медлен", "rps", "задержк"):
                self.assertNotIn(word, line.lower(),
                                 "в таблице оси 3 появилось слово о скорости")


class TheNumbersComeFromTheDatabase(unittest.TestCase):
    """Источник числа — часть утверждения, а не подробность."""

    def test_texts_are_read_from_pg_stat_activity(self):
        source = inspect.getsource(prepared.measure)
        self.assertIn("active_queries", source,
                      "тексты берутся не из pg_stat_activity — тогда они "
                      "говорят про наш замер, а не про базу")

    def test_calls_are_counted_by_position_and_not_by_like(self):
        # Подстрока `pg_sleep` содержит подчёркивание, а в LIKE это
        # метасимвол: замер задачи 3 показал сумму 2 вместо 1. Счёт
        # вызовов обязан идти через pg.statement_calls, который читает
        # position.
        self.assertIn("statement_calls", inspect.getsource(prepared.measure))
        self.assertIn("position(", inspect.getsource(pg.statement_calls))
        self.assertNotIn(" like ", inspect.getsource(pg.statement_calls).lower())

    def test_the_service_has_no_knob_for_the_query_text(self):
        # У сервиса НЕТ ручки текста запроса, и это существенно: вариант
        # шлёт то, что написано в его исходнике. Замер, выставляющий
        # такую переменную, сообщал бы о настройке, которой не существует,
        # — и строка фикстуры выглядела бы настроенной.
        self.assertNotIn("STAND_SQL", inspect.getsource(prepared))
        self.assertNotIn("STAND_SQL",
                         io.open(os.path.join(STAND, "service", "main.cpp"),
                                 encoding="utf-8").read())

    def test_the_control_probe_names_itself_in_the_database(self):
        # Проба без своего имени приложения считалась бы вместе с
        # вариантом: тексты читаются по application_name.
        source = inspect.getsource(prepared.measure_control)
        self.assertIn("STAND_APPLICATION_NAME", source)
        self.assertIn("active_queries(CONTROL)", source)

    def test_the_control_probe_does_not_touch_the_service(self):
        # Главный урок этой задачи: текст запроса живёт в ОБЩЕЙ логике
        # сервиса, и ветка с литералом там меняет бинарник всех трёх
        # вариантов — а его сумма записана в фикстурах осей 1, 2 и 4.
        # Литеральный текст обязан жить в пробе и только в ней.
        service = io.open(os.path.join(STAND, "service", "main.cpp"),
                          encoding="utf-8").read()
        self.assertEqual(service.count("pg_sleep"), 1,
                         "в общей логике сервиса появился второй текст "
                         "запроса — это переснятые фикстуры трёх чужих осей")
        probe = io.open(os.path.join(STAND, "service",
                                     "literal_sql_probe.cpp"),
                        encoding="utf-8").read()
        self.assertIn("pg_sleep", probe)
        self.assertNotIn("exec_params", probe,
                         "контрольная проба шлёт параметр — тогда она не "
                         "контроль, а четвёртый такой же")

    def test_the_control_arm_is_not_declared_in_variants_json(self):
        # Четвёртое плечо в тройку сравнения не входит: попав в
        # variants.json, оно поехало бы в оси 1, 2 и 4 и разъехалось бы с
        # их фикстурами.
        self.assertNotIn(CONTROL, dockerlib.variants(),
                         "контроль объявлен наравне с тремя вариантами — "
                         "он попадёт в чужие оси")
        self.assertEqual(sorted(dockerlib.variants()), sorted(PARAM_ARMS))


class OnTheFixture(unittest.TestCase):
    """Ось 3 на ТОМ замере, на который будет ссылаться статья."""

    def setUp(self):
        if not os.path.exists(FIXTURE):
            self.fail("нет замера оси 3: %s" % FIXTURE)
        self.rows = prepared.read(FIXTURE)

    def test_every_arm_is_measured(self):
        self.assertEqual(sorted(row["variant"] for row in self.rows),
                         sorted(list(PARAM_ARMS) + [CONTROL]))

    def test_the_control_holds(self):
        decision = prepared.verdict(self.rows)
        self.assertTrue(decision["prepared_publishable"],
                        json.dumps(decision, ensure_ascii=False,
                                   sort_keys=True))

    def test_the_control_arm_was_actually_seen_at_work(self):
        self.assertGreater(
            prepared.row_for(self.rows, CONTROL)["texts_sampled"], 0,
            "у контрольного плеча не снято ни одного текста: его ноль "
            "параметров ничего не доказывает")

    def test_the_literal_arm_shows_the_value_in_the_text(self):
        self.assertGreater(prepared.literal(self.rows, CONTROL), 0)
        self.assertEqual(prepared.parameterized(self.rows, CONTROL), 0)


class WhatTheReadmeClaims(unittest.TestCase):
    """README и фикстура обязаны говорить одно и то же."""

    def setUp(self):
        if not os.path.exists(FIXTURE):
            self.fail("нет замера оси 3: %s" % FIXTURE)
        self.rows = prepared.read(FIXTURE)
        with io.open(os.path.join(STAND, "README.md"),
                     encoding="utf-8") as handle:
            self.readme = handle.read()

    def test_the_readme_prints_the_measured_texts(self):
        for name in list(PARAM_ARMS) + [CONTROL]:
            for text in prepared.row_for(self.rows, name)["distinct_texts"]:
                self.assertIn(
                    text, self.readme,
                    "README не печатает текст, снятый у плеча %s" % name)

    def test_the_readme_does_not_claim_prepared_statements_were_counted(self):
        # Отозванная формулировка не должна вернуться в текст: считать
        # подготовленные запросы снаружи нечем, и exec_params их не
        # создаёт вовсе. Запрещено именно УТВЕРЖДЕНИЕ с числом
        # («подготовлено N»), а не слово: оговорка «сколько подготовлено —
        # стенд не утверждает» обязана в README быть, и первая редакция
        # этой проверки роняла ровно её.
        import re as _re
        self.assertIsNone(
            _re.search(r"подготовлен[оы]\s+\S*\d", self.readme.lower()),
            "README называет число подготовленных запросов — снаружи его "
            "прочитать нечем")
        self.assertIn("не утверждает", self.readme)


class LivePreparedMeasurement(unittest.TestCase):
    """Тот же контроль, но сейчас и на живых образах.

    Пропуск здесь — не зелёный: без докера проверка объявляет себя
    пропущенной. Запускать: STAND_WITH_DOCKER=1.
    """

    def setUp(self):
        if os.environ.get("STAND_WITH_DOCKER") != "1":
            self.skipTest("замер требует докера: STAND_WITH_DOCKER=1")
        if shutil.which("docker") is None:
            self.skipTest("docker не найден")

    def test_the_control_probe_really_sends_a_literal(self):
        # Проба, тайно шлющая параметр (например, исходник подменён на
        # exec_params), по закоммиченной фикстуре не видна — она-то снята
        # исправным прогоном. Здесь видно сразу.
        prepared.build_control_arm()
        database = pg.start(statements=True)
        try:
            row = prepared.measure_control(database, requests=2)
        finally:
            database.stop()
        self.assertEqual(row["probe_exit_code"], 0,
                         "проба не отработала: %s" % row["probe_output"])
        self.assertTrue(row["probe_finished"])
        self.assertGreater(row["texts_sampled"], 0,
                           "у контрольной пробы не снято ни одного текста")
        self.assertEqual(row["parameterized_texts"], 0,
                         "контрольная проба прислала параметризованный "
                         "текст: %s" % row["distinct_texts"])
        self.assertGreater(row["literal_texts"], 0)
        self.assertTrue(row["links_libpqxx"],
                        "проба собралась не тем клиентом базы: %s"
                        % row["probe_links"])


if __name__ == "__main__":
    unittest.main()
