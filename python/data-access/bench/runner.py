"""Прогон одного плеча на одном запросе: сколько это стоит.

Устройство перенесено из стенда python/web, где каждая часть появилась
после того, как без неё замер соврал:

* РАЗОГРЕВ по времени, а не по числу итераций. На быстрой цели двести
  итераций заканчиваются за сотые доли секунды и прогреть ничего не
  успевают; ограничителем обязано быть время.
* ПРОБА перед измерением. Число итераций считается по измеренной скорости
  плеча, а не наугад: иначе быстрые клетки укладываются в треть секунды и
  мерят шум планировщика, а медленные тянутся минутами.
* ОКНО по размаху самих итераций, а не вокруг цикла. Между взятием отметки
  и первой итерацией есть мёртвое время; в короткой пачке это заметная
  доля, и измеренная скорость начинала зависеть от длины прогона.
* ХЕШ РЕЗУЛЬТАТА в каждой строке фикстуры. Равенство плеч проверяется до
  замера, но «до» не гарантирует «во время».

ПЛЕЧО ПОДНИМАЕТСЯ ОДИН РАЗ НА ВСЮ ПАЧКУ. Первая версия звала `execute`,
который открывал соединение и создавал цикл событий на каждую итерацию:
замер выдавал 23 мс на запрос одной строки при 0,015 мс работы сервера.
Мерилась установка соединения, а не доступ к данным.

Чего здесь НЕТ по сравнению с python/web: счёта простоя между запросами.
Там нагрузка шла из нескольких процессов с общей очередью, и паузы внутри
соединения означали заминку клиента. Здесь цикл последовательный и
однопоточный — простаивать между итерациями ему негде, а если бы он это
делал, разница попала бы прямо в длительность итерации.
"""

import asyncio
import json
import statistics
import sys
import time

sys.path.insert(0, "/app")

import common  # noqa: E402
import measure  # noqa: E402

WARMUP_SECONDS = 1.0
PROBE_SECONDS = 1.0
MIN_SECONDS = 3.0
MAX_SECONDS = 15.0


async def _burst_async(arm, query, deadline=None, limit=None):
    out = []
    while True:
        if deadline is not None and time.perf_counter() >= deadline:
            break
        if limit is not None and len(out) >= limit:
            break
        t0 = time.perf_counter()
        rows = await arm.run(query)
        out.append((t0, time.perf_counter() - t0, rows))
    return out


def _burst_sync(arm, query, deadline=None, limit=None):
    out = []
    while True:
        if deadline is not None and time.perf_counter() >= deadline:
            break
        if limit is not None and len(out) >= limit:
            break
        t0 = time.perf_counter()
        rows = arm.run(query)
        out.append((t0, time.perf_counter() - t0, rows))
    return out


def _plan(probe, min_seconds, max_seconds):
    """Сколько итераций мерить, исходя из пробы."""
    span = max(t + d for t, d, _ in probe) - min(t for t, _, _ in probe)
    rate = len(probe) / span if span > 0 else 0.0
    lo = max(1, int(rate * min_seconds))
    hi = max(1, int(rate * max_seconds))
    return min(hi, max(lo, 1))


async def _measure_async(arm_cls, query, min_seconds, max_seconds,
                         before_measure=None):
    arm = arm_cls()
    await arm.setup()
    try:
        await _burst_async(arm, query,
                           deadline=time.perf_counter() + WARMUP_SECONDS)
        probe = await _burst_async(
            arm, query, deadline=time.perf_counter() + PROBE_SECONDS)
        if not probe:
            raise RuntimeError(f"{arm_cls.name}/{query}: проба пуста")
        limit = _plan(probe, min_seconds, max_seconds)
        if before_measure is not None:
            before_measure()
        return await _burst_async(arm, query, limit=limit)
    finally:
        await arm.teardown()


def _measure_sync(arm_cls, query, min_seconds, max_seconds,
                  before_measure=None):
    arm = arm_cls()
    arm.setup()
    try:
        _burst_sync(arm, query, deadline=time.perf_counter() + WARMUP_SECONDS)
        probe = _burst_sync(arm, query,
                            deadline=time.perf_counter() + PROBE_SECONDS)
        if not probe:
            raise RuntimeError(f"{arm_cls.name}/{query}: проба пуста")
        limit = _plan(probe, min_seconds, max_seconds)
        if before_measure is not None:
            before_measure()
        return _burst_sync(arm, query, limit=limit)
    finally:
        arm.teardown()


def run(arm_cls, query, round_no=0, seed=0,
        min_seconds=MIN_SECONDS, max_seconds=MAX_SECONDS,
        before_measure=None):
    """before_measure зовётся между пробой и измеряемой пачкой.

    Нужен там, где со стороны сервера считается то же, что со стороны
    клиента: обнулять серверную статистику надо ровно перед измеряемыми
    вызовами. Обнуление до прогрева даёт серверу лишние вызовы, и доля
    сервера выходила 100,5% — величина, которой не бывает.
    """
    if arm_cls.is_async:
        results = asyncio.run(
            _measure_async(arm_cls, query, min_seconds, max_seconds,
                           before_measure))
    else:
        results = _measure_sync(arm_cls, query, min_seconds, max_seconds,
                                before_measure)

    durations = [d for _, d, _ in results]
    span = (max(t + d for t, d, _ in results)
            - min(t for t, _, _ in results))
    fp = common.fingerprint(query, results[-1][2])

    return {
        "arm": arm_cls.name,
        "query": query,
        "round": round_no,
        "seed": seed,
        "iterations": len(results),
        "wall_s": round(span, 4),
        "ops": round(len(results) / span, 2) if span > 0 else 0.0,
        # На many-rows сопоставима именно эта величина, а не число итераций.
        "rows_per_op": fp["rows"],
        "rows_per_s": round(len(results) * fp["rows"] / span, 1)
        if span > 0 else 0.0,
        "mean_ms": round(statistics.fmean(durations) * 1000, 4),
        "p50_ms": round(measure.percentile(durations, 0.50) * 1000, 4),
        "p95_ms": round(measure.percentile(durations, 0.95) * 1000, 4),
        "p99_ms": round(measure.percentile(durations, 0.99) * 1000, 4),
        "sha256": fp["sha256"],
        # Решать, публиковать ли верхние перцентили, должен разбор, а не
        # автор статьи по памяти.
        "percentiles_publishable": len(results) >= 1000,
    }


def emit(row):
    print(json.dumps(row, ensure_ascii=False, sort_keys=True))
