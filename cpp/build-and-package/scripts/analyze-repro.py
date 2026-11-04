"""Разбор оси воспроизводимости: прогон против записанного до него.

Разбор НЕ содержит ожиданий — они лежат в expected.json, блок "repro".
Здесь только сверка и печать, плюс защиты, без которых таблицу печатать
нельзя. Порядок защит существен:

  1. КЭШ. Совпадение сумм, полученное кэшем слоёв Docker, — ноль
     информации: второй сборки тогда не было вовсе. Пока не показано,
     что компиляция выполнялась дважды, читать identical бессмысленно.
     Проверяются и улики внутри плеча, и попарная различность всех
     времён изменения бинарников прогона: шесть плеч, безупречных
     поодиночке, могли бы оказаться одним плечом, повторённым шесть раз.
  2. ИЗМЕРИТЕЛЬ И КАНАЛ. Отрицательный контроль source-changed обязан
     дать РАЗНЫЕ суммы, иначе identical=true неотличимо от измерителя,
     который всегда хеширует один и тот же файл. Положительный контроль
     debug-plain обязан дать разные суммы тоже, но по другой причине:
     иначе у флагов закрепления нет канала, к которому они приложены, и
     пара с -g не меряет ничего.
  3. ПАРЫ. В каждой паре плечи обязаны различаться лечением и не
     различаться ничем больше.

Только после этих трёх печатается таблица.

ОБА ПРОГОНА ПРОХОДЯТ ОДНИ И ТЕ ЖЕ ЗАЩИТЫ. В первой редакции защиты
применялись только к последнему прогону, а первый шёл прямо в печать —
то есть утверждение «сумма та же и в другом прогоне» строилось на
данных, не проверенных ни уликами кэша, ни согласованностью identical с
суммами. Теперь защиты применяются к каждому прогону, и прогон, их не
прошедший, останавливает разбор.

ЧТО ЗНАЧАТ ДВА ПРОГОНА. Повтор в том же окне, и только. Между ними не
меняется ничего, кроме метки прогона и времени; разделяют их секунды.
Называть это «шире, чем один раз» нельзя.
"""

import io
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import read_measurements
from scripts.repro import normalized_treatment, treatment_field
from scripts.console import force_utf8  # noqa: E402

# Потоки переключаются в UTF-8 первым делом: на консоли cp1251
# печать «≠» роняла разбор с UnicodeEncodeError. Найдено прогоном
# на втором хосте.
force_utf8()

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.environ.get(
    "REPRO_FIXTURE", os.path.join(HERE, "fixtures", "repro.jsonl"))
FIXTURE_RUN1 = os.environ.get(
    "REPRO_FIXTURE_RUN1", os.path.join(HERE, "fixtures", "repro.run-1.jsonl"))

MEASURED = ["plain", "pinned", "debug-plain", "debug-pinned", "debug-stripped"]
CONTROLS = ["source-changed"]
ORDER = MEASURED + CONTROLS

PAIRS = (("plain", "pinned", "без отладочной информации, со strip"),
         ("debug-plain", "debug-pinned", "с отладочной информацией, без strip"))

# Пара механизма. Плечо debug-stripped несёт отдельный вывод («канал
# закрывает strip, а не устройство исходника»), но в PAIRS не входит: там
# плечи различаются флагами закрепления, а здесь — шагом strip. До этой
# пары сверки инструментов и флагов не дотягивались вовсе, и посторонний
# флаг, добавленный этому плечу, прошёл бы весь набор проверок
# незамеченным: утверждение «отличается ровно шагом strip» держалось на
# устройстве спецификации, а не на замере. Здесь оно становится замером.
MECHANISM_PAIRS = (("debug-plain", "debug-stripped"),)

# Единственный пункт, который остался ТВЁРДЫМ ПРОГНОЗОМ, и два, которые
# им не были. Разделение печатается вслух: без него раздел «гипотезы»
# читается как три сбывшихся предсказания вместо одного.
FIRM_HYPOTHESIS = "debug_arms_are_really_built_with_g"
DERIVED_FROM_SEEN_DATA = ("debug_stripped_gives_the_stripped_sum",
                          "debug_pinned_does_not_equal_the_stripped_sum")

