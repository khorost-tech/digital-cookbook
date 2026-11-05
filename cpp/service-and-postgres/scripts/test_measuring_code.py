"""Проверки САМОГО ИЗМЕРИТЕЛЯ — на подменном исполнителе, без докера.

ЗАЧЕМ ОНИ ПОЯВИЛИСЬ. Пятнадцать чужих диверсий прошли зелёными, и корень
у них один: измеряющий код сам не измерен. Замер занятости, внутренности
`startup_refusal`, `omit`, `run_variant` и вся проба ожидания в базе не
были покрыты ничем. Хуже того, живая проверка сверяла свежий замер с
фикстурой ТОЙ ЖЕ САМОЙ функцией: она ловит подделку фикстуры и не может
поймать дефект измерителя — по построению.

ЧТО ИЗМЕНИЛОСЬ ВО ВТОРОЙ РЕДАКЦИИ. Занятость больше не выводится из
длительностей. Сервис СЧИТАЕТ обработчиков в полёте и кладёт число в
контрактную строку ответа, а измеритель его читает. Поэтому и модель
здесь другая: она не изображает дрожание и не ждёт волн — она держит
семафор на заданное число рабочих единиц и ОТВЕЧАЕТ тем числом, которое
на самом деле было внутри.

ГЛАВНОЕ: У МОДЕЛИ ЕСТЬ, ЧЕМ СОЛГАТЬ. Счётчик — измеряющий код внутри
самого предмета, то есть слепой заодно с ним, и проверка, у которой нет
испорченного счётчика, ничего не контролирует. Поэтому модель умеет
докладывать настройку вместо занятости, не убывать на выходе, считать
собственный наблюдающий запрос и занижать второй источник. Каждое из
этого обязано ронять контроль.

ЧЕГО ЗДЕСЬ НЕТ. Настоящих образов: их меряет `LiveMeasurement`. Здесь
проверяется измеритель, а не то, что он намерил.
"""

import json
import threading
import time
import unittest
import urllib.error

from scripts import dbwait
from scripts import docker as dockerlib


class FakeContainer(object):
    def __init__(self, env, units, honours_units=True):
        self.env = env
        self.units = units
        self.variant = env.get("STAND_VARIANT", "")
        self.running = True
        self.exit_code = None
        # Семафор — это и есть модель рабочих единиц: сколько проб
        # держится разом, столько он и пропускает.
        self.gate = threading.Semaphore(units if honours_units else 1)
        self.inflight = 0
        self.max_inflight = 0
        self.entered_total = 0
        self.lock = threading.Lock()
        self.paths = []
        self.healthz_calls_seen = 0
        self.warmup_seen = False
        self.idle_check_seen = False


