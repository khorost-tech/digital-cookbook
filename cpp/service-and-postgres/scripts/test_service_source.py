"""Проверки исходника сервиса — по тексту, и вот что они держат на самом деле.

ЧЕСТНАЯ ГРАНИЦА. Эти проверки не запускают сервис и не ходят в базу: на
машине разбора нет ни компилятора C++, ни PostgreSQL. Поведение проверяют
оси, когда варианты подняты. Здесь удерживаются свойства, которые оси не
увидят, а потерять их легко при любой правке.

ПОЧЕМУ ОНИ ПЕРЕПИСАНЫ. Первая редакция искала подстроку по всему файлу и
не держала ничего: шесть диверсий ревьюера прошли зелёными — в том числе
убранный `application_name` и захардкоженное имя варианта. Комментарий,
объясняющий запрет, сходил за его соблюдение. Теперь комментарии
вырезаются (`scripts/cppsource.py`), а искомое ищется в теле нужной
функции, а не где угодно в файле.

Каждая проверка ниже уронена вручную — копией каталога и подменой
исходника — и восстановлена. Проверка, которую никто не ронял, стоит
ровно столько же, сколько честное слово.
"""

import io
import os
import re
import unittest

from scripts.cppsource import function_body, strip_comments

SERVICE = os.path.join(
    os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "service")


def _read(name):
    with io.open(os.path.join(SERVICE, name), encoding="utf-8") as handle:
        return handle.read()


