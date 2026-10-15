"""Воркеры: сколько их нужно и почему ответ зависит от типа нагрузки.

Матрица эндпоинтов уже показала главное: на ручке с ожиданием WSGI с ОДНИМ
синхронным воркером упирается в 44 запроса в секунду при любой
одновременности, тогда как ASGI выходит на 2800. Это не разница фреймворков,
а разница моделей: синхронный воркер занят всё время ожидания целиком.

Здесь это разбирается по числу воркеров. Три плеча:

* gunicorn с sync-воркерами — потолок равен «воркеров / задержка»;
* gunicorn с gthread-воркерами — потоки внутри процесса;
* uvicorn с несколькими воркерами — процессы с циклом событий в каждом.

Плюс счётная ручка /cpu, которой в матрице не было: на ней воркеры-процессы
обходят GIL, и картина обратная. Ожидание, которое сценарий обязан проверить:
на /wait рост воркеров нужен синхронной модели и почти не нужен асинхронной,
на /cpu — нужен обеим, потому что воркеры это процессы.

Числа по /cpu обязаны согласоваться по порядку величины с соседним стендом
python/concurrency: там восемь процессов дали около шестикратного роста.
Если здесь выйдет иначе — искать причину, а не публиковать.
"""

import json
import os
import subprocess

from loadrun import run_load
import sys
import time
import urllib.error
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from measure import latin_square  # noqa: E402

PY = "/opt/py/web/bin/python"
APP_DIR = "/app"
BENCH_DIR = os.path.dirname(os.path.abspath(__file__))

# Воркерам нужно больше ядер, чем одиночной цели: восемь процессов на двух
# ядрах измеряли бы нехватку ядер, а не модель воркеров.
TARGET_CPUS = "0-7"
LOADER_CPUS = "8-15"
LOADER_PROCS = 4
PORT = 8010

WORKERS = (1, 2, 4, 8)
WAIT_SECONDS = 0.02

# Четыре способа развернуть одно и то же приложение.
#
# ВНИМАНИЕ на первые два: это ОДИН И ТОТ ЖЕ FastAPI с тем же числом
# ASGI-воркеров, развёрнутый двумя способами. У «uvicorn --workers N» при
# N>1 распределение задержек получает ступень около 50 мс: p50 ровно 50.00
# на /ping и ровно 70.00 на /wait (то есть 50 плюс само ожидание), p95
# держится там же (50.3 и 70.2), а p99 уходит к 60 и 80 — то есть это
# ступень распределения, а не одинаковая надбавка каждому запросу.
# У gunicorn с uvicorn-воркерами тех же четырёх: p50 = 3.83 и 21.50.
# Разница в пропускной способности тринадцатикратная на /ping и
# трёхкратная с ожиданием.
#
# Без второго плеча вывод звучал бы как «воркеры не помогают ASGI», что
# грубо неверно: не помогает конкретный способ запуска.
#
# Класс воркера берётся из ОТДЕЛЬНОГО пакета uvicorn-worker, а не из
# uvicorn.workers: последний объявлен устаревшим самим uvicorn, и мерить на
# нём значило бы публиковать числа для пути, который перестанут поддерживать.
FLAVOURS = [
    ("uvicorn-workers", "fastapi", lambda w: [
        "-m", "uvicorn", "app_fastapi:app", "--port", str(PORT),
        "--workers", str(w), "--log-level", "warning"]),
    ("gunicorn-uvicorn", "fastapi", lambda w: [
        "-m", "gunicorn", "-b", f"127.0.0.1:{PORT}", "-w", str(w),
        "-k", "uvicorn_worker.UvicornWorker",
        "--log-level", "warning", "app_fastapi:app"]),
    ("gunicorn-sync", "flask", lambda w: [
        "-m", "gunicorn", "-b", f"127.0.0.1:{PORT}", "-w", str(w),
        "--log-level", "warning", "app_flask:app"]),
    ("gunicorn-gthread", "flask", lambda w: [
        "-m", "gunicorn", "-b", f"127.0.0.1:{PORT}", "-w", str(w),
        "-k", "gthread", "--threads", "8",
        "--log-level", "warning", "app_flask:app"]),
]

# Пустая ручка нужна не ради «кто быстрее на ничего», а как ВТОРАЯ точка для
# надбавки способа запуска: если она одинакова на пустой ручке и на ручке с
# ожиданием, надбавка фиксированная и лежит в тракте, а не в модели.
ENDPOINTS = ("wait", "cpu", "ping")
CONCURRENCY = 64