class FakeDocker(object):
    """Маленькая модель докера и сервиса: отвечает ДЕЙСТВИЕМ, а не «да».

    `honours_units` = False моделирует измеряемый сервис, у которого ручка
    не подключена: сколько единиц ни задай, держит он одну.
    `serves` = False моделирует сервис, отвечающий отказом.
    `ignores_missing_knob` = True моделирует точку входа, проглотившую
    испорченную настройку: контейнер живёт и порт открыт.

    Испорченные счётчики — отдельной группой, см. `counter_*` и
    `healthz_counts_itself`; второй источник портится `db_undercounts`.
    """

    CANNED = {
        "g++": "g++ (Debian 14.2.0-19) 14.2.0\n",
        "cmake": "cmake version 3.31.6\n",
        "PQXX_VERSION": '#  define PQXX_VERSION "7.10.0"\n',
        "flags.make": ("CXX_DEFINES = \nCXX_INCLUDES = -I/src\n"
                       "CXX_FLAGS = -O3 -DNDEBUG -std=gnu++20\n"),
        "BOOST_LIB_VERSION": "1_83\n",
        "DROGON_VERSION": "1.9.0\n",
        "crow_all.h": "1f" + "0" * 62 + "\n",
    }

    # Точка входа: сигнатура, сумма лежащего там файла, сумма собранного
    # бинарника. Три РАЗНЫХ случая, а не одна консерва: замер, сверяющий
    # сумму саму с собой, обязан на одном из них покраснеть.
    ENTRY_CASES = {
        "honest": ("7f454c46", "aa" * 32, "aa" * 32),
        "wrapper": ("23212f62", "bb" * 32, "aa" * 32),
        "swapped": ("7f454c46", "cc" * 32, "aa" * 32),
    }

    def __init__(self, honours_units=True, serves=True,
                 ignores_missing_knob=False, wrapper_entry=False,
                 swapped_entry=False, extra_delay_s=0.0, never_ready=False,
                 temp_server_answers=0, healthz_shares_units=False,
                 healthz_silent_when_busy=False, dropped_connections=0,
                 no_counter=False, counter_reports_units=False,
                 counter_never_decrements=False, healthz_counts_itself=False,
                 db_undercounts=False, refuses_with_200=False,
                 serves_with_503=False, capacity_wobbles=False,
                 db_answers_garbage=False, healthz_overclaims=0,
                 stale_idle_once=False, healthz_overclaims_at=None,
                 db_sees_nothing=False, healthz_overclaims_skip_idle=False,
                 healthz_returns_503=False):
        self.honours_units = honours_units
        self.serves = serves
        self.ignores_missing_knob = ignores_missing_knob
        self.entry_case = ("wrapper" if wrapper_entry
                           else "swapped" if swapped_entry else "honest")
        self.extra_delay_s = extra_delay_s
        self.never_ready = never_ready
        # Сколько раз временный сервер базы отвечает, прежде чем
        # перезапуститься: модель разворачивания PostgreSQL.
        self.temp_server_answers = temp_server_answers
        # Живость делит рабочие единицы с пробами: модель слоя, у которого
        # маршрут живости стоит в той же очереди. Под полной занятостью
        # такой сервис не отличим от умершего.
        self.healthz_shares_units = healthz_shares_units
        # Живость молчит, пока кто-то в полёте: слой встал целиком. Это
        # наблюдение, и оно обязано попасть в фикстуру, а не уронить замер.
        self.healthz_silent_when_busy = healthz_silent_when_busy
        # Сколько первых проб получат оборванное соединение: модель сбоя
        # инструмента, а не сервиса.
        self.dropped_connections = dropped_connections
        # --- испорченные счётчики -------------------------------------
        # Ответ без занятости вовсе: контракт сломан, замером не является.
        self.no_counter = no_counter
        # Счётчик докладывает НАСТРОЙКУ, а не занятость. Положительный
        # контроль такой счётчик проходит блестяще: число идёт за ручкой.
        # Ронять его обязан отрицательный.
        self.counter_reports_units = counter_reports_units
        # Счётчик не убывает на выходе: занятость растёт сама по себе.
        self.counter_never_decrements = counter_never_decrements
        # Маршрут живости считает собственный запрос: занятость читается
        # на единицу больше, чем есть.
        self.healthz_counts_itself = healthz_counts_itself
        # Второй источник видит одного участника, сколько бы их ни было:
        # источники обязаны разойтись, и это обязано быть записано.
        self.db_undercounts = db_undercounts
        # ДВА НЕСОГЛАСИЯ КОДА И ИСХОДА, И ОНИ РАЗНЫЕ. Отказ, приехавший с
        # кодом 200, проходит мимо сверки кода; обслуженный ответ с кодом
        # 503 проходит мимо сверки исхода. Пока модель умела только
        # «отказ с 503», любая из двух сверок прикрывала другую, и снятие
        # каждой по отдельности не роняло ничего.
        self.refuses_with_200 = refuses_with_200
        self.serves_with_503 = serves_with_503
        # Ёмкость гуляет от залпа к залпу: так ведёт себя слой, у
        # которого число обработчиков разом не воспроизводится. Стенд
        # обязан записать это отдельным полем, а не выдать среднее за
        # величину.
        self.capacity_wobbles = capacity_wobbles
        # База ответила не числом (перезапуск, обрыв, сообщение об
        # ошибке). Молча превратить такой ответ в ноль значило бы
        # записать «никого внутри» там, где спросить не удалось.
        self.db_answers_garbage = db_answers_garbage
        # Живость докладывает БОЛЬШЕ, чем клиент считает отправленным и
        # не завершённым, — величину, которую в полёте физически не
        # могло быть. Ловит это только независимая граница клиента: обе
        # существующие проверки (положительный и отрицательный контроль)
        # выведены из того же счётчика и такую подмену не видят.
        self.healthz_overclaims = healthz_overclaims
        # Первый ЛЮБОЙ вызов /healthz у контейнера отвечает фантомной
        # единицей, даже если внутри никого нет, — модель счётчика,
        # честного во всём, КРОМЕ значения ДО начала залпа. Существующая
        # проверка `idle_reads_zero` смотрит именно на этот момент;
        # положительный и отрицательный контроли его не касаются вовсе,
        # и решение о печати оси может молча перестать учитывать
        # `idle_reads_zero`, оставаясь зелёным, пока он честно True — эта
        # модель нужна, чтобы сделать `idle_reads_zero` РЕАЛЬНО False, не
        # трогая остальные два контроля.
        self.stale_idle_once = stale_idle_once
        # Занижение работает не только «всегда» или «никогда»: слой мог
        # быть честен при одной настройке и лгать при другой, и сведение
        # ПО ВСЕМ настройкам обязано увидеть нарушение хотя бы в одной, а
        # не только там, где солгали все разом.
        self.healthz_overclaims_at = healthz_overclaims_at
        # Пережим, честный именно на пробе «до залпа», — иначе он путает
        # два разных контроля: idle_reads_zero и границу клиента.
        self.healthz_overclaims_skip_idle = healthz_overclaims_skip_idle
        # Второй источник вообще ничего не видит — не занижает, а слеп
        # целиком. Отличается от `db_undercounts`: там источник хотя бы
        # что-то замечает (единицу), здесь — нет ничего никогда.
        self.db_sees_nothing = db_sees_nothing
        # Маршрут живости отвечает, но отказом (503): готовность обязана
        # требовать именно код 200, а не «пришёл хоть какой-то ответ».
        self.healthz_returns_503 = healthz_returns_503
        self.dropped = 0
        self.db_answers = 0
        self.roles_created = 0
        self.commands = []
        self.containers = {}
        self.ports = {}
        self.next_port = 41000
        self.started = []
        self.active = {}
        self.active_lock = threading.Lock()
        self.get_timeouts = []

    # --- исполнитель ----------------------------------------------------

    def run(self, cmd):
        self.commands.append(list(cmd))
        return _Completed(self._handle(cmd), 0)

    def try_run(self, cmd):
        self.commands.append(list(cmd))
        try:
            return _Completed(self._handle(cmd), 0)
        except Exception as failure:  # исполнитель не бросает, он отвечает
            return _Completed("", 1, str(failure))

    def _line(self, probe, outcome, inflight):
        row = {"kind": "service", "variant": "fake", "probe": probe,
               "outcome": outcome, "detail": "модель"}
        if not self.no_counter:
            row["inflight"] = inflight
        return json.dumps(row, ensure_ascii=False)

    def get(self, url, timeout):
        # Записан тайм-аут КАЖДОГО обращения: диверсия, вернувшая
        # `read_occupancy` к жёстко зашитому числу вместо константы
        # HEALTHZ_TIMEOUT_S, ловится только чтением того, что реально
        # уехало в вызов, — по возвращённому значению её не отличить.
        self.get_timeouts.append(timeout)
        port = int(url.split("//", 1)[1].split("/", 1)[0].rsplit(":", 1)[1])
        path = "/" + url.split("//", 1)[1].split("/", 1)[1]
        container = self.ports.get(port)
        if container is None or not container.running:
            raise urllib.error.URLError("порт закрыт")
        container.paths.append(path)
        if path.startswith("/healthz"):
            return self._healthz(container)
        return self._wait(container, path)

    def _healthz(self, container):
        if self.never_ready:
            raise urllib.error.URLError("сервис молчит")
        if self.healthz_returns_503:
            # ОТВЕТИЛ, НО НЕ ОБСЛУЖИЛ. Готовность обязана требовать код
            # 200, а не «пришёл хоть какой-то ответ» — статус ненулевой
            # у любого кода, и «if status» пропустило бы отказ так же
            # легко, как успех.
            return 503, self._line("healthz", "refused", 0)
        if self.healthz_shares_units:
            with container.gate:
                pass
        with container.lock:
            container.healthz_calls_seen += 1
            # ЭТО ПЕРВОЕ ОБРАЩЕНИЕ ПОСЛЕ РАЗОГРЕВА — ПРОБА ЗАНЯТОСТИ «ДО
            # ЗАЛПА». Готовность сервиса (`run_variant`) сама опрашивает
            # /healthz ДО того, как `volley()` снимет свою собственную
            # занятость «до залпа»; «первый вызов вообще» бил бы по этому
            # опросу готовности, а не по проверяемому месту.
            is_idle_check = container.warmup_seen and not container.idle_check_seen
            if is_idle_check:
                container.idle_check_seen = True
            deliver_stale = self.stale_idle_once and is_idle_check
            seen = container.inflight
        if deliver_stale:
            return 200, self._line("healthz", "served", 1)
        if self.healthz_silent_when_busy and seen:
            raise urllib.error.URLError("слой встал целиком")
        if self.healthz_counts_itself:
            seen += 1
        # ПЕРЕЖИМ, ЧЕСТНЫЙ ИМЕННО НА ПРОБЕ «ДО ЗАЛПА». Счётчик, который
        # лжёт только под нагрузкой, но не на пустом сервисе, обязан
        # ловиться границей клиента — а НЕ побочно, через idle_reads_zero:
        # эти два контроля проверяют разное, и диверсия, различающая их,
        # обязана иметь способ остаться незамеченной ровно ОДНИМ из них.
        overclaim_here = (self.healthz_overclaims and not (
            self.healthz_overclaims_skip_idle and is_idle_check)
            and (self.healthz_overclaims_at is None
                or container.units in self.healthz_overclaims_at))
        if overclaim_here:
            seen += self.healthz_overclaims
        return 200, self._line("healthz", "served", seen)

    def _wait(self, container, path):
        wait_ms = int(path.split("=")[-1])
        if wait_ms == 0:
            # Разогревающий запрос стенда: всё, что происходит после
            # него, уже относится к постановке замера, а не к готовности
            # сервиса.
            container.warmup_seen = True
        if wait_ms and self.dropped < self.dropped_connections:
            self.dropped += 1
            raise ConnectionResetError("соединение оборвано")
        if not self.serves:
            return 503, self._line("q", "refused", 1)
        if self.refuses_with_200:
            return 200, self._line("q", "refused", 1)
        # ЗАНЯТОСТЬ СЧИТАЕТСЯ ВНУТРИ ЕДИНИЦЫ. Запрос, стоящий в очереди за
        # семафором, обработчика ещё не занял, и считать его занятым
        # значило бы записывать очередь как ёмкость.
        with container.gate:
            with container.lock:
                container.inflight += 1
                container.entered_total += 1
                entered = container.inflight
                container.max_inflight = max(container.max_inflight,
                                             container.inflight)
            if path.startswith("/q"):
                with self.active_lock:
                    self.active[container.variant] = (
                        self.active.get(container.variant, 0) + 1)
            try:
                time.sleep(wait_ms / 1000.0 + self.extra_delay_s)
            finally:
                if path.startswith("/q"):
                    with self.active_lock:
                        self.active[container.variant] -= 1
                with container.lock:
                    if not self.counter_never_decrements:
                        container.inflight -= 1
        if self.counter_reports_units:
            entered = container.units
        if self.counter_never_decrements:
            entered = container.entered_total
        if self.serves_with_503:
            return 503, self._line("block", "served", entered)
        return 200, self._line("block", "served", entered)

    def port_answers(self, port, timeout=0.3):
        container = self.ports.get(port)
        return bool(container and container.running)

    # --- разбор команд --------------------------------------------------

    def _handle(self, cmd):
        if cmd[:2] == ["docker", "build"]:
            return ""
        if cmd[:2] == ["docker", "run"] and "-d" in cmd:
            return self._start(cmd)
        if cmd[:2] == ["docker", "run"]:
            return self._ask(cmd)
        if cmd[:2] == ["docker", "port"]:
            container = self.containers[cmd[2]]
            port = [p for p, c in self.ports.items() if c is container][0]
            return "0.0.0.0:%d\n" % port
        if cmd[:2] == ["docker", "inspect"]:
            container = self.containers[cmd[2]]
            return json.dumps({"Running": container.running,
                               "ExitCode": container.exit_code})
        if cmd[:3] == ["docker", "image", "inspect"]:
            return json.dumps({"Env": ["PATH=/usr/bin"],
                               "Entrypoint": ["/usr/local/bin/stand_service"],
                               "Cmd": None})
        if cmd[:2] == ["docker", "rm"]:
            return ""
        if cmd[:3] == ["docker", "network", "create"]:
            return ""
        if cmd[:3] == ["docker", "network", "rm"]:
            return ""
        if cmd[:2] == ["docker", "exec"]:
            return self._database(cmd)
        raise AssertionError("подменный докер не знает команды: %s" % cmd)

    def _start(self, cmd):
        env = {}
        for index, item in enumerate(cmd):
            if item == "-e":
                key, _, value = cmd[index + 1].partition("=")
                env[key] = value
        name = "fake-%d" % len(self.containers)
        has_knobs = all(knob in env for knob in dockerlib.KNOBS)
        units = int(env.get("STAND_WORKERS", "1"))
        if self.capacity_wobbles and len(self.containers) % 2 == 0:
            units = max(1, units - 1)
        container = FakeContainer(env, units, self.honours_units)
        if not has_knobs and not self.ignores_missing_knob:
            # Точка входа падает до открытия порта: порт не отвечает, код
            # возврата ненулевой.
            container.running = False
            container.exit_code = 1
        self.containers[name] = container
        self.ports[self.next_port] = container
        self.next_port += 1
        self.started.append({"env": env, "cmd": list(cmd)})
        return name + "\n"

    def _database(self, cmd):
        """Модель базы: подъём с временным сервером и ВТОРОЙ ИСТОЧНИК.

        `temp_server_answers` — сколько раз отвечает ВРЕМЕННЫЙ сервер
        разворачивания. На нём запрос проходит, а заведение роли падает:
        ровно это и случилось посреди живого замера. Отрицательное
        значение — база, которая не поднимется никогда.

        Запрос к pg_stat_activity отвечает числом запросов варианта,
        которые ПРЯМО СЕЙЧАС внутри: это наблюдение снаружи сервиса, и
        испортить его можно независимо от счётчика (`db_undercounts`).
        """
        request = " ".join(cmd)
        if "pg_stat_activity" in request:
            # МОДЕЛЬ ОТВЕЧАЕТ ТОЛЬКО НА ТОТ ВОПРОС, КОТОРЫЙ ПОНИМАЕТ.
            # Второй источник обязан спрашивать про ИМЯ ВАРИАНТА и про
            # запросы, идущие ПРЯМО СЕЙЧАС. Счёт простаивающих соединений
            # или чужих имён — другая величина, и выдавать её за занятость
            # нельзя; здесь такой вопрос отвергается, а не обслуживается
            # молча числом, похожим на правду.
            if "application_name = '" not in request:
                raise AssertionError(
                    "второй источник спрашивает не про имя варианта: %s"
                    % request)
            if "state = 'active'" not in request:
                raise AssertionError(
                    "второй источник считает не только идущие запросы: %s"
                    % request)
            if self.db_answers_garbage:
                return "ERROR:  server closed the connection\n"
            if self.db_sees_nothing:
                return "0\n"
            variant = request.split("application_name = '")[1].split("'")[0]
            with self.active_lock:
                seen = self.active.get(variant, 0)
            return "%d\n" % (1 if self.db_undercounts and seen else seen)
        if self.temp_server_answers < 0:
            raise AssertionError("база не поднялась")
        creating = "CREATE ROLE" in request
        # ПРОБА ГОТОВНОСТИ ОТВЕЧАЕТ ЗНАЧЕНИЕМ, А НЕ ПУСТОТОЙ. Готовность —
        # это ВЫПОЛНЕННЫЙ запрос, то есть пришедшая единица; модель,
        # молчавшая с кодом возврата ноль, сходила бы за готовую базу у
        # проверки, читающей только код, и не отличала бы её от той,
        # которая отработала запрос.
        answer = "1\n" if "SELECT 1" in request else ""
        self.db_answers += 1
        if self.db_answers <= self.temp_server_answers:
            if creating:
                raise AssertionError(
                    "заведение роли на временном сервере разворачивания")
            return answer
        if creating:
            self.roles_created += 1
        return answer

    def _ask(self, cmd):
        request = " ".join(cmd)
        if "head -c 4" in request:
            # ОТВЕТ ПО ПУТИ, А НЕ КОНСЕРВОЙ. Модель, отвечавшая одинаково
            # на любую команду, не различала, какие пути суммируют, — и
            # замер, суммирующий ОДИН путь дважды, выглядел на ней
            # исправным.
            magic, entry_sum, built_sum = self.ENTRY_CASES[self.entry_case]
            answer = [magic]
            for path in _sum_targets(request):
                answer.append(entry_sum if path == dockerlib.ENTRY_PATH
                              else built_sum)
            return "\n".join(answer) + "\n"
        for needle, answer in self.CANNED.items():
            if needle in request:
                return answer
        raise AssertionError("подменный докер не знает вопроса: %s" % request)


