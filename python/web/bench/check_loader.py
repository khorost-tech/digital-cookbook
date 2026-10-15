"""Хватает ли нагрузчику мощности, чтобы насытить цель.

Вопрос не праздный: если нагрузчик упирается в себя, все цели покажут
примерно одинаковые числа, и это будет выглядеть как «фреймворки не
различаются» — вывод, который сам по себе звучит правдоподобно и потому
особенно опасен. Первая, однопроцессная версия нагрузчика именно на этом и
попалась: 251 запрос в секунду против 852 у четырёх параллельных.

Проверка прямая: одна и та же цель обстреливается нагрузчиком с разным
числом процессов. Пропускная способность обязана выйти на полку.

* Полка достигнута — цель насыщена, нагрузчик не ограничивает.
* Полки нет — нагрузчик всё ещё потолок, и замеры мерят его, а не цель.

ЧТО ИМЕННО ДОЛЖНО БЫТЬ ДОКАЗАНО. Не «где-то есть полка», а «на том числе
процессов, которым сняты фикстуры, нагрузчик уже не ограничивает». Это не
одно и то же, и первая версия проверки разницы не видела: она брала прирост
только между двумя последними ступенями, мерила каждую ступень ОДИН раз и
объявляла рабочим то число процессов, где случайно вышел максимум. На серии
5117 / 3477 / 4049 / 4230 она отчиталась успехом и выбрала два процесса,
хотя фикстуры снимаются четырьмя, а от четырёх к двенадцати рост 22%.

Поэтому здесь: каждая ступень мерится несколько раз, берётся медиана, и
полка проверяется НАЧИНАЯ С РАБОЧЕГО ЧИСЛА ПРОЦЕССОВ — ни одна ступень выше
него не должна давать заметной прибавки. Плюс ступень с большим внутренним
разбросом делает вердикт невозможным: на неустойчивых числах полку не
доказать, и правильный ответ здесь «проверка не состоялась», а не «успех».

Цель для проверки — голое ASGI-приложение на /ping: самое лёгкое, что есть
в стенде. Если нагрузчик насыщает его, фреймворки насытит тем более.
"""

import json
import os
import statistics
import subprocess
import sys
import time
import urllib.error
import urllib.request

PY = "/opt/py/web/bin/python"
APP_DIR = "/app"
BENCH_DIR = "/bench"
PORT = 8004
URL = f"http://127.0.0.1:{PORT}/ping"

CONCURRENCY = 32
COUNT = 4000
PROC_STEPS = (2, 4, 8, 12)
# Должно совпадать с LOADER_PROCS сценариев: полка доказывается именно для
# того числа процессов, которым сняты фикстуры.
WORKING_PROCS = 4
REPEATS = 3
# Цель и нагрузчик на непересекающихся ядрах: см. pin.py.
TARGET_CPUS = "0-1"
LOADER_CPUS = "2-15"
GROWTH_LIMIT = 1.15
# Разброс внутри ступени, выше которого вердикт не выносится.
SPREAD_LIMIT = 1.25
# Те же границы длительности, что у сценариев: слишком короткая пачка мерит
# шум планировщика, а разогрев снимает работу машины на разгоне.
MIN_SECONDS = 3.0
MAX_SECONDS = 15.0
PRELOAD_SECONDS = 20.0


def wait_ready(proc, timeout=25.0):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if proc.poll() is not None:
            return False
        try:
            with urllib.request.urlopen(URL, timeout=5) as resp:
                if resp.status == 200:
                    return True
        except (OSError, urllib.error.HTTPError):
            pass
        time.sleep(0.2)
    return False


def run_loader(procs, min_seconds=MIN_SECONDS, max_seconds=MAX_SECONDS):
    """Нагрузчик с заданным числом процессов. Возвращает rps и ошибки."""
    p = subprocess.run(
        [PY, "load.py", "--url", URL, "--framework", "raw-asgi",
         "--endpoint", "ping", "--concurrency", str(CONCURRENCY),
         "--count", str(COUNT), "--procs", str(procs),
         "--warmup", "256",
         "--min-seconds", str(min_seconds),
         "--max-seconds", str(max_seconds)],
        cwd=BENCH_DIR, capture_output=True, text=True, encoding="utf-8",
        env=dict(os.environ, LOADER_CPUS=LOADER_CPUS,
                 PYTHONPATH=BENCH_DIR))
    line = (p.stdout or "").strip().splitlines()
    if not line:
        return 0.0, COUNT
    data = json.loads(line[-1])
    return data["rps"], data["errors"]


