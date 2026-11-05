"""Сборка, запуск и ОБМЕР трёх вариантов сервиса из images/variants.json.

ГЛАВНОЕ СВОЙСТВО, КОТОРОЕ ДЕРЖИТ ЭТОТ МОДУЛЬ: варианты различаются
ТОЛЬКО слоем HTTP. Отсюда решения, каждое из которых иначе выглядело бы
придиркой.

1. СНАЧАЛА БАЗОВЫЙ ОБРАЗ, ПОТОМ СЛОЙ. Компилятор, cmake и libpqxx стоят
   в общем образе; образ слоя добавляет только свою библиотеку.

2. ВСЁ, ЧТО УТВЕРЖДАЕТСЯ, СНИМАЕТСЯ ЗАМЕРОМ. `measure_variant` спрашивает
   у собранного образа, чем он собран, с какими флагами собрана ОБЩАЯ
   библиотека, что лежит в его точке входа, что будет при испорченной
   настройке и сколько обработчиков он держит в полёте разом — число,
   которое он СООБЩАЕТ сам, а не которое выведено из времени ответа.

3. РУЧКИ ПЕРЕДАЮТСЯ ПРИ ЗАПУСКЕ И НИКЕМ НЕ ПЕРЕБИВАЮТСЯ. docker берёт
   ПОСЛЕДНЕЕ значение повторённой переменной окружения, поэтому
   произвольный словарь `env` не имеет права нести имя варианта или число
   рабочих единиц. Единственное место, где собирается команда запуска, —
   `run_variant_command`; всё остальное зовёт её, а не строит `docker run`
   руками.

4. САМ ИЗМЕРИТЕЛЬ ТОЖЕ ИЗМЕРЯЕТСЯ. Все обращения к докеру и к сети идут
   через ОДИН исполнитель (`DockerHost`), и его можно подменить. Иначе
   замер, поднимающий контейнер с одним числом рабочих единиц и пишущий в
   фикстуру другое, не заметил бы никто: проверка сравнивала бы свежий
   замер с фикстурой ТОЙ ЖЕ САМОЙ функцией и краснела бы только на
   подделке фикстуры. Это тот же класс дефекта, что и «механизм, который
   нельзя вызвать, можно только прочитать».

ПРО `--no-cache`. Здесь сборка НЕ замеряется: замеряется поведение
поднятого сервиса. Поэтому по умолчанию кеш слоёв используется. Там, где
предметом станет сама сборка, есть `no_cache=True`.
"""

import contextlib
import http.client
import io
import json
import os
import socket
import subprocess
import threading
import time
import urllib.request
from concurrent.futures import ThreadPoolExecutor

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
IMAGES_DIR = os.path.join(HERE, "images")

# Тег общего базового образа. Он собирается здесь же, из Dockerfile.base,
# внешним не является и закреплять его дайджестом нечем: дайджестом
# закреплён образ, от которого он сам наследуется.
BASE_TAG = "cpp-service-and-postgres-base:latest"
IMAGE_PREFIX = "cpp-service-and-postgres"

# Порт, который слушает сервис внутри контейнера. Один и тот же у трёх
# слоёв: разный порт был бы различием обвязки, а не слоя.
SERVICE_PORT = 8080

# Путь, по которому лежит собранный сервис, и путь, откуда его кладут.
# Совпадение содержимого по этим двум путям — то, чем ловится обёртка:
# сценарий сборки мог бы положить на место бинарника скрипт, который
# выставляет ручку и запускает настоящий сервис, — и ручка оказалась бы
# запечена мимо и ENV, и ENTRYPOINT.
ENTRY_PATH = "/usr/local/bin/stand_service"
BUILD_OUTPUT_PATH = "/build/stand_service"

# Ручки запуска. Ни одной из них не место ни в образе, ни в произвольном
# словаре окружения.
KNOBS = ("STAND_VARIANT", "STAND_WORKERS", "STAND_POOL")

# Размер пула соединений, при котором снимаются ОСТАЛЬНЫЕ оси.
#
# ЗАЧЕМ У НЕГО ЗДЕСЬ ИМЯ. Ось 2 меряет сам размер пула и называет свои
# величины сама (1 и 8). Остальным осям пул безразличен, но ручка у
# сервиса обязательная — и значение, разбросанное по вызовам, означало бы,
# что фикстуры сняты при разных условиях, о которых они молчат.
#
# ПОЧЕМУ ИМЕННО СТОЛЬКО. Не меньше числа проб в залпе: пул, меньший
# залпа, стал бы В ЭТИХ осях лишним ограничителем — ожидание в базе
# упиралось бы в слот, а не в рабочую единицу, и ось 1 мерила бы пул.
POOL_FOR_OTHER_AXES = 8

# Чем занимается рабочая единица на время замера: ожидание.
#
# ПОЧЕМУ СЕКУНДА, А НЕ ПЯТНАДЦАТЬ. Прежняя редакция стенда ВЫВОДИЛА
# занятость из длительностей: ответы делились на «волны» по времени, и
# чтобы отличить волну от дрожания машины, ожидание пришлось растянуть до
# пятнадцати секунд. Это была ошибка метода. Спека оси 1 нигде не просила
# отсчёта по времени: она просит ПОСЧИТАТЬ — послать запросы, дождаться,
# пока все примутся, спросить маршрут живости. Время на рабочей машине
# мерить нельзя: рядом идёт чужая нагрузка, и растянутое окно замера лишь
# увеличивает шанс поймать её в замер.
#
# Теперь занятость ЧИТАЕТСЯ: сервис ведёт счётчик обработчиков в полёте и
# кладёт его значение в контрактную строку ответа, а вторым, независимым
# источником служит счёт активных запросов в pg_stat_activity. Ожидание
# нужно лишь настолько, чтобы залп успел перекрыться сам с собой; сдвиг
# ответа на секунду ничего не меняет — счётчик всё равно скажет, сколько
# обработчиков было внутри.
WAIT_MS = 1000

# Сколько проб уходит в залпе. Больше, чем наибольшее число рабочих
# единиц: если бы проб было ровно столько же, «обслужено все» не
# отличалось бы от «обслужено сколько задано».
PROBE_COUNT = 4

# Сколько ждать, пока залп ДОЕДЕТ до сервиса, и с каким шагом спрашивать
# занятость. Это не замер времени: сюда попадает только решение «залп
# доехал / не доехал», а сама занятость — прочитанное число.
ARRIVAL_TIMEOUT_S = 5.0
POLL_S = 0.02