def _sum_targets(request):
    """Пути, у которых команда просит сумму, В ПОРЯДКЕ обращения."""
    out = []
    for chunk in request.split("sha256sum ")[1:]:
        out.append(chunk.split()[0])
    return out


class _Completed(object):
    def __init__(self, stdout, returncode, stderr=""):
        self.stdout = stdout
        self.returncode = returncode
        self.stderr = stderr


# Ожидание в проверках измерителя. Ему больше не нужно быть длинным:
# занятость читается счётчиком, и опоздание ответа ничего не меняет.
FAST_MS = 300


def _fake_variants_row(served_concurrently=2, sha256=None):
    """Строка `fixtures/variants.jsonl` для проверок `dbwait` на FakeDocker.

    ЗАЧЕМ ОНА НУЖНА. `dbwait.control_headroom` и `dbwait.measure` читают
    запас контроля и сумму точки входа из НАСТОЯЩЕГО committed-файла —
    правильно для боевого вызова и бессмысленно для FakeDocker, у
    которого нет с этим файлом ничего общего. Без подмены каждая проверка
    здесь либо сверялась бы со случайно оказавшимся на диске чужим
    (настоящим) замером, либо не собиралась вовсе.
    """
    return {
        "entry_binary": {"sha256": sha256 or FakeDocker.ENTRY_CASES[
            "honest"][1]},
        "occupancy": {"at": [{"workers": dbwait.CONTROL_WORKERS,
                              "served_concurrently": served_concurrently}]},
    }


