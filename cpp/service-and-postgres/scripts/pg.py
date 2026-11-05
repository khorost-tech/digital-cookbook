"""ЕДИНСТВЕННОЕ место, где стенд поднимает PostgreSQL и читает счётчики.

ПОЧЕМУ СЧЁТЧИКИ ИЗ БАЗЫ. Драйвер может сказать, что открыл одно
соединение и подготовил запрос. База знает, что было на самом деле. Это
то же различие, на котором волна A развела объявленную версию библиотеки
и измеренную.

ПОЧЕМУ ОДИН МОДУЛЬ, А НЕ ПО ОСНАСТКЕ НА ОСЬ. Вторая функция, поднимающая
базу по-своему, — это второй набор условий замера, о котором фикстура
молчит: роль, база, флаги сервера и способ проверки готовности влияют на
то, что покажут счётчики. Проверенная логика подъёма пришла из
`scripts/dbwait.py` (ось 1) вместе со своими обоснованиями, добытыми
шестью кругами ревью, — они сохранены здесь дословно по сути, а сам
`dbwait` теперь зовёт эти функции, а не повторяет их.

ЧТО ИЗМЕРЕНО ПРО САМО ЭТО ЧТЕНИЕ (не выведено из документации):

* `pg_prepared_statements` — представление СЕССИИ. В PostgreSQL 18 у него
  столбцы name, statement, prepare_time, parameter_types, result_types,
  from_sql, generic_plans, custom_plans: ни `pid`, ни
  `application_name`, то есть и соединить его с `pg_stat_activity`
  не по чему. Замер: пока одна сессия держала подготовленный запрос,
  отдельная сессия читала оттуда НОЛЬ. Подробности — в `prepared`.
* `pg_stat_statements` требует флага при СТАРТЕ сервера. Замер: без него
  `CREATE EXTENSION` проходит, а чтение представления отвечает ошибкой.
  Громко — но только если ответ базы читается, а не превращается в ноль.
* `pg_stat_statements` хранит НОРМАЛИЗОВАННЫЙ текст: `SELECT pg_sleep(4)`
  лежит там как `SELECT pg_sleep($1)`. Подробности — в `statement_calls`.
"""

import io
import json
import os
import sys
import time

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts import docker as dockerlib  # noqa: E402

IMAGE_FILE = os.path.join(STAND, "images", "postgres.json")

# Имя контейнера и сети. Одно на стенд: две оси, поднявшие базу под
# разными именами, поднимали бы две разные базы и не замечали этого.
CONTAINER = "stand-pg"
NETWORK = "stand-pg"

# Роль, база и пароль стенда. Сервис ходит сюда под ролью стенда, а
# счётчики читаются суперпользователем: `pg_stat_activity` показывает
# чужие запросы только ему, и наблюдатель, ходящий под ролью стенда,
# видел бы вместо запросов варианта NULL.
ROLE = "stand"
DATABASE = "stand"
PASSWORD = "stand"
SUPERUSER = "postgres"

STATEMENTS_EXTENSION = "pg_stat_statements"
PRELOAD_FLAG = "shared_preload_libraries=" + STATEMENTS_EXTENSION

# Сколько ждать готовности. Число большое НАМЕРЕННО: это предел, за
# которым подъём объявляется несостоявшимся, а не пауза, которую
# выжидают. Опрос уходит, как только база ответила.
READY_TIMEOUT_S = 90.0
# Сколько раз повторить заведение роли. Сервер мог перезапуститься между
# проверкой готовности и этой командой — см. `_create_role`.
ROLE_TRIES = 10


class SessionScopedView(RuntimeError):
    """Вопрос задан не о той сессии, и ответ на него был бы не про предмет.

    ПОЧЕМУ ОТДЕЛЬНЫЙ ТИП, А НЕ НОЛЬ. Ноль здесь означал бы «вариант
    ничего не подготовил», тогда как на самом деле «спросили не у того
    backend'а». Эти два ответа неразличимы числом и обязаны различаться
    типом — иначе ось 3 записала бы отрицательный контроль там, где его
    не ставили.
    """


