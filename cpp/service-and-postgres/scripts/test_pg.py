"""Проверки канонической оснастки PostgreSQL (`scripts/pg.py`).

ПЕРВОЙ ЗДЕСЬ НАПИСАНА ПРОВЕРКА ГОТОВНОСТИ, и написана она была до
модуля. Контейнер с базой стартует дольше сервиса, а задержка «на
глазок» прошла бы на быстрой машине и развалилась на медленной — первый
замер поймал бы не то. Готовность обязана ПРОВЕРЯТЬСЯ запросом.

ПОЧЕМУ У ЭТОГО ФАЙЛА СВОЙ ПОДМЕННЫЙ ИСПОЛНИТЕЛЬ. `FakeDocker` из
`test_measuring_code.py` моделирует СЕРВИС и тот единственный вопрос к
базе, который задаёт ось 1. Здесь предмет другой: сама база — её подъём,
её представления и то, КАК они врут, если спросить неправильно. Модель
ниже отвечает только на те вопросы, которые понимает, и отвергает
остальные, а не обслуживает их числом, похожим на правду.

ЧАСТЬ ПРОВЕРОК ТРЕБУЕТ ДОКЕРА (`STAND_WITH_DOCKER=1`): утверждения про
то, что видно из ЧУЖОЙ сессии и что отвечает база без расширения, на
модели были бы утверждениями про модель.
"""

import inspect
import json
import os
import sys
import time
import unittest

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts import pg  # noqa: E402

WITH_DOCKER = os.environ.get("STAND_WITH_DOCKER") == "1"


class _Completed:
    def __init__(self, stdout="", returncode=0, stderr=""):
        self.stdout = stdout
        self.stderr = stderr
        self.returncode = returncode


