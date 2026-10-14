"""Измеритель: перцентили, сбалансированная ротация и паспорт прогона."""

import json
import math
import sys
from dataclasses import dataclass, asdict


def percentile(xs, q: float) -> float:
    """Перцентиль методом nearest-rank: idx = ceil(q*n) - 1.

    Не floor((n-1)*q): та формула занижает верхние перцентили на позицию и на
    коротких выборках систематически приукрашивает p95/p99.

    Сортируется КОПИЯ: список вызывающего не портится.
    """
    if not xs:
        return 0
    s = sorted(xs)
    idx = math.ceil(q * len(s)) - 1
    return s[min(max(idx, 0), len(s) - 1)]


def latin_square(seed: int, round_no: int, k: int) -> list[int]:
    """Циклическая ротация порядка плеч — латинский квадрат порядка k.

    За k последовательных кругов каждое плечо занимает каждую позицию ровно
    один раз, при любом seed. seed задаёт лишь сдвиг начала, а не случайность:
    случайный порядок не гарантировал бы баланс на малом числе кругов.
    """
    n = seed + round_no
    return [(n + i) % k for i in range(k)]


def gil_enabled() -> bool:
    """Фактическое состояние GIL.

    Вызывать ПОСЛЕ импорта всех модулей прогона: на free-threaded сборке GIL
    возвращается обратно и через PYTHON_GIL=1, и автоматически, если
    загруженное C-расширение не заявило поддержку free-threading. Путь
    интерпретатора состояние GIL не доказывает.
    """
    return sys._is_gil_enabled()


@dataclass
class Result:
    mode: str          # asyncio | threads | processes
    build: str         # gil | nogil — как ЗАПРОШЕНО
    gil_enabled: bool  # как ОКАЗАЛОСЬ на самом деле
    python: str
    workload: str      # cpu | io
    workers: int
    count: int
    p50_ms: float
    p95_ms: float
    p99_ms: float
    wall_s: float
    ops_per_sec: float
    checksum: int
    seed: int
    round_no: int

    def to_json(self) -> str:
        return json.dumps(asdict(self), ensure_ascii=False, sort_keys=True)


def summarize(mode, build, workload, workers, durations_s, wall_s,
              checksum, seed, round_no) -> Result:
    n = len(durations_s)
    return Result(
        mode=mode,
        build=build,
        gil_enabled=gil_enabled(),
        python=sys.version.split()[0],
        workload=workload,
        workers=workers,
        count=n,
        p50_ms=round(percentile(durations_s, 0.50) * 1000, 4),
        p95_ms=round(percentile(durations_s, 0.95) * 1000, 4),
        p99_ms=round(percentile(durations_s, 0.99) * 1000, 4),
        wall_s=round(wall_s, 4),
        ops_per_sec=round(n / wall_s, 2) if wall_s > 0 else 0.0,
        checksum=checksum,
        seed=seed,
        round_no=round_no,
    )