def verdict(results):
    """Разбор ступеней. Возвращает (код возврата, строки отчёта)."""
    out = []
    unstable = [n for n, v in results.items()
                if min(v) <= 0 or max(v) / min(v) > SPREAD_LIMIT]
    for procs in PROC_STEPS:
        v = sorted(results[procs])
        mark = " <- рабочее" if procs == WORKING_PROCS else ""
        flag = "  НЕУСТОЙЧИВО" if procs in unstable else ""
        circles = " ".join("%.0f" % x for x in v)
        out.append(f"  процессов {procs:>2}: медиана "
                   f"{statistics.median(v):9.1f} rps, повторы {circles}"
                   f"{flag}{mark}")
    if unstable:
        out.append("")
        out.append(f"CHECK НЕ СОСТОЯЛСЯ: ступени {unstable} дали разброс выше "
                   f"{SPREAD_LIMIT} между повторами — на таких числах полку "
                   f"не доказать. Это не успех и не провал: повторите на "
                   f"спокойной машине.")
        return 3, out

    base = statistics.median(results[WORKING_PROCS])
    out.append("")
    worst = 1.0
    for procs in [n for n in PROC_STEPS if n > WORKING_PROCS]:
        growth = statistics.median(results[procs]) / base
        worst = max(worst, growth)
        out.append(f"  прирост {WORKING_PROCS} -> {procs} процессов: "
                   f"{growth:.2f}x (порог {GROWTH_LIMIT})")
    if worst > GROWTH_LIMIT:
        out.append("")
        out.append(f"CHECK FAIL: на {WORKING_PROCS} процессах полки нет — "
                   f"больше процессов дают до {worst:.2f}x. Фикстуры, снятые "
                   f"этим числом, мерят нагрузчик. Увеличьте LOADER_PROCS "
                   f"сценариев и повторите.")
        return 1, out
    out.append("")
    out.append(f"CHECK OK: на {WORKING_PROCS} процессах нагрузчика "
               f"пропускная способность уже на полке — добавление процессов "
               f"до {PROC_STEPS[-1]} даёт не больше {worst:.2f}x.")
    return 0, out


def main():
    server = subprocess.Popen(
        [PY, f"{BENCH_DIR}/pin.py", TARGET_CPUS, "--",
         PY, "-m", "uvicorn", "app_raw:asgi_app", "--port", str(PORT),
         "--log-level", "warning"],
        cwd=APP_DIR, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        if not wait_ready(server):
            print("CHECK FAIL: контрольная цель не поднялась", file=sys.stderr)
            return 1

        # Разогрев: без него первые ступени мерят машину на разгоне, и
        # ряд выходит немонотонным независимо от числа процессов.
        print(f"  разогрев {PRELOAD_SECONDS:.0f} с, результат отбрасывается")
        run_loader(WORKING_PROCS, PRELOAD_SECONDS, PRELOAD_SECONDS)

        results = {n: [] for n in PROC_STEPS}
        for rep_no in range(1, REPEATS + 1):
            for procs in PROC_STEPS:
                rps, errors = run_loader(procs)
                results[procs].append(rps)
                print(f"  повтор {rep_no}, процессов {procs:>2}: "
                      f"{rps:9.1f} rps, ошибок {errors}")
                if errors:
                    print(f"CHECK FAIL: {errors} ошибок при {procs} "
                          f"процессах", file=sys.stderr)
                    return 1
    finally:
        server.terminate()
        try:
            server.wait(timeout=10)
        except subprocess.TimeoutExpired:
            server.kill()

    code, lines = verdict(results)
    print()
    for line in lines:
        print(line)
    print(f"  цель на ядрах {TARGET_CPUS}, нагрузчик на {LOADER_CPUS}")
    return code


if __name__ == "__main__":
    sys.exit(main())