class FakePg:
    """Модель базы: подъём, представления и способы спросить неправильно.

    `sessions` — список соединений вида {"app", "state", "pid", "query"}.
    `ready_after` — сколько проб готовности отвечают отказом, прежде чем
    база ответит; отрицательное значение — база, которая не ответит
    никогда.
    """

    def __init__(self, sessions=(), ready_after=0, statements=None,
                 garbage=False):
        self.sessions = [dict(item) for item in sessions]
        self.ready_after = ready_after
        self.statements = dict(statements or {})
        self.garbage = garbage
        self.commands = []
        self.ready_probes = 0
        self.roles_created = 0
        self.extension_created = 0
        self.preloaded = False
        self.started = []
        self.removed = []

    # --- исполнитель ----------------------------------------------------

    def run(self, cmd):
        self.commands.append(list(cmd))
        return _Completed(self._handle(cmd), 0)

    def try_run(self, cmd):
        self.commands.append(list(cmd))
        try:
            return _Completed(self._handle(cmd), 0)
        except _Refused as refusal:
            return _Completed(refusal.stdout, 1, str(refusal))
        except Exception as failure:
            return _Completed("", 1, str(failure))

    def _handle(self, cmd):
        if cmd[:3] == ["docker", "network", "create"]:
            return ""
        if cmd[:3] == ["docker", "network", "rm"]:
            return ""
        if cmd[:2] == ["docker", "rm"]:
            self.removed.append(cmd[-1])
            return ""
        if cmd[:3] == ["docker", "run", "-d"]:
            self.started.append(list(cmd))
            self.preloaded = pg.PRELOAD_FLAG in cmd
            return "fake\n"
        if cmd[:2] == ["docker", "exec"]:
            return self._psql(cmd)
        raise AssertionError("подменная база не знает команды: %s" % cmd)

    # --- представления --------------------------------------------------

    def _psql(self, cmd):
        sql = " ".join(cmd)
        if "SELECT 1" in sql:
            self.ready_probes += 1
            if self.ready_after < 0 or self.ready_probes <= self.ready_after:
                raise _Refused("could not connect to server")
            return "1\n"
        if "CREATE ROLE" in sql:
            self.roles_created += 1
            return ""
        if "CREATE EXTENSION" in sql:
            # ИЗМЕРЕНО: без флага при старте сервера эта команда ПРОХОДИТ.
            # Молчаливого отказа здесь нет — он приходит позже, при чтении
            # представления, и модель обязана вести себя так же.
            self.extension_created += 1
            return ""
        if "pg_stat_activity" in sql:
            return self._activity(sql)
        if "pg_prepared_statements" in sql:
            # СЕССИЯ НАБЛЮДАТЕЛЯ НИЧЕГО НЕ ГОТОВИЛА. Это не упрощение
            # модели, а замеренное свойство: представление сессионное.
            return "0\n"
        if pg.STATEMENTS_EXTENSION in sql:
            return self._statements(sql)
        raise AssertionError("подменная база не понимает запроса: %s" % sql)

    def _named(self, sql):
        if "application_name = '" not in sql:
            raise AssertionError(
                "вопрос задан не про имя варианта: %s" % sql)
        return sql.split("application_name = '")[1].split("'")[0]

    def _activity(self, sql):
        if self.garbage:
            raise _Refused("server closed the connection unexpectedly")
        name = self._named(sql)
        rows = [item for item in self.sessions if item["app"] == name]
        if "state = 'active'" in sql:
            rows = [item for item in rows if item["state"] == "active"]
        if "count(*)" in sql:
            return "%d\n" % len(rows)
        return "".join("%d\t%s\t%s\n" % (item["pid"], item["state"],
                                         item["query"]) for item in rows)

    def _statements(self, sql):
        if not self.preloaded:
            raise _Refused(
                'ERROR:  pg_stat_statements must be loaded via'
                ' "shared_preload_libraries"')
        # МОДЕЛЬ ОТВЕЧАЕТ ТОЛЬКО НА ПОНЯТЫЙ ВОПРОС. Одну подстроку могут
        # нести РАЗНЫЕ строки статистики (разный queryid, одинаковый
        # нормализованный текст), и число вызовов — сумма по ним. Запрос,
        # берущий одну строку, спрашивает другую величину, и выдавать её
        # за число вызовов нельзя.
        if "sum(calls)" not in sql:
            raise AssertionError(
                "число вызовов спрошено не суммой по строкам: %s" % sql)
        # ПОДСТРОКА ИЩЕТСЯ position, А НЕ LIKE. В LIKE `_` и `%` —
        # метасимволы: подстрока с подчёркиванием находит заодно чужую
        # строку. Модель, обслужившая LIKE как «просто поиск», приняла бы
        # этот перебор за верный ответ.
        if "position(" not in sql:
            raise AssertionError(
                "подстрока ищется не position: в LIKE есть метасимволы: %s"
                % sql)
        fragment = sql.split("position('")[1].split("' in query)")[0]
        matched = [calls for query, calls in self.statements.items()
                   if fragment in query]
        if not matched and "coalesce" not in sql:
            # Сумма по ПУСТОМУ набору строк — NULL, и psql печатает её
            # пустотой. Модель, всегда отвечавшая нулём, прятала бы
            # разницу между «ни одного такого запроса» и «спросить не
            # удалось».
            return "\n"
        return "%d\n" % sum(matched)


class _Refused(Exception):
    """Отказ базы: psql выходит ненулевым кодом и печатает сообщение."""

    def __init__(self, message, stdout=""):
        Exception.__init__(self, message)
        self.stdout = stdout


def _started(fake, statements=True, **kwargs):
    return pg.start(host=fake, statements=statements, timeout_s=5, **kwargs)


# --- готовность ---------------------------------------------------------


