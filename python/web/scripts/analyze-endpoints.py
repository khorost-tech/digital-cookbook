"""Разбор матрицы эндпоинтов: разложение, а не рейтинг.

Скрипт намеренно НЕ печатает «победителя». Он печатает, как разрыв между
целями меняется с ростом объёма полезной работы: велик на пустой ручке,
меньше на валидации с сериализацией, почти исчезает там, где есть поход в
базу. Если так — «какой фреймворк быстрее» действительно неправильный
вопрос, и это видно из чисел, а не заявлено.

Коды возврата:

* 0 — замер годен;
* 2 — есть прогоны с ошибками или цели отдали разные тела;
* 3 — на /wait превышен теоретический потолок, то есть ожидание не состоялось.

Неустойчивые конфигурации (разброс между кругами выше 1.5x) помечаются, но
код возврата не меняют: такая конфигурация непригодна для ранжирования, а
остальные данные годны.
"""

import json
import os
import sys
from pathlib import Path

FIX = Path(os.environ.get("FIXTURES_DIR",
                          Path(__file__).resolve().parent.parent / "fixtures"))

ORDER = ["raw-asgi", "fastapi", "django", "raw-wsgi", "flask"]
ASGI = {"raw-asgi", "fastapi", "django"}
ENDPOINTS = ("ping", "echo", "wait")
WAIT_SECONDS = 0.02
SPREAD_LIMIT = 1.5


MAX_IDLE = 0.05


def rows():
    path = FIX / "endpoints.txt"
    return [json.loads(l) for l in path.read_text(encoding="utf-8").splitlines()
            if l.startswith("{")]


