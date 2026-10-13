"""Разбор чтения: кто не полез в разделы, не нужные ответу.

ЧТО ПРОВЕРЯЕТСЯ ДО ТОГО, как таблица что-то значит.

1. КОНТРОЛЬ ПОЛНЫЙ. При всех доступных разделах до ответа обязаны дойти
   все пять плеч. Плечо, падающее всегда, «избегает лишней работы» ровно
   так же, как то, что избегает её по делу.

2. КОНТРОЛЬ ЕДИНОГЛАСЕН. Пять плеч обязаны дать один и тот же ответ.
   Иначе сравнивать нечего: они решают разные задачи.

3. ОТБРОСИВШИЕ ЛИШНЕЕ ОТВЕЧАЮТ ТО ЖЕ САМОЕ. Ответ плеча, работавшего при
   закрытых разделах, обязан совпасть с контрольным. Это главная
   проверка всего сценария: проброс, меняющий ответ, — не оптимизация, а
   ошибка, и выглядела бы она как успех.

4. ПАДЕНИЕ ИМЕННО ПО ДОСТУПУ. Плечо, упавшее при закрытых разделах,
   доказывает, что полезло в них, лишь если отказано было в ДОСТУПЕ.
   Любая другая ошибка означала бы, что плечо сломалось по своей причине,
   и вывод о чтении из неё не следует.

5. СРАВНИВАТЬ ЕСТЬ ЧТО. Хотя бы одно плечо обязано отбросить лишнее и
   хотя бы одно — не отбросить. Таблица, где все одинаковы, не отвечает
   ни на один вопрос.
"""

import collections
import json
import os
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FIXTURE = Path(os.environ.get("FIXTURE", ROOT / "fixtures" / "data.txt"))

ARMS = ("pandas-full", "pandas-columns", "pandas-filters",
        "polars-eager", "polars-lazy")
CONTROL = "all-readable"
LOCKED = "pruned-locked"
ACCESS_ERROR = "PermissionError"

WHAT = {
    "pandas-full": "прочитать всё, отобрать в памяти",
    "pandas-columns": "названы столбцы, разделы — нет",
    "pandas-filters": "отбор передан читателю файлов",
    "polars-eager": "прочитать всё, отобрать в памяти",
    "polars-lazy": "план строится целиком, отбор доезжает до файлов",
}


def rows():
    return [json.loads(line) for line in
            FIXTURE.read_text(encoding="utf-8").splitlines()
            if line.strip() and not line.startswith("#")]


def verdict(runs):
    problems = []
    by = {(r["state"], r["arm"]): r for r in runs}

    missing = [(s, a) for s in (CONTROL, LOCKED) for a in ARMS
               if (s, a) not in by]
    if missing:
        problems.append(
            "в фикстуре нет клеток: "
            + ", ".join(f"{s}/{a}" for s, a in missing))
        return problems

    failed_control = [a for a in ARMS if not by[(CONTROL, a)]["reached_answer"]]
    if failed_control:
        problems.append(
            f"при всех доступных разделах не дошли до ответа: "
            f"{', '.join(failed_control)} — плечо, падающее всегда, ничего "
            "не доказывает закрытыми разделами")
        return problems

    control_hashes = {by[(CONTROL, a)]["result_sha256"] for a in ARMS}
    if len(control_hashes) != 1:
        problems.append(
            f"контрольные ответы РАЗНЫЕ ({len(control_hashes)} хешей) — "
            "плечи решают разные задачи, и сравнивать их нельзя")
        return problems
    expected = control_hashes.pop()

    pruners = [a for a in ARMS if by[(LOCKED, a)]["reached_answer"]]
    readers = [a for a in ARMS if not by[(LOCKED, a)]["reached_answer"]]
    if not pruners:
        problems.append(
            "при закрытых разделах не дошло НИ ОДНО плечо — либо закрыто "
            "лишнее, либо проброс не работает ни у кого")
    if not readers:
        problems.append(
            "при закрытых разделах дошли ВСЕ — закрытые разделы никому не "
            "понадобились, и сценарий ничего не различает")

    for arm in pruners:
        got = by[(LOCKED, arm)]["result_sha256"]
        if got != expected:
            problems.append(
                f"{arm}: отбросив разделы, ответил ИНАЧЕ ({got} против "
                f"{expected}) — это не оптимизация, а потеря данных")

    for arm in readers:
        kind = by[(LOCKED, arm)]["error_type"]
        if kind != ACCESS_ERROR:
            problems.append(
                f"{arm}: упал с {kind}, а не с отказом в доступе — из такой "
                "ошибки не следует, что плечо читало закрытые разделы")

    control_locked = by[(CONTROL, ARMS[0])].get("locked_buckets")
    if control_locked:
        problems.append(
            f"в контрольном состоянии закрыты разделы {control_locked} — "
            "тогда это не контроль")
    if not by[(LOCKED, ARMS[0])].get("locked_buckets"):
        problems.append(
            "в состоянии pruned-locked не закрыт ни один раздел — "
            "проверять нечего")
    return problems