# Оговорка обязана печататься вместе с разделом гипотез: expected.json
# правке не подлежит (он предрегистрационный), и единственное место, где
# дефект можно назвать, — печать разбора и отчёт.
HYPOTHESES_CAVEAT = [
    "  ОГОВОРКА: ГИПОТЕЗАМИ БЫЛИ НЕ ВСЕ ТРИ.",
    "  Твёрдым прогнозом здесь засчитывается ОДНА строка —",
    "  %s: размеров проба не давала," % FIRM_HYPOTHESIS,
    "  и отношение размеров до прогона никто не видел.",
    "",
    "  Две другие — %s" % DERIVED_FROM_SEEN_DATA[0],
    "  и %s — были ВЫВОДИМЫ" % DERIVED_FROM_SEEN_DATA[1],
    "  из уже наблюдённых данных, а не предсказаны. Проба дала",
    "  0c510779… для «-g, затем strip» и 2d45ce66… для «-g с",
    "  закреплением», а первая редакция НАШЕЙ ЖЕ фикстуры давала",
    "  0c510779… для plain (git show 7936180:…/fixtures/repro.jsonl).",
    "  Значит и равенство debug-stripped = plain, и неравенство",
    "  debug-pinned ≠ plain вычитывались из уже виденного.",
    "",
    "  Обоснование в expected.json при этом перевёрнуто буквально:",
    "  совпадение с суммой нашей собственной первой фикстуры названо",
    "  там доводом ЗА гипотезу, тогда как это довод за знание.",
    "  Запись не правится: expected.json предрегистрационный, и правка",
    "  задним числом уничтожила бы его смысл. Дефект зафиксирован здесь",
    "  и в отчёте — там, где фиксировать его можно.",
]

# Плечи, у которых суммы обязаны разойтись, и что означало бы совпадение.
MUST_DIVERGE = (
    ("source-changed",
     "ИЗМЕРИТЕЛЬ НЕ УМЕЕТ ОТВЕТИТЬ «РАЗЛИЧАЮТСЯ».",
     ["Совпадение сумм у измеряемых плеч в этом случае",
      "неотличимо от измерителя, который хеширует один и тот же",
      "файл. Вся ось недействительна."]),
    ("debug-plain",
     "КАНАЛА ДЛЯ ЛЕЧЕНИЯ НЕТ — ПАРА С -g НИЧЕГО НЕ МЕРЯЕТ.",
     ["Сборка с -g и без закрепления обязана разойтись: путь",
      "каталога сборки лежит в отладочной информации, а каталоги",
      "у двух сборок разные намеренно. Совпадение означает, что",
      "лечить в этой паре нечего — ровно та беда, ради которой",
      "пара и добавлялась."]),
)


def load_json(name):
    with io.open(os.path.join(HERE, name), encoding="utf-8") as handle:
        return json.loads(handle.read())


def rows_of(path):
    return {r["variant"]: r for r in read_measurements(path)}


def _stop(title, why_lines, items):
    lines = [title, ""] + list(why_lines) + [""]
    for name, why in items:
        lines.append("  %s: %s" % (name, why))
    raise SystemExit("\n".join(lines))


def check_cache_did_not_measure_itself(rows, label="прогон"):
    """Первая защита оси и самая важная.

    Обе улики физические: метка прочитана обратно из собранного образа,
    время изменения снято с самого файла внутри образа. Совпадение любой
    из них означает, что вторая сборка не выполнялась.

    Третья проверка — про прогон целиком: все времена изменения обязаны
    быть попарно различны. Внутри плеча этого мало, потому что метка
    прогона у всех плеч одна и та же на позицию, каталоги общие, а у
    некоторых плеч совпадает и весь набор аргументов сборки.
    """
    broken = []
    for name, row in sorted(rows.items()):
        if row["nonce_first"] == row["nonce_second"]:
            broken.append((name, "метка прогона у обеих сборок одна (%r)"
                           % row["nonce_first"]))
        if row["binary_mtime_first"] == row["binary_mtime_second"]:
            broken.append((name, "время изменения бинарника совпало (%r)"
                           % row["binary_mtime_first"]))
        if not row.get("no_cache"):
            broken.append((name, "сборки шли без --no-cache"))

    seen = {}
    for name, row in sorted(rows.items()):
        for position in ("first", "second"):
            mtime = row["binary_mtime_%s" % position]
            where = "%s/%s" % (name, position)
            if mtime in seen:
                broken.append((where,
                               "время изменения %r уже встречалось у %s — две "
                               "сборки прогона были одной"
                               % (mtime, seen[mtime])))
            seen[mtime] = where

    if broken:
        _stop("СБОРКА БЫЛА ОДНА, А НЕ ДВЕ — ТАБЛИЦЫ НЕТ (%s)." % label,
              ["Совпадение сумм, полученное кэшем слоёв Docker, не значит",
               "ничего: сравнивались два имени одного и того же файла."],
              broken)