class OccupancyVolley(unittest.TestCase):
    """Залп — то, чем читается занятость."""

    def test_it_reads_what_the_model_actually_held(self):
        fake = FakeDocker()
        one = dockerlib.volley("beast", workers=1, probes=3,
                               wait_ms=FAST_MS, host=fake)
        three = dockerlib.volley("beast", workers=3, probes=3,
                                 wait_ms=FAST_MS, host=fake)
        self.assertEqual(dockerlib.peak_of(one), 1)
        self.assertEqual(dockerlib.peak_of(three), 3)
        self.assertEqual(one["counter_peak"], 1)
        self.assertEqual(three["counter_peak"], 3)

    def test_the_probes_arrive_simultaneously(self):
        # Пробы, ушедшие по очереди, дали бы единицу при любом числе
        # единиц — и слой выглядел бы держащим один запрос разом.
        fake = FakeDocker()
        dockerlib.volley("beast", workers=3, probes=3, wait_ms=FAST_MS,
                         host=fake)
        container = fake.containers["fake-0"]
        self.assertEqual(container.max_inflight, 3,
                         "пробы ушли не одновременно: разом в полёте было "
                         "%d" % container.max_inflight)

    def test_the_container_gets_the_number_that_will_be_written_down(self):
        # Замер, поднявший контейнер с одним числом и записавший другое,
        # не заметил бы никто: фикстура сошлась бы сама с собой.
        fake = FakeDocker()
        measured = dockerlib.volley("crow", workers=2, probes=2,
                                    wait_ms=FAST_MS, host=fake)
        self.assertEqual(measured["workers"], 2)
        self.assertEqual(fake.started[0]["env"]["STAND_WORKERS"], "2")
        self.assertEqual(fake.started[0]["env"]["STAND_VARIANT"], "crow")

    def test_an_unserved_probe_is_not_a_reading(self):
        # Проба, получившая отказ, возвращается быстро — и без сверки
        # исхода выглядела бы как свободная рабочая единица.
        fake = FakeDocker(serves=False)
        with self.assertRaises(RuntimeError) as caught:
            dockerlib.volley("beast", workers=1, probes=2, wait_ms=FAST_MS,
                             host=fake)
        self.assertNotIsInstance(
            caught.exception, dockerlib.StalledRun,
            "отказ сервиса принят за отказ инструмента: такой залп стали "
            "бы повторять, пока не выйдет удобный ответ")

    def test_a_refusal_that_came_with_a_good_status_is_still_a_refusal(self):
        # Отказ, приехавший с кодом 200, мимо сверки КОДА проходит
        # насквозь. Ловит его только сверка ИСХОДА — и наоборот. Пока
        # модель умела лишь «отказ с 503», каждая из двух сверок
        # прикрывала другую, и снятие любой не роняло ничего.
        fake = FakeDocker(refuses_with_200=True)
        with self.assertRaises(RuntimeError) as caught:
            dockerlib.volley("beast", workers=1, probes=2, wait_ms=FAST_MS,
                             host=fake)
        self.assertNotIsInstance(caught.exception, dockerlib.StalledRun)

    def test_a_served_outcome_with_a_bad_status_is_not_a_reading(self):
        # Обратная сторона: исход «обслужено» при коде 503. Занятость из
        # такого ответа читать нельзя — сервис сообщил об отказе.
        fake = FakeDocker(serves_with_503=True)
        with self.assertRaises(RuntimeError) as caught:
            dockerlib.volley("beast", workers=1, probes=2, wait_ms=FAST_MS,
                             host=fake)
        self.assertNotIsInstance(caught.exception, dockerlib.StalledRun)

    def test_a_reply_without_the_counter_is_not_a_measurement(self):
        # Занятость ЧИТАЕТСЯ из ответа. Ответ без неё — сломанный
        # контракт, и молча считать такой залп нулём или единицей нельзя.
        fake = FakeDocker(no_counter=True)
        with self.assertRaises(RuntimeError):
            dockerlib.volley("beast", workers=3, probes=2, wait_ms=FAST_MS,
                             host=fake)

    def test_the_wait_asked_for_is_the_wait_sent(self):
        fake = FakeDocker()
        dockerlib.volley("beast", workers=1, probes=1, wait_ms=FAST_MS,
                         host=fake)
        paths = fake.containers["fake-0"].paths
        self.assertIn("/block?ms=%d" % FAST_MS, paths,
                      "проба ушла с другим ожиданием: %s" % paths)

    def test_probes_handed_out_one_by_one_fail_instead_of_hanging(self):
        # Пробы отпускаются барьером — иначе они уходят вразнобой и
        # перекрытия может не случиться вовсе. Но барьер, ждущий проб,
        # которых не будет, ВИСИТ, а повисший замер неотличим от долгого.
        real = dockerlib.ThreadPoolExecutor

        class OneAtATime(real):
            def __init__(self, max_workers=None, *args, **kwargs):
                super(OneAtATime, self).__init__(max_workers=1, *args,
                                                 **kwargs)

        dockerlib.ThreadPoolExecutor = OneAtATime
        try:
            with self.assertRaises(threading.BrokenBarrierError):
                dockerlib.volley("beast", workers=2, probes=3,
                                 wait_ms=FAST_MS, host=FakeDocker())
        finally:
            dockerlib.ThreadPoolExecutor = real

    def test_the_warm_up_probe_goes_first_and_is_not_counted(self):
        # Разогревающая проба названа в коде важной — и снималась одной
        # диверсией без единого падения. Она обязана уйти ПЕРВОЙ, с
        # нулевым ожиданием, и не попасть в замер.
        fake = FakeDocker()
        measured = dockerlib.volley("beast", workers=3, probes=3,
                                    wait_ms=FAST_MS, host=fake)
        waits = [path for path in fake.containers["fake-0"].paths
                 if path.startswith("/block")]
        self.assertEqual(waits[0], "/block?ms=0",
                         "первой ушла не разогревающая проба: %s" % waits)
        self.assertEqual(
            waits.count("/block?ms=%d" % FAST_MS), 3,
            "замерных проб не три: %s" % waits)
        self.assertEqual(len(measured["inflight_by_probe"]), 3,
                         "разогревающая проба попала в замер")

    def test_the_liveness_route_is_asked_while_the_volley_is_in_flight(self):
        # Спека оси 1: послать запросы, дождаться, пока все примутся,
        # спросить маршрут живости. Вопрос, заданный до или после залпа,
        # не про занятость.
        fake = FakeDocker()
        measured = dockerlib.volley("beast", workers=3, probes=3,
                                    wait_ms=FAST_MS, host=fake)
        values = [call["value"] for call in measured["healthz_calls"]]
        self.assertGreater(len(measured["healthz_calls"]), 0)
        self.assertTrue(
            any(value and value > 0 for value in values),
            "живость спрошена, но ни разу не застала в полёте никого: %s"
            % values)


class CounterControls(unittest.TestCase):
    """Контроли счётчика. Без них ось недействительна.

    Счётчик живёт ВНУТРИ измеряемого предмета и слеп заодно с ним. Здесь
    он портится тремя способами, и каждый обязан быть пойман.
    """

    def test_an_honest_counter_reads_one_when_probes_do_not_overlap(self):
        control = dockerlib.negative_control(
            "beast", probes=3, wait_ms=FAST_MS, runs=2, host=FakeDocker())
        self.assertTrue(control["reads_one"])
        self.assertEqual(control["runs"], [1, 1])

    def test_a_counter_that_reports_the_setting_fails_the_negative_control(
            self):
        # ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ ТАКОЙ СЧЁТЧИК ПРОХОДИТ: число идёт за
        # ручкой идеально. Ловит его только постановка, где занятость
        # обязана быть единицей.
        fake = FakeDocker(counter_reports_units=True)
        evidence = dockerlib.knob_evidence([1, 1], [3, 3])
        self.assertTrue(evidence["follows_workers"])
        control = dockerlib.negative_control(
            "beast", probes=3, wait_ms=FAST_MS, runs=1, host=fake)
        self.assertFalse(
            control["reads_one"],
            "счётчик, докладывающий настройку, прошёл отрицательный "
            "контроль — контроль ничего не контролирует")

    def test_a_counter_that_never_decrements_is_seen_before_the_volley(self):
        # Занятость до залпа обязана читаться нулём. Счётчик, не
        # убывающий на выходе, растёт сам по себе.
        row = dockerlib.measure_variant(
            "beast", runs=1, probes=2, settings=(1, 3), wait_ms=FAST_MS,
            host=FakeDocker(counter_never_decrements=True))
        self.assertFalse(row["idle_reads_zero"])
        self.assertFalse(row["axis_1_publishable"])

    def test_a_counter_that_reports_the_setting_does_not_publish(self):
        # РАЗЛИЧАЮЩИЙ СЛУЧАЙ. У этого сервиса всё остальное честно:
        # занятость до залпа читается нулём, положительный контроль
        # проходит идеально — число растёт с настройкой. Не сходится
        # только отрицательный контроль. Если решение о печати перестанет
        # его учитывать, строка оси уедет в таблицу с испорченным
        # счётчиком.
        row = dockerlib.measure_variant(
            "beast", runs=1, probes=2, settings=(1, 3), wait_ms=FAST_MS,
            host=FakeDocker(counter_reports_units=True))
        self.assertTrue(row["idle_reads_zero"])
        self.assertTrue(row["knob_evidence"]["follows_workers"])
        self.assertFalse(row["negative_control"]["reads_one"])
        self.assertFalse(
            row["axis_1_publishable"],
            "строка оси объявлена печатаемой при провалившемся "
            "отрицательном контроле")

    def test_axis_publication_requires_idle_reads_zero_specifically(self):
        # РАЗЛИЧАЮЩИЙ СЛУЧАЙ ДЛЯ ТРЕТЬЕГО КОНТРОЛЯ. Прежде эта дыра была
        # НЕВИДИМА обычной сверкой «печать = AND трёх честных полей»: на
        # реальных данных idle_reads_zero и так был True, и его пропажа
        # из формулы AND ничего не меняла. Здесь idle_reads_zero сделан
        # ложным НАРОЧНО, а положительный и отрицательный контроль
        # остаются честными — только такая постановка доказывает, что
        # печать действительно смотрит на все три поля, а не на два.
        fake = FakeDocker(stale_idle_once=True)
        row = dockerlib.measure_variant(
            "beast", runs=1, probes=2, settings=(1, 3), wait_ms=FAST_MS,
            host=fake)
        self.assertFalse(row["idle_reads_zero"])
        self.assertTrue(row["knob_evidence"]["follows_workers"])
        self.assertTrue(row["negative_control"]["reads_one"])
        self.assertFalse(
            row["axis_1_publishable"],
            "печать прошла без учёта idle_reads_zero, хотя оба других "
            "контроля честны")

    def test_a_capacity_that_wobbles_is_recorded_as_unstable(self):
        # Ёмкость, гуляющая от залпа к залпу, — не величина, и всё, что
        # из неё выводится, выводить не из чего. Модель заставляет её
        # гулять; стенд обязан это записать, а не сгладить.
        wobbly = dockerlib.measure_variant(
            "drogon", runs=2, probes=3, settings=(1, 3), wait_ms=FAST_MS,
            host=FakeDocker(capacity_wobbles=True))
        self.assertFalse(
            wobbly["capacity_stable"],
            "ёмкость гуляла между залпами, а записана устойчивой: %s"
            % wobbly["occupancy"]["runs"])
        steady = dockerlib.measure_variant(
            "drogon", runs=2, probes=3, settings=(1, 3), wait_ms=FAST_MS,
            host=FakeDocker())
        self.assertTrue(steady["capacity_stable"])

    def test_an_honest_service_passes_both_controls_and_publishes(self):
        row = dockerlib.measure_variant(
            "beast", runs=1, probes=2, settings=(1, 3), wait_ms=FAST_MS,
            host=FakeDocker())
        self.assertTrue(row["idle_reads_zero"])
        self.assertTrue(row["negative_control"]["reads_one"])
        self.assertTrue(row["knob_evidence"]["follows_workers"])
        self.assertTrue(row["axis_1_publishable"])

    def test_a_service_that_ignores_the_knob_does_not_publish(self):
        row = dockerlib.measure_variant(
            "drogon", runs=1, probes=2, settings=(1, 3), wait_ms=FAST_MS,
            host=FakeDocker(honours_units=False))
        self.assertFalse(row["knob_evidence"]["follows_workers"])
        self.assertFalse(
            row["axis_1_publishable"],
            "строка оси объявлена печатаемой у сервиса, чья ручка ни к "
            "чему не подключена")