# Тайм-аут ОДНОГО обращения к маршруту живости.
#
# ПОЧЕМУ ЭТО КОНСТАНТА, А НЕ ПРОСТО АРГУМЕНТ СО ЗНАЧЕНИЕМ ПО УМОЛЧАНИЮ.
# Круг 5 нашёл: маршрут живости — такой же запрос, как ручки ожидания, и
# при полной занятости он вправе встать в очередь и ответить только
# когда одна из единиц освободится. Раздутое окно превращает такую
# задержку в тишину, которую опрос молча пережидает, — а именно так одна
# правка стенда пропустила эту диверсию: окно подняли с 2 до 60 секунд, и
# ни одна проверка не покраснела. Значение закреплено отдельной проверкой
# на равенство константе.
HEALTHZ_TIMEOUT_S = 2.0

# Сколько раз повторяется прогон, не состоявшийся по вине инструмента
# (оборванное соединение, барьер не собрался). Ограничение существует
# именно как ограничение: без него повтор превращается в выпрашивание
# удачного прогона.
MAX_ATTEMPTS = 3

# Настройки, при которых снимается занятость. Одна единица — нижняя точка
# и она же положительный контроль снизу, три — верхняя, по ней варианты
# сравниваются. Две записаны потому, что про это число стенд ГОВОРИТ: на
# нём держится выбор контрольной настройки у пробы ожидания в базе.
WORKER_SETTINGS = (1, 2, 3)

# Сколько барьер ждёт остальные пробы, прежде чем сдаться.
#
# ЗАЧЕМ БАРЬЕР ИМЕННО ЗДЕСЬ. Счётчик покажет единицу и у слоя, который
# обслуживает всё разом, если пробы ушли по очереди: перекрытия не было
# бы вовсе. Барьер отпускает залп одной точкой, а то, что залп
# действительно уходит разом, доказывает не барьер, а положительный
# контроль: тот же клиентский код при трёх единицах даёт перекрытие.
BARRIER_TIMEOUT_S = 3.0
LOW_WORKERS = 1
HIGH_WORKERS = 3


class StalledRun(RuntimeError):
    """Прогон не состоялся по вине ИНСТРУМЕНТА: заминка, обрыв соединения.

    ПОЧЕМУ ОТДЕЛЬНЫЙ ТИП, А НЕ СЛОВО В СООБЩЕНИИ. Заминку положено
    повторять, отказ сервиса — нет: повторять отказ значило бы ждать
    удобного ответа. Пока эти два случая различались подстрокой в тексте
    ошибки, снятая сверка исхода превращала отказ сервиса в заминку — и
    проверка, смотревшая на текст, этого не замечала. Тип различает их
    так, что мимо не пройти.
    """


class DockerHost:
    """Единственное место, где стенд трогает докер и сеть.

    ЗАЧЕМ КЛАСС, А НЕ ПРЯМЫЕ ВЫЗОВЫ. Измеряющий код без подмены
    исполнителя нельзя проверить нигде, кроме машины с докером, — а там
    свежий замер сравнивают с фикстурой той же функцией, которая его
    снимает: дефект измерителя в такое сравнение не попадает по
    построению. С подменным исполнителем `volley`,
    `startup_refusal` и передача ручек проверяются без докера и
    ОТВЕТАМИ, а не чтением.
    """

    def run(self, cmd):
        return subprocess.run(cmd, check=True, capture_output=True, text=True,
                              encoding="utf-8", errors="replace")

    def try_run(self, cmd):
        return subprocess.run(cmd, capture_output=True, text=True,
                              encoding="utf-8", errors="replace")

    def get(self, url, timeout):
        """HTTP-запрос. Возвращает (код, тело); бросает при отказе сети."""
        with urllib.request.urlopen(url, timeout=timeout) as answer:
            return answer.status, answer.read().decode("utf-8", "replace")

    def port_answers(self, port, timeout=0.3):
        try:
            with socket.create_connection(("127.0.0.1", port),
                                          timeout=timeout):
                return True
        except OSError:
            return False


HOST = DockerHost()


def _host(host):
    return HOST if host is None else host


def variants():
    """Объявление трёх вариантов: слой, базовый образ, ручка рабочих единиц."""
    with io.open(os.path.join(IMAGES_DIR, "variants.json"),
                 encoding="utf-8") as handle:
        return json.loads(handle.read())


def _variant(name):
    spec = variants()
    if name not in spec:
        raise KeyError("нет варианта %r в variants.json" % name)
    return spec[name]


def image_tag(name):
    return "%s:%s" % (IMAGE_PREFIX, name)


def build_base(no_cache=False, host=None):
    """Собирает общий базовый образ и возвращает его тег."""
    cmd = ["docker", "build", "-f",
           os.path.join(IMAGES_DIR, "Dockerfile.base"), "-t", BASE_TAG]
    if no_cache:
        cmd.append("--no-cache")
    cmd.append(HERE)
    _host(host).run(cmd)
    return BASE_TAG


def build_variant(name, no_cache=False, host=None):
    """Собирает вариант (сначала базовый образ) и возвращает тег образа."""
    variant = _variant(name)
    build_base(no_cache=no_cache, host=host)
    tag = image_tag(name)
    cmd = ["docker", "build", "-f",
           os.path.join(IMAGES_DIR, variant["dockerfile"]), "-t", tag]
    if no_cache:
        cmd.append("--no-cache")
    cmd.append(HERE)
    _host(host).run(cmd)
    return tag


def _first_line(text):
    for line in text.splitlines():
        if line.strip():
            return line.strip()
    return ""


def _ask(name, argv, host=None):
    """Спрашивает величину у собранного образа, запуская в нём команду."""
    proc = _host(host).try_run(["docker", "run", "--rm", "--entrypoint", "",
                                image_tag(name)] + argv)
    return (proc.stdout or "") + (proc.stderr or "")


