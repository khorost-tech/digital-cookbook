"""Один прогон: одна модель исполнения, одна нагрузка, один круг.

Печатает в stdout ровно одну строку JSON — результат. Всё остальное идёт
в stderr, чтобы вывод можно было складывать в фикстуру как есть.

Что именно означают два разных числа результата:

* p50/p95/p99 — длительность САМОЙ операции, снятая внутри исполнителя.
  Для потоков под GIL сюда попадает ожидание GIL: операция физически идёт
  дольше. Для процессов сюда НЕ попадает межпроцессный обмен.
* ops_per_sec — сквозная пропускная способность, включая накладные расходы
  модели: диспетчеризацию, обмен, сборку результатов.

Поэтому у процессов операция может быть быстрой, а сквозная пропускная
способность — низкой. Это не противоречие, а два разных вопроса.
"""

import argparse
import asyncio
import json
import sys
import time
from concurrent.futures import ThreadPoolExecutor, ProcessPoolExecutor

from measure import summarize
from workload import cpu_task, io_task, async_io_task

MASK = 0xFFFFFFFF

# Модели перечислены в фиксированном порядке: ротацию задаёт скрипт-сценарий
# через latin_square, а не этот список.
MODES = ("asyncio", "threads", "processes")


# Функции верхнего уровня: ProcessPoolExecutor умеет передавать только то,
# что пиклится, поэтому замыкания и лямбды здесь не годятся.
def timed_cpu(size: int):
    t0 = time.perf_counter()
    r = cpu_task(size)
    return r, time.perf_counter() - t0


def timed_io(delay_s: float):
    # Возвращаем 1, а не 0: на нулях контрольная сумма io-нагрузки была бы
    # нулём независимо от того, выполнилось хоть что-нибудь. Такая проверка
    # не отличает работу от бездействия.
    t0 = time.perf_counter()
    io_task(delay_s)
    return 1, time.perf_counter() - t0


def run_pool(executor_cls, workers, fn, arg, count):
    """Общий путь для потоков и процессов: различается только класс пула."""
    with executor_cls(max_workers=workers) as ex:
        # Прогрев отдельно: создание пула, запуск процессов и первый импорт
        # не должны попасть в измеряемый интервал.
        list(ex.map(fn, [arg] * workers))
        t0 = time.perf_counter()
        pairs = list(ex.map(fn, [arg] * count))
        wall = time.perf_counter() - t0
    return pairs, wall


async def run_asyncio(workers, workload, arg, count):
    """asyncio с ограничением одновременности в workers операций.

    Для CPU-нагрузки ограничение ни на что не влияет: корутина без единой
    точки ожидания занимает единственный поток цикла событий целиком, и
    операции идут одна за другой. Это и есть содержательный результат, а не
    недостаток постановки.
    """
    sem = asyncio.Semaphore(workers)

    async def one():
        async with sem:
            t0 = time.perf_counter()
            if workload == "cpu":
                r = cpu_task(arg)
            else:
                await async_io_task(arg)
                r = 1
            return r, time.perf_counter() - t0

    # Прогрев вне измерения — как и у пулов.
    await asyncio.gather(*[one() for _ in range(workers)])
    t0 = time.perf_counter()
    pairs = await asyncio.gather(*[one() for _ in range(count)])
    return pairs, time.perf_counter() - t0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--mode", required=True, choices=MODES)
    ap.add_argument("--workload", required=True, choices=("cpu", "io"))
    ap.add_argument("--build", required=True, choices=("gil", "nogil"),
                    help="какая сборка ЗАПРОШЕНА; фактическое состояние GIL "
                         "снимается отдельно и кладётся в поле gil_enabled")
    ap.add_argument("--workers", type=int, default=4)
    ap.add_argument("--count", type=int, default=1000)
    ap.add_argument("--size", type=int, default=25000,
                    help="шагов в cpu-задаче")
    ap.add_argument("--delay", type=float, default=0.02,
                    help="секунд ожидания в io-задаче")
    ap.add_argument("--seed", type=int, default=0)
    ap.add_argument("--round", dest="round_no", type=int, default=1)
    ap.add_argument("--verify", action="store_true",
                    help="напечатать только контрольную сумму: она обязана "
                         "совпадать у всех моделей, иначе они считают разное")
    args = ap.parse_args()

    arg = args.size if args.workload == "cpu" else args.delay

    if args.mode == "asyncio":
        pairs, wall = asyncio.run(
            run_asyncio(args.workers, args.workload, arg, args.count))
    else:
        fn = timed_cpu if args.workload == "cpu" else timed_io
        cls = ThreadPoolExecutor if args.mode == "threads" else ProcessPoolExecutor
        pairs, wall = run_pool(cls, args.workers, fn, arg, args.count)

    results = [p[0] for p in pairs]
    durations = [p[1] for p in pairs]

    checksum = 0
    for r in results:
        checksum = (checksum * 31 + r) & MASK

    if args.verify:
        print(json.dumps({"mode": args.mode, "workload": args.workload,
                          "checksum": checksum, "count": len(results)},
                         sort_keys=True))
        return 0

    res = summarize(
        mode=args.mode, build=args.build, workload=args.workload,
        workers=args.workers, durations_s=durations, wall_s=wall,
        checksum=checksum, seed=args.seed, round_no=args.round_no,
    )

    # Контрольная сумма подтверждает, что операции ВЫПОЛНИЛИСЬ и что модели
    # посчитали одно и то же, но не подтверждает, что io-операция ждала.
    # Это проверяется отдельно, по фактической длительности.
    if args.workload == "io" and res.p50_ms < args.delay * 1000 * 0.9:
        print(f"ОТКАЗ: медиана io-операции {res.p50_ms} мс меньше заказанного "
              f"ожидания {args.delay * 1000} мс — ожидание не состоялось",
              file=sys.stderr)
        return 2

    # Плечо "без GIL", на котором GIL оказался включён, — не плечо без GIL.
    if args.build == "nogil" and res.gil_enabled:
        print("ОТКАЗ: запрошена сборка без GIL, но GIL включён "
              "(PYTHON_GIL или C-расширение без поддержки free-threading)",
              file=sys.stderr)
        return 2

    print(res.to_json())
    return 0


if __name__ == "__main__":
    sys.exit(main())