class LivenessReading(unittest.TestCase):
    """Маршрут живости: и ответ оси 1, и чтение занятости.

    ЗДЕСЬ НЕТ ГОТОВОГО ВЫВОДА «ОТВЕЧАЛА». Круг 5 нашёл: маршрут живости
    встаёт в очередь наравне с пробами и отвечает, только когда одна из
    занятых единиц освободилась. Ответ, пришедший так, показывает
    занятость УЖЕ ПОСЛЕ освобождения — заниженную, а не ту, что была в
    момент вопроса. Проверки здесь смотрят на сырую задержку, а не на
    производный вывод «успел / не успел».
    """

    def test_liveness_reports_the_occupancy_it_finds(self):
        fake = FakeDocker()
        measured = dockerlib.volley("beast", workers=3, probes=3,
                                    wait_ms=FAST_MS, host=fake)
        values = [call["value"] for call in measured["healthz_calls"]]
        self.assertIn(3, values,
                      "живость не увидела занятости, которую держала "
                      "модель: %s" % values)
        answers = [call for call in measured["healthz_calls"]
                  if call["value"] is not None]
        self.assertEqual(len(answers), len(measured["healthz_calls"]))
        for call in measured["healthz_calls"]:
            self.assertIsInstance(call["delay_ms"], int)
            self.assertGreaterEqual(call["delay_ms"], 0)

    def test_liveness_that_counts_itself_is_seen(self):
        # Наблюдатель, попавший в наблюдаемое, читает на единицу больше,
        # чем есть, — и занятость завышается ровно в сторону тезиса.
        fake = FakeDocker(healthz_counts_itself=True)
        measured = dockerlib.volley("beast", workers=1, probes=2,
                                    wait_ms=FAST_MS, host=fake)
        self.assertGreater(
            dockerlib.peak_of(measured), 1,
            "живость посчитала себя, а замер этого не увидел")

    def test_a_layer_that_stops_answering_is_recorded_not_crashed(self):
        # Слой, вставший целиком, — это НАБЛЮДЕНИЕ, и оно обязано попасть
        # в фикстуру. Уронить замер здесь значило бы потерять находку.
        fake = FakeDocker(healthz_silent_when_busy=True)
        measured = dockerlib.volley("beast", workers=3, probes=3,
                                    wait_ms=FAST_MS, host=fake)
        seen = dockerlib.liveness_of([measured])
        self.assertGreater(seen["asks"], 0)
        self.assertFalse(
            seen["every_call_got_a_value"],
            "молчащая под нагрузкой живость записана как ответившая")

    def test_liveness_queued_behind_the_probes_is_visible_as_delay(self):
        # Слой, у которого живость стоит в той же очереди, что и пробы,
        # под полной занятостью не отличим от умершего снаружи — но
        # ЗАДЕРЖКА ответа выдаёт очередь. Гейт держится этим тестом
        # НАПРЯМУЮ, а не гонкой с настоящей пробой: барьер `volley()`
        # отпускает пробу и опрос живости ОДНОВРЕМЕННО, и кто из двух
        # потоков первым войдёт в семафор — дело планировщика, а не
        # метода; такая проверка была бы то красной, то зелёной без
        # единой правки кода.
        fake = FakeDocker(healthz_shares_units=True)
        with dockerlib.run_variant("beast", workers=1, host=fake) as service:
            self.assertTrue(service["ready"])
            container = fake.containers[service["container"]]
            acquired = threading.Event()
            release = threading.Event()

            def hold():
                with container.gate:
                    acquired.set()
                    release.wait(timeout=2.0)

            holder = threading.Thread(target=hold)
            holder.start()
            self.assertTrue(acquired.wait(timeout=1.0),
                            "не удалось занять единственную единицу")
            result = {}

            def ask():
                result["value"], result["delay_ms"] = (
                    dockerlib.read_occupancy(service["port"], host=fake))

            asker = threading.Thread(target=ask)
            asker.start()
            time.sleep(0.2)
            release.set()
            asker.join(timeout=2.0)
            holder.join(timeout=2.0)
        self.assertGreaterEqual(
            result["delay_ms"], 150,
            "живость, стоящая в очереди за единственной занятой единицей, "
            "ответила быстро — очередь не создала задержки в модели")

    def test_the_healthz_timeout_bounds_a_single_call(self):
        # Тайм-аут — константа, а не аргумент со значением по умолчанию,
        # который диверсия может молча раздуть. Раздутое окно превращает
        # задержку под полной занятостью в тишину, которую опрос
        # переживает не падая, и находка про очередь под нагрузкой
        # становится ненаблюдаемой.
        self.assertEqual(dockerlib.HEALTHZ_TIMEOUT_S, 2.0)
        fake = FakeDocker(never_ready=True)
        # never_ready бросает исключение мгновенно (не ждёт тайм-аута) —
        # здесь важно только то, что вызов вообще принимает `timeout` и
        # не падает на его отсутствии.
        value, delay_ms = dockerlib.read_occupancy(41000, host=fake)
        self.assertIsNone(value)

    def test_liveness_of_tags_the_setting_it_was_actually_given(self):
        # Метка настройки обязана СЛЕДОВАТЬ из переданных залпов, а не
        # быть завёрнутой константой: залпы при одной единице обязаны
        # получить метку «1», а не «сколько бы ни передали — три».
        low_volley = {"workers": dockerlib.LOW_WORKERS, "healthz_calls": []}
        seen = dockerlib.liveness_of([low_volley])
        self.assertEqual(seen["workers"], dockerlib.LOW_WORKERS)

    def test_measure_variant_ties_liveness_to_the_high_setting(self):
        # Печатаемая строка оси говорит про ВЕРХНЮЮ настройку; живость,
        # молча снятая с нижней, отвечала бы не на тот вопрос.
        fake = FakeDocker()
        row = dockerlib.measure_variant(
            "beast", runs=1, probes=2, settings=(1, 3), wait_ms=FAST_MS,
            host=fake)
        self.assertEqual(row["liveness"]["workers"], dockerlib.HIGH_WORKERS)