def check_measurer_can_say_different(rows, label="прогон"):
    """Вторая защита: умеет ли измеритель ответить «различаются» и есть ли
    у лечения канал, к которому оно приложено."""
    for name, title, why in MUST_DIVERGE:
        row = rows.get(name)
        if row is None:
            _stop("%s (%s)" % (title, label), why, [(name, "нет строки")])
        if row["identical"]:
            _stop("%s (%s)" % (title, label), why,
                  [(name, "суммы совпали: %s" % row["sha256_first"][:16])])


def check_pairs_are_controlled(rows, label="прогон"):
    """Третья защита: в каждой паре различается ровно одно, и это лечение."""
    broken = []
    missing = [n for n in ORDER if rows.get(n) is None]
    if missing:
        _stop("НЕТ ОДНОГО ИЗ ПЛЕЧ (%s)." % label,
              ["Пары нет — сравнивать нечего."],
              [(n, "нет строки") for n in missing])

    for plain, pinned, what in PAIRS:
        if (normalized_treatment(rows[plain], "first")
                == normalized_treatment(rows[pinned], "first")):
            broken.append(("%s/%s" % (plain, pinned),
                           "оба плеча получили одно лечение (%r) — пара не "
                           "меряет ничего"
                           % normalized_treatment(rows[plain], "first")))
        for field in ("cxx_version", "cmake_version", "fmt_version"):
            if rows[plain][field] != rows[pinned][field]:
                broken.append(("%s/%s" % (plain, pinned),
                               "различаются ещё и по «%s»: %r против %r"
                               % (field, rows[plain][field],
                                  rows[pinned][field])))
    # Пара механизма сверяется иначе: у неё различаться обязан ТОЛЬКО шаг
    # strip, а набор флагов компиляции и SOURCE_DATE_EPOCH обязаны
    # совпасть. Раньше этого не проверялось ничем, и плечо, получившее
    # лишний флаг, продолжало бы называться «отличающимся ровно шагом
    # strip».
    for base, stripped in MECHANISM_PAIRS:
        for field in ("cxx_version", "cmake_version", "fmt_version"):
            if rows[base][field] != rows[stripped][field]:
                broken.append(("%s/%s" % (base, stripped),
                               "различаются ещё и по «%s»: %r против %r"
                               % (field, rows[base][field],
                                  rows[stripped][field])))
        for position in ("first", "second"):
            ta = rows[base]["treatment_%s" % position]
            tb = rows[stripped]["treatment_%s" % position]
            if treatment_field(ta, "FLAGS").split() != \
                    treatment_field(tb, "FLAGS").split():
                broken.append((
                    "%s/%s" % (base, stripped),
                    "сборка %s: наборы флагов различаются (%r против %r) — "
                    "значит различие не одно, и «ровно шагом strip» неверно"
                    % (position, treatment_field(ta, "FLAGS"),
                       treatment_field(tb, "FLAGS"))))
            if treatment_field(ta, "SOURCE_DATE_EPOCH") != \
                    treatment_field(tb, "SOURCE_DATE_EPOCH"):
                broken.append((
                    "%s/%s" % (base, stripped),
                    "сборка %s: различается ещё и SOURCE_DATE_EPOCH (%r "
                    "против %r)"
                    % (position, treatment_field(ta, "SOURCE_DATE_EPOCH"),
                       treatment_field(tb, "SOURCE_DATE_EPOCH"))))
            if treatment_field(ta, "STRIP") == treatment_field(tb, "STRIP"):
                broken.append((
                    "%s/%s" % (base, stripped),
                    "сборка %s: шаг strip у обоих плеч одинаков (%r) — "
                    "пара механизма не меряет ничего"
                    % (position, treatment_field(ta, "STRIP"))))

    # Сравнение идёт по лечению, приведённому к виду без каталога сборки:
    # -ffile-prefix-map по устройству называет этот каталог, а каталоги у
    # двух сборок разные намеренно. Дословное сравнение ловило бы
    # устройство флага, а не подмену лечения.
    for name in ORDER:
        row = rows[name]
        if name != "source-changed" and (
                normalized_treatment(row, "first")
                != normalized_treatment(row, "second")):
            broken.append((name, "внутри плеча лечение разное (%r против "
                                 "%r) — это две разные сборки, а не повтор "
                                 "одной"
                           % (normalized_treatment(row, "first"),
                              normalized_treatment(row, "second"))))
        for position in ("first", "second"):
            named = treatment_field(row["treatment_%s" % position],
                                    "BUILD_DIR")
            if named != row["build_dir_%s" % position]:
                broken.append((name,
                               "каталог сборки в поле (%r) и каталог, "
                               "названный шагом сборки (%r), — разные"
                               % (row["build_dir_%s" % position], named)))
    if broken:
        _stop("ПАРА НЕ КОНТРОЛИРУЕМА — ТАБЛИЦА НИЧЕГО НЕ ЗНАЧИТ (%s)."
              % label,
              ["Пара контролируема, только если различается ровно одно.",
               "Здесь это лечение: флаги закрепления."],
              broken)