def tool_versions(name, host=None):
    """Версии инструментов В СОБРАННОМ ОБРАЗЕ варианта: cxx, cmake, libpqxx.

    ПОЧЕМУ ЭТО ЗАМЕР, А НЕ ПОЛЕ В JSON. Базовый образ общий по тексту
    Dockerfile'ов, но внутри образа слоя работает apt, и подтянутое им
    обновление компилятора не оставило бы в тексте следа. Величина,
    которую никто не мерит, ломается молча.

    ПОЧЕМУ libpqxx СНИМАЕТСЯ ИЗ ЗАГОЛОВКА, А НЕ У ПАКЕТНОГО МЕНЕДЖЕРА.
    Собирается сервис с тем, что лежит в /usr/include, а не с тем, что
    числится установленным: это разные утверждения, и второе к
    собранному бинарнику отношения не имеет.
    """
    versions = {
        "cxx": _first_line(_ask(name, ["g++", "--version"], host)),
        "cmake": _first_line(_ask(name, ["cmake", "--version"], host)),
        "libpqxx": _first_line(_ask(name, [
            "sh", "-c",
            "grep -h 'define PQXX_VERSION ' /usr/include/pqxx/version.hxx"],
            host)),
    }
    for field, value in sorted(versions.items()):
        if not any(ch.isdigit() for ch in value):
            raise RuntimeError(
                "%s: %s измерился как %r — в величине нет ни одной цифры, "
                "версией это не является" % (image_tag(name), field, value))
    return versions


def common_build_flags(name, host=None):
    """Флаги, которыми собрана ОБЩАЯ библиотека, — прямо из образа.

    ЗАЧЕМ ИМЕННО ОБЩАЯ. Вторая переменная умеет приехать не только другим
    компилятором: разойтись по слоям способны стандарт, уровень
    оптимизации, определения — и завести это можно и в CMakeLists, и в
    сценарии сборки. У stand_common код у трёх вариантов ОДИН, значит и
    строка флагов обязана быть одна; расхождение здесь — расхождение
    сборки, а не слоя.
    """
    text = _ask(name, ["sh", "-c",
                       "cat /build/CMakeFiles/stand_common.dir/flags.make"],
                host)
    found = {}
    for key in ("CXX_FLAGS", "CXX_DEFINES", "CXX_INCLUDES"):
        for line in text.splitlines():
            if line.startswith(key + " ="):
                found[key] = line.split("=", 1)[1].strip()
                break
    if "CXX_FLAGS" not in found:
        raise RuntimeError(
            "%s: не удалось снять флаги общей библиотеки" % image_tag(name))
    return {
        "common_build_flags": found.get("CXX_FLAGS", ""),
        "common_build_defines": found.get("CXX_DEFINES", ""),
        "common_build_includes": found.get("CXX_INCLUDES", ""),
    }


def entry_binary(name, host=None):
    """Что на самом деле лежит в точке входа образа.

    ЗАЧЕМ. Ручку можно запечь мимо `ENV` и мимо `ENTRYPOINT`: сценарий
    сборки положил бы на место бинарника коротенький сценарий, который
    выставляет STAND_WORKERS и запускает настоящий сервис. Снаружи образ
    выглядел бы нетронутым. Поэтому снимается ФОРМАТ файла (у ELF первые
    четыре байта — 7f 45 4c 46) и его сумма, и она сверяется с суммой
    того, что вышло из сборки.
    """
    text = _ask(name, ["sh", "-c",
                       "head -c 4 %s | od -An -tx1 | tr -d ' \\n'; echo; "
                       "sha256sum %s | cut -d' ' -f1; "
                       "sha256sum %s | cut -d' ' -f1"
                       % (ENTRY_PATH, ENTRY_PATH, BUILD_OUTPUT_PATH)], host)
    lines = [line.strip() for line in text.splitlines() if line.strip()]
    if len(lines) < 3:
        raise RuntimeError("%s: не удалось снять точку входа: %r"
                           % (image_tag(name), text))
    magic, entry_sum, built_sum = lines[0], lines[1], lines[2]
    return {
        "magic": magic,
        "is_elf": magic.lower() == "7f454c46",
        "sha256": entry_sum,
        "same_as_build_output": entry_sum == built_sum,
    }


# Чем меряется версия библиотеки слоя. Рецепт лежит в коде, а не в
# variants.json: величина, добытая по рецепту из тех же данных, которые
# ею проверяются, ничего бы не проверяла.
_LAYER_LIBRARY = {
    "beast": ("Boost",
              ["sh", "-c",
               "sed -n 's/.*BOOST_LIB_VERSION \"\\(.*\\)\".*/\\1/p' "
               "/usr/include/boost/version.hpp"]),
    "drogon": ("Drogon",
               ["sh", "-c",
                "sed -n 's/.*DROGON_VERSION \"\\(.*\\)\".*/\\1/p' "
                "/usr/include/drogon/version.h"]),
    # У Crow версии в заголовке нет вовсе. Сумма — тождественность более
    # сильная, чем номер, и это ровно то, что закреплено в сценарии
    # установки слоя.
    "crow": ("Crow",
             ["sh", "-c",
              "sha256sum /usr/local/include/crow_all.h | cut -d' ' -f1"]),
}


def layer_library(name, host=None):
    """Версия библиотеки слоя — из самого образа."""
    library, argv = _LAYER_LIBRARY[_variant(name)["layer"]]
    value = _first_line(_ask(name, argv, host))
    if library == "Crow":
        value = "sha256:" + value
    if not any(ch.isdigit() for ch in value):
        raise RuntimeError(
            "%s: версия библиотеки слоя измерилась как %r" % (name, value))
    return {"name": library, "version": value}


def image_entry(name, host=None):
    """Что лежит в образе: переменные окружения, точка входа, команда.

    ПОЧЕМУ НЕ ХВАТАЕТ ЧТЕНИЯ DOCKERFILE'А. Ручку можно запечь мимо
    инструкции `ENV`: `ENTRYPOINT ["/bin/sh","-c","STAND_WORKERS=2 exec …"]`
    сохраняет и симметрию трёх файлов, и отсутствие слова ENV. Здесь
    спрашивается собранный образ.
    """
    config = json.loads(_host(host).run(
        ["docker", "image", "inspect", image_tag(name),
         "--format", "{{json .Config}}"]).stdout)
    env = config.get("Env") or []
    return {
        "baked_env": sorted(item for item in env
                            if any(knob in item for knob in KNOBS)),
        "entrypoint": config.get("Entrypoint") or [],
        "cmd": config.get("Cmd") or [],
    }