def image_spec(path=IMAGE_FILE):
    """Объявление образа базы: images/postgres.json, закреплённый дайджест.

    `path` — только для проверки самого сторожа. Настоящий файл закреплён
    дайджестом, поэтому на нём сторож НИКОГДА НЕ ИСПОЛНЯЕТСЯ: его можно
    было снять целиком, и набор проверок этого не заметил бы. Подменное
    объявление — единственный способ задать сторожу его собственный
    вопрос.
    """
    with io.open(path, encoding="utf-8") as handle:
        spec = json.loads(handle.read())
    if "@sha256:" not in spec["image"]:
        raise RuntimeError(
            "образ базы закреплён не дайджестом: %r — под тем же тегом "
            "завтра лежит другое содержимое" % spec["image"])
    return spec


def image():
    return image_spec()["image"]


def dsn(container=CONTAINER):
    """Адрес базы ДЛЯ СЕРВИСА: он ходит по имени контейнера внутри сети."""
    return ("host=%s port=5432 dbname=%s user=%s password=%s"
            % (container, DATABASE, ROLE, PASSWORD))


class Database:
    """Поднятая база: адрес, сеть, имя контейнера и способ её погасить.

    Контекст: выход из `with` гасит контейнер и убирает сеть ДАЖЕ при
    исключении внутри. Контейнер, переживший неудачный замер, — это
    чужие соединения в `pg_stat_activity` следующего замера, то есть
    искажённые счётчики без единого признака того, что что-то пошло не
    так.
    """

    def __init__(self, container, network, host=None, statements=False):
        self.container = container
        self.network = network
        self.host = host
        self.statements = statements
        self.dsn = dsn(container)

    def __enter__(self):
        return self

    def __exit__(self, kind, value, trace):
        self.stop()
        return False

    def stop(self):
        stop(container=self.container, network=self.network, host=self.host)

    # Тонкие обёртки: тот же вопрос, но без повторения имени контейнера.
    def connections(self, application_name):
        return connections(application_name, container=self.container,
                           host=self.host)

    def active_queries(self, application_name):
        return active_queries(application_name, container=self.container,
                              host=self.host)

    def active_query_count(self, application_name):
        return active_query_count(application_name, container=self.container,
                                  host=self.host)

    def prepared(self, application_name, ask=None):
        return prepared(application_name, ask=ask, container=self.container,
                        host=self.host)

    def statement_calls(self, text_fragment):
        if not self.statements:
            raise RuntimeError(
                "база поднята БЕЗ %s: число вызовов читать неоткуда, и ноль "
                "здесь означал бы отсутствие расширения, а не отсутствие "
                "вызовов" % STATEMENTS_EXTENSION)
        return statement_calls(text_fragment, container=self.container,
                               host=self.host)


def _psql(sql, container=CONTAINER, host=None, database=DATABASE):
    """Один запрос от суперпользователя; возвращает исполненный процесс."""
    return dockerlib._host(host).try_run(
        ["docker", "exec", container, "psql", "-U", SUPERUSER,
         "-d", database, "-tAc", sql])


def _quoted(value):
    return value.replace("'", "''")


def _number(done, what):
    """Ответ базы, прочитанный ЧИСЛОМ, — или громкий отказ.

    ПОЧЕМУ НЕ str И НЕ «похоже на число». Строка «2» сравнивается с
    числом 2 неверно и молча, а `int(...)` без проверки превратил бы
    сообщение об ошибке в исключение с непонятным текстом. Здесь
    проверяется и код возврата, и то, что ответ состоит из цифр: база,
    ответившая «ERROR: …» или пустотой, — это НЕ ноль, а невозможность
    спросить.
    """
    text = (done.stdout or "").strip()
    if done.returncode != 0 or not text.isdigit():
        raise RuntimeError(
            "%s: база не ответила числом: rc=%s, %r"
            % (what, done.returncode, (done.stdout or "") + (done.stderr or "")))
    return int(text)


def _application_name(value):
    """Имя варианта, которым МОЖНО фильтровать.

    Пустая строка — не фильтр: `application_name = ''` совпадает с любым
    соединением, у которого имя не выставлено, а таких в базе сколько
    угодно (у psql самого наблюдателя имя своё, но у фоновых процессов
    его нет вовсе). Счёт по такому «фильтру» вернул бы правдоподобное
    число ни о чём.
    """
    if not isinstance(value, str) or not value.strip():
        raise ValueError(
            "имя приложения пусто: фильтр, который ничему не соответствует "
            "явно, считает чужие соединения")
    return _quoted(value)


