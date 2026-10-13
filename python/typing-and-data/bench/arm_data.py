"""Одно плечо чтения — в отдельном процессе.

ВОПРОС У ВСЕХ ПЯТИ ОДИН: сумма по категориям за поздние дни. Отличается
только способ его задать, и от способа зависит, полезет ли движок в
разделы, которые к ответу отношения не имеют.

ЧТО СЧИТАЕТСЯ ИСХОДОМ. Дошло плечо до ответа или упало. Когда ненужные
разделы сделаны нечитаемыми, это и есть доказательство: движок, который их
не тронул, отвечает; движок, который полез читать всё, получает отказ в
доступе. Целое «да/нет», одинаково устроенное для обоих движков.

ПОЧЕМУ НЕ БАЙТЫ. Первая версия считала прочитанное через /proc/self/io.
Величина оказалась несопоставимой: pandas читает системными вызовами, а
polars отображает файл в память, и у него счётчик показывал 23 КБ там, где
у pandas три мегабайта — на одной и той же работе. Отключить отображение у
ленивого плеча нечем, а подсунуть свой считающий файловый объект значит
лишить polars проброса, то есть сломать измеряемое.

ЧТО ЭТО НЕ МЕРЯЕТ. Скорость. Плечо, не полезшее в лишние разделы, не
обязано быть быстрее — оно именно что не сделало работу.
"""

import hashlib
import json
import os
import sys

DATASET = os.environ.get("DATASET", "/fixtures/sales")
BUCKET_TO = int(os.environ.get("BUCKET_TO", "5"))
NEEDED = ["day_bucket", "category", "amount_cents"]


def canonical(pairs) -> str:
    """Ответ в одном виде для всех плеч.

    Порядок групп у движков разный, поэтому сортируем; суммы целые,
    поэтому совпадают точно, без округления.
    """
    body = ";".join(f"{name}={int(value)}" for name, value in sorted(pairs))
    return hashlib.sha256(body.encode("utf-8")).hexdigest()[:16]


def pandas_full():
    """Прочитать всё, отобрать в памяти. Как пишут, когда не думают."""
    import pandas as pd
    frame = pd.read_parquet(DATASET)
    frame["day_bucket"] = frame["day_bucket"].astype(int)
    picked = frame[frame["day_bucket"] < BUCKET_TO]
    return list(picked.groupby("category")["amount_cents"].sum().items())


def pandas_columns():
    """Столбцы названы, разделы — нет."""
    import pandas as pd
    frame = pd.read_parquet(DATASET, columns=NEEDED)
    frame["day_bucket"] = frame["day_bucket"].astype(int)
    picked = frame[frame["day_bucket"] < BUCKET_TO]
    return list(picked.groupby("category")["amount_cents"].sum().items())


def pandas_filters():
    """Отбор передан читателю файлов, а не сделан после чтения."""
    import pandas as pd
    frame = pd.read_parquet(DATASET, columns=NEEDED,
                            filters=[("day_bucket", "<", BUCKET_TO)])
    return list(frame.groupby("category")["amount_cents"].sum().items())


def polars_eager():
    """Тот же смысл, что pandas-full: сперва прочитать, потом отобрать."""
    import polars as pl
    frame = pl.read_parquet(DATASET, hive_partitioning=True)
    grouped = (frame.filter(pl.col("day_bucket") < BUCKET_TO)
               .group_by("category")
               .agg(pl.col("amount_cents").sum()))
    return [(row[0], row[1]) for row in grouped.iter_rows()]


def polars_lazy():
    """План строится целиком, и отбор доезжает до чтения файлов."""
    import polars as pl
    plan = (pl.scan_parquet(DATASET, hive_partitioning=True)
            .filter(pl.col("day_bucket") < BUCKET_TO)
            .group_by("category")
            .agg(pl.col("amount_cents").sum()))
    grouped = plan.collect()
    return [(row[0], row[1]) for row in grouped.iter_rows()]


ARMS = {
    "pandas-full": pandas_full,
    "pandas-columns": pandas_columns,
    "pandas-filters": pandas_filters,
    "polars-eager": polars_eager,
    "polars-lazy": polars_lazy,
}


def main():
    name = sys.argv[1]
    try:
        pairs = ARMS[name]()
    except Exception as exc:  # noqa: BLE001 — исход плеча, а не авария
        print(json.dumps({
            "arm": name,
            "reached_answer": False,
            "error_type": type(exc).__name__,
            "error": str(exc).replace(chr(10), " ")[:200],
            "result_sha256": None,
            "groups": 0,
        }, ensure_ascii=False, sort_keys=True))
        return 0

    print(json.dumps({
        "arm": name,
        "reached_answer": True,
        "error_type": None,
        "error": None,
        "result_sha256": canonical(pairs),
        "groups": len(pairs),
    }, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