class Readiness(unittest.TestCase):
    def test_readiness_is_checked_not_waited_out(self):
        source = inspect.getsource(pg.wait_ready)
        self.assertNotIn("sleep(", source.replace("time.sleep(0.1)", ""),
                         "готовность выжидается задержкой, а не проверяется")
        self.assertIn("SELECT 1", source)

    def test_readiness_polls_until_the_database_answers(self):
        # ПОВЕДЕНЧЕСКАЯ ПАРА К ПРОВЕРКЕ ИСХОДНИКА. Чтение текста ловит
        # задержку, написанную словом; эта проверка ловит любую готовность,
        # объявленную раньше первого удачного ответа.
        fake = FakePg(ready_after=4)
        attempts = pg.wait_ready(timeout_s=5, host=fake)
        self.assertEqual(attempts, 5)
        self.assertEqual(fake.ready_probes, 5)

    def test_a_database_that_never_answers_is_refused(self):
        fake = FakePg(ready_after=-1)
        with self.assertRaises(RuntimeError):
            pg.wait_ready(timeout_s=0.5, host=fake)

    def test_a_database_that_answers_at_once_is_not_waited_on(self):
        # Готовность, выжидаемая паузой, прошла бы обе проверки выше:
        # она ведь тоже дождалась бы ответа. Различает их СРОК.
        fake = FakePg()
        started = time.time()
        pg.wait_ready(timeout_s=30, host=fake)
        self.assertLess(time.time() - started, 1.0,
                        "готовая база всё равно выжидалась")

    def test_an_empty_answer_with_a_zero_exit_code_is_not_readiness(self):
        # `docker exec`, вернувший ноль и промолчавший, — это не
        # выполненный запрос. Готовность — пришедшая единица.
        class Mute(FakePg):
            def _psql(self, cmd):
                if "SELECT 1" in " ".join(cmd):
                    return ""
                return FakePg._psql(self, cmd)

        with self.assertRaises(RuntimeError):
            pg.wait_ready(timeout_s=0.5, host=Mute())

    def test_the_temporary_server_does_not_pass_for_the_real_one(self):
        # Образ PostgreSQL на время разворачивания поднимает ВРЕМЕННЫЙ
        # сервер; роль на нём завести нельзя. Подъём обязан дойти до роли.
        fake = FakePg(ready_after=2)
        _started(fake, statements=False)
        self.assertGreaterEqual(fake.roles_created, 1)


# --- образ и флаги старта ------------------------------------------------


class ImagePinning(unittest.TestCase):
    def test_the_image_is_pinned_by_digest(self):
        self.assertIn("@sha256:", pg.image())

    def test_a_tag_only_pin_is_refused(self):
        spec = json.loads(json.dumps(pg.image_spec()))
        self.assertIn("tag_at_pinning", spec)
        self.assertNotEqual(pg.image(), spec["tag_at_pinning"],
                            "закреплён тег, а не дайджест")

    def test_the_pinned_image_is_what_actually_starts(self):
        fake = FakePg()
        _started(fake, statements=False)
        self.assertIn(pg.image(), fake.started[0],
                      "контейнер поднят не закреплённым образом")

    def test_a_declaration_pinned_by_a_tag_is_refused(self):
        """СТОРОЖ ДАЙДЖЕСТА ОБЯЗАН ИСПОЛНЯТЬСЯ ХОТЬ РАЗ.

        В настоящем объявлении дайджест есть, поэтому на нём сторож не
        исполняется НИКОГДА: его можно было снять целиком, и проверки
        выше остались бы зелёными — они смотрят на содержимое файла, а не
        на то, что будет с плохим содержимым. Здесь сторожу задаётся его
        собственный вопрос.
        """
        import tempfile
        spoiled = os.path.join(tempfile.mkdtemp(), "postgres.json")
        with open(spoiled, "w", encoding="utf-8") as handle:
            handle.write(json.dumps({"image": "postgres:18"}))
        with self.assertRaises(RuntimeError):
            pg.image_spec(path=spoiled)

    def test_the_address_carries_the_container_name(self):
        # Адрес без имени контейнера увёл бы сервис в чужую базу — или
        # никуда, а замер списал бы это на слой.
        self.assertIn("host=stand-pg-elsewhere",
                      pg.dsn("stand-pg-elsewhere"))

    def test_the_variants_file_does_not_gain_a_fourth_variant(self):
        # Образ базы лежит отдельным файлом НАМЕРЕННО: обвязка перебирает
        # ключи images/variants.json как варианты сервиса.
        from scripts import docker as dockerlib
        self.assertEqual(sorted(dockerlib.variants()),
                         ["beast", "crow", "drogon"])