def wait_ready(timeout_s=READY_TIMEOUT_S, container=CONTAINER, host=None):
    """Ждёт базу ОПРОСОМ, а не задержкой. Возвращает, как только ответила.

    ПОЧЕМУ ЗАПРОСОМ, А НЕ pg_isready. Образ PostgreSQL на время
    разворачивания поднимает ВРЕМЕННЫЙ сервер, и pg_isready отвечает
    «готов» ещё ему: следом заведение роли падало с кодом 2 посреди
    прогона. Готовность — это ВЫПОЛНЕННЫЙ запрос.

    ПОЧЕМУ НЕ ФИКСИРОВАННАЯ ПАУЗА. Пауза «на глазок» прошла бы на
    быстрой машине и развалилась на медленной, а первый замер поймал бы
    не то — и поймал бы молча: счётчики на непрогретой базе выглядят
    обычными числами. 0.1 ниже — шаг ОПРОСА между попытками, а не срок
    ожидания: цикл уходит по первому удачному ответу.
    """
    deadline = time.time() + timeout_s
    attempts = 0
    while time.time() < deadline:
        attempts += 1
        done = _psql("SELECT 1", container=container, host=host,
                     database=SUPERUSER)
        if done.returncode == 0 and (done.stdout or "").strip() == "1":
            return attempts
        time.sleep(0.1)
    raise RuntimeError(
        "PostgreSQL не ответил на запрос за %.0f с (%d попыток): готовность "
        "не наступила" % (timeout_s, attempts))


def _create_role(container=CONTAINER, host=None, tries=ROLE_TRIES):
    """Роль и база стенда — с повторами.

    Повторы здесь по той же причине, по какой готовность проверяется
    запросом: сервер мог перезапуститься между проверкой и этой
    командой, и заведение роли упало бы посреди удачного во всём
    остальном подъёма.
    """
    done = None
    for _ in range(tries):
        done = dockerlib._host(host).try_run(
            ["docker", "exec", container, "psql", "-U", SUPERUSER, "-q",
             "-c", "CREATE ROLE %s LOGIN PASSWORD '%s';" % (ROLE, PASSWORD),
             "-c", "CREATE DATABASE %s OWNER %s;" % (DATABASE, ROLE)])
        if done.returncode == 0:
            return
        time.sleep(1)
    raise RuntimeError("не удалось завести роль и базу стенда: %s"
                       % (done.stderr or done.stdout if done else "ни одной "
                          "попытки"))


def start(host=None, container=CONTAINER, network=NETWORK, statements=True,
          timeout_s=READY_TIMEOUT_S):
    """Поднимает базу и возвращает контекст с её адресом.

    `statements` — включать ли `pg_stat_statements`. Флаг передаётся
    СЕРВЕРУ ПРИ СТАРТЕ (`shared_preload_libraries`): расширение,
    добавленное к уже поднятому серверу одним `CREATE EXTENSION`, не
    активируется — это ИЗМЕРЕНО, см. images/postgres.json. Ось 1
    (`scripts/dbwait.py`) поднимает базу без статистики намеренно: там
    предмет — занятость рабочих единиц, статистика не бесплатна, и
    фикстура той оси снята без неё.
    """
    runner = dockerlib._host(host)
    runner.try_run(["docker", "network", "create", network])
    # Остаток прошлого прогона убирается ДО старта: контейнер, переживший
    # предыдущий замер, — это чужие соединения в счётчиках этого.
    runner.try_run(["docker", "rm", "-f", container])
    cmd = ["docker", "run", "-d", "--name", container, "--network", network,
           "-e", "POSTGRES_PASSWORD=postgres", image()]
    if statements:
        cmd += ["-c", PRELOAD_FLAG]
    runner.run(cmd)
    wait_ready(timeout_s=timeout_s, container=container, host=host)
    _create_role(container=container, host=host)
    if statements:
        _psql("CREATE EXTENSION IF NOT EXISTS " + STATEMENTS_EXTENSION,
              container=container, host=host)
    return Database(container, network, host=host, statements=statements)


def stop(container=CONTAINER, network=NETWORK, host=None):
    runner = dockerlib._host(host)
    runner.try_run(["docker", "rm", "-f", container])
    runner.try_run(["docker", "network", "rm", network])


