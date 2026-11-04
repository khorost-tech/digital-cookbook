"""Разбор оси упаковки: прогон против ожиданий, записанных до прогона.

Разбор НЕ содержит ожиданий — они лежат в expected.json. Здесь только
сверка и печать, плюс единственная защита, без которой печатать таблицу
нельзя: если контроли (glibc-dynamic, musl-static) не резолвят, значит в
контейнере нет сети, и отказ старой статики нельзя отличить от отказа
сети. В этом случае разбор ОБЯЗАН остановиться, а не напечатать таблицу.
"""

import io
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import read_fixture, read_measurements
from scripts import link as linklib  # noqa: E402
from scripts.console import force_utf8  # noqa: E402

# Потоки переключаются в UTF-8 первым делом: на консоли cp1251
# печать «≠» роняла разбор с UnicodeEncodeError. Найдено прогоном
# на втором хосте.
force_utf8()

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.environ.get(
    "PACKAGE_FIXTURE", os.path.join(HERE, "fixtures", "package.jsonl"))
LINK_FIXTURE = os.environ.get(
    "LINK_FIXTURE", os.path.join(HERE, "fixtures", "link.jsonl"))

NAMES = ["glibc-dynamic", "glibc-static", "glibc-static-old",
         "musl-static", "musl-no-resolv", "distroless"]
CONTROLS = ("glibc-dynamic", "musl-static")

MARK = {"resolved": "резолвит", "refused": "ОТКАЗ", "error": "СБОЙ ПРОБЫ"}


def load_json(name):
    with io.open(os.path.join(HERE, name), encoding="utf-8") as handle:
        return json.loads(handle.read())


def _by_variant_probe(rows):
    out = {}
    for row in rows:
        if row["kind"] != "package":
            continue
        out[(row["variant"], row["probe"])] = row
    return out


def check_controls(by_vp):
    """Останавливает разбор, если контроли не зелёные.

    Без этого шага таблица напечаталась бы поверх недействительных
    данных: отказ старой статики выглядел бы как находка оси, будучи на
    деле обрывом сети в контейнере.
    """
    broken = []
    for name in CONTROLS:
        row = by_vp.get((name, "resolve"))
        if row is None:
            broken.append((name, "нет строки resolve"))
        elif row["outcome"] != "resolved":
            broken.append((name, "outcome=%r detail=%r"
                           % (row["outcome"], row.get("detail"))))
    if broken:
        lines = ["КОНТРОЛИ НЕ ЗЕЛЁНЫЕ — ОСТАЛЬНЫЕ СТРОКИ ОСИ НЕДЕЙСТВИТЕЛЬНЫ.",
                 "",
                 "Контроль обязан резолвить в ЛЮБОМ рабочем прогоне. Если он",
                 "не резолвит, в контейнере нет сети (или сломан сам стенд),",
                 "и отказ любого другого варианта неотличим от этой поломки.",
                 ""]
        for name, why in broken:
            lines.append("  %s: %s" % (name, why))
        raise SystemExit("\n".join(lines))


def print_tool_versions(by_vp):
    """Версии инструментов образов сборки — и чем на самом деле различается пара.

    Пока этих строк не было, утверждение «у пары про версию glibc
    различается ровно одна вещь» держалось на имени образа в
    variants.json. Различается образ сборки ЦЕЛИКОМ: вместе с версией
    glibc меняется и компилятор. Здесь это видно числами.
    """
    rows = {name: by_vp[(name, "healthz")] for name in NAMES
            if (name, "healthz") in by_vp}
    if not rows or "cxx_version" not in next(iter(rows.values())):
        raise SystemExit(
            "в фикстуре нет версий инструментов — различия между "
            "вариантами держатся на имени образа в variants.json, а не "
            "на замере. Пересними ось: python3 scripts/run-package.py")

    print()
    print("ВЕРСИИ ИНСТРУМЕНТОВ ОБРАЗОВ СБОРКИ (сняты замером, не объявлены)")
    print()
    header = "%-18s %-34s %-26s %s" % (
        "вариант", "компилятор", "cmake", "libc образа сборки")
    print(header)
    print("-" * len(header))
    for name in NAMES:
        row = rows.get(name)
        if row is None:
            continue
        print("%-18s %-34s %-26s %s" % (
            name, row["cxx_version"][:34], row["cmake_version"][:26],
            row["libc_version"]))

    new = rows.get("glibc-static")
    old = rows.get("glibc-static-old")
    print()
    if new is None or old is None:
        print("  В фикстуре нет обеих половин пары — сказать о её "
              "различиях нечего.")
        return
    print("  ПАРА ПРО ПРАВИЛО: ЧТО В НЕЙ РАЗЛИЧАЕТСЯ НА САМОМ ДЕЛЕ.")
    print("  Различается образ сборки целиком, и это ДВЕ переменные:")
    print("    libc:       %s  ->  %s" % (old["libc_version"],
                                          new["libc_version"]))
    print("    компилятор: %s  ->  %s" % (old["cxx_version"],
                                          new["cxx_version"]))
    if new["cmake_version"] == old["cmake_version"]:
        print("    cmake:      совпал (%s) — единственное равенство пары,"
              % new["cmake_version"])
        print("                сделанное вручную, а не следующее из образа.")
    else:
        print("    cmake:      РАЗОШЁЛСЯ (%s против %s) — у пары появилась"
              % (old["cmake_version"], new["cmake_version"]))
        print("                третья переменная, и это надо разбирать.")
    print("  Разделить вклад версии библиотеки и вклад компилятора стенд")
    print("  НЕ умеет: для этого понадобилось бы третье плечо, где")
    print("  меняется что-то одно. Измерено, что исход различается между")
    print("  двумя ОБРАЗАМИ СБОРКИ.")