class StatementsExtension(unittest.TestCase):
    def test_the_preload_flag_goes_to_the_server_at_start(self):
        fake = FakePg()
        _started(fake)
        self.assertIn(pg.PRELOAD_FLAG, fake.started[0])
        self.assertEqual(fake.extension_created, 1)

    def test_statistics_are_on_unless_asked_otherwise(self):
        # Умолчание — рабочая постановка осей 2–4. Молча перевернувшись в
        # «без статистики», оно оставило бы ось 3 без счётчика вызовов, а
        # ось 1 своё `statements=False` передаёт явно и не заметила бы
        # ничего.
        fake = FakePg()
        pg.start(host=fake, timeout_s=5)
        self.assertIn(pg.PRELOAD_FLAG, fake.started[0])

    def test_without_statistics_neither_the_flag_nor_the_extension(self):
        fake = FakePg()
        _started(fake, statements=False)
        self.assertNotIn(pg.PRELOAD_FLAG, fake.started[0])
        self.assertEqual(fake.extension_created, 0)

    def test_a_database_without_statistics_refuses_the_question(self):
        """Отказ обязан прийти ОТ САМОГО ЗАМЕРА, а не от базы.

        ПОЧЕМУ ПОСТАНОВКА ТАКАЯ СТРАННАЯ. Первая редакция этой проверки
        поднимала базу без расширения и требовала отказа — и проходила
        ЗЕЛЁНОЙ, когда сторож из `Database.statement_calls` убирали
        диверсией: отказ приходил от базы, у которой расширения и правда
        не было, и проверка не различала два источника отказа. Здесь
        база ОТВЕТИЛА БЫ (расширение в ней есть), а объект замера знает,
        что ЭТОТ замер поставлен без статистики: число вызовов из чужой
        постановки — не его число.
        """
        fake = FakePg(statements={"SELECT pg_sleep($1)": 4})
        _started(fake)
        self.assertEqual(pg.statement_calls("pg_sleep", host=fake), 4)
        elsewhere = pg.Database(pg.CONTAINER, pg.NETWORK, host=fake,
                                statements=False)
        with self.assertRaises(RuntimeError):
            elsewhere.statement_calls("pg_sleep")


# --- уборка --------------------------------------------------------------


class Teardown(unittest.TestCase):
    def test_the_context_releases_the_database_even_on_failure(self):
        # Контейнер, переживший неудачный замер, — это чужие соединения в
        # счётчиках следующего, и ни одного признака того, что что-то
        # пошло не так.
        # УБОРКУ ПОСЛЕ СЕБЯ НЕЛЬЗЯ ПУТАТЬ СО СНЯТИЕМ ОСТАТКА ПЕРЕД
        # СТАРТОМ. Первая редакция этой проверки смотрела на список
        # удалённых контейнеров целиком — а `start` удаляет остаток
        # прошлого прогона САМ, и проверка проходила зелёной даже когда
        # выход из контекста не убирал ничего. Список очищается после
        # подъёма: считается только то, что сделано на выходе.
        fake = FakePg()
        base = _started(fake, statements=False)
        del fake.removed[:]
        with self.assertRaises(ValueError):
            with base:
                raise ValueError("замер не состоялся")
        self.assertIn(pg.CONTAINER, fake.removed)

    def test_a_leftover_container_is_removed_before_the_start(self):
        # ПРОВЕРЯЕТСЯ АРГУМЕНТ, А НЕ ТОЛЬКО ПОРЯДОК. Снятие остатка,
        # целящееся в имя по умолчанию вместо переданного, соблюдает
        # порядок команд идеально — и оставляет чужой контейнер жить
        # ровно там, где базу подняли под своим именем.
        fake = FakePg()
        _started(fake, statements=False, container="stand-pg-elsewhere",
                 network="stand-pg-elsewhere")
        removals = [index for index, cmd in enumerate(fake.commands)
                    if cmd[:2] == ["docker", "rm"]]
        starts = [index for index, cmd in enumerate(fake.commands)
                  if cmd[:3] == ["docker", "run", "-d"]]
        self.assertTrue(removals and starts and removals[0] < starts[0],
                        "прошлый контейнер не убран до старта нового")
        self.assertEqual(fake.commands[removals[0]][-1], "stand-pg-elsewhere",
                         "снимается не тот контейнер, который поднимается")

    def test_the_network_goes_away_with_the_database(self):
        # Оставленная сеть — не косметика: следующий подъём молча
        # переиспользует её вместе с тем, что в ней осталось висеть.
        fake = FakePg()
        base = _started(fake, statements=False)
        base.stop()
        removed_networks = [cmd[-1] for cmd in fake.commands
                            if cmd[:3] == ["docker", "network", "rm"]]
        self.assertIn(pg.NETWORK, removed_networks)