def connections(application_name, container=CONTAINER, host=None):
    """Сколько СОЕДИНЕНИЙ варианта есть в базе — включая простаивающие.

    Это ось 2: драйвер объявляет размер пула, база показывает, сколько
    соединений он открыл на самом деле. Состояние здесь НЕ фильтруется
    намеренно: соединение, ничего сейчас не выполняющее, — всё равно
    открытое соединение, и пул меряется именно им.
    """
    name = _application_name(application_name)
    return _number(
        _psql("SELECT count(*) FROM pg_stat_activity"
              " WHERE application_name = '%s'" % name,
              container=container, host=host),
        "соединения варианта %s" % application_name)


def active_query_count(application_name, container=CONTAINER, host=None):
    """ВТОРОЙ ИСТОЧНИК: сколько запросов варианта выполняется ПРЯМО СЕЙЧАС.

    Считается по application_name, который сервис выставляет равным имени
    варианта, и по состоянию active — то есть запрос идёт прямо сейчас.
    Это наблюдение СНАРУЖИ предмета: счётчик в сервисе на него повлиять
    не может никак.

    Своё собственное соединение сюда не попадает: у psql своё имя
    приложения, и оно не равно имени варианта.
    """
    name = _application_name(application_name)
    return _number(
        _psql("SELECT count(*) FROM pg_stat_activity"
              " WHERE application_name = '%s' AND state = 'active'" % name,
              container=container, host=host),
        "второй источник")


def active_queries(application_name, container=CONTAINER, host=None):
    """Те же живые запросы, но СТРОКАМИ: pid, состояние, текст запроса.

    ЗАЧЕМ ТЕКСТ. Он — единственный per-variant признак подготовленности,
    видимый СНАРУЖИ: замер показал, что у соединения с подготовленным
    запросом `pg_stat_activity.query` содержит `$1`, а у литерального
    плеча — саму величину. Счётчик подготовленных запросов снаружи не
    читается вовсе (см. `prepared`), и ось 3 опирается в том числе на
    эту строку.
    """
    name = _application_name(application_name)
    done = _psql(
        "SELECT pid || E'\\t' || state || E'\\t' || replace(query, E'\\n', ' ')"
        " FROM pg_stat_activity"
        " WHERE application_name = '%s' AND state = 'active'"
        " ORDER BY pid" % name, container=container, host=host)
    if done.returncode != 0:
        raise RuntimeError(
            "живые запросы варианта %s не прочитались: %s"
            % (application_name, done.stderr or done.stdout))
    rows = []
    for line in (done.stdout or "").splitlines():
        if not line.strip():
            continue
        pid, _, rest = line.partition("\t")
        state, _, query = rest.partition("\t")
        rows.append({"pid": int(pid), "state": state, "query": query})
    return rows


def prepared(application_name, ask=None, container=CONTAINER, host=None):
    """Сколько запросов подготовила СЕССИЯ ВАРИАНТА.

    ЭТОТ ВОПРОС НЕЛЬЗЯ ЗАДАТЬ ИЗ ЧУЖОЙ СЕССИИ, и функция это говорит, а
    не подменяет вопрос похожим. `pg_prepared_statements` — представление
    СЕССИИ: в PostgreSQL 18 его столбцы — name, statement, prepare_time,
    parameter_types, result_types, from_sql, generic_plans, custom_plans.
    Ни `pid`, ни `application_name`: соединить его с `pg_stat_activity`
    не по чему, и отобрать строки «того backend'а» нечем.

    ЭТО ИЗМЕРЕНО, А НЕ ВЫЧИТАНО. Пока одна сессия держала подготовленный
    запрос (`connections` видел её по имени приложения), ОТДЕЛЬНАЯ сессия
    читала `SELECT count(*) FROM pg_prepared_statements` и получала НОЛЬ.
    То есть наивный счёт ответил бы про наблюдателя, всегда нулём, и
    отрицательный контроль оси 3 («у литерального плеча подготовленных
    ноль») прошёл бы одинаково у обоих плеч — подтверждая не устройство
    драйвера, а устройство вопроса.

    ОБХОДНОЙ ПУТЬ, КОТОРЫЙ РАБОТАЕТ. Спросить ту самую сессию. `ask` —
    исполнитель SQL ВНУТРИ соединения варианта (ось 3 подаёт сюда ручку
    сервиса, который держит это соединение сам); функция даёт ему
    запрос и читает ответ числом. Без `ask` она отказывает типом
    `SessionScopedView`, а не возвращает ноль.

    ИМЯ ВАРИАНТА ЗДЕСЬ НЕ УКРАШЕНИЕ. Перед тем как поверить ответу,
    функция убеждается, что вариант ВООБЩЕ есть в базе: ноль у
    неподключённого варианта означал бы «не подготовил», тогда как на
    самом деле — «не подключался».
    """
    if ask is None:
        raise SessionScopedView(
            "подготовленные запросы варианта %s из чужой сессии не видны: "
            "pg_prepared_statements — представление сессии без pid и "
            "application_name; нужен исполнитель ВНУТРИ соединения варианта"
            % application_name)
    if connections(application_name, container=container, host=host) == 0:
        raise RuntimeError(
            "варианта %s нет в базе ни одним соединением: счёт подготовленных "
            "запросов был бы про отсутствие соединения, а не про отсутствие "
            "подготовки" % application_name)
    answer = ask("SELECT count(*) FROM pg_prepared_statements")
    text = str(answer).strip()
    if not text.isdigit():
        raise RuntimeError(
            "сессия варианта %s ответила про подготовленные запросы не "
            "числом: %r" % (application_name, answer))
    return int(text)