def print_retraction():
    """Отзыв трактовки, записанной ДО прогона. Печатается всегда.

    Читатель приходит сюда по ссылке из статьи и видит разбор целиком.
    Если отзыв печатать в одной из веток или в конце «мелким шрифтом»,
    его увидит не тот, кто увидел бы вывод, — а кто-то другой. Поэтому
    отзыв стоит вплотную к наблюдению и не зависит ни от одной ветки.

    Что именно отозвано и почему — ниже дословно тем же текстом, что
    печатается читателю. Правки expected.json здесь нет и быть не
    может: запись сделана до прогона и остаётся историческим фактом.
    """
    print()
    print("  ОТЗЫВ ТРАКТОВКИ, ЗАПИСАННОЙ ДО ПРОГОНА.")
    print("  В expected.json (блок link_warning, ключ")
    print("  the_warning_is_identical_in_both_arms) записано, что")
    print("  совпадение строки при разных исходах ДОКАЗЫВАЕТ причинное")
    print("  утверждение: предупреждение печатается там, где ломать уже")
    print("  нечего, и этим держит старое правило на плаву.")
    print("  ЭТА ТРАКТОВКА ОТОЗВАНА. Оснований два:")
    print("    1) плечи пары различаются ОБРАЗОМ СБОРКИ ЦЕЛИКОМ — и")
    print("       версией glibc, и компилятором. Две переменные разом")
    print("       не дают сказать, что именно развело исходы, тем более")
    print("       что стало с причиной предупреждения;")
    print("    2) EAI_SYSTEM означает лишь «системная ошибка,")
    print("       подробности в errno». Он не доказывает ни того, что")
    print("       модуль поиска имён не загрузился, ни того, что")
    print("       предупреждать компоновщику стало не о чем.")
    print("  Остаётся наблюдение: строка совпала дословно, исходы")
    print("  различаются. Механизм НЕ УСТАНОВЛЕН — для него нужно")
    print("  третье плечо, где меняется что-то одно, и проба, читающая")
    print("  сам механизм, а не код возврата.")
    print("  Запись в expected.json НЕ ПРАВЛЕНА намеренно: до прогона мы")
    print("  действительно думали так, и предрегистрация, переписанная")
    print("  задним числом, перестала бы что-либо доказывать.")


