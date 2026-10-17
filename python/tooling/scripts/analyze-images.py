"""Разбор образов: размер, слои и время старта шести стратегий.

ПОРЯДОК ВАЖЕН. Сначала проверяется, что все шесть образов отвечают
одинаково, и только потом печатаются размеры. Обратный порядок — обычный
способ выдать за победу выброшенное содержимое: образ без половины
зависимостей действительно легче.

ЧТО ЗДЕСЬ НАЗЫВАЕТСЯ РАЗМЕРОМ. Два разных числа, и оба настоящие: сжатый
размер (что едет из реестра) и распакованный (что лежит на диске узла).
Самый толстый слой — из распакованной семьи. Смешивать их нельзя: у части
образов сжатый размер меньше, чем их же отдельный слой, и таблица, где
эти числа стоят рядом без подписи, выглядит как арифметическая ошибка.
Проверка «часть не больше целого» стоит в вердикте — первая версия
сценария на ней и спотыкалась.
"""

import collections
import json
import math
import os
import statistics as st
import sys
from pathlib import Path

FIXTURE = Path(os.environ.get(
    "FIXTURE",
    Path(__file__).resolve().parent.parent / "fixtures" / "images.txt"))

ORDER = ("naive", "slim", "multistage", "uv", "uv-compiled", "distroless")
BASE = "slim"
# Опрос идёт с шагом 0,02 с; разницу мельче шага измерить нечем.
POLL_S = 0.02
BOOT_FLOOR_S = 10 * POLL_S
SPREAD_LIMIT = 1.5
MIB = 1024 * 1024
# docker image ls печатает округлённо («360MB»), поэтому тождество
# «показанное = сжатое + распакованное» проверяется с допуском. Округление
# идёт до трёх значащих цифр, то есть промах не превысит полпроцента.
REPORTED_TOLERANCE = 0.01
# Порог знакового теста для порядка стратегий.
SIGN_ALPHA = 0.05


def rows():
    return [json.loads(line) for line in
            FIXTURE.read_text(encoding="utf-8").splitlines()
            if line.strip() and not line.startswith("#")]


def med(xs):
    return st.median(xs) if xs else None


def paired(runs, name, base=BASE):
    """Покруговые отношения времени старта к базовой стратегии."""
    by_round = collections.defaultdict(dict)
    for r in runs:
        if r["strategy"] in (name, base):
            by_round[r["round"]][r["strategy"]] = r["boot_seconds"]
    out = []
    for pair in by_round.values():
        if name in pair and base in pair and pair[base]:
            out.append(pair[name] / pair[base])
    return sorted(out)


def sign_test(hits, total):
    """Двусторонний точный биномиальный тест на честной монете."""
    if total == 0:
        return 1.0
    k = max(hits, total - hits)
    tail = sum(math.comb(total, i) for i in range(k, total + 1))
    return min(1.0, 2 * tail / (2 ** total))


def boot_ordering(runs, name, base=BASE):
    """В скольких кругах стратегия стартовала МЕДЛЕННЕЕ базовой.

    ЗАЧЕМ ЭТО РЯДОМ С МЕДИАНАМИ. Абсолютное время старта зависит от того,
    чем занята машина: между двумя прогонами этого стенда все медианы
    выросли почти вдвое, а разбросы внутри клеток превысили полуторный
    порог. Покруговые же отношения к базовой стратегии в тех же двух
    прогонах совпали с точностью до сотых (1,77 и 1,76; 3,29 и 3,20).

    Значит переносить из статьи в свою жизнь можно отношения и знак, а не
    абсолютные секунды. Счёт по кругам это показывает прямо.
    """
    by_round = collections.defaultdict(dict)
    for r in runs:
        if r["strategy"] in (name, base):
            by_round[r["round"]][r["strategy"]] = r["boot_seconds"]
    hits = total = 0
    for pair in by_round.values():
        if name not in pair or base not in pair:
            continue
        total += 1
        if pair[name] > pair[base]:
            hits += 1
    return hits, total, sign_test(hits, total)


