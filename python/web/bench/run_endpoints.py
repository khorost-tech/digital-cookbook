"""Матрица: пять целей на трёх эндпоинтах при трёх уровнях одновременности.

Смысл сценария — не рейтинг, а разложение. Три эндпоинта отличаются объёмом
полезной работы:

* /ping  — пустой ответ, только накладные расходы фреймворка и сервера;
* /echo  — разбор JSON, валидация, сериализация: то, за что платят осознанно;
* /wait  — то же плюс ожидание 20 мс, имитирующее поход в базу.

Ожидание, которое сценарий обязан подтвердить ИЛИ опровергнуть: разрыв между
целями велик на /ping, меньше на /echo и почти исчезает на /wait. Если так —
«какой фреймворк быстрее» действительно неправильный вопрос, и это будет
показано, а не заявлено.

Цикл идёт целиком внутри контейнера одним вызовом: цепочка из десятков
docker compose exec из внешней оболочки в соседнем стенде дважды молча
обрывалась на середине.
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

TARGET_CPUS = "0-1"
LOADER_CPUS = "2-15"
LOADER_PROCS = 4

# Цель и её сервер. Каждое приложение — под своим естественным сервером;
# контрольные плечи raw-* дают пол, над которым читается надбавка фреймворка.
TARGETS = [
    ("raw-asgi", 8004, ["-m", "uvicorn", "app_raw:asgi_app", "--port", "8004"]),
    ("fastapi", 8001, ["-m", "uvicorn", "app_fastapi:app", "--port", "8001"]),
    ("django", 8002, ["-m", "uvicorn", "app_django:application", "--port", "8002"]),
    ("raw-wsgi", 8005, ["-m", "gunicorn", "-b", "127.0.0.1:8005", "app_raw:wsgi_app"]),
    ("flask", 8003, ["-m", "gunicorn", "-b", "127.0.0.1:8003", "app_flask:app"]),
]

ENDPOINTS = ("ping", "echo", "wait")
CONCURRENCY = (1, 16, 64)
WAIT_SECONDS = 0.02
# Бюджет измеряемой пачки. Число запросов считается от теоретического
# потолка, а плечи, которые до него не достают, на нём застревают: клетка
# flask/wait/c=64 тянулась 284 секунды вместо задуманных четырёх, и два
# плеча из пяти съедали 90% круга. Пропускная способность — скорость, от
# длины пачки она не зависит; урезанные клетки помечаются в фикстуре.
MAX_SECONDS = 15.0
# Нижняя граница длительности клетки. Быстрым плечам двух тысяч запросов
# хватало на треть секунды, и разброс по кругам доходил до 1.61x — то есть
# мерился шум планировщика, а не цель.
MIN_SECONDS = 3.0


def counts(endpoint, concurrency):
    """Сколько запросов слать.

    На /wait потолок равен «одновременность / задержка», поэтому фиксированное
    число запросов при малой одновременности растянуло бы прогон на минуты.
    Считаем от потолка: примерно четыре секунды работы.
    """
    if endpoint == "wait":
        return max(200, int(concurrency / WAIT_SECONDS * 4))
    return 2000


def start(name, port, argv):
    proc = subprocess.Popen(
        [PY, f"{BENCH_DIR}/pin.py", TARGET_CPUS, "--", PY] + argv
        + ["--log-level", "warning"],
        cwd=APP_DIR, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    url = f"http://127.0.0.1:{port}/ping"
    deadline = time.time() + 30
    while time.time() < deadline:
        if proc.poll() is not None:
            return None
        try:
            with urllib.request.urlopen(url, timeout=5) as resp:
                if resp.status == 200:
                    return proc
        except (OSError, urllib.error.HTTPError):
            pass
        time.sleep(0.2)
    proc.terminate()
    return None


def stop(proc):
    proc.terminate()
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()


PRELOAD_SECONDS = 20.0


def preload(port):
    """Разогрев плеча перед замером; результат отбрасывается.

    Без него первые круги мерят машину на разгоне, а не в установившемся
    режиме. Проявляется это только на самых быстрых клетках: доля FastAPI
    от своего пола на /ping при c=64 давала по кругам 44, 73, 112, 70, 74,
    71, 75, 72, 71, 71, 69 — девять кругов в пределах 69–75 и два выброса.
    У медленных плеч разброс той же доли 1.04x, то есть дело не в целях.

    Двадцать секунд подобраны замером: после десяти клетка всё ещё давала
    1.51x, после двадцати — 1.04x. Протокол одинаков для всех плеч, поэтому
    сравнение между ними остаётся честным.
    """
    subprocess.run(
        [PY, "load.py", "--url", f"http://127.0.0.1:{port}/ping",
         "--framework", "preload", "--endpoint", "ping",
         "--concurrency", "64", "--count", "2000", "--warmup", "256",
         "--procs", str(LOADER_PROCS),
         "--max-seconds", str(PRELOAD_SECONDS),
         "--min-seconds", str(PRELOAD_SECONDS)],
        cwd=BENCH_DIR, capture_output=True, text=True, encoding="utf-8",
        env=dict(os.environ, LOADER_CPUS=LOADER_CPUS, PYTHONPATH=BENCH_DIR))


def measure(name, port, endpoint, concurrency, round_no, seed):
    cmd = [PY, "load.py", "--url", f"http://127.0.0.1:{port}/{endpoint}",
           "--framework", name, "--endpoint", endpoint,
           "--concurrency", str(concurrency),
           "--count", str(counts(endpoint, concurrency)),
           "--warmup", str(max(50, concurrency * 4)),
           "--max-seconds", str(MAX_SECONDS),
           "--min-seconds", str(MIN_SECONDS),
           "--procs", str(LOADER_PROCS),
           "--round", str(round_no), "--seed", str(seed)]
    data, _ = run_load(cmd, BENCH_DIR,
                       dict(os.environ, LOADER_CPUS=LOADER_CPUS,
                            PYTHONPATH=BENCH_DIR))
    return json.dumps(data, sort_keys=True) if data else None


def main():
    seed = int(os.environ.get("SEED", "0"))
    rounds = int(os.environ.get("ROUNDS", "3"))
    out_path = os.environ.get("OUT", "/fixtures/endpoints.txt")

    with open(out_path, "a", encoding="utf-8") as out:
        for r in range(1, rounds + 1):
            # Порядок целей внутри круга — латинский квадрат: при фиксированном
            # первая цель систематически получает более холодную машину.
            order = latin_square(seed, r, len(TARGETS))
            for idx in order:
                name, port, argv = TARGETS[idx]
                proc = start(name, port, argv)
                if proc is None:
                    print(f"ОТКАЗ: {name} не поднялся", file=sys.stderr)
                    return 2
                try:
                    preload(port)
                    for endpoint in ENDPOINTS:
                        for conc in CONCURRENCY:
                            line = measure(name, port, endpoint, conc, r, seed)
                            if line is None:
                                print(f"ОТКАЗ: нет результата "
                                      f"{name}/{endpoint}/c={conc}",
                                      file=sys.stderr)
                                return 2
                            out.write(line + "\n")
                            out.flush()
                finally:
                    stop(proc)
                print(f"круг {r}: {name} готов", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
