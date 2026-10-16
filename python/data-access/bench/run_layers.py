"""Разложение цены доступа по слоям: пять плеч на трёх запросах.

ПОРЯДОК РОТИРУЕТСЯ ДВАЖДЫ: и по плечам, и по запросам.

Порядок плеч внутри круга — латинский квадрат: за пять кругов каждое плечо
занимает каждую позицию ровно один раз. Фиксированный порядок дал бы
систематическое смещение первому плечу, а случайная перестановка на малом
числе кругов такого баланса не даёт.

Порядок ЗАПРОСОВ внутри плеча ротируется тем же способом, со смещением от
номера плеча — иначе позиция запроса совпала бы у всех плеч одного круга.

Но одной ротации мало, и это выяснилось со второго захода. Циклический
сдвиг балансирует ПОЗИЦИИ, а не относительный порядок пары: heavy-rows
стоит в списке третьим, heavy-entities четвёртым, и из пяти сдвигов лишь
один ставит их в обратном порядке. За одиннадцать кругов выходило 9:2 — то
есть для контролируемой пары порядок запуска оставался почти постоянным и
подмешивался к измеряемому эффекту ровно так же, как при полном отсутствии
ротации.

Поэтому относительный порядок ПАРЫ задаётся явно и чередуется по кругам:
за одиннадцать кругов каждое плечо получает 5 кругов одним порядком и 6
другим. Чередование зависит и от номера плеча, чтобы плечи не переворачивали
пару одновременно. Позиции при этом по-прежнему балансирует сдвиг.

РАЗОГРЕВ РАЗ В КРУГ, А НЕ ПЕРЕД КАЖДЫМ ПЛЕЧОМ. В стенде python/web разогрев
стоял перед каждой целью, потому что там цель — отдельный процесс, который
поднимался заново. Здесь ничего не перезапускается, нагрузка на машину
непрерывна внутри круга, и хватает одного разогрева на круг. Это не
экономия: лишние двадцать секунд перед каждым плечом ничего бы не
измерили, зато удлинили бы прогон вчетверо.
"""

import json
import os
import sys
import time

sys.path.insert(0, "/app")
sys.path.insert(0, "/bench")

import arms  # noqa: E402
import common  # noqa: E402
import measure  # noqa: E402
import runner  # noqa: E402

PRELOAD_SECONDS = 20.0

# Пара, ради которой введена контрбалансировка: у неё дословно одинаковый
# SQL, и на ней держится вывод про цену материализации.
CONTROLLED_PAIR = ("heavy-rows", "heavy-entities")


def preload():
    """Разогрев машины. Результат отбрасывается целиком."""
    runner.run(arms.RawAsyncpg, "many-rows",
               min_seconds=PRELOAD_SECONDS, max_seconds=PRELOAD_SECONDS)


def main():
    rounds = int(os.environ.get("ROUNDS", "11"))
    seed = int(os.environ.get("SEED", "0"))
    out_path = os.environ.get("OUT", "/fixtures/layers.txt")

    with open(out_path, "a", encoding="utf-8") as out:
        for r in range(1, rounds + 1):
            started = time.time()
            preload()
            order = measure.latin_square(seed, r, len(arms.ARMS))
            queries = list(common.SQL)
            for idx in order:
                arm_cls = arms.ARMS[idx]
                q_order = measure.latin_square(seed + idx, r, len(queries))
                q_order = measure.counterbalance(
                    q_order,
                    queries.index(CONTROLLED_PAIR[0]),
                    queries.index(CONTROLLED_PAIR[1]),
                    flip=bool((r + idx) % 2))
                for qi in q_order:
                    query = queries[qi]
                    row = runner.run(arm_cls, query, round_no=r, seed=seed)
                    out.write(json.dumps(row, ensure_ascii=False,
                                         sort_keys=True) + "\n")
                    out.flush()
            print(f"круг {r} готов за {time.time() - started:.0f} с",
                  file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