class OneStarter(unittest.TestCase):
    """База поднимается в ОДНОМ месте — иначе условий замера два.

    Роль, база, флаги сервера и способ проверки готовности влияют на то,
    что покажут счётчики. Вторая функция, поднимающая PostgreSQL
    по-своему, — это второй набор условий, о котором фикстура молчит:
    числа читались бы правильно, но про другую базу.
    """

    def test_postgres_is_started_in_one_place_only(self):
        # `sabotage_source.py` — реестр НАМЕРЕННО испорченного текста, а
        # не исполняемая обвязка: диверсия, возвращающая второй подъём
        # базы в dbwait, обязана лежать там целиком, иначе её нечем
        # применить. Сам этот файл собирает искомую строку из кусков по
        # той же причине.
        marker = "POSTGRES_" + "PASSWORD"
        skipped = ("pg.py", "sabotage_source.py", os.path.basename(__file__))
        folder = os.path.join(STAND, "scripts")
        elsewhere = []
        for name in sorted(os.listdir(folder)):
            if not name.endswith(".py") or name in skipped:
                continue
            with open(os.path.join(folder, name), encoding="utf-8") as handle:
                if marker in handle.read():
                    elsewhere.append(name)
        self.assertEqual(elsewhere, [],
                         "PostgreSQL поднимается не только в scripts/pg.py")


# --- счётчики ------------------------------------------------------------


SESSIONS = (
    {"app": "beast", "state": "active", "pid": 11, "query": "SELECT $1"},
    {"app": "beast", "state": "idle", "pid": 12, "query": "SELECT $1"},
    {"app": "crow", "state": "active", "pid": 13, "query": "SELECT pg_sleep(1)"},
)


class Connections(unittest.TestCase):
    def test_connections_count_every_connection_of_the_variant(self):
        # Ось 2 меряет ПУЛ: соединение, ничего сейчас не выполняющее, —
        # всё равно открытое соединение.
        fake = FakePg(sessions=SESSIONS)
        self.assertEqual(pg.connections("beast", host=fake), 2)
        self.assertEqual(pg.connections("crow", host=fake), 1)
        self.assertEqual(pg.connections("drogon", host=fake), 0)

    def test_the_answer_is_a_number_and_not_a_string_shaped_like_one(self):
        # Строка «2» сравнивается с числом 2 неверно и молча.
        fake = FakePg(sessions=SESSIONS)
        value = pg.connections("beast", host=fake)
        self.assertIsInstance(value, int)
        self.assertNotIsInstance(value, str)

    def test_an_empty_application_name_is_not_a_filter(self):
        # `application_name = ''` совпадает со всем, у чего имя не
        # выставлено: счёт по такому «фильтру» — правдоподобное число ни
        # о чём.
        fake = FakePg(sessions=SESSIONS)
        for bad in ("", "   ", None):
            with self.assertRaises(ValueError):
                pg.connections(bad, host=fake)

    def test_a_database_that_did_not_answer_is_not_a_zero(self):
        fake = FakePg(sessions=SESSIONS, garbage=True)
        with self.assertRaises(RuntimeError):
            pg.connections("beast", host=fake)


