"""Чего стоит держать одновременность: память и дескрипторы. ОДНО плечо за запуск.

ЧЕМ МЕРИТСЯ ПАМЯТЬ. Основная величина — PSS (Proportional Set Size) из
/proc/<pid>/smaps_rollup, просуммированная по процессам. Ядро делит общие
страницы между теми, кто их разделяет, поэтому сумма PSS — это физическая
память, приходящаяся на набор процессов.

Сумма RSS для этого не годится и приводится только для сравнения: она
считает общие страницы заново в каждом процессе. У модели процессов, где
67 процессов делят интерпретатор и страницы после fork, это завышает
результат в разы. Даже на одном тривиальном процессе Rss/Pss около 1,9.

Третья величина, cgroup — независимая перекрёстная проверка на уровне
контейнера. В неё входит страничный кэш, поэтому она не равна сумме PSS,
но обязана быть с ней одного порядка.

Доля от теоретического потолка отвечает на вопрос «сколько операций модель
вытягивает», но НЕ на вопрос «чего это стоит». Слово «дешевле» относится ко
второму, и без этого замера оно было бы выводом сверх измеренного.

ПОЧЕМУ ОДНО ПЛЕЧО ЗА ЗАПУСК. Первая версия мерила все три модели в одном
интерпретаторе подряд — и была неверной. ProcessPoolExecutor поднимает
forkserver, который переживает закрытие пула и живёт до конца родительского
процесса. Наблюдатель суммирует все процессы python, поэтому следующие плечи
получали чужой хвост: в фикстуре это выглядело как «разброс» потоков
46,3 -> 84,8 МБ, хотя было систематической ступенькой.

Теперь каждое плечо живёт в собственном процессе и уносит свой хвост с собой,
а baseline снимается в НАЧАЛЕ этого процесса и печатается в результат:
baseline_procs, отличный от 1, означает, что мерить начали в грязной среде,
и разбор такой замер отвергает.
"""

import asyncio
import json
import os
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor, ProcessPoolExecutor

from measure import gil_enabled
from workload import io_task, async_io_task

DELAY = 0.02
COUNT = 2000


def python_pids():
    pids = []
    for name in os.listdir("/proc"):
        if not name.isdigit():
            continue
        try:
            with open(f"/proc/{name}/comm") as f:
                if f.read().strip().startswith("python"):
                    pids.append(name)
        except OSError:
            pass
    return pids


CGROUP_PATHS = ("/sys/fs/cgroup/memory.current",
                "/sys/fs/cgroup/memory/memory.usage_in_bytes")


def cgroup_bytes():
    """Память контейнера по cgroup. Включает страничный кэш — это контекст,
    а не замена PSS."""
    for path in CGROUP_PATHS:
        try:
            with open(path) as f:
                return int(f.read().strip())
        except OSError:
            continue
    return 0


def sample():
    """Суммы PSS и RSS в килобайтах, дескрипторы, процессы, cgroup в байтах."""
    pss = rss = fds = 0
    pids = python_pids()
    for pid in pids:
        try:
            with open(f"/proc/{pid}/smaps_rollup") as f:
                for line in f:
                    if line.startswith("Pss:"):
                        pss += int(line.split()[1])
                    elif line.startswith("Rss:"):
                        rss += int(line.split()[1])
            fds += len(os.listdir(f"/proc/{pid}/fd"))
        except OSError:
            pass
    return pss, rss, fds, len(pids), cgroup_bytes()


class Watcher(threading.Thread):
    def __init__(self):
        super().__init__(daemon=True)
        self.stop = threading.Event()
        self.peak_pss = self.peak_rss = self.peak_fds = 0
        self.peak_procs = self.peak_cgroup = 0

    def run(self):
        while not self.stop.is_set():
            pss, rss, fds, procs, cg = sample()
            self.peak_pss = max(self.peak_pss, pss)
            self.peak_rss = max(self.peak_rss, rss)
            self.peak_fds = max(self.peak_fds, fds)
            self.peak_procs = max(self.peak_procs, procs)
            self.peak_cgroup = max(self.peak_cgroup, cg)
            # 100 мс, а не 20: smaps_rollup обходит области памяти, и на
            # 67 процессах частый опрос сам стал бы нагрузкой. Память выходит
            # на полку, поэтому пик от этого не теряется.
            time.sleep(0.1)


def timed_io(delay):
    io_task(delay)
    return 1


async def run_mode(mode, concurrency):
    if mode == "asyncio":
        sem = asyncio.Semaphore(concurrency)

        async def one():
            async with sem:
                await async_io_task(DELAY)
        t0 = time.perf_counter()
        await asyncio.gather(*[one() for _ in range(COUNT)])
        return COUNT / (time.perf_counter() - t0)

    cls = ThreadPoolExecutor if mode == "threads" else ProcessPoolExecutor
    loop = asyncio.get_running_loop()
    with cls(max_workers=concurrency) as ex:
        # Прогрев вне измеряемого интервала.
        await asyncio.gather(*[loop.run_in_executor(ex, timed_io, DELAY)
                               for _ in range(concurrency)])
        t0 = time.perf_counter()
        await asyncio.gather(*[loop.run_in_executor(ex, timed_io, DELAY)
                               for _ in range(COUNT)])
        wall = time.perf_counter() - t0
    return COUNT / wall


async def main():
    build, mode = sys.argv[1], sys.argv[2]
    concurrency = int(sys.argv[3])
    round_no = int(sys.argv[4])
    if build == "nogil" and gil_enabled():
        print("ОТКАЗ: запрошена сборка без GIL, но GIL включён", file=sys.stderr)
        return 2

    base_pss, base_rss, base_fds, base_procs, base_cg = sample()
    w = Watcher()
    w.start()
    rps = await run_mode(mode, concurrency)
    w.stop.set()
    w.join()

    print(json.dumps({
        "build": build, "gil_enabled": gil_enabled(),
        "python": sys.version.split()[0], "mode": mode,
        "concurrency": concurrency, "count": COUNT, "round": round_no,
        "ops_per_sec": round(rps, 2),
        "peak_pss_mb": round(w.peak_pss / 1024, 1),
        "peak_rss_sum_mb": round(w.peak_rss / 1024, 1),
        "peak_fds": w.peak_fds,
        "peak_procs": w.peak_procs,
        "peak_cgroup_mb": round(w.peak_cgroup / 1048576, 1),
        "baseline_pss_mb": round(base_pss / 1024, 1),
        "baseline_cgroup_mb": round(base_cg / 1048576, 1),
        "baseline_fds": base_fds,
        "baseline_procs": base_procs,
    }, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