def print_linker_warning():
    """Что компоновщик напечатал — и какая трактовка этого ОТОЗВАНА.

    Строка предупреждения читается из fixtures/link.jsonl —
    перехваченного потока ошибок шага линковки, а не воспроизводится
    рукой по памяти.

    Печатается НАБЛЮДЕНИЕ: у пары строка совпадает дословно, а исходы
    различаются. Записанная до прогона трактовка этого совпадения
    (см. expected.json, блок link_warning) отозвана, и отзыв печатается
    тут же, рядом с наблюдением: читатель обязан увидеть его там, где
    увидел бы вывод. Сам expected.json не правится — предрегистрация,
    переписанная задним числом, перестаёт быть предрегистрацией.

    Разбор ОСТАНАВЛИВАЕТСЯ, если хоть у одного варианта линковка не шла:
    пустой stderr тогда означал бы «линковать было нечего», а не
    «предупреждения нет», и отсутствие предупреждения у контролей
    доказывало бы ровно ничего.
    """
    rows = {r["variant"]: r for r in read_measurements(LINK_FIXTURE)
            if r["kind"] == "link"}
    if not rows:
        raise SystemExit("в %s нет ни одной строки линковки" % LINK_FIXTURE)

    print()
    print("ПРЕДУПРЕЖДЕНИЕ КОМПОНОВЩИКА — ЧТО ИЗМЕРЕНО")
    print()
    broken = [n for n, r in sorted(rows.items())
              if not r["link_step_ran"] or r["link_returncode"] != 0]
    if broken:
        raise SystemExit(
            "ЛИНКОВКА НЕ ШЛА (или упала) у %r — отсутствие предупреждения "
            "у остальных вариантов означало бы лишь, что линковать было "
            "нечего. Разбор остановлен." % broken)

    header = "%-18s %-6s %-12s %-13s %s" % (
        "вариант", "libc", "линковка", "линковка шла", "предупреждение")
    print(header)
    print("-" * len(header))
    for name in sorted(rows):
        row = rows[name]
        print("%-18s %-6s %-12s %-13s %s" % (
            name, row["libc"], row["linkage"],
            "да" if row["link_step_ran"] else "НЕТ",
            "ЕСТЬ" if row["link_warning_present"] else "нет"))
    print()
    print("  Признак присутствия — подстрока %r, сопоставленная с"
          % linklib.MARKER)
    print("  ПЕРЕХВАЧЕННЫМ stderr процесса линковки. Сырой вывод целиком")
    print("  лежит в фикстуре рядом: нормализацию можно перепроверить.")
    print("  Нормализация: %s." % rows[sorted(rows)[0]]
          ["link_warning_normalization"])

    print()
    new = rows.get("glibc-static")
    old = rows.get("glibc-static-old")
    if new is None or old is None:
        raise SystemExit("в фикстуре линковки нет пары про правило")
    if not (new["link_warning_present"] and old["link_warning_present"]):
        print("  ПРЕДУПРЕЖДЕНИЕ ЕСТЬ НЕ У ОБОИХ ПЛЕЧ ПАРЫ (%s / %s) —"
              % (new["link_warning_present"], old["link_warning_present"]))
        print("  наблюдение про одинаковую строку не состоялось.")
    elif new["link_warning_lines"] == old["link_warning_lines"]:
        print("  НАБЛЮДЕНИЕ: ОДИНАКОВАЯ СТРОКА ПРИ РАЗНЫХ ИСХОДАХ ПАРЫ.")
        print("  Компоновщик печатает у обоих плеч дословно одно и то же:")
        for line in new["link_warning_lines"]:
            print("    %s" % line)
        print("  При этом старое плечо имя не разрешает, а новое —")
        print("  разрешает. Это ровно два измеренных факта: строка")
        print("  совпала, исходы разошлись. Почему они разошлись, отсюда")
        print("  НЕ СЛЕДУЕТ.")
    else:
        print("  Предупреждение есть у обоих плеч, но тексты РАЗОШЛИСЬ:")
        print("    новое: %r" % new["link_warning_lines"])
        print("    старое: %r" % old["link_warning_lines"])
        print("  Тогда совпадения строки нет вовсе — наблюдение, ради")
        print("  которого сделан замер, не состоялось, и разбирать надо")
        print("  расхождение текстов.")

    print_retraction()

    print()
    print("  КОНТРОЛИ ПРЕДУПРЕЖДЕНИЯ. Оно не про статику вообще и не про")
    print("  glibc вообще, и это видно по двум вариантам:")
    musl = rows.get("musl-static")
    dyn = rows.get("glibc-dynamic")
    if musl is not None:
        print("    musl-static: линковка %s, libc %s -> предупреждение %s"
              % (musl["linkage"], musl["libc"],
                 "ЕСТЬ" if musl["link_warning_present"] else "нет"))
    if dyn is not None:
        print("    glibc-dynamic: линковка %s, libc %s -> предупреждение %s"
              % (dyn["linkage"], dyn["libc"],
                 "ЕСТЬ" if dyn["link_warning_present"] else "нет"))
    print("  Предупреждение печатается там, где статика встречается с")
    print("  glibc, — и только там среди снятых вариантов.")
    print()
    print("  ОБЛАСТЬ. Снято пять образов сборки этого стенда, один")
    print("  прогон, одна платформа, компоновщик GNU ld. О других")
    print("  компоновщиках, других версиях и о том, насколько часто это")
    print("  предупреждение видят, здесь не сказано ничего.")