def run_variant_command(name, workers, pool=None, env=None, network=None,
                        host_port=0, remove=True, omit=()):
    """Команда запуска варианта: обе ручки передаются ПРИ ЗАПУСКЕ.

    Возвращается список аргументов, а не запущенный контейнер, чтобы
    требование «ручки не запечены и не перебиты» проверялось без докера.
    ВСЯ обвязка стенда обязана поднимать варианты через эту функцию:
    второе место сборки `docker run` было бы вторым местом передачи
    ручек, не защищённым здешними правилами.

    ПРО `env`. docker берёт ПОСЛЕДНЕЕ значение повторённой переменной, и
    словарь, дописанный после фиксированных `-e`, молча поднял бы
    контейнер чужим вариантом с чужим числом рабочих единиц — а
    вызывающий код записал бы в фикстуру то, что просил. Поэтому ручкам в
    `env` места нет вовсе: отказ громче подмены.

    ПРО `pool`. Это ТРЕТЬЯ ручка, и до оси 2 она здесь отвергалась: пула
    не держал ни один слой, соединение открывалось на каждый запрос —
    замерено, а не вычитано (expected.json, блок pool). Переменная,
    которую не читает никто, делает прогон настроенным только на вид.
    Теперь пул читает общая логика сервиса, размер приходит при запуске, и
    ЗНАЧЕНИЕ ПО УМОЛЧАНИЮ ЗДЕСЬ НЕ ПОДСТАВЛЯЕТСЯ: контроль оси 2 — пара по
    размеру пула, и вызов, забывший его назвать, сравнивал бы пул сам с
    собой. Отказ громче молчаливой подстановки.

    ПРО `omit`. Служит замеру отказа: контейнер поднимается БЕЗ названной
    ручки. Она не подменяется пустым значением — она не передаётся вовсе.
    """
    if pool is None and "STAND_POOL" not in omit:
        raise ValueError(
            "размер пула соединений не назван: у сервиса это обязательная "
            "ручка запуска, и подставлять за вызывающего нечего — пара "
            "«пул на 1 против пула на 8» и есть контроль оси 2")
    if pool is not None and (isinstance(pool, bool)
                             or not isinstance(pool, int) or pool <= 0):
        raise ValueError(
            "размер пула обязан быть положительным целым, получено %r"
            % (pool,))
    if isinstance(workers, bool) or not isinstance(workers, int):
        raise TypeError(
            "число рабочих единиц обязано быть целым, получено %r"
            % (workers,))
    if workers <= 0:
        raise ValueError(
            "число рабочих единиц обязано быть положительным, получено %r"
            % (workers,))
    for key in sorted(env or {}):
        if key in KNOBS:
            raise ValueError(
                "%s передан в env: docker берёт последнее значение "
                "повторённой переменной, контейнер поднялся бы чужой "
                "настройкой — а записано было бы то, что просили" % key)
    for key in omit:
        if key not in KNOBS:
            raise ValueError("нечего опускать: %s не ручка запуска" % key)
    cmd = ["docker", "run", "-d"]
    if remove:
        cmd.append("--rm")
    if "STAND_VARIANT" not in omit:
        cmd += ["-e", "STAND_VARIANT=%s" % name]
    if "STAND_WORKERS" not in omit:
        cmd += ["-e", "STAND_WORKERS=%d" % workers]
    if "STAND_POOL" not in omit:
        cmd += ["-e", "STAND_POOL=%d" % pool]
    cmd += ["-p", "%d:%d" % (host_port, SERVICE_PORT)]
    if network:
        cmd += ["--network", network]
    for key, value in sorted((env or {}).items()):
        cmd += ["-e", "%s=%s" % (key, value)]
    cmd.append(image_tag(name))
    return cmd


def _published_port(container, host=None):
    line = _first_line(_host(host).run(
        ["docker", "port", container, "%d/tcp" % SERVICE_PORT]).stdout)
    if not line:
        raise RuntimeError("контейнер %s не опубликовал порт" % container)
    return int(line.rsplit(":", 1)[1])


def _wait_for_service(port, seconds=20.0, host=None):
    """Готовность — это ОТВЕТ на /healthz, а не принятое соединение.

    Соединение принимает ядро: сокет слушает ещё до того, как слой начал
    разбирать запросы, и первый же запрос сразу после удавшегося connect
    у части слоёв обрывается. Замер, начатый в этот момент, записал бы
    задержку запуска как занятость рабочей единицы.
    """
    deadline = time.time() + seconds
    while time.time() < deadline:
        try:
            status, _ = _host(host).get(
                "http://127.0.0.1:%d/healthz" % port, timeout=1)
            if status == 200:
                return True
        except Exception:
            pass
        time.sleep(0.05)
    return False


@contextlib.contextmanager
def run_variant(name, workers, pool=POOL_FOR_OTHER_AXES, env=None,
                network=None, host=None):
    """Поднимает вариант и отдаёт адрес сервиса; снимает контейнер на выходе.

    Контейнер снимается в finally, а не после замера: упавшая проба не
    должна оставлять за собой занятый порт — следующий прогон встал бы на
    запуске и выглядел бы отказом варианта.
    """
    cmd = run_variant_command(name, workers, pool=pool, env=env,
                              network=network)
    container = _host(host).run(cmd).stdout.strip()
    try:
        port = _published_port(container, host)
        yield {
            "container": container,
            "variant": name,
            "workers": workers,
            "pool": pool,
            "port": port,
            "address": "127.0.0.1:%d" % port,
            "ready": _wait_for_service(port, host=host),
        }
    finally:
        _host(host).try_run(["docker", "rm", "-f", container])


def startup_refusal(name, missing, workers=2, pool=POOL_FOR_OTHER_AXES,
                    seconds=15.0, host=None):
    """Что будет, если ручки не дать: код возврата и открывался ли порт.

    ЗАЧЕМ ЗАМЕР, А НЕ ЧТЕНИЕ ИСХОДНИКА. Проверка текста держит порядок
    вызовов, и этот порядок сохраняет обёртка try/catch с запасным
    значением: вызов на месте, стоит он до открытия порта — а порт всё
    равно откроется. Ось 1 меряет различие «сервер занят / сервер умер»,
    и испорченную настройку она прочла бы как отсутствие живости.

    Ручка именно НЕ ПЕРЕДАЁТСЯ (`omit`), а не подменяется пустой: пустое
    значение — это другой вход, и отвечать на него сервис мог бы иначе.
    """
    if missing not in KNOBS:
        raise ValueError("нечего опускать: %s не ручка запуска" % missing)
    cmd = run_variant_command(name, workers, pool=pool, remove=False,
                              omit=(missing,))
    container = _host(host).run(cmd).stdout.strip()
    try:
        try:
            port = _published_port(container, host)
        except (RuntimeError, subprocess.CalledProcessError):
            port = None
        opened = False
        exit_code = None
        deadline = time.time() + seconds
        while time.time() < deadline:
            if port is not None and _host(host).port_answers(port,
                                                             timeout=0.2):
                opened = True
            state = json.loads(_host(host).run(
                ["docker", "inspect", container,
                 "--format", "{{json .State}}"]).stdout)
            if not state.get("Running"):
                exit_code = state.get("ExitCode")
                break
            time.sleep(0.05)
        if exit_code is None:
            raise RuntimeError(
                "%s без %s не завершился за %.0f с: отказ не доехал до кода "
                "возврата" % (name, missing, seconds))
        return {"missing": missing, "exit_code": exit_code,
                "port_open": opened}
    finally:
        _host(host).try_run(["docker", "rm", "-f", container])