class ClientBoundary(unittest.TestCase):
    """Граница клиента: независимая от счётчика верхняя оценка занятости.

    Число проб, которые клиент ЕЩЁ считает отправленными и не
    завершёнными, посчитано СНАРУЖИ сервиса — оно не зависит от того,
    что докладывает счётчик, и не может быть подделано той же правкой,
    что портит счётчик.
    """

    def test_an_honest_counter_never_exceeds_the_client_boundary(self):
        fake = FakeDocker()
        measured = dockerlib.volley("beast", workers=3, probes=3,
                                    wait_ms=FAST_MS, host=fake)
        self.assertTrue(measured["client_bound_holds"])
        for call in measured["healthz_calls"]:
            if call["value"] is not None:
                self.assertLessEqual(call["value"], call["client_pending"])

    def test_a_counter_that_overclaims_beyond_the_boundary_is_caught(self):
        # Счётчик, докладывающий больше, чем клиент считает отправленным
        # и не завершённым, — не может быть занятостью ни при каком
        # прочтении: столько запросов физически не было в полёте.
        fake = FakeDocker(healthz_overclaims=2)
        measured = dockerlib.volley("beast", workers=3, probes=3,
                                    wait_ms=FAST_MS, host=fake)
        self.assertFalse(
            measured["client_bound_holds"],
            "счётчик превысил границу клиента, а это не поймано")

    def test_the_boundary_is_sampled_before_asking_not_after(self):
        # Граница снимается ДО обращения к живости, а не после: пока
        # ответ в пути, ещё одна проба могла успеть завершиться, и
        # граница, снятая после, недооценила бы истинное число в момент
        # чтения счётчика. Снятая до — верна всегда, потому что
        # уменьшаться она может, увеличиваться — нет.
        fake = FakeDocker(healthz_shares_units=True)
        measured = dockerlib.volley("beast", workers=1, probes=2,
                                    wait_ms=FAST_MS, host=fake)
        self.assertTrue(measured["client_bound_holds"])
        # ПОРЯДОК СТРОК — то, что делает предыдущую проверку небезопасной
        # при перестановке. Задержку между двумя строками нельзя надёжно
        # вызвать на быстрой модели без гонки (см. отчёт круга 5), а
        # перестановка строк, поменявшая ИСТОЧНИК истины на «после»,
        # ничего не роняет ЗДЕСЬ и НИГДЕ иначе, кроме как под реальной
        # задержкой сети. Одна текстовая проверка порядка — меньшее зло,
        # чем дыра, которую ничто не ловит.
        import inspect
        source = inspect.getsource(dockerlib.volley)
        pending_before = source.index("client_pending = probes - sum")
        read_after = source.index("value, delay_ms = read_occupancy(port")
        self.assertLess(
            pending_before, read_after,
            "граница клиента снимается ПОСЛЕ обращения к живости, а не "
            "до — задержка ответа делает такую границу заниженной")

    def test_the_boundary_actually_varies_as_probes_finish(self):
        # `client_pending` обязан МЕНЯТЬСЯ по ходу залпа, а не быть
        # объявленным числом проб на все обращения разом: единица
        # держит пробы строго по очереди, и к концу залпа в полёте
        # остаётся меньше, чем в начале.
        fake = FakeDocker()
        measured = dockerlib.volley("beast", workers=1, probes=3,
                                    wait_ms=FAST_MS, host=fake)
        pendings = [call["client_pending"] for call in
                   measured["healthz_calls"]]
        self.assertGreater(
            len(set(pendings)), 1,
            "граница клиента не меняется по ходу залпа: %s" % pendings)
        self.assertEqual(
            pendings[0], 3,
            "граница в начале залпа не равна числу отправленных проб")
        self.assertLessEqual(min(pendings), 1)

    def test_the_row_level_boundary_requires_every_volley_not_just_one(self):
        # Нарушение ТОЛЬКО при одной единице (не при трёх) обязано
        # погасить сведённое поле целиком: «хотя бы один залп честен» —
        # не то же самое, что «граница держится».
        fake = FakeDocker(healthz_overclaims=5,
                          healthz_overclaims_at={dockerlib.LOW_WORKERS})
        row = dockerlib.measure_variant(
            "beast", runs=1, probes=2, settings=(1, 3), wait_ms=FAST_MS,
            host=fake)
        by_workers = {item["workers"]: item for item in row["occupancy"]["at"]}
        self.assertTrue(
            all(v["client_bound_holds"]
                for v in by_workers[dockerlib.HIGH_WORKERS]["volleys"]),
            "залпы при трёх единицах не тронуты диверсией и обязаны быть "
            "честны")
        self.assertTrue(
            any(not v["client_bound_holds"]
                for v in by_workers[dockerlib.LOW_WORKERS]["volleys"]),
            "залпы при одной единице обязаны показать нарушение")
        self.assertFalse(
            row["client_bound_holds"],
            "нарушение при одной настройке потерялось при сведении по "
            "всем настройкам")

    def test_a_boundary_violation_blocks_publication(self):
        # РАЗЛИЧАЮЩИЙ СЛУЧАЙ. Пережим бьёт ТОЛЬКО по верхней настройке и
        # ТОЛЬКО под нагрузкой (не на пробе «до залпа»): нижняя настройка
        # и отрицательный контроль остаются честны, а раздутая верхняя
        # всё равно больше нижней, так что положительный контроль тоже
        # не замечает подмены. Если бы печать держалась на всех трёх
        # ДРУГИХ полях, эта диверсия прошла бы зелёной по совсем другой
        # причине — и её никто бы не заметил.
        fake = FakeDocker(healthz_overclaims=5,
                          healthz_overclaims_skip_idle=True,
                          healthz_overclaims_at={dockerlib.HIGH_WORKERS})
        row = dockerlib.measure_variant(
            "beast", runs=1, probes=2, settings=(1, 3), wait_ms=FAST_MS,
            host=fake)
        self.assertTrue(row["idle_reads_zero"])
        self.assertTrue(row["knob_evidence"]["follows_workers"])
        self.assertTrue(row["negative_control"]["reads_one"])
        self.assertFalse(row["client_bound_holds"])
        self.assertFalse(
            row["axis_1_publishable"],
            "печать прошла при нарушенной границе клиента, хотя остальные "
            "три контроля честны")

    def test_read_occupancy_uses_the_pinned_timeout_by_default(self):
        # Тайм-аут обязан ехать константой в реальный вызов, а не
        # значением, зашитым отдельно и независимо от неё: раздутая или
        # подменённая константа тогда не отразилась бы ни на чём.
        fake = FakeDocker()
        dockerlib.read_occupancy(41000, host=fake)
        self.assertEqual(fake.get_timeouts[-1], dockerlib.HEALTHZ_TIMEOUT_S)


class DroppedConnections(unittest.TestCase):
    """Оборванное соединение — отказ инструмента, а не наблюдение."""

    def test_a_dropped_connection_is_retried_and_counted(self):
        fake = FakeDocker(dropped_connections=1)
        measured = dockerlib.measure_run("beast", workers=3, probes=3,
                                         wait_ms=FAST_MS, host=fake)
        self.assertEqual(measured["stalls"], 1)
        self.assertEqual(measured["attempts_used"], 2)
        self.assertEqual(dockerlib.peak_of(measured), 3)

    def test_a_refused_probe_is_not_a_stall(self):
        fake = FakeDocker(serves=False)
        with self.assertRaises(RuntimeError) as caught:
            dockerlib.measure_run("beast", workers=1, probes=2,
                                  wait_ms=FAST_MS, host=fake)
        self.assertNotIsInstance(caught.exception, dockerlib.StalledRun)
        # И главное: отказ НЕ ПОВТОРЯЛСЯ. Контейнер поднимался один раз.
        self.assertEqual(
            len(fake.started), 1,
            "отказ сервиса повторяли %d раз" % len(fake.started))

    def test_retries_are_bounded(self):
        with self.assertRaises(ValueError):
            dockerlib.measure_run("beast", workers=3, probes=3,
                                  wait_ms=FAST_MS,
                                  attempts=dockerlib.MAX_ATTEMPTS + 1,
                                  host=FakeDocker())
        always = FakeDocker(dropped_connections=99)
        with self.assertRaises(dockerlib.StalledRun):
            dockerlib.measure_run("beast", workers=3, probes=3,
                                  wait_ms=FAST_MS, attempts=2, host=always)


class Readiness(unittest.TestCase):
    """Готовность — это ОТВЕТ маршрута живости, а не объявление."""

    def test_a_silent_service_is_not_ready(self):
        fake = FakeDocker(never_ready=True)
        with dockerlib.run_variant("beast", workers=2, host=fake) as service:
            self.assertFalse(
                service["ready"],
                "готовность объявлена сервису, который не ответил ни разу")

    def test_a_silent_service_stops_the_measurement(self):
        fake = FakeDocker(never_ready=True)
        with self.assertRaises(RuntimeError):
            dockerlib.volley("beast", workers=2, probes=2, wait_ms=FAST_MS,
                             host=fake)

    def test_a_service_answering_with_a_refusal_is_not_ready(self):
        # ОТВЕТИЛ, НО НЕ ОБСЛУЖИЛ. Готовность — это код 200, а не «пришёл
        # хоть какой-то ответ»: код состояния у отказа тоже ненулевой, и
        # проверка на «истинность статуса» его не отличила бы от успеха.
        fake = FakeDocker(healthz_returns_503=True)
        with dockerlib.run_variant("beast", workers=2, host=fake) as service:
            self.assertFalse(
                service["ready"],
                "готовность объявлена сервису, ответившему отказом")


