"""Собственный пул Drogon — замер асимметрии, а не ссылка на неё.

ЧТО ЗДЕСЬ ПРОВЕРЯЕТСЯ. Спека волны предвидела: «у Drogon пул свой, у
Beast и Crow его нет — писать руками». Проба до прогона показала, что в
стенде пула не было НИ У КОГО: соединение открывалось на каждый запрос у
всех трёх слоёв. Значит асимметрия была ожиданием, а не наблюдением, и
утверждать её без замера нельзя — это ровно тот класс подмены, который
стенд ловит у себя: объявление вместо измерения.

ПОЧЕМУ ЭТО НЕ ЧАСТЬ ОСИ. Собственный пул ходит в базу через libpq САМОГО
Drogon, а три варианта оси — через общий для них libpqxx. Одна таблица
сравнивала бы драйверы, а не слои HTTP, и рушила бы главное правило
стенда. Строка помечена `used_in_axis: false`, и разбор её в таблицу не
берёт.

КАК ЭТО УСТРОЕНО. Проба (`service/drogon_pool_probe.cpp`) собирается и
запускается ВНУТРИ образа варианта drogon: там уже лежат и заголовки, и
библиотека, и компилятор. Исходник подаётся на вход процессу, а не
монтируется и не кладётся в образ: образы трёх вариантов обязаны
оставаться построчно симметричными, а лишний шаг сборки у одного из них
эту симметрию бы сломал — то есть ради замера асимметрии мы бы завели
настоящую.

ЧИСЛО СОЕДИНЕНИЙ СЧИТАЕТСЯ СНАРУЖИ, из `pg_stat_activity`, тем же
способом, что и у оси. Ответ самого клиента о размере его пула здесь не
спрашивается вовсе: объявление драйвера — не замер.
"""

import io
import os
import subprocess
import sys
import threading
import time

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts import docker as dockerlib  # noqa: E402
from scripts import pg  # noqa: E402
from scripts import pool  # noqa: E402

PROBE = os.path.join(STAND, "service", "drogon_pool_probe.cpp")

# Имя приложения пробы. НЕ равно имени варианта намеренно: сервис drogon
# и эта проба — разные участники, и общее имя склеило бы их соединения в
# один счёт.
APPLICATION_NAME = "drogon-own-pool"

# Клиент базы, которым ходит эта проба. Отличается от DRIVER оси — в этом
# вся суть отдельной строки.
DRIVER = "drogon::orm::DbClient (libpq)"

# Сборка идёт ВНУТРИ образа одной командой. -I/usr/include/jsoncpp не
# украшение: заголовок SqlBinder.h включает <json/writer.h>, и без этого
# пути сборка падает — замерено, а не предположено.
BUILD_AND_RUN = (
    "cat > /tmp/probe.cpp && "
    "g++ -std=c++20 -I/usr/include/jsoncpp -o /tmp/probe /tmp/probe.cpp "
    "-ldrogon -ltrantor -ljsoncpp -lpthread && "
    # ЧЕМ ПРОБА СОБРАЛАСЬ — СНИМАЕТСЯ С БИНАРНИКА, а не объявляется.
    # Основание, по которому эта строка не входит в ось, — «другой клиент
    # базы», и до сих пор оно держалось на константе DRIVER в этом файле.
    # Диверсия ревью, дописавшая `-lpqxx -lpq` в эту самую команду,
    # прошла зелёной: ярлык остался прежним. Теперь список библиотек
    # едет в фикстуру тем же способом, каким стенд снимает версии
    # библиотек слоёв, — замером у собранного бинарника.
    "echo '---ldd---' && ldd /tmp/probe && echo '---run---' && /tmp/probe")


def sampler(host=None):
    """Счётчик соединений ПРОБЫ — тот же, что у оси, и про своё имя.

    ВЫНЕСЕН ОТДЕЛЬНО НАМЕРЕННО. Число собственного пула так же легко
    подменить настройкой, как и число оси: «пул на 8 показал 8» верно
    всегда, если восьмёрку взяли из аргумента. Отдельная функция
    позволяет спросить, КОГО она спрашивает, не поднимая ни базы, ни
    образа.
    """
    return pool.sampler_for(APPLICATION_NAME, host=host)


def _source():
    with io.open(PROBE, encoding="utf-8") as handle:
        return handle.read()


def measure(pool_size, concurrent=pool.CONCURRENT, sleep_ms=pool.SLEEP_MS,
            host=None):
    """Строка фикстуры про собственный пул Drogon при одном размере.

    База должна быть уже поднята: проба ходит в неё по имени контейнера
    внутри сети стенда.
    """
    count = sampler(host=host)
    command = [
        "docker", "run", "--rm", "-i", "--network", pg.NETWORK,
        "--entrypoint", "",
        # Адрес базы БЕЗ application_name: имя приложения передаётся
        # libpq переменной PGAPPNAME — drogon разбирает строку
        # подключения сам, и ключ, которого он не знает, до libpq мог бы
        # не доехать. Переменная окружения обходит этот разбор целиком.
        "-e", "STAND_DSN=%s" % pg.dsn(),
        "-e", "PGAPPNAME=%s" % APPLICATION_NAME,
        "-e", "STAND_POOL=%d" % pool_size,
        "-e", "STAND_CONCURRENT=%d" % concurrent,
        "-e", "STAND_SLEEP_MS=%d" % sleep_ms,
        dockerlib.image_tag("drogon"), "sh", "-c", BUILD_AND_RUN,
    ]
    samples = []
    done = {}

    def run():
        done["proc"] = subprocess.run(command, input=_source(),
                                      capture_output=True, text=True)

    worker = threading.Thread(target=run)
    worker.start()
    # Счёт идёт, ПОКА проба жива: сборка внутри образа занимает секунды,
    # и выборки до залпа дадут нули — они не мешают, потому что печатается
    # наибольшее.
    while worker.is_alive():
        samples.append(count())
        time.sleep(pool.POLL_S)
    worker.join()
    proc = done["proc"]
    output = (proc.stdout or "") + (proc.stderr or "")
    linked = sorted(
        line.strip().split(" ")[0]
        for line in output.splitlines()
        if "=>" in line and line.strip().startswith("lib"))
    return {
        "kind": "drogon-own-pool",
        "variant": "drogon",
        "driver": DRIVER,
        "used_in_axis": False,
        "why_not_in_the_axis":
            "ходит в базу через libpq самого фреймворка, а три варианта оси "
            "— через общий для них libpqxx; в одной таблице сравнивались бы "
            "драйверы, а не слои HTTP",
        "application_name": APPLICATION_NAME,
        "pool_size": pool_size,
        "concurrent": concurrent,
        "sleep_ms": sleep_ms,
        "runs": 1,
        "connections_by_run": [max(samples) if samples else 0],
        "connections_observed": max(samples) if samples else 0,
        # ЧТО ПРОБА СЛИНКОВАЛА НА САМОМ ДЕЛЕ. Проверки требуют здесь
        # libpq и требуют ОТСУТСТВИЯ libpqxx: иначе «другой клиент базы»
        # — слова, а не наблюдение.
        "probe_links": linked,
        "links_libpq": any(name.startswith("libpq.so") for name in linked),
        "links_libpqxx": any(name.startswith("libpqxx") for name in linked),
        "probe_exit_code": proc.returncode,
        "probe_finished_every_query": "PROBE DONE" in (proc.stdout or ""),
        "probe_output": ((proc.stdout or "") + (proc.stderr or "")).strip()[
            -2000:],
    }