class ActiveQueries(unittest.TestCase):
    def test_only_the_queries_running_right_now_are_returned(self):
        fake = FakePg(sessions=SESSIONS)
        rows = pg.active_queries("beast", host=fake)
        self.assertEqual([item["pid"] for item in rows], [11])
        self.assertEqual(rows[0]["state"], "active")
        self.assertEqual(rows[0]["query"], "SELECT $1")

    def test_the_rows_carry_the_query_text(self):
        # Текст — единственный per-variant признак подготовленности,
        # видимый снаружи; потерять его значило бы потерять ось 3.
        fake = FakePg(sessions=SESSIONS)
        rows = pg.active_queries("crow", host=fake)
        self.assertEqual(rows[0]["query"], "SELECT pg_sleep(1)")

    def test_the_count_and_the_rows_answer_about_the_same_thing(self):
        fake = FakePg(sessions=SESSIONS)
        for name in ("beast", "crow", "drogon"):
            self.assertEqual(pg.active_query_count(name, host=fake),
                             len(pg.active_queries(name, host=fake)))

    def test_active_queries_refuse_an_empty_application_name(self):
        fake = FakePg(sessions=SESSIONS)
        with self.assertRaises(ValueError):
            pg.active_queries("", host=fake)


class Prepared(unittest.TestCase):
    """Вопрос, который нельзя задать из чужой сессии, — и это сказано."""

    def test_asking_from_a_foreign_session_is_refused_not_answered_zero(self):
        fake = FakePg(sessions=SESSIONS)
        with self.assertRaises(pg.SessionScopedView):
            pg.prepared("beast", host=fake)

    def test_the_variants_own_session_answers(self):
        fake = FakePg(sessions=SESSIONS)
        asked = []

        def ask(sql):
            asked.append(sql)
            return "3"

        self.assertEqual(pg.prepared("beast", ask=ask, host=fake), 3)
        self.assertEqual(len(asked), 1)
        self.assertIn("pg_prepared_statements", asked[0])

    def test_a_variant_absent_from_the_database_is_refused(self):
        # Ноль у неподключённого варианта означал бы «не подготовил»,
        # тогда как на самом деле «не подключался».
        fake = FakePg(sessions=SESSIONS)
        with self.assertRaises(RuntimeError) as refusal:
            pg.prepared("drogon", ask=lambda sql: "0", host=fake)
        self.assertNotIsInstance(refusal.exception, pg.SessionScopedView)

    def test_an_answer_that_is_not_a_number_is_not_a_zero(self):
        fake = FakePg(sessions=SESSIONS)
        with self.assertRaises(RuntimeError):
            pg.prepared("beast", ask=lambda sql: "ERROR", host=fake)

    def test_the_refusal_names_the_reason(self):
        # Отказ обязан объяснять, ПОЧЕМУ снаружи не видно: иначе
        # следующий круг перепишет его на наивный счёт.
        with self.assertRaises(pg.SessionScopedView) as refusal:
            pg.prepared("beast", host=FakePg())
        self.assertIn("pg_prepared_statements", str(refusal.exception))


class StatementCalls(unittest.TestCase):
    def test_calls_are_summed_over_every_matching_row(self):
        # Одинаковый текст могут иметь РАЗНЫЕ строки статистики:
        # параметризованный запрос и нормализованный литеральный.
        fake = FakePg(statements={"SELECT pg_sleep($1)": 4,
                                  "PREPARE p AS SELECT pg_sleep($1)": 3,
                                  "SELECT $1": 9})
        _started(fake)
        self.assertEqual(pg.statement_calls("pg_sleep", host=fake), 7)

    def test_a_fragment_that_matches_nothing_is_zero(self):
        fake = FakePg(statements={"SELECT $1": 9})
        _started(fake)
        self.assertEqual(pg.statement_calls("pg_sleep", host=fake), 0)

    def test_a_missing_extension_is_not_zero_calls(self):
        fake = FakePg(statements={"SELECT pg_sleep($1)": 4})
        _started(fake, statements=False)
        with self.assertRaises(RuntimeError):
            pg.statement_calls("pg_sleep", host=fake)

    def test_an_empty_fragment_is_refused(self):
        fake = FakePg(statements={"SELECT $1": 9})
        _started(fake)
        for bad in ("", "  ", None):
            with self.assertRaises(ValueError):
                pg.statement_calls(bad, host=fake)


# --- живая база ----------------------------------------------------------