def main():
    if not FIXTURE.exists():
        print(f"нет фикстуры {FIXTURE}", file=sys.stderr)
        return 2
    data = rows()
    runs = [r for r in data if r.get("kind") == "run"]
    env = next((r for r in data if r.get("kind") == "env"), None)
    if not runs:
        print("в фикстуре нет прогонов", file=sys.stderr)
        return 2

    problems = verdict(runs)
    by = {(r["state"], r["arm"]): r for r in runs}

    print("=" * 78)
    print("ПРИЗНАКИ НЕГОДНОГО ЗАМЕРА")
    print("=" * 78)
    print()
    reached = sum(1 for a in ARMS if by.get((CONTROL, a), {}).get(
        "reached_answer"))
    print(f"1) при всех доступных разделах дошли до ответа: {reached} из "
          f"{len(ARMS)}")
    hashes = sorted({by[(CONTROL, a)]["result_sha256"] for a in ARMS
                     if (CONTROL, a) in by})
    print(f"2) различных контрольных ответов: {len(hashes)} (обязан быть 1) "
          f"{hashes}")
    locked = by.get((LOCKED, ARMS[0]), {}).get("locked_buckets") or []
    print(f"3) закрытых разделов в основном состоянии: {len(locked)}")
    if env:
        print(f"4) набор: {env['rows']} строк, {env['buckets']} разделов, "
              f"хеш {env['dataset_sha256']}, запрос берёт до "
              f"{env['bucket_to']}")

    print()
    if problems:
        print("ВЕРДИКТ: НЕГОДЕН")
        for item in problems:
            print("  - " + item)
        return 2
    print("ВЕРДИКТ: замер годен")

    print()
    print("=" * 78)
    print("КТО НЕ ПОЛЕЗ В ЛИШНИЕ РАЗДЕЛЫ")
    print("=" * 78)
    print()
    print("  Разделы, не нужные ответу, сделаны нечитаемыми. Дошедшее до")
    print("  ответа плечо тем самым доказало, что в них не заглядывало.")
    print()
    print(f"  {'плечо':<16}{'всё доступно':>16}{'лишнее закрыто':>18}"
          f"   что делает")
    for arm in ARMS:
        control = "дошёл" if by[(CONTROL, arm)]["reached_answer"] else "упал"
        row = by[(LOCKED, arm)]
        if row["reached_answer"]:
            locked_word = "дошёл"
        else:
            locked_word = "отказ в доступе"
        print(f"  {arm:<16}{control:>16}{locked_word:>18}   {WHAT[arm]}")

    print()
    print("  Ответ у всех дошедших один и тот же — и в контроле, и при")
    print("  закрытых разделах. Проброс, меняющий ответ, был бы не")
    print("  оптимизацией, а потерей данных, и выглядел бы как успех.")

    print()
    print("=" * 78)
    print("ЧТО ИМЕННО СКАЗАЛ ДВИЖОК, КОГДА ПОЛЕЗ В ЗАКРЫТОЕ")
    print("=" * 78)
    for arm in ARMS:
        row = by[(LOCKED, arm)]
        if row["reached_answer"]:
            continue
        print()
        print(f"  --- {arm}")
        print(f"    {row['error']}")

    if env:
        print()
        print(f"  Снято на python {env['versions']['python']}, "
              f"pandas {env['versions']['pandas']}, "
              f"polars {env['versions']['polars']}, "
              f"pyarrow {env['versions']['pyarrow']}.")
        print(f"  Плечи выполняются от пользователя "
              f"{env['unprivileged_user']}: для root права доступа не")
        print("  действуют, и замер под ним показал бы, что все справляются.")

    print()
    print("  ГРАНИЦА ВЫВОДА. Здесь измерено, ЧИТАЛ ли движок лишнее, а не")
    print("  сколько времени он потратил. Плечо, не полезшее в закрытые")
    print("  разделы, не обязано быть быстрее — оно именно что не сделало")
    print("  работу. Про скорость этот стенд не говорит ничего.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