class DatabaseStartup(unittest.TestCase):
    """Подъём базы: временный сервер разворачивания не сходит за готовый.

    Образ PostgreSQL на время разворачивания поднимает временный сервер;
    на нём заведение роли падает. Этот кусок держался только живым
    прогоном — и сломался ровно так посреди замера.
    """

    def test_the_temporary_server_does_not_pass_for_the_real_one(self):
        fake = FakeDocker(temp_server_answers=2)
        dbwait.start_postgres(host=fake)
        self.assertGreaterEqual(
            fake.roles_created, 1,
            "роль стенда не заведена, а подъём базы объявлен удачным")

    def test_a_database_that_never_comes_up_is_refused(self):
        fake = FakeDocker(temp_server_answers=-1)
        with self.assertRaises(RuntimeError):
            dbwait.start_postgres(host=fake, timeout_s=2)


class EntryBinaryMeasurement(unittest.TestCase):
    """Единственная замеренная защита от обёртки вместо бинарника.

    До этого она была НЕФАЛЬСИФИЦИРУЕМА: подменный исполнитель отдавал две
    одинаковые суммы консервой, а живая проверка сверяла истину с истиной.
    """

    def test_an_honest_image_reports_the_binary_it_built(self):
        measured = dockerlib.entry_binary("beast", host=FakeDocker())
        self.assertTrue(measured["is_elf"])
        self.assertTrue(measured["same_as_build_output"])

    def test_a_wrapper_in_the_entry_point_is_seen(self):
        measured = dockerlib.entry_binary("beast",
                                          host=FakeDocker(wrapper_entry=True))
        self.assertFalse(measured["is_elf"],
                         "в точке входа сценарий, а замер видит бинарник")
        self.assertFalse(
            measured["same_as_build_output"],
            "суммы точки входа и собранного бинарника разошлись, а замер "
            "этого не заметил — похоже, он сверяет величину саму с собой")

    def test_an_elf_that_is_not_the_one_built_is_seen(self):
        measured = dockerlib.entry_binary("beast",
                                          host=FakeDocker(swapped_entry=True))
        self.assertTrue(measured["is_elf"])
        self.assertFalse(measured["same_as_build_output"])


class StartupRefusalMeasurement(unittest.TestCase):
    """`startup_refusal` — то, чем доказано, что порт не открывается."""

    def test_the_knob_is_omitted_and_not_emptied(self):
        fake = FakeDocker()
        measured = dockerlib.startup_refusal("beast", "STAND_WORKERS",
                                             host=fake)
        self.assertEqual(measured["exit_code"], 1)
        self.assertFalse(measured["port_open"])
        self.assertNotIn("STAND_WORKERS", fake.started[0]["env"],
                         "ручка передана пустой вместо того, чтобы не "
                         "передаваться вовсе")
        self.assertIn("STAND_VARIANT", fake.started[0]["env"],
                      "опущена не та ручка")

    def test_a_service_that_swallows_the_setting_is_not_reported_as_refusal(
            self):
        fake = FakeDocker(ignores_missing_knob=True)
        with self.assertRaises(RuntimeError):
            dockerlib.startup_refusal("beast", "STAND_VARIANT", seconds=1.0,
                                      host=fake)

    def test_only_a_knob_can_be_omitted(self):
        with self.assertRaises(ValueError):
            dockerlib.startup_refusal("beast", "STAND_DSN")


class KnobEvidence(unittest.TestCase):
    """Положительный контроль — и его отсутствие."""

    def test_a_connected_knob_holds_more_than_one_unit_would(self):
        self.assertTrue(
            dockerlib.knob_evidence([1, 1], [3, 3])["follows_workers"])
        self.assertTrue(
            dockerlib.knob_evidence([1, 1], [1, 2])["follows_workers"])

    def test_a_disconnected_knob_leaves_no_evidence(self):
        self.assertFalse(
            dockerlib.knob_evidence([1, 1], [1, 1])["follows_workers"])

    def test_one_unit_holding_two_is_not_evidence_but_a_broken_counter(self):
        # Если при ОДНОЙ единице прочитано двое, счётчик считает не
        # занятость единицы. Рост при трёх этого не искупает.
        evidence = dockerlib.knob_evidence([2, 2], [3, 3])
        self.assertFalse(evidence["one_unit_reads_one"])
        self.assertFalse(evidence["follows_workers"])

    def test_evidence_needs_both_settings(self):
        with self.assertRaises(ValueError):
            dockerlib.knob_evidence([], [3])

    def test_evidence_refuses_samples_of_different_size(self):
        with self.assertRaises(ValueError):
            dockerlib.knob_evidence([1, 1], [1, 1, 2])

    def test_the_whole_measurement_covers_every_setting_it_speaks_about(self):
        honest = dockerlib.measure_variant("drogon", runs=1, probes=3,
                                           wait_ms=FAST_MS, host=FakeDocker())
        settings = {item["workers"]: item
                    for item in honest["occupancy"]["at"]}
        self.assertEqual(sorted(settings), list(dockerlib.WORKER_SETTINGS),
                         "замер снят не при всех настройках, о которых "
                         "говорит стенд")
        self.assertEqual(settings[1]["runs"], [1])
        # Выборки при всех настройках одного размера — иначе сравнение
        # максимумов доказывало бы усердие, а не ручку.
        self.assertEqual(
            len({len(item["runs"]) for item in honest["occupancy"]["at"]}), 1)