@unittest.skipUnless(WITH_DOCKER, "нужен докер: STAND_WITH_DOCKER=1")
class AgainstARealDatabase(unittest.TestCase):
    """То, что на модели было бы утверждением про модель.

    Подъём базы один на весь класс: он занимает секунды, а проверки
    ниже друг другу не мешают — каждая заводит своё соединение со своим
    именем приложения.
    """

    @classmethod
    def setUpClass(cls):
        cls.base = pg.start(container="stand-pg-check",
                            network="stand-pg-check")

    @classmethod
    def tearDownClass(cls):
        cls.base.stop()

    def _session(self, name, sql, expect=1):
        """Фоновая сессия с ИМЕНЕМ ПРИЛОЖЕНИЯ, живущая, пока идёт запрос.

        `expect` — сколько соединений с этим именем обязано стать после
        неё: ждать «хоть одного» при второй сессии того же имени значило
        бы не ждать вовсе.
        """
        from scripts import docker as dockerlib
        dockerlib.HOST.run(
            ["docker", "exec", "-d", self.base.container, "psql",
             "dbname=%s user=%s application_name=%s"
             % (pg.DATABASE, pg.SUPERUSER, name), "-c", sql])
        for _ in range(100):
            if self.base.connections(name) >= expect:
                return
            time.sleep(0.1)
        raise AssertionError("сессия %s не появилась в базе" % name)

    def test_the_container_runs_the_pinned_digest(self):
        from scripts import docker as dockerlib
        seen = dockerlib.HOST.run(
            ["docker", "inspect", self.base.container,
             "--format", "{{.Config.Image}}"]).stdout.strip()
        self.assertEqual(seen, pg.image())

    def test_a_separate_session_sees_none_of_another_sessions_prepared(self):
        """ЗАМЕР, НА КОТОРОМ ДЕРЖИТСЯ УСТРОЙСТВО `prepared`.

        Пока чужая сессия держит подготовленный запрос, наблюдатель
        читает из `pg_prepared_statements` ноль. Значит, наивный счёт
        отвечал бы про наблюдателя — всегда нулём, — и отрицательный
        контроль оси 3 прошёл бы у обоих плеч одинаково.
        """
        self._session("probe-prepared",
                      "PREPARE p(int) AS SELECT pg_sleep($1); EXECUTE p(6);")
        from scripts import pg as module
        seen = module._psql("SELECT count(*) FROM pg_prepared_statements",
                            container=self.base.container)
        self.assertEqual(seen.stdout.strip(), "0",
                         "чужие подготовленные запросы вдруг видны: устройство "
                         "prepared надо перепроверить")
        self.assertGreaterEqual(self.base.connections("probe-prepared"), 1)

    def test_the_name_filter_keeps_other_connections_out(self):
        """ФИЛЬТР ПО ИМЕНИ ОБЯЗАН ФИЛЬТРОВАТЬ — на живой базе, с чужими.

        На подменном исполнителе этого не проверить в принципе: он не
        исполняет SQL, а разбирает его текст и фильтрует сам, и приписка
        вроде ` OR true` для него невидима. Отключённый фильтр даёт
        ПРАВДОПОДОБНОЕ число — ровно то, ради чего ось 2 и существует.
        Поэтому здесь в базе одновременно живут два имени, и у каждого
        своё число: отрицательный контроль — чужое соединение не имеет
        права попасть в счёт своего.
        """
        self._session("probe-mine", "SELECT pg_sleep(8);")
        self._session("probe-theirs", "SELECT pg_sleep(8);")
        self._session("probe-theirs", "SELECT pg_sleep(8);", expect=2)
        mine = self.base.connections("probe-mine")
        theirs = self.base.connections("probe-theirs")
        self.assertEqual(mine, 1)
        self.assertEqual(theirs, 2)
        self.assertEqual([row["query"] for row in
                          self.base.active_queries("probe-mine")],
                         ["SELECT pg_sleep(8);"])
        self.assertEqual(len(self.base.active_queries("probe-theirs")), 2)
        # И у имени, которого в базе нет, — ноль, а не «сколько-то».
        self.assertEqual(self.base.connections("probe-nobody"), 0)
        self.assertEqual(self.base.active_queries("probe-nobody"), [])

    def test_a_fragment_with_an_underscore_matches_only_itself(self):
        """ЗАМЕР, ИЗ-ЗА КОТОРОГО ПОДСТРОКА ИЩЕТСЯ НЕ ЧЕРЕЗ LIKE.

        Два запроса РАЗНОЙ формы (значит, разные строки статистики),
        тексты которых различаются одним символом ровно на месте
        подчёркивания. В `LIKE` подчёркивание — метасимвол «любой один
        символ», и подстрока первого нашла бы оба, дав сумму вдвое
        больше. Все рабочие подстроки этой оси — с подчёркиванием.
        """
        pg._psql("SELECT 1 AS zz_one, 2, 3", container=self.base.container)
        pg._psql("SELECT 1 AS zzXone, 2, 3, 4", container=self.base.container)
        self.assertEqual(self.base.statement_calls("zz_one"), 1)
        self.assertEqual(self.base.statement_calls("zzXone"), 1)

    def test_the_query_text_tells_a_parameter_from_a_literal(self):
        """Внешний per-variant признак подготовленности — текст запроса."""
        self._session("probe-param",
                      "PREPARE q(int) AS SELECT pg_sleep($1); EXECUTE q(6);")
        self._session("probe-literal", "SELECT pg_sleep(6);")
        param = self.base.active_queries("probe-param")
        literal = self.base.active_queries("probe-literal")
        self.assertTrue(param and "$1" in param[0]["query"])
        self.assertTrue(literal and "$1" not in literal[0]["query"])

    def test_the_stored_text_is_the_first_occurrence_of_its_kind(self):
        """ЗАМЕР, ОГРАНИЧИВАЮЩИЙ `statement_calls`.

        Два запроса одной формы (различаются литералом и псевдонимом
        столбца) кладутся статистикой в ОДНУ строку, и текст у неё —
        текст ПЕРВОГО вхождения. Значит, второй запрос посчитан, но по
        своей собственной подстроке не находится вовсе: искать в
        статистике по тексту можно только то, что входит в нормализацию.
        """
        pg._psql("SELECT length('x') AS alpha_marker",
                 container=self.base.container)
        pg._psql("SELECT length('yy') AS beta_marker",
                 container=self.base.container)
        self.assertEqual(self.base.statement_calls("alpha_marker"), 2,
                         "второй запрос не попал в строку первого")
        self.assertEqual(self.base.statement_calls("beta_marker"), 0,
                         "текст второго запроса вдруг сохранён: ограничение "
                         "statement_calls надо перепроверить")

    def test_a_real_query_is_counted_by_its_normalized_text(self):
        before = self.base.statement_calls("pg_sleep")
        pg._psql("SELECT pg_sleep(0)", container=self.base.container)
        self.assertGreater(self.base.statement_calls("pg_sleep"), before)

    def test_the_view_is_unreadable_without_the_preload_flag(self):
        """Отсутствие расширения ГРОМКОЕ — и это замерено, а не обещано.

        ЗАПИСАННЫЙ ЗАМЕР — ПРО ДРУГУЮ ОШИБКУ, ЧЕМ КАЖЕТСЯ. Без
        `CREATE EXTENSION` чтение отвечает «relation … does not exist» —
        это про отсутствие расширения ВООБЩЕ, а не про флаг. Утверждение
        стенда («команда ПРОХОДИТ, а чтение отвечает ошибкой про
        shared_preload_libraries») воспроизводится только если расширение
        сперва завести. Первая редакция этой проверки ловила соседнюю
        ошибку и выглядела подтверждением записанного.
        """
        plain = pg.start(container="stand-pg-plain", network="stand-pg-plain",
                         statements=False)
        try:
            created = pg._psql("CREATE EXTENSION " + pg.STATEMENTS_EXTENSION,
                               container=plain.container)
            self.assertEqual(created.returncode, 0,
                             "CREATE EXTENSION без флага не прошёл: "
                             "записанный замер надо перепроверить")
            with self.assertRaises(RuntimeError) as refusal:
                pg.statement_calls("pg_sleep", container=plain.container)
            self.assertIn("shared_preload_libraries", str(refusal.exception))
        finally:
            plain.stop()


if __name__ == "__main__":
    unittest.main()