def start(argv):
    proc = subprocess.Popen(
        [PY, f"{BENCH_DIR}/pin.py", TARGET_CPUS, "--", PY] + argv,
        cwd=APP_DIR, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    url = f"http://127.0.0.1:{PORT}/ping"
    deadline = time.time() + 40
    while time.time() < deadline:
        if proc.poll() is not None:
            return None
        try:
            with urllib.request.urlopen(url, timeout=5) as resp:
                if resp.status == 200:
                    # Воркеры поднимаются не мгновенно: даём им дойти до
                    # готовности, иначе первый замер попадёт на неполный пул.
                    time.sleep(1.5)
                    return proc
        except (OSError, urllib.error.HTTPError):
            pass
        time.sleep(0.2)
    proc.terminate()
    return None


def stop(proc):
    proc.terminate()
    try:
        proc.wait(timeout=15)
    except subprocess.TimeoutExpired:
        proc.kill()


MAX_SECONDS = 15.0
# Нижняя граница длительности клетки. Быстрым плечам двух тысяч запросов
# хватало на треть секунды, и разброс по кругам доходил до 1.61x — то есть
# мерился шум планировщика, а не цель.
MIN_SECONDS = 3.0


def measure(flavour, app, workers, endpoint, round_no, seed):
    # Число запросов считается от ОЖИДАЕМОЙ пропускной способности, а не
    # берётся фиксированным: у синхронного воркера на счётной ручке потолок
    # около 47 запросов в секунду, и полторы тысячи растянулись бы на
    # полминуты на каждую клетку.
    if endpoint == "ping":
        count = 4000
    elif endpoint == "wait":
        count = max(400, int(CONCURRENCY / WAIT_SECONDS * 3))
    else:
        # На счёте потолок задаёт число воркеров: примерно workers / 0.021.
        count = max(300, int(workers / 0.021 * 4))
    cmd = [PY, "load.py", "--url", f"http://127.0.0.1:{PORT}/{endpoint}",
           "--framework", f"{flavour}/w={workers}", "--endpoint", endpoint,
           "--concurrency", str(CONCURRENCY), "--count", str(count),
           "--warmup", "200", "--procs", str(LOADER_PROCS),
           "--max-seconds", str(MAX_SECONDS),
           "--min-seconds", str(MIN_SECONDS),
           "--round", str(round_no), "--seed", str(seed)]
    data, _ = run_load(cmd, BENCH_DIR,
                       dict(os.environ, LOADER_CPUS=LOADER_CPUS,
                            PYTHONPATH=BENCH_DIR))
    if data is None:
        return None
    data["flavour"] = flavour
    data["app"] = app
    data["workers"] = workers
    return json.dumps(data, sort_keys=True)


PRELOAD_SECONDS = 20.0


def preload():
    """Разогрев конфигурации перед замером; результат отбрасывается.

    Тот же приём, что в run_endpoints.py: без него первые круги мерят
    машину на разгоне. Протокол одинаков для всех плеч.
    """
    subprocess.run(
        [PY, "load.py", "--url", f"http://127.0.0.1:{PORT}/ping",
         "--framework", "preload", "--endpoint", "ping",
         "--concurrency", str(CONCURRENCY), "--count", "2000",
         "--warmup", "256", "--procs", str(LOADER_PROCS),
         "--max-seconds", str(PRELOAD_SECONDS),
         "--min-seconds", str(PRELOAD_SECONDS)],
        cwd=BENCH_DIR, capture_output=True, text=True, encoding="utf-8",
        env=dict(os.environ, LOADER_CPUS=LOADER_CPUS, PYTHONPATH=BENCH_DIR))


def main():
    seed = int(os.environ.get("SEED", "0"))
    rounds = int(os.environ.get("ROUNDS", "5"))
    out_path = os.environ.get("OUT", "/fixtures/workers.txt")

    with open(out_path, "a", encoding="utf-8") as out:
        for r in range(1, rounds + 1):
            order = latin_square(seed, r, len(FLAVOURS))
            for idx in order:
                flavour, app, build = FLAVOURS[idx]
                for w in WORKERS:
                    proc = start(build(w))
                    if proc is None:
                        print(f"ОТКАЗ: {flavour} w={w} не поднялся",
                              file=sys.stderr)
                        return 2
                    try:
                        preload()
                        for endpoint in ENDPOINTS:
                            line = measure(flavour, app, w, endpoint, r, seed)
                            if line is None:
                                print(f"ОТКАЗ: нет результата "
                                      f"{flavour}/w={w}/{endpoint}",
                                      file=sys.stderr)
                                return 2
                            out.write(line + "\n")
                            out.flush()
                    finally:
                        stop(proc)
                print(f"круг {r}: {flavour} готов", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