# Две ручки ожидания и их параметры. Ждут они в РАЗНЫХ местах — и ровно
# это различие интересно: /block ждёт в обработчике, /q ждёт в базе.
PATHS = {
    "block": ("/block", "ms"),
    "q": ("/q", "sleep_ms"),
}


def parse_reply(body, least=0):
    """Контрактная строка ответа вместе с ПРОЧИТАННОЙ занятостью.

    Поле inflight — число обработчиков, бывших в полёте на момент входа
    этого запроса. Его считает сам сервис; здесь оно только читается.
    Отсутствие поля или нецелое значение — поломка контракта, а не
    наблюдение: ответ без занятости замером не является.

    `least` разный у двух случаев не по прихоти: ответ РУЧКИ ОЖИДАНИЯ
    обязан считать хотя бы себя (не меньше единицы), а маршрут живости
    себя не считает и на пустом сервисе обязан прочитаться нулём.
    """
    row = json.loads(body)
    if "inflight" not in row:
        raise RuntimeError(
            "в ответе нет поля inflight: занятость читается из ответа, и "
            "строка без неё замером не является: %s" % body)
    value = row["inflight"]
    if isinstance(value, bool) or not isinstance(value, int):
        raise RuntimeError("inflight не целое число: %r" % (value,))
    if value < least:
        raise RuntimeError(
            "inflight = %d, а меньше %d он быть не может: %s"
            % (value, least, body))
    return row


def probe_wait(port, endpoint, wait_ms, host=None):
    """Одна проба ожидания: сколько обработчиков было в полёте вместе с ней.

    Исход ПРОВЕРЯЕТСЯ: проба, получившая отказ или ошибку, занятости не
    показывает — она вернулась не потому, что рабочая единица была
    свободна. Без этой сверки сломанная проба выглядела бы наблюдением.

    Длительность записывается сырым числом и НИ ВО ЧТО НЕ ВЫВОДИТСЯ: она
    здесь для читателя фикстуры. Прежняя редакция стенда выводила из
    длительностей занятость — и мерила этим машину, а не варианты.
    """
    path, parameter = PATHS[endpoint]
    started = time.time()
    status, body = _host(host).get(
        "http://127.0.0.1:%d%s?%s=%d" % (port, path, parameter, wait_ms),
        timeout=60)
    elapsed = time.time() - started
    if status != 200:
        raise RuntimeError("проба не обслужена (%s): %s" % (status, body))
    row = parse_reply(body, least=1)
    if row.get("outcome") != "served":
        raise RuntimeError("проба не обслужена: %s" % body)
    return {"inflight": row["inflight"], "duration_ms": int(elapsed * 1000)}


def read_occupancy(port, timeout=HEALTHZ_TIMEOUT_S, host=None):
    """Занятость, прочитанная маршрутом живости, вместе с СЫРОЙ задержкой.

    ПОЧЕМУ ЗАДЕРЖКА ИДЁТ ЧИСЛОМ, А НЕ ГОТОВЫМ ВЫВОДОМ «ОТВЕТИЛ». Круг 5
    нашёл: маршрут живости — такой же запрос, как ручки ожидания, и при
    полной занятости он вправе встать в очередь и ответить только когда
    одна из единиц освободится. Ответ, пришедший так, показывает занятость
    уже ПОСЛЕ освобождения — то есть заниженную, а не ту, что была в
    момент вопроса. Прежняя редакция превращала «ответил (пусть и поздно)»
    в «отвечает под нагрузкой» — тот же класс ошибки, которым уже болел
    прежний метод целиком: время, ставшее выводом вместо сырого числа.
    Здесь задержка остаётся числом рядом с прочитанной величиной, и
    вывод из неё не делается тут же — его делает читатель фикстуры.

    Молчание (отказ соединения, тайм-аут) — НАБЛЮДЕНИЕ, поэтому
    (None, задержка), а не исключение: слой, целиком вставший под
    нагрузкой, обязан попасть в фикстуру, а не уронить замер. Сломанный
    контракт ответа — не наблюдение, и parse_reply бросает.
    """
    started = time.time()
    try:
        status, body = _host(host).get(
            "http://127.0.0.1:%d/healthz" % port, timeout=timeout)
    except (OSError, http.client.HTTPException):
        return None, int((time.time() - started) * 1000)
    delay_ms = int((time.time() - started) * 1000)
    if status != 200:
        return None, delay_ms
    return parse_reply(body, least=0)["inflight"], delay_ms


def peak_of(row):
    """Занятость залпа: наибольшее ПРОЧИТАННОЕ число обработчиков в полёте.

    ВЫНЕСЕНО ОТДЕЛЬНО НАМЕРЕННО. Это всё правило счёта целиком, и
    испортить его можно, не тронув ни одного образа: считай оно, скажем,
    число ответов, любая ручка выглядела бы подключённой. Здесь его можно
    позвать на придуманном залпе, без докера.
    """
    readings = list(row["inflight_by_probe"])
    readings += [call["value"] for call in row["healthz_calls"]
                if call["value"] is not None]
    if not readings:
        raise ValueError("в залпе нет ни одного прочитанного числа")
    return max(readings)