def expectation_of(expected, name):
    """Что было записано про плечо до прогона — и на каком основании.

    Разделение существенное. Исход плеч с -g был известен из пробы
    ревьюера ДО прогона, и записать его в гипотезы значило бы выдать
    знание за предвидение. Разбор печатает основание вслух, чтобы
    сравнение «сбылось / не сбылось» не читалось как удачное
    предсказание там, где предсказания не было.
    """
    hypothesis = expected["hypothesis_before_run"].get(name)
    if hypothesis is not None:
        return hypothesis["expected_identical"], "гипотеза, записана до прогона"
    probe = expected["known_from_pre_run_probes"][name]
    return probe["expected_identical"], "известно из пробы, НЕ предсказание"


def run_stamp(rows):
    """Метка прогона: она же время его начала, в секундах эпохи."""
    return int(rows["plain"]["nonce_first"].split("-")[0])


def main():
    expected = load_json("expected.json")["repro"]
    rows = rows_of(FIXTURE)
    run1 = rows_of(FIXTURE_RUN1) if os.path.exists(FIXTURE_RUN1) else {}

    runs = [("прогон 2", rows)]
    if run1:
        runs.insert(0, ("прогон 1", run1))
    for label, one in runs:
        check_cache_did_not_measure_itself(one, label)
        check_measurer_can_say_different(one, label)
        check_pairs_are_controlled(one, label)

    print("ОСЬ 3: ВОСПРОИЗВОДИМОСТЬ СБОРКИ")
    print()
    print(expected["what_this_axis_measures"])
    print()

    header = "%-16s %-9s %11s %11s %-9s" % (
        "плечо", "совпали", "байт_1", "байт_2", "роль")
    print(header)
    print("-" * len(header))
    for name in ORDER:
        r = rows[name]
        print("%-16s %-9s %11d %11d %-9s"
              % (name, "да" if r["identical"] else "НЕТ",
                 r["binary_bytes_first"], r["binary_bytes_second"],
                 r["role"]))
    print()
    for name in ORDER:
        r = rows[name]
        print("  %s" % name)
        print("     сборка 1: %s" % r["sha256_first"])
        print("     сборка 2: %s" % r["sha256_second"])

    print()
    print("ЧЕМ ДОКАЗАНО, ЧТО СБОРОК БЫЛО ДВЕНАДЦАТЬ, А НЕ ОДНА")
    print("  Улики прочитаны ОБРАТНО из собранных образов, а не объявлены")
    print("  прогоном: метка (docker run … cat /nonce) и время изменения")
    print("  самого бинарника (stat внутри образа).")
    for name in ORDER:
        r = rows[name]
        print("  %-16s метки %r и %r; время изменения бинарника %d и %d "
              "(разница %d s)"
              % (name, r["nonce_first"], r["nonce_second"],
                 r["binary_mtime_first"], r["binary_mtime_second"],
                 abs(r["binary_mtime_second"] - r["binary_mtime_first"])))
    stamps = sorted(r["binary_mtime_%s" % p]
                    for r in rows.values() for p in ("first", "second"))
    print("  Все %d времён изменения попарно различны: от %d до %d."
          % (len(stamps), stamps[0], stamps[-1]))
    print("  Метка прогона у всех плеч одна и та же на позицию — так")
    print("  задумано, иначе плечи различались бы не только лечением.")
    print("  Различность СБОРОК между плечами доказывает поэтому не метка,")
    print("  а время изменения.")
    print()
    print("  Отдельно — то, что уликой НЕ является. Поля no_cache и network")
    print("  говорят, с какими ключами запускался docker build; ключи")
    print("  строятся из тех же значений спецификации, а полная командная")
    print("  строка лежит в строке фикстуры и сверяется с полями. Это")
    print("  связанное объявление, а не признак, снятый с действительности:")
    print("  командную строку записывает тот же скрипт, который её составил.")

    print()
    print("ЧЕМ РАЗЛИЧАЮТСЯ ПЛЕЧИ (лечение прочитано из образа)")
    for name in ORDER:
        r = rows[name]
        print("  %-16s сборка 1: %s" % (name, r["treatment_first"]))
        print("  %-16s сборка 2: %s" % ("", r["treatment_second"]))
    print("  Совпадает у пар: компилятор (%s), cmake (%s), версия fmt (%s)."
          % (rows["plain"]["cxx_version"], rows["plain"]["cmake_version"],
             rows["plain"]["fmt_version"]))
    print("  Каталог сборки печатает сам шаг сборки, и он сверяется с полем")
    print("  build_dir_*: на это поле опирается нормализация лечения.")

    print()
    print("ЧТО ДАЛИ САМИ ФЛАГИ ЗАКРЕПЛЕНИЯ")
    for plain, pinned, what in PAIRS:
        same_across = (rows[plain]["sha256_first"]
                       == rows[pinned]["sha256_first"])
        print("  пара %s / %s (%s):" % (plain, pinned, what))
        print("     %-16s %s" % (plain,
                                 "суммы совпали" if rows[plain]["identical"]
                                 else "суммы РАЗОШЛИСЬ"))
        print("     %-16s %s" % (pinned,
                                 "суммы совпали" if rows[pinned]["identical"]
                                 else "суммы РАЗОШЛИСЬ"))
        print("     бинарник %s"
              % ("тот же, что у плеча без закрепления" if same_across
                 else "отличается от плеча без закрепления"))
    print()
    if rows["plain"]["identical"] and rows["pinned"]["identical"]:
        print("  Пара без отладочной информации: лечение не изменило ничего.")
        print("  Область этого утверждения — КОНФИГУРАЦИЯ сборки, а не")
        print("  исходник: Release, без -g, со strip. Тот же самый исходник,")
        print("  собранный с -g, расходится — это видно строкой ниже.")
    if not rows["debug-plain"]["identical"] and rows["debug-pinned"]["identical"]:
        print("  Пара с отладочной информацией: без закрепления суммы")
        print("  расходятся, с закреплением совпадают. Лечение приложено к")
        print("  живому каналу, и видно, что оно лечит.")

    print()
    print("МЕХАНИЗМ: ЧТО ИМЕННО ЗАКРЫВАЕТ КАНАЛ")
    stripped_same = (rows["debug-stripped"]["sha256_first"]
                     == rows["plain"]["sha256_first"])
    print("  debug-stripped отличается от debug-plain ровно шагом strip —")
    print("  и это ЗАМЕР, а не устройство спецификации: наборы флагов,")
    print("  SOURCE_DATE_EPOCH и все три версии инструментов у пары сверены")
    print("  выше, различается только STRIP.")
    print("  debug-plain: суммы %s; debug-stripped: суммы %s."
          % ("совпали" if rows["debug-plain"]["identical"] else "разошлись",
             "совпали" if rows["debug-stripped"]["identical"]
             else "разошлись"))
    print("  Сумма debug-stripped %s сумме plain."
          % ("равна" if stripped_same else "НЕ равна"))
    if rows["debug-stripped"]["identical"] and stripped_same:
        print("  Значит расхождение убирает strip, а не отсутствие путей в")
        print("  исходнике: пути в объектах были, их снял шаг strip.")

    print()
    print("КОНТРОЛИ")
    control = rows["source-changed"]
    print("  source-changed: суммы %s"
          % ("РАЗОШЛИСЬ — измеритель умеет ответить «различаются»"
             if not control["identical"] else "совпали"))
    print("  Размеры бинарников: %d Б против %d Б."
          % (control["binary_bytes_first"], control["binary_bytes_second"]))
    print("  %s" % expected["controls"]["source-changed"])
    print()
    positive = rows["debug-plain"]
    print("  debug-plain: суммы %s"
          % ("РАЗОШЛИСЬ — у флагов закрепления есть что закреплять"
             if not positive["identical"] else "совпали"))
    print("  %s" % expected["controls"]["debug-plain"])

    print()
    print("СВЕРКА С ЗАПИСАННЫМ ДО ПРОГОНА (expected.json, блок repro)")
    surprises = []
    for name in ORDER:
        want, basis = expectation_of(expected, name)
        got = rows[name]["identical"]
        mark = "сошлось" if want == got else "РАЗОШЛОСЬ С ЗАПИСАННЫМ"
        print("  %-16s ждали identical=%s (%s), получили %s — %s"
              % (name, want, basis, got, mark))
        if want != got:
            surprises.append(name)
    if surprises:
        print("  ЭТО НАХОДКА: записанное не подгоняется задним числом.")
        print("  Разошлись: %s" % ", ".join(surprises))
    else:
        print("  Прогон сошёлся с записанным по всем плечам.")
    print("  Три плеча с -g помечены «известно из пробы»: их исход был")
    print("  известен до прогона из замера внешнего ревью, а не предсказан.")
    print("  Засчитывать их как сбывшийся прогноз нельзя.")

    print()
    print("ГИПОТЕЗЫ, КОТОРЫЕ ОСТАВАЛИСЬ ПРЕДСКАЗАНИЯМИ")
    for line in HYPOTHESES_CAVEAT:
        print(line)
    print()
    hypothesis = expected["hypothesis_before_run"]
    checks = (
        ("debug_arms_are_really_built_with_g",
         min(rows[n]["binary_bytes_%s" % p]
             for n in ("debug-plain", "debug-pinned")
             for p in ("first", "second"))
         > rows["plain"]["binary_bytes_first"],
         "бинарники с -g больше стриппнутого (%d Б против %d Б)"
         % (rows["debug-plain"]["binary_bytes_first"],
            rows["plain"]["binary_bytes_first"])),
        ("debug_stripped_gives_the_stripped_sum", stripped_same,
         "сумма debug-stripped и сумма plain"),
        ("debug_pinned_does_not_equal_the_stripped_sum",
         rows["debug-pinned"]["sha256_first"] != rows["plain"]["sha256_first"],
         "сумма debug-pinned и сумма plain"),
    )
    for key, got, what in checks:
        want = hypothesis[key]["expected"]
        print("  %-45s ждали %s, получили %s — %s"
              % (key, want, got,
                 "сбылось" if want == got else "НЕ СБЫЛОСЬ"))
        print("     %s" % what)
        print("     основание: %s"
              % ("твёрдый прогноз" if key == FIRM_HYPOTHESIS
                 else "выводимо из уже наблюдённых данных, прогнозом НЕ было"))

    print()
    print("ПОВТОР В ТОМ ЖЕ ОКНЕ")
    if not run1:
        print("  Второго прогона нет. Утверждать можно только про одно окно")
        print("  замера.")
    else:
        gap = run_stamp(rows) - run_stamp(run1)
        for name in ORDER:
            a, b = run1[name], rows[name]
            same_between = a["sha256_first"] == b["sha256_first"]
            print("  %-16s внутри прогона: %s и %s; между прогонами первая "
                  "сумма %s"
                  % (name, "совпали" if a["identical"] else "разошлись",
                     "совпали" if b["identical"] else "разошлись",
                     "та же" if same_between else "ДРУГАЯ"))
        print("  Второй прогон начат через %d s после первого, на той же" % gap)
        print("  машине, в том же окне. Это ПОВТОР, а не расширение области:")
        print("  между прогонами меняются только метка прогона и время.")
        print("  Каталоги сборки у прогонов одни и те же (/build-a и")
        print("  /build-b), и ничего, кроме перечисленного, не измерено.")
        print("  Оба прогона прошли одни и те же защиты — иначе второй был")
        print("  бы просто напечатан.")

    print()
    print("ЧЕГО ПО ЭТОЙ ОСИ УТВЕРЖДАТЬ НЕЛЬЗЯ")
    print("  " + expected["what_will_not_be_claimed"])
    print()
    print("  " + expected["if_they_coincide_unexpectedly"])


if __name__ == "__main__":
    main()