def med(xs):
    s = sorted(xs)
    n = len(s)
    return s[n // 2] if n % 2 else (s[n // 2 - 1] + s[n // 2]) / 2


def paired_shares(data, target, floor_name, endpoint, conc):
    """Покруговые отношения цели к своему полу, в процентах.

    Именно эти числа публикуются в статье, поэтому считаются они здесь, а не
    вручную. Отношение медиан — другая величина: на этом стенде расхождение
    доходило до двух процентных пунктов (74 против 72), и headline-таблица
    переставала воспроизводиться публичной командой.
    """
    by_round = {}
    for r in data:
        if r["endpoint"] == endpoint and r["concurrency"] == conc:
            if r["framework"] in (target, floor_name):
                by_round.setdefault(r["round"], {})[r["framework"]] = r["rps"]
    out = []
    for pair in by_round.values():
        if target in pair and floor_name in pair and pair[floor_name]:
            out.append(100 * pair[target] / pair[floor_name])
    return sorted(out)


def pick(data, target, endpoint, conc, field="rps"):
    v = [r[field] for r in data
         if r["framework"] == target and r["endpoint"] == endpoint
         and r["concurrency"] == conc]
    return med(v) if v else None


def main():
    data = rows()
    if not data:
        print("ОТКАЗ: фикстура пуста", file=sys.stderr)
        return 2

    targets = [t for t in ORDER if any(r["framework"] == t for r in data)]
    concurrency = sorted({r["concurrency"] for r in data})
    problems = []

    print("=" * 78)
    print("ПРИЗНАКИ НЕГОДНОГО ЗАМЕРА")
    print("=" * 78)
    print()

    bad = [r for r in data if r["errors"]]
    print(f"1) прогонов с ошибками: {len(bad)} из {len(data)}")
    if bad:
        total = sum(r["errors"] for r in bad)
        problems.append(f"ошибок {total} в {len(bad)} прогонах — это не результат")

    # Сравнивать хеши МЕЖДУ эндпоинтами нельзя: /ping отдаёт другое тело,
    # чем /echo и /wait, и это по построению. Равенство проверяется внутри
    # эндпоинта — там все цели обязаны совпасть.
    print("2) совпадение тел внутри эндпоинта:")
    for ep in ENDPOINTS:
        hs = {r["body_sha256"] for r in data if r["endpoint"] == ep}
        if not hs:
            continue
        print(f"   /{ep:<5} различных хешей: {len(hs)}")
        if len(hs) != 1:
            problems.append(f"/{ep}: цели отдали разные тела ({len(hs)} хешей) "
                            "— равенство работы нарушено под нагрузкой")

    over = []
    for r in data:
        if r["endpoint"] != "wait":
            continue
        ceiling = r["concurrency"] / WAIT_SECONDS
        if r["rps"] > ceiling * 1.05:
            over.append(f"{r['framework']}/c={r['concurrency']}: "
                        f"{r['rps']:.0f} > потолка {ceiling:.0f}")
    print(f"3) превышений потолка на /wait: {len(over)}")
    for o in over:
        print("   " + o)

    # Простой нагрузчика: круг, в котором соединения подолгу не ждали
    # ответов, мерил заминку клиента, а не цель. Нагрузчик такие круги
    # перезапускает, но если попытки исчерпаны — строка доезжает сюда, и
    # молча усреднять её нельзя.
    # Отсутствие поля — не «простоя нет», а «фикстура снята нагрузчиком без
    # контроля простоя». Разбор 23.08 прошёл по такой фикстуре и выдал
    # «0 из 368»: прогон в тот раз вообще не состоялся, а разбор шёл по
    # файлу от прерванного. Пропуск поля обязан быть отказом.
    stale = [r for r in data if "idle_fraction" not in r]
    if stale:
        problems.append(f"{len(stale)} строк без idle_fraction — фикстура "
                        "снята старым нагрузчиком, разбор идёт не по этому "
                        "прогону")
    idle_bad = [r for r in data if r.get("idle_fraction", 0.0) > MAX_IDLE]
    retried = sum(len(r.get("idle_discarded", [])) for r in data)
    print(f"4) кругов с простоем нагрузчика выше {MAX_IDLE:.0%}: "
          f"{len(idle_bad)} из {len(data)}; перезапущено ранее: {retried}")
    for r in idle_bad[:5]:
        print(f"   {r['framework']}/{r['endpoint']}/c={r['concurrency']}: "
              f"простой {r['idle_fraction']:.1%}")
    if idle_bad:
        problems.append(f"{len(idle_bad)} кругов с простоем нагрузчика — "
                        "они мерили клиента, а не цель")

    print()
    if problems:
        print("ВЕРДИКТ: НЕГОДЕН")
        for p in problems:
            print("  - " + p)
        return 2
    if over:
        print("ВЕРДИКТ: НЕГОДЕН — ожидание на /wait не состоялось")
        return 3
    print("ВЕРДИКТ: замер годен")

    unstable = []
    for t in targets:
        for ep in ENDPOINTS:
            for c in concurrency:
                v = [r["rps"] for r in data if r["framework"] == t
                     and r["endpoint"] == ep and r["concurrency"] == c]
                if len(v) > 1 and max(v) / min(v) > SPREAD_LIMIT:
                    unstable.append(f"{t}/{ep}/c={c}: {max(v) / min(v):.2f}x")

    for ep in ENDPOINTS:
        print()
        print("=" * 78)
        print(f"/{ep}: пропускная способность, rps (медианы кругов)")
        print("=" * 78)
        print(f"  {'цель':<10}" + "".join(f"{('c=' + str(c)):>12}"
                                          for c in concurrency))
        for t in targets:
            line = f"  {t:<10}"
            for c in concurrency:
                v = pick(data, t, ep, c)
                line += f"{v:>12.1f}" if v else f"{'—':>12}"
            print(line)

    print()
    print("=" * 78)
    print("РАЗЛОЖЕНИЕ: надбавка фреймворка над СВОИМ полом")
    print("=" * 78)
    print()
    print("  Сравнение с чужим сервером было бы сравнением серверов, поэтому")
    print("  каждый фреймворк сопоставляется со своим голым приложением:")
    print("  FastAPI и Django с raw-asgi, Flask с raw-wsgi.")
    print()
    print("  Доля считается ПОКРУГОВО: в каждом круге отношение фреймворка к")
    print("  своему полу, и уже из этих отношений берётся медиана и размах.")
    print("  Отношение медиан дало бы другое число, и оно хуже: фреймворк и")
    print("  его контроль меряются в одном круге, поэтому общее состояние")
    print("  машины из парного отношения частично сокращается, а из")
    print("  отношения медиан — нет. Размах здесь важнее медианы: на быстрых")
    print("  ручках он широкий, и точность до процента там мнимая.")
    print()
    top = max(concurrency)
    print(f"  при одновременности {top}:")
    print(f"  {'фреймворк':<10}{'/ping':>22}{'/echo':>22}{'/wait':>22}")
    for t in targets:
        if t.startswith("raw-"):
            continue
        floor_name = "raw-asgi" if t in ASGI else "raw-wsgi"
        line = f"  {t:<10}"
        for ep in ENDPOINTS:
            sh = paired_shares(data, t, floor_name, ep, top)
            if sh:
                cell = f"{med(sh):.0f}% ({min(sh):.0f}-{max(sh):.0f})"
                line += f"{cell:>22}"
            else:
                line += f"{'—':>22}"
        print(line)
    print()
    print("  Читается как доля от пола: 100% — фреймворк не стоит ничего")
    print("  сверх своего сервера, 50% — вдвое медленнее голого приложения.")
    print("  В скобках — наблюдённый размах по кругам.")

    print()
    print("=" * 78)
    print("ГЛАВНЫЙ ВОПРОС: сохраняется ли разрыв при росте полезной работы")
    print("=" * 78)
    print()
    for ep in ENDPOINTS:
        vals = [(t, pick(data, t, ep, top)) for t in targets]
        vals = [(t, v) for t, v in vals if v]
        if len(vals) < 2:
            continue
        hi = max(v for _, v in vals)
        lo = min(v for _, v in vals)
        best = max(vals, key=lambda x: x[1])[0]
        worst = min(vals, key=lambda x: x[1])[0]
        print(f"  /{ep:<5} разрыв между крайними целями: {hi / lo:5.2f}x "
              f"({best} против {worst})")
    print()
    print("  Если разрыв велик на /ping и мал на /wait — цена фреймворка")
    print("  тонет в реальной работе, и вопрос «кто быстрее» поставлен неверно.")
    print("  Если разрыв держится везде — вывод обратный, и статья обязана")
    print("  сказать именно это.")

    if unstable:
        print()
        print("=" * 78)
        print("НЕПРИГОДНО ДЛЯ РАНЖИРОВАНИЯ (разброс между кругами выше "
              f"{SPREAD_LIMIT}x)")
        print("=" * 78)
        for u in unstable:
            print("  " + u)
        print()
        print("  По этим конфигурациям сравнивать цели нельзя.")

    return 0


if __name__ == "__main__":
    sys.exit(main())