def volley(name, workers, pool=POOL_FOR_OTHER_AXES, probes=PROBE_COUNT,
           endpoint="block", wait_ms=WAIT_MS, sequential=False, sampler=None,
           env=None, network=None, host=None):
    """Один залп проб. Занятость ЧИТАЕТСЯ, а не выводится из времени.

    Метод. Контейнер поднимается с заданным числом единиц; занятость до
    залпа читается маршрутом живости и обязана быть нулём; уходит залп из
    `probes` одновременных проб ожидания; пока они в полёте, маршрут
    живости спрашивается раз за разом — он и отвечает на вопрос оси 1, и
    отдаёт прочитанную занятость. Наибольшее прочитанное число и есть
    ответ.

    ПОЧЕМУ ЭТО НЕ ЗАВИСИТ ОТ ДРОЖАНИЯ МАШИНЫ. Пусть ответ придёт хоть
    через восемь секунд: счётчик всё равно скажет, сколько обработчиков
    было внутри. Ни порогов, ни полос, ни опорной пробы здесь нет.

    `sequential` — ОТРИЦАТЕЛЬНЫЙ КОНТРОЛЬ: пробы уходят по очереди, и
    счётчик обязан читаться единицей при любом числе рабочих единиц.
    Счётчик, докладывающий настройку или никогда не убывающий, тут и
    ломается.

    `sampler` — второй источник: вызываемое без доводов, возвращающее
    число активных запросов варианта в самой базе. Опрашивается ОТДЕЛЬНЫМ
    потоком со своим шагом (см. `_sample_second_source`), а не из того же
    цикла, что спрашивает живость: тот цикл сам стоит в очереди за
    занятым сервисом, и единственная выборка из него может прийтись уже
    на опустевший сервис — источник тогда НЕ ПОЙМАЛ момент, а не
    подтвердил и не опроверг счётчик. Источники обязаны сойтись;
    расхождение записывается, а не усредняется, и отличается от
    непойманного момента.

    ГРАНИЦА КЛИЕНТА. У каждого обращения к маршруту живости рядом лежит
    число проб, которые клиент ЕЩЁ СЧИТАЕТ отправленными и не
    завершёнными (`client_pending`), снятое ДО обращения — потому что
    это число может только убывать, и снятое заранее остаётся верной
    (пусть и не самой тесной) верхней границей для момента ответа.
    Прочитанная занятость не имеет права превысить эту границу: если
    превысила — счётчик считает не входящие в полёт запросы, а что-то
    ещё. Эта граница не зависит от самого счётчика: она посчитана снаружи
    сервиса, на стороне клиента.
    """
    with run_variant(name, workers=workers, pool=pool, env=env,
                     network=network, host=host) as service:
        if not service["ready"]:
            raise RuntimeError("%s не поднялся" % name)
        port = service["port"]
        # Разогревающий запрос: первое соединение у части слоёв платит за
        # отложенную инициализацию, и эта плата пришлась бы на залп.
        probe_wait(port, endpoint, 0, host=host)
        # Занятость ДО залпа. Счётчик, не убывающий на выходе, виден
        # здесь же: после разогрева в полёте не должно быть никого.
        idle, idle_delay_ms = read_occupancy(port, host=host)
        replies = []
        healthz_calls = []
        samples = []
        if sequential:
            for _ in range(probes):
                replies.append(probe_wait(port, endpoint, wait_ms, host=host))
                if sampler is not None:
                    samples.append(sampler())
        else:
            # Барьер отпускает залп одной точкой; в нём участвует и тот,
            # кто спрашивает живость, — иначе он успел бы спросить до
            # того, как пробы ушли.
            gate = threading.Barrier(probes + 1, timeout=BARRIER_TIMEOUT_S)

            def one(_):
                gate.wait()
                try:
                    return probe_wait(port, endpoint, wait_ms, host=host)
                except (OSError, http.client.HTTPException) as failure:
                    raise StalledRun(
                        "%s (%d единиц): залп не состоялся — соединение "
                        "оборвалось: %s" % (name, workers, failure))

            # Имя НЕ `pool`: так звался исполнитель до появления ручки
            # размера пула соединений, и она молча перекрывалась им —
            # в строку замера уезжал объект исполнителя вместо числа.
            with ThreadPoolExecutor(max_workers=probes) as clients:
                pending = [clients.submit(one, index)
                           for index in range(probes)]
                gate.wait()

                deadline = time.time() + ARRIVAL_TIMEOUT_S
                sampler_thread = None
                if sampler is not None:
                    # ПРИВЯЗАН К ПРОБАМ, А НЕ К ОПРОСУ ЖИВОСТИ. Опрос
                    # живости обязан остановиться, как только занятость
                    # достигла максимума (считать дальше нечего) — а
                    # ёмкость с запасом даёт это на ПЕРВОМ же обращении,
                    # почти мгновенно. Сампер, остановленный ВМЕСТЕ с ним,
                    # не успел бы взять ни одной пробы: он должен жить,
                    # пока пробы физически в полёте, — это другое условие.
                    sampler_thread = threading.Thread(
                        target=_sample_second_source,
                        args=(sampler, samples, pending, deadline))
                    sampler_thread.start()

                while time.time() < deadline:
                    # Граница клиента — ДО обращения: убывать она может,
                    # расти не может, и потому остаётся верной даже если
                    # сервис ответит нескоро.
                    client_pending = probes - sum(1 for item in pending
                                                  if item.done())
                    value, delay_ms = read_occupancy(port, host=host)
                    healthz_calls.append({
                        "value": value,
                        "delay_ms": delay_ms,
                        "client_pending": client_pending,
                    })
                    if value is not None and value >= probes:
                        # Все пробы приняты — дальше считать нечего.
                        break
                    if all(item.done() for item in pending):
                        break
                    time.sleep(POLL_S)
                if sampler_thread is not None:
                    sampler_thread.join(timeout=ARRIVAL_TIMEOUT_S)
                replies = [item.result() for item in pending]
    row = {
        "workers": workers,
        "pool": pool,
        "probes": probes,
        "endpoint": endpoint,
        "wait_ms": wait_ms,
        "sequential": sequential,
        "idle_before": idle,
        "idle_before_delay_ms": idle_delay_ms,
        "inflight_by_probe": [item["inflight"] for item in replies],
        "healthz_calls": healthz_calls,
        "durations_ms": [item["duration_ms"] for item in replies],
        "db_samples": samples,
    }
    row["counter_peak"] = peak_of(row)
    # ГРАНИЦА КЛИЕНТА, ПРОВЕРЕННАЯ. Обе стороны бывают пусты: у
    # последовательной постановки границы нет — там перекрытия нет по
    # определению.
    row["client_bound_holds"] = all(
        call["value"] is None or call["value"] <= call["client_pending"]
        for call in healthz_calls)
    row["db_peak"] = max(samples) if samples else None
    # ИСТОЧНИК УВИДЕЛ ХОТЬ ЧТО-ТО. Источник, ни разу не заставший
    # активного запроса, НЕ ПОЙМАЛ момент — это отдельный исход от
    # «поймал, и число разошлось», и путать их в фикстуре нельзя.
    row["db_observed_activity"] = bool(samples) and max(samples) >= 1
    if row["db_peak"] is None:
        row["sources_agree"] = None
        row["sources_missed"] = False
    elif not row["db_observed_activity"] and row["counter_peak"] >= 2:
        row["sources_agree"] = False
        row["sources_missed"] = True
    else:
        row["sources_agree"] = row["db_peak"] == row["counter_peak"]
        row["sources_missed"] = False
    return row


