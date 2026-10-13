"""Сценарий чтения: кто не полез в разделы, которые не нужны ответу.

ДВА СОСТОЯНИЯ НАБОРА, И ПЕРВОЕ — КОНТРОЛЬ.

    all-readable   все разделы доступны: все пять плеч обязаны дойти до
                   ответа и получить ОДИН И ТОТ ЖЕ результат
    pruned-locked  разделы, которые запрос обязан отбросить, физически
                   нечитаемы

Без первого второе ничего не значит: плечо, падающее всегда, «избегает
лишней работы» ровно так же, как плечо, которое её действительно избегает.

ПОЧЕМУ ПРАВА, А НЕ СЧЁТЧИК БАЙТ. Подробности в gen_parquet и arm_data,
коротко: pandas читает системными вызовами, polars отображает файл в
память, и счётчик прочитанного у них означает разное. «Дошёл до ответа при
закрытых разделах» устроено одинаково для обоих и не зависит ни от способа
чтения, ни от страничного кэша.

ПОЧЕМУ ПЛЕЧИ ЗАПУСКАЮТСЯ ОТ НЕПРИВИЛЕГИРОВАННОГО ПОЛЬЗОВАТЕЛЯ. Для root
права доступа не действуют: он прочитает файл с режимом 000 и не заметит.
Замер под root показал бы, что все плечи прекрасно справляются, и это было
бы неправдой.
"""

import json
import os
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import gen_parquet  # noqa: E402

DATASET = Path(os.environ.get("DATASET", "/data"))
ARMS = ("pandas-full", "pandas-columns", "pandas-filters",
        "polars-eager", "polars-lazy")
RUNNER = str(Path(__file__).resolve().parent / "arm_data.py")
# Пользователь без прав: под ним режим 000 действительно закрывает файл.
UNPRIVILEGED = os.environ.get("UNPRIVILEGED", "nobody")


def partitions():
    return sorted(DATASET.glob("day_bucket=*"),
                  key=lambda p: int(p.name.split("=")[1]))


def set_permissions(state: str):
    """Готовит состояние набора и возвращает список закрытых разделов."""
    locked = []
    for part in partitions():
        bucket = int(part.name.split("=")[1])
        target = part / "part.parquet"
        if state == "pruned-locked" and bucket >= gen_parquet.BUCKET_TO:
            target.chmod(0o000)
            locked.append(bucket)
        else:
            target.chmod(0o644)
    return locked


def run_arm(arm: str):
    done = subprocess.run(
        ["runuser", "-u", UNPRIVILEGED, "--",
         "env", f"DATASET={DATASET}", f"BUCKET_TO={gen_parquet.BUCKET_TO}",
         "python", RUNNER, arm],
        capture_output=True, text=True)
    if done.returncode != 0 or not done.stdout.strip():
        raise RuntimeError(
            f"плечо {arm} не отчиталось: код {done.returncode}, "
            f"{(done.stderr or '').strip()[-200:]}")
    return json.loads(done.stdout.strip().splitlines()[-1])


def versions():
    import polars
    import pyarrow
    from importlib.metadata import version
    return {
        "python": sys.version.split()[0],
        "pandas": version("pandas"),
        "polars": polars.__version__,
        "pyarrow": pyarrow.__version__,
    }


def main():
    out_path = os.environ.get("OUT", "/fixtures/data.txt")
    dataset_digest = gen_parquet.main_into(DATASET)

    with open(out_path, "a", encoding="utf-8") as out:
        out.write(json.dumps({
            "kind": "env",
            "versions": versions(),
            "buckets": gen_parquet.BUCKETS,
            "bucket_to": gen_parquet.BUCKET_TO,
            "rows": gen_parquet.ROWS,
            "dataset_sha256": dataset_digest,
            "unprivileged_user": UNPRIVILEGED,
        }, ensure_ascii=False, sort_keys=True) + chr(10))

        for state in ("all-readable", "pruned-locked"):
            locked = set_permissions(state)
            for arm in ARMS:
                row = run_arm(arm)
                row.update({"kind": "run", "state": state,
                            "locked_buckets": locked})
                out.write(json.dumps(row, ensure_ascii=False, sort_keys=True)
                          + chr(10))
                out.flush()
                verdict = ("дошёл" if row["reached_answer"]
                           else f"упал: {row['error_type']}")
                print(f"{state:15} {arm:16} {verdict}", file=sys.stderr)

    # Права возвращаются на место: набор живёт внутри контейнера, но
    # оставлять его в закрытом виде значит ломать следующий прогон.
    set_permissions("all-readable")
    return 0


if __name__ == "__main__":
    sys.exit(main())