def main():
    expected = load_json("expected.json")
    rows = read_fixture(FIXTURE)
    by_vp = _by_variant_probe(rows)

    check_controls(by_vp)

    missing = [n for n in NAMES if (n, "resolve") not in by_vp]
    if missing:
        raise SystemExit("в фикстуре нет resolve-строки для: %r" % missing)

    print("ОСЬ 1: УПАКОВКА И УСТАРЕВШЕЕ ПРАВИЛО")
    print()
    header = "%-18s %10s %11s %10s %5s %-10s %-24s %10s" % (
        "вариант", "image_B", "unpacked_B", "binary_B", "deps", "resolve",
        "detail", "t_med_ms")
    print(header)
    print("-" * len(header))
    for name in NAMES:
        healthz = by_vp.get((name, "healthz"), {})
        resolve = by_vp[(name, "resolve")]
        image_bytes = healthz.get("image_bytes", "-")
        unpacked_bytes = healthz.get("image_unpacked_bytes", "-")
        binary_bytes = healthz.get("binary_bytes", "-")
        deps = len(healthz.get("dynamic_deps", []) or [])
        t_med = healthz.get("first_response_ms", "-")
        detail = (resolve.get("detail") or "-")[:24]
        print("%-18s %10s %11s %10s %5s %-10s %-24s %10s" % (
            name, image_bytes, unpacked_bytes, binary_bytes, deps,
            MARK.get(resolve["outcome"], resolve["outcome"]), detail, t_med))
    print()
    print("  ДВЕ ШКАЛЫ РАЗМЕРА ОБРАЗА. image_B — упакованное содержимое:")
    print("  цена хранения и передачи. Между платформами оно расходится на")
    print("  единицы байт (биты прав доступа файлов контекста сборки) и")
    print("  поэтому сравнивается с допуском. unpacked_B — объём файлов")
    print("  итоговой файловой системы образа: он сравнивается ТОЧНО, и")
    print("  именно он стережёт содержимое. Упакованное число содержимое")
    print("  не стережёт: дописанные к бинарнику 8 байт сдвигали его на 2,")
    print("  причём вниз (диверсия siblings-size-follows-the-file).")

    print()
    print("РАСХОЖДЕНИЯ С ПРОГНОЗОМ (expected.json)")
    surprises = []
    for name in NAMES:
        want = expected["package"][name]["expected_resolve_outcome"]
        have = by_vp[(name, "resolve")]["outcome"]
        if want != have:
            surprises.append((name, want, have))
    if not surprises:
        print("  Прогон совпал с прогнозом по всем шести вариантам.")
    else:
        for name, want, have in surprises:
            print("  %s: ждали %s, получили %s" % (name, want, have))
            print("     основание прогноза: %s"
                  % expected["package"][name]["why"])
            print("     ЭТО НАХОДКА: ожидание записано заранее и не")
            print("     подгоняется задним числом — расхождение остаётся")
            print("     как есть и требует объяснения, а не правки прогноза.")

    print()
    print("ПАРА ПРО УСТАРЕВШЕЕ ПРАВИЛО (glibc-static vs glibc-static-old)")
    new_row = by_vp[("glibc-static", "resolve")]
    old_row = by_vp[("glibc-static-old", "resolve")]
    if new_row["outcome"] == old_row["outcome"]:
        print("  ВНИМАНИЕ: пара НЕ разошлась (%s == %s) — главная находка"
              " оси не подтвердилась." % (new_row["outcome"], old_row["outcome"]))
    else:
        print("  Разошлась, как и ожидалось: glibc-static -> %s, "
              "glibc-static-old -> %s (%s)"
              % (new_row["outcome"], old_row["outcome"], old_row["detail"]))
        print("  Правило «статика+glibc не резолвит имена» верно для")
        print("  старого glibc и перестало быть верным для нового.")
        print("  ОГОВОРКА: у половин пары различается образ сборки")
        print("  ЦЕЛИКОМ — вместе с версией glibc меняется компилятор.")
        print("  Переменных две, и приписать расхождение исходов одной")
        print("  версии библиотеки этот замер не позволяет. Версии обеих")
        print("  сняты и напечатаны ниже.")

    print_tool_versions(by_vp)

    print_linker_warning()

    print()
    print("ВРЕМЯ ДО ПЕРВОГО ОТВЕТА — единственная временная величина стенда")
    times = []
    for name in NAMES:
        healthz = by_vp.get((name, "healthz"), {})
        med = healthz.get("first_response_ms")
        lo = healthz.get("first_response_min_ms")
        hi = healthz.get("first_response_max_ms")
        if med is None:
            continue
        times.append((name, med, lo, hi))
        print("  %-18s медиана %7.1f ms, разброс %.1f..%.1f ms"
              % (name, med, lo, hi))
    if len(times) >= 2:
        values = [t[1] for t in times]
        cross_variant_spread = max(values) - min(values)
        max_within_spread = max((t[3] - t[2]) for t in times)
        print()
        if max_within_spread >= cross_variant_spread:
            print("  ВЫВОД ПО ВРЕМЕНИ НЕЛЬЗЯ СДЕЛАТЬ: разброс внутри "
                  "варианта (до %.1f ms) перекрывает разницу между "
                  "вариантами (%.1f ms)." % (max_within_spread,
                                              cross_variant_spread))
        else:
            print("  Разброс внутри варианта (до %.1f ms) меньше разницы "
                  "между вариантами (%.1f ms) — по этой величине "
                  "предположительно можно судить, но здесь нет "
                  "статистического теста, только эта проверка на глаз."
                  % (max_within_spread, cross_variant_spread))


if __name__ == "__main__":
    main()