def _sample_second_source(sampler, samples, pending, deadline):
    """Опрос второго источника СВОИМ шагом, в отдельном потоке.

    ЗАЧЕМ ОТДЕЛЬНЫЙ ПОТОК. Опрос из цикла, который спрашивает живость,
    зависит от того же цикла — а тот сам стоит в очереди за занятым
    сервисом (см. `read_occupancy`). При малом числе единиц этот цикл
    успевает мало витков за время ожидания, и единственная выборка может
    прийтись уже на опустевший сервис. Здесь опрос идёт своим шагом, не
    деля цикл ни с чем.

    ОСТАНОВКА ПРИВЯЗАНА К ПРОБАМ (`pending`), А НЕ К ОПРОСУ ЖИВОСТИ. Тот
    цикл вправе остановиться, как только занятость достигла максимума, —
    и при ёмкости с запасом это случается почти мгновенно, на первом же
    обращении. Сампер, остановленный вместе с ним, не успел бы взять ни
    одной пробы: он обязан жить, пока пробы физически в полёте, — это
    ДРУГОЕ условие, и приравнивать их значило бы вернуть ту же слепоту,
    ради которой этот поток и заведён. `deadline` — только аварийный
    потолок, на случай если пробы никогда не завершатся.
    """
    while time.time() < deadline and not all(item.done() for item in pending):
        samples.append(sampler())
        time.sleep(POLL_S)


def measure_run(name, workers, pool=POOL_FOR_OTHER_AXES,
                probes=PROBE_COUNT, endpoint="block", wait_ms=WAIT_MS,
                sequential=False, sampler=None, env=None, network=None,
                attempts=MAX_ATTEMPTS, host=None):
    """Залп с ограниченным числом повторов на отказ ИНСТРУМЕНТА.

    Повторяется только `StalledRun` — оборванное соединение или не
    собравшийся барьер. Отказ сервиса не повторяется: повторять его
    значило бы ждать удобного ответа. Число повторов идёт в фикстуру.
    """
    if attempts > MAX_ATTEMPTS:
        raise ValueError(
            "повторов запрошено больше предела (%d > %d): повтор существует, "
            "чтобы пережить отказ инструмента, а не чтобы выпрашивать "
            "удачный залп" % (attempts, MAX_ATTEMPTS))
    stalls = 0
    last = None
    for _ in range(attempts):
        try:
            measured = volley(name, workers=workers, pool=pool,
                              probes=probes,
                              endpoint=endpoint, wait_ms=wait_ms,
                              sequential=sequential, sampler=sampler,
                              env=env, network=network, host=host)
            measured["stalls"] = stalls
            measured["attempts_used"] = stalls + 1
            return measured
        except StalledRun as failure:
            stalls += 1
            last = failure
    raise StalledRun(
        "%s (%d единиц): %d залпов подряд не состоялись. Последний: %s"
        % (name, workers, attempts, last))


def healthy_port_open(name, workers=2, pool=POOL_FOR_OTHER_AXES,
                     host=None):
    """Отвечает ли вариант при ГОДНОЙ настройке.

    Без этой величины проба на закрытый порт проходила бы всегда: порт,
    не открывающийся никогда, тоже «не открылся при испорченной
    настройке».
    """
    with run_variant(name, workers=workers, pool=pool,
                     host=host) as service:
        return bool(service["ready"])


def published_concurrency(runs):
    """Число, которое стенд ПУБЛИКУЕТ по набору залпов, — наименьшее.

    Утверждение «вариант держит разом столько-то» обязано выдерживать
    каждый залп, а не лучший из них. Наибольшее было бы отчётом об удаче:
    у слоя, чьё число плавает, оно росло бы с числом попыток.
    """
    if not runs:
        raise ValueError("публиковать нечего: залпов нет")
    return min(runs)


def knob_evidence(low, high):
    """ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: счётчик обязан идти за числом единиц.

    Счётчик — измеряющий код ВНУТРИ предмета, то есть слеп заодно с ним.
    Без контроля он мог бы докладывать что угодно — хоть число проб в
    залпе. Подключённость доказывает разница между настройками: при одной
    единице прочитанная занятость обязана быть ровно единицей, при
    большем числе — хотя бы в одном залпе больше.

    ПОЧЕМУ ВЫБОРКИ ОБЯЗАНЫ БЫТЬ РАВНЫМИ. Сравниваются наибольшие, а у
    слоя, чьё число плавает независимо от ручки, больший максимум
    добывается просто бо́льшим числом залпов.
    """
    if not low or not high:
        raise ValueError("контролю нужны залпы при обеих настройках")
    if len(low) != len(high):
        raise ValueError(
            "выборки разного размера (%d против %d): больший максимум "
            "добывается бо́льшим числом залпов, а не подключённой ручкой"
            % (len(low), len(high)))
    return {
        "runs_per_setting": len(low),
        "low_peak_max": max(low),
        "high_peak_max": max(high),
        "one_unit_reads_one": max(low) == 1,
        "follows_workers": max(low) == 1 and max(high) > max(low),
    }


def negative_control(name, workers=HIGH_WORKERS, pool=POOL_FOR_OTHER_AXES,
                     probes=PROBE_COUNT, wait_ms=WAIT_MS, runs=3,
                     endpoint="block", sampler=None, env=None, network=None,
                     host=None):
    """ОТРИЦАТЕЛЬНЫЙ КОНТРОЛЬ: постановка, где счётчик обязан читаться единицей.

    Те же пробы, то же число рабочих единиц, единственное отличие — они
    уходят ПО ОЧЕРЕДИ и не перекрываются. Счётчик, докладывающий
    настройку, число проб или общее число обслуженных запросов, здесь
    прочитается не единицей. Без этого контроля ось недействительна:
    один положительный контроль доказывает лишь, что число за чем-то
    следует.
    """
    rows = [volley(name, workers=workers, pool=pool, probes=probes,
                   endpoint=endpoint,
                   wait_ms=wait_ms, sequential=True, sampler=sampler, env=env,
                   network=network, host=host)
            for _ in range(runs)]
    peaks = [peak_of(item) for item in rows]
    return {
        "workers": workers,
        "probes": probes,
        "runs": peaks,
        "volleys": rows,
        "reads_one": all(value == 1 for value in peaks),
    }