def statement_calls(text_fragment, container=CONTAINER, host=None):
    """Сколько раз база выполнила запросы, чей текст содержит подстроку.

    СУММА ПО СТРОКАМ, А НЕ ОДНА СТРОКА. `pg_stat_statements` кладёт в
    одну строку запросы с одинаковым queryid и хранит текст ПЕРВОГО
    вхождения НОРМАЛИЗОВАННЫМ: замер показал, что `SELECT pg_sleep(4)`
    лежит там как `SELECT pg_sleep($1)`, а псевдоним столбца в
    нормализацию не входит вовсе. Отсюда два следствия, и оба важны для
    оси 3:

    * искать по подстроке С ЛИТЕРАЛОМ бесполезно — литерала в тексте нет;
    * две РАЗНЫЕ строки (разный queryid) могут иметь ОДИНАКОВЫЙ текст —
      параметризованный запрос и нормализованный литеральный выглядят
      одинаково. Поэтому текст запроса НЕ отличает подготовленное плечо
      от литерального, и отрицательный контроль оси 3 на него опираться
      не может (см. `prepared` и `active_queries`).

    ПОДСТРОКА ИЩЕТСЯ `position`, А НЕ `LIKE`. В `LIKE` символы `_` и `%`
    — метасимволы, а не буквальные знаки, и это не теория: подстрока
    `zz_one` находила заодно строку с `zzXone` и давала сумму 2 вместо 1
    (замерено). Рабочие подстроки этой оси — `pg_sleep`,
    `pg_prepared_statements` — все с подчёркиванием, то есть перебор был
    бы молчаливым и систематическим. Экранирование решило бы ту же
    задачу, но требует помнить про три символа сразу и про `ESCAPE`;
    `position` не имеет метасимволов вовсе.

    ОТСУТСТВИЕ РАСШИРЕНИЯ — НЕ НОЛЬ. Без флага при старте сервера чтение
    представления отвечает ошибкой; ошибка здесь становится отказом, а не
    нулём вызовов.
    """
    if not isinstance(text_fragment, str) or not text_fragment.strip():
        raise ValueError(
            "подстрока запроса пуста: под неё подходит любая строка "
            "статистики, и сумма была бы про всю базу разом")
    # `coalesce` здесь не украшение: сумма по ПУСТОМУ набору строк — NULL,
    # а не ноль, и psql печатает его пустотой. Без неё «ни одного такого
    # запроса» приходило бы отказом «база не ответила числом» — то есть
    # честный ноль был бы неотличим от невозможности спросить.
    return _number(
        _psql("SELECT coalesce(sum(calls), 0) FROM %s"
              " WHERE position('%s' in query) > 0"
              % (STATEMENTS_EXTENSION, _quoted(text_fragment)),
              container=container, host=host),
        "число вызовов запросов с подстрокой %r" % text_fragment)