class ServiceSource(unittest.TestCase):
    def setUp(self):
        self.code = strip_comments(_read("main.cpp"))
        self.headers = strip_comments(_read("handlers.hpp"))
        self.healthz = function_body(self.code, r"Reply\s+healthz\s*\(\s*\)")
        self.query = function_body(self.code, r"Reply\s+query\s*\(")
        self.block = function_body(self.code, r"Reply\s+block\s*\(")
        # Соединение теперь ОТКРЫВАЕТСЯ ДЛЯ ПУЛА, а не для запроса, и
        # функция возвращает его во владение (unique_ptr). Якорь
        # обновлён вместе с сигнатурой: якорь, не нашедшийся в
        # исходнике, ронял бы весь набор одинаково — независимо от
        # того, что именно сломано.
        self.connect = function_body(
            self.code, r"unique_ptr<pqxx::connection>\s+connect\s*\(")
        self.variant = function_body(
            self.code, r"std::string&\s+stand_variant\s*\(\s*\)")
        self.require = function_body(
            self.code, r"void\s+require_variant\s*\(")
        self.classify = function_body(
            self.code, r"bool\s+is_resource_refusal\s*\(")

    # --- ручки объявлены -------------------------------------------------

    def test_three_handlers_are_declared(self):
        # Ищем объявления, а не слова: `query` и `block` встречаются и в
        # прозе, и первая редакция проверки этим и удовлетворялась.
        for pattern in (r"Reply\s+healthz\s*\(\s*\)",
                        r"Reply\s+query\s*\(\s*int",
                        r"Reply\s+block\s*\(\s*int"):
            self.assertRegex(self.headers, pattern)

    # --- запрос параметризован -------------------------------------------

    def test_pg_sleep_occurs_exactly_once(self):
        # Второе вхождение — это либо второй текст запроса (и ось 3
        # считала бы два разных), либо сборка текста по кускам.
        self.assertEqual(self.code.count("pg_sleep"), 1,
                         "pg_sleep обязан встречаться в коде ровно один раз")

    def test_query_text_is_a_whole_literal_passed_to_exec_params(self):
        # Диверсия «величина вклеена конкатенацией, а $1 оставлен в
        # комментарии» проходит только мимо этой проверки: текст обязан
        # быть цельным литералом, а величина — следующим аргументом.
        self.assertRegex(
            self.query,
            r"exec_params\(\s*\"SELECT pg_sleep\(\$1\)\"\s*,",
            "текст запроса обязан быть цельным литералом в exec_params")

    def test_sleep_value_is_not_pasted_into_sql(self):
        self.assertNotRegex(
            self.query, r"pg_sleep[^\"]*\"\s*\+",
            "величина подставлена в текст запроса конкатенацией")

    # --- имя варианта ----------------------------------------------------

    def test_environment_is_read_in_exactly_one_place(self):
        # Считаем именно чтения окружения, а не упоминания имени
        # переменной: оно встречается ещё и в тексте ошибки, которую эта
        # же функция бросает.
        self.assertEqual(
            len(re.findall(r'getenv\(\s*"STAND_VARIANT"', self.code)), 1,
            "имя варианта обязано читаться ровно в одном месте")
        self.assertRegex(self.variant, r'getenv\(\s*"STAND_VARIANT"')

    def test_environment_name_is_never_assembled(self):
        # Счёт вхождений литерала обходится сборкой имени из кусков:
        # `getenv((std::string("STAND_") + "VARIANT").c_str())` читает ту
        # же переменную, а литерала целиком в файле не появляется — и
        # второе, скрытое чтение проходит мимо счёта.
        #
        # Поэтому требование к ФОРМЕ АРГУМЕНТА: каждое обращение к
        # окружению берёт либо цельный литерал, либо имя параметра `name`
        # (так устроен env_or, единственный посредник). Всё прочее —
        # выражение, а выражение может собрать что угодно.
        calls = re.findall(r"getenv\(\s*([^)]*)\)", self.code)
        self.assertTrue(calls, "в исходнике нет ни одного чтения окружения")
        for argument in calls:
            self.assertRegex(
                argument.strip(), r'^(name|"[A-Z][A-Z0-9_]*")$',
                "имя переменной окружения собрано выражением: %s" % argument)

    def test_missing_variant_raises_instead_of_defaulting(self):
        # Диверсия «env_or("STAND_VARIANT", "svc")» роняется этой парой:
        # значения по умолчанию у имени варианта нет, а пустое обязано
        # ронять запуск.
        self.assertIn("throw", self.variant,
                      "пустое имя варианта обязано ронять запуск")
        self.assertNotIn("env_or", self.variant,
                         "у имени варианта не бывает значения по умолчанию")

    def test_no_variant_name_is_baked_into_the_source(self):
        for forbidden in ('"unknown"', '"default"', '"beast"', '"drogon"',
                          '"crow"', '"svc"'):
            self.assertNotIn(forbidden, self.code,
                             "имя варианта запечено в исходник: %s" % forbidden)

    # --- соединение называет себя именем варианта ------------------------

    def test_connection_application_name_is_the_variant(self):
        # Две диверсии сразу: `application_name` убран совсем и
        # `application_name` захардкожен фиксированным именем. Обе
        # оставляли комментарии на месте — и обе проходили.
        self.assertRegex(
            self.connect,
            r"application_name=\"\s*\+\s*dsn_quote\(variant\)",
            "application_name соединения обязан равняться имени варианта")

    def test_default_dsn_password_is_not_empty(self):
        # Роль в schema.sql заводится с паролем. Строка подключения без
        # пароля молча работала бы только в режиме trust, и первый же
        # прогон на обычной базе упал бы на соединении.
        #
        # Держим ЗНАЧЕНИЕ, а не подстроку: `password=` с пустым значением
        # оставляет подстроку на месте и ломает ровно то, ради чего
        # проверка написана.
        match = re.search(r'password=([^\s"]*)', self.connect)
        self.assertIsNotNone(match, "в строке подключения нет пароля")
        self.assertNotEqual(match.group(1), "",
                            "пароль в строке подключения пуст")

    # --- живость не зависит от базы --------------------------------------

    def test_healthz_does_not_touch_the_database(self):
        # Ось 1 читает по этой ручке «занят или умер». Поход в базу
        # подмешал бы к ответу состояние общего ресурса.
        for forbidden in ("connect(", "pqxx", "exec"):
            self.assertNotIn(forbidden, self.healthz,
                             "/healthz обязан отвечать без похода в базу: %s"
                             % forbidden)

    # --- имя варианта защищено и в месте применения ----------------------

    def test_handlers_check_the_variant_they_were_given(self):
        # Защита у источника не мешает слою передать в ручку литерал или
        # пустую строку. Сверка с прочитанным при старте именем ловит это
        # на первом же запросе.
        for name, body in (("query", self.query), ("block", self.block)):
            self.assertIn("require_variant(variant)", body,
                          "ручка %s обязана сверить полученное имя" % name)

    def test_variant_check_compares_with_the_name_read_at_start(self):
        # Вызов на месте ещё не значит, что сверка сверяет. `variant !=
        # variant` — тавтология: текст вызова цел, проверять перестало.
        # Поэтому смотрим ВНУТРЬ функции: сравнение обязано быть с
        # прочитанным при старте именем.
        self.assertRegex(
            self.require, r"variant\s*!=\s*stand_variant\(\)",
            "сверка обязана сравнивать с прочитанным при старте именем")
        self.assertIn("throw", self.require,
                      "расхождение имён обязано быть ошибкой, а не записью")

    def test_no_comparison_is_a_tautology(self):
        # Запрет шире одного случая: сравнение величины с самой собой —
        # всегда один и тот же ответ, и любая такая проверка в коде
        # стенда ничего не проверяет.
        tautology = re.search(r"\b(\w+)\s*(?:!=|==)\s*\1\b", self.code)
        self.assertIsNone(
            tautology,
            "сравнение величины с самой собой: %s"
            % (tautology.group(0) if tautology else ""))

    # --- отказ базы не выдаётся за поломку пробы -------------------------

    def test_database_refusal_is_told_apart_from_our_breakage(self):
        # «too many clients» — свойство общего ресурса и самый интересный
        # исход оси 2. Записать его в error значило бы спрятать находку в
        # графу «стенд сломался».
        # Сами коды объявлены именованными константами — ищем их там, где
        # они объявлены, а применение констант там, где решается исход.
        for code in ('"53300"', '"57P03"'):
            self.assertIn(code, self.code,
                          "код отказа общего ресурса не назван: %s" % code)
        self.assertIn("broken_connection", self.query,
                      "разорванное соединение обязано быть отказом базы")
        self.assertIn('"refused"', self.query)
        self.assertIn('"error"', self.query)
        for token in ("kTooManyConnections", "kCannotConnectNow"):
            self.assertIn(token, self.classify,
                          "код отказа общего ресурса не участвует в разборе:"
                          " %s" % token)

    def test_classification_is_not_always_true(self):
        # Набор токенов держится и при `|| true`: все коды на месте, а
        # решение перестало зависеть от кода — НАША поломка (синтаксис,
        # права) поехала бы в таблицу как отказ общего ресурса. Держим
        # ФОРМУ выражения; поведение на произвольном коде держит
        # test_service_behaviour.py.
        always = re.search(r"(\|\|\s*(true|1)\b)|(&&\s*(false|0)\b)",
                           self.classify)
        self.assertIsNone(
            always,
            "решение об отказе не зависит от кода: %s"
            % (always.group(0) if always else ""))
        self.assertRegex(
            self.classify, r"return\s+[^;]*kTooManyConnections[^;]*;",
            "решение обязано возвращаться из сравнения с кодами")

    # --- ожидание там, где его меряют ------------------------------------

    def test_block_actually_waits_in_the_handler(self):
        # Ось 1 меряет занятость рабочей единицы. Ручка, которая не спит,
        # не занимает ничего — и ось показала бы, что блокирующих
        # обработчиков не хватает, чтобы занять сервер, ни при каком их
        # числе.
        self.assertRegex(self.block, r"sleep_for\(\s*std::chrono::"
                                     r"milliseconds\(\s*ms\s*\)",
                         "/block обязан ждать в самом обработчике")

    def test_query_does_not_wait_in_the_handler(self):
        # Зеркальное требование: у второй ручки ожидание уходит В БАЗУ, и
        # сон в обработчике стёр бы различие, ради которого ось 1 и
        # существует.
        self.assertNotIn("sleep_for", self.query,
                         "/q обязан ждать в базе, а не в обработчике")

    def test_query_actually_goes_to_the_database(self):
        # Ручка, вернувшая served без соединения, дала бы оси 2 ноль
        # соединений — и ноль выглядел бы измерением.
        #
        # СОЕДИНЕНИЕ БЕРЁТСЯ У ПУЛА, а не открывается здесь: пул появился
        # вместе с осью 2, и открытие на запрос — ровно то устройство,
        # которое она перестала мерить. Требование осталось прежним по
        # сути: ручка обязана ВЗЯТЬ соединение и выполнить запрос.
        self.assertIn("connections().acquire()", self.query,
                      "/q обязан взять соединение из пула")
        self.assertIn("exec_params", self.query,
                      "/q обязан выполнить запрос")

    def test_the_pool_is_what_opens_connections(self):
        # Соединение, открытое мимо пула, не считается ни одним слотом:
        # размер пула перестал бы быть потолком, а контроль оси 2 —
        # контролем.
        self.assertNotIn("connect(", self.query,
                         "/q открывает соединение сам, минуя пул — тогда "
                         "размер пула не ограничивает ничего")
        made = function_body(
            self.code, r"ConnectionPool<pqxx::connection>&\s+connections\s*\(")
        self.assertIn("connect(stand_variant())", made,
                      "пулу нечем открывать соединение")
        self.assertIn("stand_pool_size()", made,
                      "пул заводится не по прочитанной ручке — размер, "
                      "взятый откуда-то ещё, при запуске не меняется")

    def test_the_slot_is_released_on_a_failed_query(self):
        # Соединение, на котором отказал запрос, обязано быть объявлено
        # негодным: вернувшись в набор, оно отравило бы каждый следующий
        # запрос, взявший тот же слот, — и замер списал бы череду отказов
        # на слой.
        #
        # ИЩЕТСЯ НЕ ПОДСТРОКА, А МЕСТО. Ревью показало, чего стоит поиск
        # по всему телу: `lease.discard()`, перенесённый в заведомо
        # мёртвую ветку (проверка отрицательного ожидания отбивается
        # раньше), оставлял проверку зелёной — текст на месте, решение не
        # исполняется никогда. Здесь требуется, чтобы вызов стоял внутри
        # перехвата ВСЕХ исключений вокруг запроса и перед пробросом.
        catch = self.query.index("catch (...) {")
        rethrow = self.query.index("throw;", catch)
        self.assertIn(
            "lease.discard()", self.query[catch:rethrow],
            "порванное соединение возвращается в пул как живое: вызов "
            "discard() стоит не в перехвате отказа запроса")

    def test_the_failed_query_is_not_swallowed(self):
        # Перехват «всех исключений» ради освобождения слота обязан
        # пробросить отказ дальше: проглоченный отказ превратился бы в
        # исход served, и ось напечатала бы обслуженный запрос там, где
        # база отказала.
        catch = self.query.index("catch (...) {")
        self.assertIn("throw;", self.query[catch:],
                      "отказ запроса проглочен вместе с освобождением слота")

    def test_the_pool_size_is_read_from_the_environment_without_a_fallback(
            self):
        size = function_body(self.code, r"int\s+stand_pool_size\s*\(")
        self.assertIn('"STAND_POOL"', size)
        self.assertIn("throw std::runtime_error", size,
                      "незаданный размер пула подставляется запасным "
                      "значением: две настройки оси 2 склеились бы в одну")

    def test_sqlstate_reaches_the_detail(self):
        # Без кода в объяснении отказ нечем отличить от другого отказа, а
        # весь смысл разделения исходов — в том, чтобы различать.
        self.assertRegex(self.query, r'"SQLSTATE "\s*\+\s*state',
                         "SQLSTATE обязан попасть в объяснение исхода")

    def test_detail_is_escaped(self):
        # В detail приходит текст ошибки базы. Кавычка в нём молча
        # оборвала бы строку контракта, и поломка выглядела бы как сбой
        # пробы.
        line = function_body(self.code, r"std::string\s+json_line\s*\(")
        self.assertIn("json_escape(detail)", line,
                      "объяснение исхода обязано экранироваться")


class EntryPoints(unittest.TestCase):
    """Точки входа слоёв. Предмет появляется в задаче 2.

    Проверка написана заранее и НЕ зелёная на пустом месте: при отсутствии
    `*_main.cpp` она объявляет себя пропущенной, а не пройденной. Зелёная
    проверка без предмета проверки — это отчёт о работе, которой не было.
    """

    def test_entry_points_read_the_variant_at_start(self):
        entries = sorted(name for name in os.listdir(SERVICE)
                         if name.endswith("_main.cpp"))
        if not entries:
            self.skipTest("точки входа слоёв появляются в задаче 2")
        for name in entries:
            code = strip_comments(_read(name))
            main = function_body(code, r"int\s+main\s*\(")
            self.assertIn("stand_variant()", main,
                          "%s обязан прочитать имя варианта при старте" % name)
            self.assertIsNone(
                re.search(r'variant\s*=\s*"', main),
                "%s подставляет литерал вместо имени варианта" % name)