def liveness_of(volleys):
    """Сырые данные оси 1: сколько раз спрошен маршрут живости, с какой
    задержкой и что он тогда прочитал.

    ЗДЕСЬ НЕТ ГОТОВОГО ВЫВОДА «ОТВЕЧАЛ». Круг 5 нашёл: маршрут живости
    встаёт в очередь наравне с пробами и отвечает, только когда одна из
    занятых единиц освободилась — задержка первого обращения при полной
    занятости оказалась близка к самому ожиданию залпа. Вывод «ответил
    под нагрузкой» из этих чисел не следует, и печатать его здесь —
    значило бы повторить ошибку прежнего метода: превратить время в
    вывод, не дав читателю увидеть сырое число. `max_delay_ms` и есть то
    сырое число; чтение и формулировка — дело README и статьи, не этой
    функции.

    `workers` в возвращаемом словаре — НЕ метка, а то, что действительно
    вычислено из переданных залпов: если они сняты при разных настройках,
    функция бросает. Это привязывает вывод к тем волнам, из которых он
    получен, — иначе печать могла бы молча свестись на залпы при другой
    настройке, а число, лежащее в поле `workers` фикстуры, осталось бы
    прежним.
    """
    if not volleys:
        raise ValueError("живости нечего мерить: залпов нет")
    settings = {item["workers"] for item in volleys}
    if len(settings) != 1:
        raise ValueError(
            "залпы сняты при разных настройках (%s): вывод живости не "
            "привязан ни к одной из них" % sorted(settings))
    calls = [call for item in volleys for call in item["healthz_calls"]]
    asks = len(calls)
    answers = sum(1 for call in calls if call["value"] is not None)
    delays = [call["delay_ms"] for call in calls]
    busy = [call["value"] for call in calls
            if call["value"] is not None and call["value"] > 0]
    return {
        "workers": next(iter(settings)),
        "asks": asks,
        "answers": answers,
        "every_call_got_a_value": asks > 0 and asks == answers,
        "max_delay_ms": max(delays) if delays else 0,
        "asked_while_busy": len(busy),
        "max_read_while_busy": max(busy) if busy else 0,
    }


def measure_variant(name, runs=3, probes=PROBE_COUNT, settings=WORKER_SETTINGS,
                    wait_ms=WAIT_MS, pool=POOL_FOR_OTHER_AXES, host=None):
    """Полный обмер варианта — строка фикстуры.

    ПОЧЕМУ ЗАЛП ПОВТОРЯЕТСЯ И ИДЁТ ПРИ НЕСКОЛЬКИХ НАСТРОЙКАХ. У одного из
    слоёв прочитанная занятость от залпа к залпу разная, и единственный
    залп выдал бы случайное значение за свойство. А одна настройка не
    отличает подключённую ручку от отключённой.
    """
    row = {
        "kind": "variant",
        "variant": name,
        "layer": _variant(name)["layer"],
        "image_tag": image_tag(name),
        "tools": tool_versions(name, host=host),
        "layer_library": layer_library(name, host=host),
        "entry_binary": entry_binary(name, host=host),
    }
    row.update(common_build_flags(name, host=host))
    row.update(image_entry(name, host=host))
    row["healthy_port_open"] = healthy_port_open(name, pool=pool, host=host)
    row["startup_refusals"] = [startup_refusal(name, knob, pool=pool,
                                               host=host)
                               for knob in KNOBS]
    # Размер пула — такое же условие замера, как число рабочих единиц, и
    # он записывается В СТРОКУ: ось 1 меряет не его, но снята она при
    # названном значении, и фикстура, молчащая об этом, не позволила бы
    # отличить один набор условий от другого.
    row["pool"] = pool
    at = []
    for workers in sorted(settings):
        measured = [measure_run(name, workers=workers, pool=pool,
                                probes=probes,
                                wait_ms=wait_ms, host=host)
                    for _ in range(runs)]
        at.append({
            "workers": workers,
            "probes": probes,
            "runs": [peak_of(item) for item in measured],
            "volleys": measured,
            "attempts_used": [item["attempts_used"] for item in measured],
            "stalls": sum(item["stalls"] for item in measured),
            "served_concurrently": published_concurrency(
                [peak_of(item) for item in measured]),
        })
    by_workers = {item["workers"]: item for item in at}
    high = by_workers[HIGH_WORKERS]
    row["occupancy"] = {
        "workers": HIGH_WORKERS,
        "endpoint": "block",
        "wait_ms": wait_ms,
        "probes": probes,
        "runs_per_setting": runs,
        "at": at,
        "runs": high["runs"],
        "served_concurrently": high["served_concurrently"],
    }
    row["knob_evidence"] = knob_evidence(by_workers[LOW_WORKERS]["runs"],
                                         high["runs"])
    row["negative_control"] = negative_control(
        name, pool=pool, probes=probes, wait_ms=wait_ms, runs=runs, host=host)
    row["liveness"] = liveness_of(high["volleys"])
    # Счётчик, не убывающий на выходе, виден по занятости ДО залпа: после
    # разогрева в полёте не должно быть никого.
    row["idle_reads_zero"] = all(
        item["idle_before"] == 0
        for setting in at for item in setting["volleys"])
    # ГРАНИЦА КЛИЕНТА, СВЕДЕННАЯ ПО ВСЕМ ЗАЛПАМ. Она снята снаружи
    # сервиса — сколько проб клиент ЕЩЁ считает отправленными и не
    # завершёнными, — и прочитанная занятость не имеет права её
    # превысить. Нарушение здесь означает, что счётчик считает не то, что
    # заявлено, а какая-то другая, третья проверка это не поймает.
    row["client_bound_holds"] = all(
        item["client_bound_holds"]
        for setting in at for item in setting["volleys"])
    # УСТОЙЧИВА ЛИ ЁМКОСТЬ. Если прочитанная занятость при одной и той же
    # настройке скачет, «ёмкость» — не величина, и выводить из неё нечего.
    row["capacity_stable"] = min(high["runs"]) == max(high["runs"])
    # ПЕЧАТАЕТСЯ ЛИ СТРОКА ОСИ 1. Только если ВСЕ контроли держатся:
    # счётчик идёт за числом единиц, читается единицей там, где обязан,
    # не копит остаток между залпами и не превышает границу клиента.
    # Иначе ось недействительна — это законный исход, записанный, а не
    # спрятанный.
    row["axis_1_publishable"] = bool(
        row["knob_evidence"]["follows_workers"]
        and row["negative_control"]["reads_one"]
        and row["idle_reads_zero"]
        and row["client_bound_holds"])
    return row