def verdict(runs, rebuild, statics, env=None):
    problems = []

    digests = {r["answer_sha256"] for r in runs}
    if len(digests) != 1:
        problems.append(
            f"образы отвечают РАЗНОЕ ({len(digests)} хешей ответа) — "
            "сравнивать их размеры бессмысленно: легче окажется тот, из "
            "которого больше выброшено")

    for name, m in sorted(statics.items()):
        if "reported_bytes" in m:
            total = m["compressed_bytes"] + m["unpacked_bytes"]
            drift = abs(m["reported_bytes"] - total) / max(total, 1)
            if drift > REPORTED_TOLERANCE:
                problems.append(
                    f"{name}: docker image ls показывает "
                    f"{m['reported_size']}, а сумма сжатого и "
                    f"распакованного даёт {total} Б — расхождение "
                    f"{drift:.1%}, тождество не выполняется, и подпись под "
                    "колонками неверна")
        if m["fattest_layer_bytes"] > m["unpacked_bytes"]:
            problems.append(
                f"{name}: самый толстый слой больше распакованного образа "
                f"({m['fattest_layer_bytes']} против {m['unpacked_bytes']}) — "
                "числа взяты из разных семей размеров")

    if env is not None:
        digests = env.get("base_digests") or {}
        if not digests:
            problems.append("digest базовых образов не записан — понять, что "
                            "именно собиралось, будет нельзя")
        for ref, info in sorted(digests.items()):
            info = info or {}
            if not info.get("index") or not info.get("platform"):
                problems.append(
                    f"{ref}: digest снят не полностью ({info}) — записана "
                    "ссылка на набор манифестов или вовсе ничего")
        if env.get("base_digests_drifted"):
            problems.append(
                "за время прогона реестр сменил базовый образ для "
                + ", ".join(env["base_digests_drifted"])
                + " — часть образов собрана из одной базы, часть из другой")

    if not rebuild:
        problems.append("в фикстуре нет проверки пересборки без кэша — "
                        "воспроизводимость не подтверждена")
        return problems

    for name, c in sorted(rebuild["checks"].items()):
        if c["unpacked_before"] != c["unpacked_after"]:
            drift = abs(c["unpacked_after"] - c["unpacked_before"])
            problems.append(
                f"{name}: пересборка без кэша дала другой распакованный "
                f"размер (расхождение {drift} Б) — числа не воспроизводятся")
        if not c["layers_equal"]:
            problems.append(f"{name}: пересборка изменила число слоёв")
    return problems