class DatabaseWaitMeasurement(unittest.TestCase):
    """Проба «занимает ли ожидание в базе рабочую единицу»."""

    def test_the_verdict_says_only_what_the_runs_showed(self):
        seen = dbwait.verdict([1, 1, 1], [2, 2, 2], probes=2)
        self.assertTrue(seen["occupies_unit"])
        self.assertTrue(seen["control_overlaps_always"])
        partly = dbwait.verdict([1, 1, 1], [2, 1, 1], probes=2)
        self.assertTrue(partly["occupies_unit"])
        self.assertFalse(partly["control_overlaps_always"])
        self.assertTrue(partly["control_overlaps_ever"])
        never = dbwait.verdict([1, 1, 1], [1, 1, 1], probes=2)
        self.assertFalse(never["control_overlaps_always"])
        self.assertFalse(never["control_overlaps_ever"])

    def test_a_service_that_does_not_wait_leaves_no_occupancy(self):
        seen = dbwait.verdict([2, 2], [2, 2], probes=2)
        self.assertFalse(seen["occupies_unit"])

    def test_the_verdict_needs_both_settings(self):
        with self.assertRaises(ValueError):
            dbwait.verdict([1], [], probes=2)

    def test_the_probe_goes_to_the_database_handle_with_the_dsn(self):
        fake = FakeDocker()
        row = dbwait.measure("beast", host=fake, runs=1, probes=2,
                             sleep_ms=FAST_MS,
                             variants_row=_fake_variants_row())
        self.assertEqual(row["kind"], "db-wait")
        self.assertEqual(row["endpoint"], "q")
        # Ручка ожидания — именно /q: ожидание обязано уходить В БАЗУ, а
        # не в обработчик, иначе проба меряет соседний вопрос.
        first = fake.containers["fake-0"]
        self.assertTrue(any(path.startswith("/q?sleep_ms=")
                            for path in first.paths),
                        "проба ушла не в базу: %s" % first.paths)
        started = fake.started[0]
        self.assertIn("STAND_DSN", started["env"])
        self.assertIn("--network", started["cmd"])
        self.assertEqual([item["workers"] for item in row["at"]],
                         [1, dbwait.CONTROL_WORKERS])

    def test_the_measurement_reproduces_what_the_model_does(self):
        fake = FakeDocker()
        row = dbwait.measure("beast", host=fake, runs=1, probes=2,
                             sleep_ms=FAST_MS,
                             variants_row=_fake_variants_row())
        self.assertTrue(row["occupies_unit"])
        self.assertTrue(row["control_overlaps_always"])
        ignoring = dbwait.measure(
            "beast", host=FakeDocker(honours_units=False), runs=1, probes=2,
            sleep_ms=FAST_MS, variants_row=_fake_variants_row())
        self.assertFalse(ignoring["control_overlaps_always"],
                         "контроль перекрылся у сервиса, который держит "
                         "одну единицу при любой настройке")

    def test_the_second_source_asks_about_this_variant_only(self):
        # Счёт, не привязанный к имени варианта, считал бы чужие
        # соединения — включая соединение самого наблюдателя.
        fake = FakeDocker()
        fake.active["beast"] = 2
        self.assertEqual(dbwait.active_query_count("beast", host=fake), 2)
        self.assertEqual(dbwait.active_query_count("crow", host=fake), 0)

    def test_a_second_source_that_did_not_answer_is_not_a_zero(self):
        # База ответила не числом — перезапуск, обрыв, сообщение об
        # ошибке. Превратить такой ответ в ноль значило бы записать
        # «внутри никого» там, где спросить НЕ УДАЛОСЬ, — и согласие
        # источников сошлось бы на выдуманном числе.
        fake = FakeDocker(db_answers_garbage=True)
        with self.assertRaises(RuntimeError):
            dbwait.active_query_count("beast", host=fake)

    def test_the_second_source_is_asked_while_the_probes_are_inside(self):
        # Второй источник обязан спрашиваться ВО ВРЕМЯ залпа: снятый
        # после, он показал бы ноль у кого угодно.
        fake = FakeDocker()
        row = dbwait.measure("beast", host=fake, runs=1, probes=2,
                             sleep_ms=FAST_MS,
                             variants_row=_fake_variants_row())
        seen = [item["db_peak"] for setting in row["at"]
                for item in setting["volleys"]]
        self.assertTrue(all(value is not None for value in seen))
        self.assertTrue(
            any(value >= 1 for value in seen),
            "второй источник ни разу не застал активного запроса: он "
            "спрашивается не тогда, когда пробы внутри")
        self.assertTrue(row["sources"]["agree_everywhere"])
        self.assertTrue(row["publishable"])

    def test_sources_that_disagree_are_recorded_and_not_averaged(self):
        # РАСХОЖДЕНИЕ — НАХОДКА. Модель занижает второй источник, и это
        # обязано быть записано: ни усреднения, ни выбора удобного.
        fake = FakeDocker(db_undercounts=True)
        row = dbwait.measure("beast", host=fake, runs=1, probes=2,
                             sleep_ms=FAST_MS,
                             variants_row=_fake_variants_row())
        self.assertGreater(
            row["sources"]["disagreed"], 0,
            "счётчик и база разошлись, а фикстура об этом молчит")
        self.assertFalse(row["sources"]["agree_everywhere"])
        self.assertFalse(
            row["publishable"],
            "строка объявлена печатаемой при разошедшихся источниках")

    def test_summarize_sources_needs_all_three_terms(self):
        # ФОРМУЛА НА ПРИДУМАННЫХ ЗАЛПАХ, ГДЕ ТРИ ИСХОДА НЕ СВЯЗАНЫ. На
        # живом замере `disagreed` и `missed` почти всегда идут парой (оба
        # ловят один и тот же неудачный залп), и потерянный член формулы
        # прячется за другим. Здесь — только `missed`, без единого
        # расхождения и без единого непойманного источника.
        only_missed = [
            {"sources_agree": False, "sources_missed": True},
            {"sources_agree": True, "sources_missed": False},
        ]
        seen = dbwait.summarize_sources(only_missed)
        self.assertEqual(seen["missed"], 1)
        self.assertEqual(seen["disagreed"], 0)
        self.assertEqual(seen["without_second_source"], 0)
        self.assertFalse(
            seen["agree_everywhere"],
            "один непойманный момент, без единого расхождения, — а свод "
            "объявляет полное согласие")
        only_disagreed = [
            {"sources_agree": False, "sources_missed": False},
            {"sources_agree": True, "sources_missed": False},
        ]
        seen = dbwait.summarize_sources(only_disagreed)
        self.assertEqual(seen["disagreed"], 1)
        self.assertEqual(seen["missed"], 0)
        self.assertFalse(seen["agree_everywhere"])
        only_unseen = [{"sources_agree": None, "sources_missed": False}]
        seen = dbwait.summarize_sources(only_unseen)
        self.assertEqual(seen["without_second_source"], 1)
        self.assertFalse(seen["agree_everywhere"])
        all_clean = [{"sources_agree": True, "sources_missed": False}] * 3
        self.assertTrue(dbwait.summarize_sources(all_clean)["agree_everywhere"])

    def test_a_source_that_saw_nothing_is_missed_not_agreed(self):
        # ИСТОЧНИК СЛЕП ЦЕЛИКОМ, А НЕ ЗАНИЖАЕТ. При контрольной настройке
        # счётчик держит двоих разом; источник, не увидевший НИКОГО,
        # обязан быть отмечен промахом, а не тихим согласием на нулях.
        fake = FakeDocker(db_sees_nothing=True)
        row = dbwait.measure("beast", host=fake, runs=1, probes=2,
                             sleep_ms=FAST_MS,
                             variants_row=_fake_variants_row())
        control = [item for setting in row["at"] if setting["workers"] > 1
                  for item in setting["volleys"]]
        self.assertTrue(
            any(item["sources_missed"] for item in control),
            "источник не увидел ничего при занятости, а промах не отмечен")
        self.assertTrue(
            all(not item["db_observed_activity"] for item in control))
        self.assertGreater(row["sources"]["missed"], 0)
        # Ни один КОНТРОЛЬНЫЙ залп (там, где счётчик держит двоих) не
        # должен попасть в "расхождение" — это отдельный исход от
        # промаха, и смешивать их в одном ведре нельзя.
        self.assertFalse(
            any(item["sources_agree"] is False and not item["sources_missed"]
                for item in control),
            "промах при занятости 2+ записан ещё и как расхождение")
        self.assertFalse(row["sources"]["agree_everywhere"])
        self.assertFalse(row["publishable"])

    def test_the_sampler_keeps_running_until_the_last_probe_finishes(self):
        # Одна единица держит три пробы СТРОГО ПО ОЧЕРЕДИ — окно занятости
        # растягивается на все три ожидания подряд. Сампер, остановленный
        # раньше (по первой готовой пробе, а не по всем), возьмёт заметно
        # меньше выборок, чем допускает это окно.
        fake = FakeDocker()
        measured = dockerlib.volley(
            "beast", workers=1, probes=3, wait_ms=FAST_MS,
            sampler=lambda: dbwait.active_query_count("beast", host=fake),
            env={}, host=fake)
        self.assertGreater(
            len(measured["db_samples"]), 25,
            "второй источник опрошен слишком мало раз для окна в три "
            "последовательных ожидания — сампер остановился раньше "
            "срока: %d выборок" % len(measured["db_samples"]))

    def test_a_negative_headroom_is_not_a_valid_control(self):
        # ЁМКОСТЬ НИЖЕ ЧИСЛА ПРОБ. Контроль физически не может
        # перекрыться, если единиц меньше, чем проб, — это не «запас
        # ноль», а отсутствие контроля вовсе.
        fake = FakeDocker()
        row = dbwait.measure(
            "beast", host=fake, runs=1, probes=2, sleep_ms=FAST_MS,
            variants_row=_fake_variants_row(served_concurrently=1))
        self.assertEqual(row["control_headroom"], -1)
        self.assertFalse(row["control_is_valid"])
        self.assertFalse(
            row["publishable"],
            "печать прошла при отрицательном запасе контроля")

    def test_a_mismatched_entry_binary_blocks_publication(self):
        # СВЯЗЬ ФИКСТУР. Сумма из fixtures/variants.jsonl обязана
        # совпасть со свежей суммой ЭТОЙ сессии; несовпадение означает
        # разные сборки, и запас контроля читается не про тот бинарник.
        fake = FakeDocker()
        row = dbwait.measure(
            "beast", host=fake, runs=1, probes=2, sleep_ms=FAST_MS,
            variants_row=_fake_variants_row(sha256="ff" * 32))
        self.assertFalse(row["entry_binary_matches_variants_fixture"])
        self.assertFalse(
            row["publishable"],
            "печать прошла при несовпавшей сумме точки входа")

    def test_control_headroom_uses_the_row_it_was_given(self):
        # `variants_row` обязан реально использоваться, а не быть
        # параметром для вида: подставленное значение ёмкости обязано
        # дойти до результата, а не потеряться за чтением committed-файла.
        self.assertEqual(
            dbwait.control_headroom(
                "beast", probes=2,
                variants_row=_fake_variants_row(served_concurrently=9)),
            7)


if __name__ == "__main__":
    unittest.main()