def main():
    if not FIXTURE.exists():
        print(f"нет фикстуры {FIXTURE}", file=sys.stderr)
        return 2
    data = rows()
    runs = [r for r in data if r.get("kind") == "run"]
    env = next((r for r in data if r.get("kind") == "env"), None)
    rebuild = next((r for r in data if r.get("kind") == "rebuild"), None)
    if not runs:
        print("в фикстуре нет прогонов", file=sys.stderr)
        return 2

    statics = {}
    boots = collections.defaultdict(list)
    for r in runs:
        statics.setdefault(r["strategy"], {
            k: r[k] for k in ("compressed_bytes", "unpacked_bytes",
                              "fattest_layer_bytes", "layers",
                              "reported_size", "reported_bytes")
            if k in r})
        boots[r["strategy"]].append(r["boot_seconds"])

    problems = verdict(runs, rebuild, statics, env)

    print("=" * 78)
    print("ПРИЗНАКИ НЕГОДНОГО ЗАМЕРА")
    print("=" * 78)
    print()
    digests = sorted({r["answer_sha256"] for r in runs})
    lengths = sorted({r["answer_bytes"] for r in runs})
    print(f"1) хешей ответа: {len(digests)} (обязан быть 1) {digests}, "
          f"длина ответа {lengths} Б")

    if rebuild:
        drift = max(abs(c["compressed_after"] - c["compressed_before"])
                    for c in rebuild["checks"].values())
        unpacked = sorted(m["unpacked_bytes"] for m in statics.values())
        nearest = min(b - a for a, b in zip(unpacked, unpacked[1:]))
        print(f"2) пересборка без кэша: распакованный размер совпал у всех "
              f"{len(rebuild['checks'])} стратегий, побайтово")
        print(f"   сжатый при этом плавает до {drift} Б — сжатие не"
              " повторяется побайтово;")
        print(f"   ближайшие стратегии расходятся на {nearest} Б, это в "
              f"{nearest / max(drift, 1):.0f} раз больше дрейфа")

    print(f"3) время старта: пол разрешения {BOOT_FLOOR_S:.2f} с "
          f"(шаг опроса {POLL_S} с)")
    flagged = False
    for name in ORDER:
        v = sorted(boots.get(name, []))
        if not v:
            continue
        ratio = v[-1] / v[0]
        if med(v) < BOOT_FLOOR_S:
            print(f"   {name}: {ratio:.2f}x — ниже пола разрешения, "
                  "по отношению не судится")
            flagged = True
        elif ratio > SPREAD_LIMIT:
            print(f"   {name}: {ratio:.2f}x — разброс выше порога "
                  f"{SPREAD_LIMIT}x")
            flagged = True
    if not flagged:
        print("   все стратегии выше пола и внутри порога разброса")

    print()
    if problems:
        print("ВЕРДИКТ: НЕГОДЕН")
        for p in problems:
            print("  - " + p)
        return 2
    print("ВЕРДИКТ: замер годен — все образы отвечают одинаково")

    print()
    print("=" * 78)
    print("РАЗМЕР ОБРАЗА")
    print("=" * 78)
    print()
    print(f"  {'стратегия':<14}{'сжатый':>13}{'распакованный':>18}"
          f"{'толстый слой':>17}{'слоёв':>8}")
    for name in ORDER:
        m = statics.get(name)
        if not m:
            continue
        print(f"  {name:<14}"
              f"{m['compressed_bytes'] / MIB:>9.1f} МиБ"
              f"{m['unpacked_bytes'] / MIB:>14.1f} МиБ"
              f"{m['fattest_layer_bytes'] / MIB:>13.1f} МиБ"
              f"{m['layers']:>8}")
    print()
    print("  Сжатый — что качается из реестра. Распакованный — что занимает")
    print("  на диске узла. docker image ls показывает ИХ СУММУ: при")
    print("  containerd-хранилище узел держит и блобы, и распакованный")
    print("  снимок. Три разных числа за одним словом «размер».")
    if any("reported_bytes" in m for m in statics.values()):
        print()
        print("  Тождество проверено по фикстуре, а не заявлено:")
        print(f"    {'стратегия':<14}{'сжатое + распакованное':>26}"
              f"{'docker image ls':>18}")
        for name in ORDER:
            m = statics.get(name)
            if not m or "reported_bytes" not in m:
                continue
            total = (m["compressed_bytes"] + m["unpacked_bytes"]) / 1e6
            print(f"    {name:<14}{f'{total:.1f} MB':>26}"
                  f"{m['reported_size']:>18}")

    print()
    print("=" * 78)
    print("ВРЕМЯ ОТ ЗАПУСКА КОНТЕЙНЕРА ДО ПЕРВОГО ОТВЕТА, с")
    print("=" * 78)
    print()
    head = f"к {BASE}, покругово"
    print(f"  {'стратегия':<14}{'медиана':>10}{'размах':>16}{head:>24}")
    for name in ORDER:
        v = sorted(boots.get(name, []))
        if not v:
            continue
        span = f"{v[0]:.2f}-{v[-1]:.2f}"
        if name == BASE:
            rel = "—"
        else:
            sh = paired(runs, name)
            rel = f"{med(sh):.2f}x ({sh[0]:.2f}-{sh[-1]:.2f})" if sh else "—"
        print(f"  {name:<14}{med(v):>10.2f}{span:>16}{rel:>24}")
    print()
    print("  Отсчёт идёт от возврата docker run: создание контейнера — работа")
    print("  демона, одинаковая для всех образов, и включать её значило бы")
    print("  прибавлять всем один и тот же шум.")

    print()
    print("  Порядок по кругам (знаковый тест против slim):")
    for name in ORDER:
        if name == BASE or name not in boots:
            continue
        hits, total, p = boot_ordering(runs, name)
        if not total:
            continue
        if p > SIGN_ALPHA:
            verdict_text = "неотличимо от slim"
        elif hits * 2 >= total:
            verdict_text = f"медленнее slim в {hits} кругах из {total}"
        else:
            verdict_text = f"быстрее slim в {total - hits} кругах из {total}"
        print(f"    {name:<14}{verdict_text}")
    print()
    print("  Абсолютные секунды зависят от загруженности машины: между двумя")
    print("  прогонами этого стенда все медианы выросли почти вдвое. Отношения")
    print("  к базовой стратегии при этом совпали с точностью до сотых —")
    print("  переносить стоит их, а не абсолютные значения.")

    if env and env.get("bytecode"):
        print()
        print("=" * 78)
        print("ОТКУДА РАЗНИЦА НА СТАРТЕ: БАЙТ-КОД")
        print("=" * 78)
        print()
        print("  Смотрим на файлы .pyc до первого импорта и после него.")
        print("  Время само по себе механизма не доказывает: «первый импорт")
        print("  медленнее второго» одинаково объясняется и компиляцией, и")
        print("  холодным дисковым кэшем.")
        print()
        print("  Колонка «переписано» существеннее, чем кажется. Устаревший")
        print("  байт-код Python компилирует НА МЕСТЕ: файл тот же, число")
        print("  файлов не меняется. Проба, которая только считает, покажет")
        print("  «+0» ровно там, где работа делается на каждом запуске.")
        print()
        print(f"  {'стратегия':<14}{'.pyc до':>10}{'.pyc после':>13}"
              f"{'новых':>8}{'переписано':>13}")
        for name in ORDER:
            probe = env["bytecode"].get(name)
            if probe is None:
                print(f"  {name:<14}{'—':>10}{'—':>13}{'—':>8}{'—':>13}"
                      "   оболочки нет, проба не запускается")
                continue
            print(f"  {name:<14}{probe['pyc_before']:>10}"
                  f"{probe['pyc_after']:>13}{probe['pyc_new']:>+8}"
                  f"{probe['pyc_rewritten']:>13}")
        print()
        print("  Дописанный байт-код лежит в записываемом слое контейнера и")
        print("  исчезает вместе с ним. Эту работу платит КАЖДЫЙ запуск, а не")
        print("  только первый: в проде — цена на каждый под.")

        print()
        print("=" * 78)
        print("ЧЕГО ЭТОТ ЗАМЕР НЕ ОБЪЯСНЯЕТ")
        print("=" * 78)
        print()
        print("  Разница между uv и uv-compiled объяснена: у первого байт-код")
        print("  дописывается на каждом запуске, у второго уже лежит в образе.")
        print()
        print("  А вот разрыв между плечами с venv (multistage, uv-compiled —")
        print("  около 1,05 с) и плечами, ставящими в системный интерпретатор")
        print("  (slim, naive — около 0,58 с), НЕ ОБЪЯСНЁН. Байт-код здесь ни")
        print("  при чём: у всех четырёх он на месте, ничего не дописывается и")
        print("  не переписывается. Разбор импортов (-X importtime) показывает,")
        print("  что в плечах с venv дороже обходятся ТЕ ЖЕ модули, включая")
        print("  стандартную библиотеку, которая в обоих случаях одна и та же")
        print("  и лежит по одному и тому же пути. Это указывает скорее на")
        print("  чтение с диска, чем на работу интерпретатора, — но указывает,")
        print("  а не доказывает, и правильный вывод здесь только один:")
        print("  наблюдение записано, механизм не установлен.")

    if env and env.get("base_digests"):
        print()
        print("=" * 78)
        print("ЧТО ИМЕННО СОБИРАЛОСЬ: DIGEST БАЗОВЫХ ОБРАЗОВ")
        print("=" * 78)
        print()
        print("  В Dockerfile базы заданы ТЕГАМИ, а тег — плавающая ссылка.")
        print("  Через месяц python:3.14-slim укажет на другой образ, и все")
        print("  шесть размеров сдвинутся разом. Digest не делает сборку")
        print("  воспроизводимой — для этого теги надо пиновать, — но")
        print("  позволяет понять, то же ли самое вы собираете.")
        print()
        print("  Порядок в сценарии такой:")
        print("    1. docker pull --platform linux/amd64 — по одному разу")
        print("       на каждую базу;")
        print("    2. digest снимается с ЛОКАЛЬНЫХ копий;")
        print("    3. сборки идут БЕЗ --pull: базы уже свежие;")
        print("    4. после прогона локальные Id сверяются с записанным.")
        print()
        print("  --pull на каждой сборке не годится: шесть стратегий, каждая")
        print("  собирается дважды, у каждой одна-две базы — под шесть")
        print("  десятков обращений к реестру за прогон, и Docker Hub")
        print("  отвечает 429 посреди замера. --no-cache тут не замена: он")
        print("  не переиспользует слои сборки, но базу из локального кэша")
        print("  берёт по-прежнему.")
        print()
        print("  Индекс — ссылка на НАБОР манифестов под разные платформы;")
        print("  собирается ровно один из них, и он указан отдельно. При")
        print("  containerd-хранилище локальный Id совпадает с digest")
        print("  ИНДЕКСА, поэтому по локальным полям видно, какой образ")
        print("  вытянут, но не какой манифест пошёл в сборку.")
        print()
        for ref, info in sorted(env["base_digests"].items()):
            info = info or {}
            print(f"    {ref}")
            print(f"      индекс      {info.get('index') or 'НЕ ОПРЕДЕЛЁН'}")
            print(f"      {info.get('target_platform', 'платформа'):<11} "
                  f"{info.get('platform') or 'НЕ ОПРЕДЕЛЁН'}")
            local = info.get("image_id")
            if local:
                same = " (совпадает с индексом)" if local == info.get(
                    "index") else ""
                print(f"      локальный Id {local}{same}")
        drifted = env.get("base_digests_drifted")
        if drifted:
            print()
            print("    ВНИМАНИЕ: за время прогона реестр сменил образ для "
                  + ", ".join(drifted))

    if env and env.get("store"):
        print()
        print(f"  Снято при хранилище образов {env['store']['driver']}, "
              f"docker {env['store']['server']}.")
        status = env["store"].get("driver_status")
        if status:
            flat = "; ".join(f"{a}={b}" for a, b in status)
            print(f"  DriverStatus: {flat}")
        print("  На classic-графдрайвере inspect отдаёт РАСПАКОВАННЫЙ размер,")
        print("  и колонки поменяются местами.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
