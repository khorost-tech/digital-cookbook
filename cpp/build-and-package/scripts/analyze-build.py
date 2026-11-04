"""Разбор оси сборки: прогон против ожиданий, записанных до прогона.

Разбор НЕ содержит ожиданий — они лежат в expected.json, блок "build".
Здесь только сверка и печать, плюс защиты, без которых таблицу печатать
нельзя:

  * измеряемые плечи обязаны СОБРАТЬСЯ — иначе таблицы нет вовсе, и
    разбор обязан сказать это словами, а не свалиться на арифметике над
    null;
  * контроли обязаны дать заведомо известный результат (два из трёх
    обязаны УПАСТЬ, и упасть на своём шаге);
  * холодная сборка не быстрее тёплой ни у одного плеча, иначе
    --no-cache не сработал и кэш померил сам себя;
  * величина, по которой вывода нет, объявляется неинформативной вслух,
    а не показывается как вывод.

ТРИ ПРАВИЛА НЕИНФОРМАТИВНОСТИ, И ВСЕ ТРИ ДЕЙСТВУЮТ.
Первое записано в expected.json дословно: «если разница между плечами по
warm_seconds окажется меньше секунды, разбор говорит вслух, что по этой
величине вывода нет» (и то же решение принято для rebuild_seconds).
Второе — межпрогонное: если разброс одного плеча между двумя прогонами
шире разницы между плечами, различие не показано. Раньше в коде жило
только второе; смысл предрегистрации в том, что применяется ЗАПИСАННОЕ
правило, а не то, которое даёт тот же ответ.

Третье появилось после того, как записанное правило дало РАЗНЫЕ ответы
на двух прогонах одной сессии: на первом разница между плечами оказалась
меньше порога и правило сняло утверждение, на втором — не меньше, и
правило промолчало. Записанное правило применяется как записано; но
разбор обязан сказать вслух, что прогоны о его применимости РАСХОДЯТСЯ.
Величина, у которой информативность переворачивается между двумя
прогонами одной сессии, вывода не даёт тем более: пара прогонов не
оценивает разброс величины, а здесь её половины спорят даже о том, есть
ли что оценивать. Порог при этом не трогается — подгонка правила под
свежие числа была бы ровно тем дефектом, против которого стенд построен.

ОКНО ЗАМЕРА. Плечи с разным measurement_window по cold_seconds не
сравниваются: у одних в замер входит получение зависимости, у других
нет. Кратности печатаются только внутри одного окна.

Второй прогон фикстуры (fixtures/build.run-1.jsonl) читается, если он
есть: две точки — не среднее, а проверка устойчивости.
"""

import io
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import read_measurements
from scripts.console import force_utf8  # noqa: E402

# Потоки переключаются в UTF-8 первым делом: на консоли cp1251
# печать «≠» роняла разбор с UnicodeEncodeError. Найдено прогоном
# на втором хосте.
force_utf8()

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
# Путь фикстуры переопределяется окружением: скрипт диверсий
# (scripts/sabotage.py) натравливает разбор на КОПИЮ фикстуры и не
# трогает файл в репозитории.
FIXTURE = os.environ.get(
    "BUILD_FIXTURE", os.path.join(HERE, "fixtures", "build.jsonl"))
FIXTURE_RUN1 = os.environ.get(
    "BUILD_FIXTURE_RUN1", os.path.join(HERE, "fixtures", "build.run-1.jsonl"))

MEASURED = ["system", "vcpkg", "conan", "system-prebaked"]
# Контроль -> (обязан ли собраться, обязательный шаг падения)
CONTROLS = {
    "system-offline": (True, None),
    "vcpkg-latest": (False, "compile"),
    "no-fmt": (False, "find_package"),
}
# Записанное ДО прогона правило для warm_seconds и rebuild_seconds.
RECORDED_FLOOR_SECONDS = 1.0


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


def check_measured_arms_built(rows):
    """Измеряемое плечо, которое упало, — это не строка с сюрпризом.

    Без этой защиты падение любого измеряемого плеча выливалось в
    TypeError на первом же форматировании None вместо секунд:
    трассировка вместо вердикта. Провал измеряемого плеча означает, что
    таблицы нет.
    """
    broken = []
    for name in MEASURED:
        row = rows.get(name)
        if row is None:
            broken.append((name, "нет строки в фикстуре"))
        elif not row.get("build_ok"):
            broken.append((name, "измеряемое плечо не собралось (код %r, "
                                 "шаг %r)" % (row.get("cold_returncode"),
                                              row.get("failure_stage"))))
    if broken:
        _stop("ИЗМЕРЯЕМОЕ ПЛЕЧО НЕ СОБРАЛОСЬ — ТАБЛИЦЫ НЕТ.",
              ["Плечо, которое обязано было собраться и не собралось, —",
               "это сломанный прогон, а не находка про менеджеры.",
               "Печатать остальные строки поверх дыры нельзя."],
              broken)


def check_controls(rows):
    """Останавливает разбор, если контроли дали не то, что обязаны.

    Контроль, упавший не на своём шаге, не засчитывается: no-fmt,
    упавший на обрыве сети, и no-fmt, упавший на find_package, дают
    одинаковый код возврата и разные утверждения.
    """
    broken = []
    for name, (must_build, must_fail_at) in CONTROLS.items():
        row = rows.get(name)
        if row is None:
            broken.append((name, "нет строки"))
            continue
        if must_build and not row["build_ok"]:
            broken.append((name, "обязан был собраться, упал на %r"
                           % row.get("failure_stage")))
        if not must_build:
            if row["build_ok"]:
                broken.append((name, "обязан был упасть, но собрался"))
            elif row.get("failure_stage") != must_fail_at:
                broken.append((name, "упал на %r вместо %r"
                               % (row.get("failure_stage"), must_fail_at)))

    if not (rows.get("vcpkg", {}).get("build_ok")
            or rows.get("conan", {}).get("build_ok")):
        broken.append(("vcpkg+conan",
                       "упали оба менеджера — это неотличимо от отсутствия "
                       "сети"))

    if broken:
        _stop("КОНТРОЛИ НЕ ДАЛИ ОЖИДАЕМОГО — ТАБЛИЦА НЕДЕЙСТВИТЕЛЬНА.",
              ["Контроль на этой оси не украшение: без него успех или",
               "провал менеджера объясняется двумя причинами сразу, и",
               "различить их нечем."],
              broken)


def check_cache_did_not_measure_itself(rows):
    bad = [(n, rows[n]["cold_seconds"], rows[n]["warm_seconds"])
           for n in MEASURED
           if rows[n]["cold_seconds"] < rows[n]["warm_seconds"]]
    if bad:
        _stop("ХОЛОДНАЯ СБОРКА БЫСТРЕЕ ТЁПЛОЙ — ПРОГОН НЕДЕЙСТВИТЕЛЕН.",
              ["Это не находка про менеджеры: --no-cache не сработал, и",
               "кэш померил сам себя."],
              [(n, "холодная %.1f s < тёплая %.1f s" % (c, w))
               for n, c, w in bad])


def comparable_arms(rows):
    """Плечи, у которых cold_seconds меряет одно и то же.

    Окно замера берётся из САМОЙ СТРОКИ фикстуры, а не из описания оси:
    сравнивается то, что снято, а не то, что задумано.
    """
    return [n for n in MEASURED
            if rows[n]["measurement_window"] == "dependency_inside"]


def _verdict(label, values, run1_values, floor=None):
    """Информативна ли величина. Все три правила действуют.

    floor — записанное до прогона абсолютное правило: разница между
    плечами меньше floor секунд означает, что вывода нет. Второе правило
    межпрогонное и floor не отменяет. Третье правило — про сами прогоны:
    если записанное правило срабатывает на одном прогоне и молчит на
    другом, прогоны одной сессии расходятся о применимости записанного
    правила, и вывода по величине нет тем более. Величина считается
    различающей плечи, только если прошла ВСЕ ТРИ.
    """
    between_arms = max(values.values()) - min(values.values())
    run1_between = (max(run1_values.values()) - min(run1_values.values())
                    if run1_values else None)
    verdicts = []

    if floor is not None and between_arms < floor:
        verdicts.append(
            "правило, записанное ДО прогона: разница между плечами %.2f s "
            "меньше %.1f s" % (between_arms, floor))

    # Записанное правило применяется к КАЖДОМУ прогону отдельно, и если
    # ответы разошлись, это само по себе вердикт. Порог здесь не
    # пересматривается: правило применяется как записано, а расхождение
    # прогонов описывается СВЕРХ него.
    if floor is not None and run1_between is not None:
        fires_run1 = run1_between < floor
        fires_now = between_arms < floor
        if fires_run1 != fires_now:
            verdicts.append(
                "прогоны одной сессии РАСХОДЯТСЯ о применимости "
                "записанного правила: на первом прогоне разница между "
                "плечами %.2f s (%s %.1f s — правило %s), на втором "
                "%.2f s (%s %.1f s — правило %s). Величина, у которой "
                "информативность переворачивается между двумя прогонами "
                "одной сессии, вывода не даёт тем более"
                % (run1_between,
                   "меньше" if fires_run1 else "не меньше", floor,
                   "снимает утверждение" if fires_run1 else "молчит",
                   between_arms,
                   "меньше" if fires_now else "не меньше", floor,
                   "снимает утверждение" if fires_now else "молчит"))

    if run1_values:
        run_to_run = max(abs(values[n] - run1_values[n]) for n in values)
        print("  %s: разница между плечами %.2f s (на первом прогоне "
              "%.2f s), расхождение одного плеча между прогонами до %.2f s"
              % (label, between_arms, run1_between, run_to_run))
        if run_to_run >= between_arms:
            verdicts.append(
                "межпрогонное правило: прогон к прогону гуляет не меньше "
                "(%.2f s), чем плечо к плечу (%.2f s)"
                % (run_to_run, between_arms))
    else:
        print("  %s: второго прогона нет, устойчивость не проверена "
              "(разница между плечами %.2f s)" % (label, between_arms))

    if verdicts:
        print("     ВЫВОДА НЕТ. Сработало правил: %d" % len(verdicts))
        for text in verdicts:
            print("       — %s" % text)
        print("     Величина не различает источники зависимости и в статью")
        print("     как сравнение не идёт.")
    else:
        print("     Различие между плечами прошло все три правила — по этой")
        print("     величине плечи различимы. Статистического теста здесь")
        print("     нет, только это сравнение.")


def _print_session_drift():
    """Насколько кратность уезжает внутри сессии и между сессиями.

    Числа берутся из свидетельства по сессиям, а не из фикстур: в
    фикстурах лежит ОДНА сессия, и по ней межсессионный разброс не
    виден в принципе. В README этих процентов нет намеренно — они
    меняются с каждым прогоном ровно так же, как сам множитель, и
    статический текст ломался бы после каждого. Печатаются они здесь.
    """
    path = os.path.join(HERE, "fixtures", "evidence",
                        "cold-seconds-by-session.json")
    if not os.path.exists(path):
        print("  Свидетельства по сессиям нет: соберите его командой")
        print("  `python3 scripts/run-evidence.py` — без него утверждение о")
        print("  межсессионном разбросе нечем подтвердить.")
        return
    with io.open(path, encoding="utf-8") as handle:
        evidence = json.loads(handle.read())
    sessions = [s for s in evidence["sessions"]
                if set(s["measurement_window"].values())
                == {"dependency_inside"}]
    if len(sessions) < 2:
        print("  Сравнимых сессий в свидетельстве меньше двух: о разбросе")
        print("  между сессиями говорить не с чего.")
        return
    print()
    print("  РАЗБРОС КРАТНОСТИ ПО СЕССИЯМ (сравнимых сессий: %d;"
          % len(sessions))
    print("  fixtures/evidence/cold-seconds-by-session.json):")
    for arm in ("conan", "vcpkg"):
        pairs = []
        pool = []
        for session in sessions:
            values = [runs[arm] / runs["system"]
                      for _, runs in sorted(session["cold_seconds"].items())]
            pool += values
            pairs.append(100.0 * (max(values) - min(values)) / min(values))
        total = 100.0 * (max(pool) - min(pool)) / min(pool)
        print("    %-6s общий разброс %.1f %%; пары внутри сессий: %s"
              % (arm, total,
                 ", ".join("%.1f %%" % p for p in pairs)))
    print("  Пара прогонов НЕ ОЦЕНИВАЕТ разброс величины: у одних сессий")
    print("  она сходится до долей процента, у других расходится шире,")
    print("  чем расходятся сессии между собой. «Устойчивость», которую")
    print("  показывает пара, бывает свойством пары, а не величины.")


def main():
    expected = load_json("expected.json")["build"]
    rows = rows_of(FIXTURE)
    run1 = rows_of(FIXTURE_RUN1) if os.path.exists(FIXTURE_RUN1) else {}

    # Порядок защит существен. Сначала контроли: если упали ОБА
    # менеджера, это неотличимо от отсутствия сети, и сказать надо
    # именно это, а не «измеряемое плечо не собралось». Провал одного
    # измеряемого плеча ловит следующая защита.
    check_controls(rows)
    check_measured_arms_built(rows)
    check_cache_did_not_measure_itself(rows)

    print("ОСЬ 2: ОТКУДА БЕРЁТСЯ ЗАВИСИМОСТЬ")
    print()
    print(expected["what_this_axis_measures"])
    print()

    header = ("%-16s %8s %8s %9s %11s %8s %-9s %s" %
              ("плечо", "холод_с", "тепло_с", "пересб_с", "бинарник_Б",
               "fmt", "линковка", "окно замера"))
    print(header)
    print("-" * len(header))
    for name in MEASURED:
        r = rows[name]
        print("%-16s %8.1f %8.1f %9.1f %11d %8s %-9s %s"
              % (name, r["cold_seconds"], r["warm_seconds"],
                 r["rebuild_seconds"], r["binary_bytes"],
                 r["fmt_version"], r["fmt_linkage"],
                 r["measurement_window"]))
    print()
    for name in MEASURED:
        print("  %-16s зависимость: %s"
              % (name, rows[name]["dependency_from"]))

    print()
    print("ОКНО ЗАМЕРА: ЧТО С ЧЕМ СРАВНИВАЕТСЯ")
    inside = comparable_arms(rows)
    outside = [n for n in MEASURED if n not in inside]
    print("  получение зависимости ВНУТРИ замера: %s" % ", ".join(inside))
    print("  получение зависимости ВНЕ замера:    %s" % ", ".join(outside))
    print("  Кратности по холодной сборке печатаются только внутри первой")
    print("  группы. У второй cold_seconds покрывает лишь компиляцию")
    print("  сервиса — это другая величина под тем же именем, и делить одну")
    print("  на другую значило бы сравнивать несравнимое.")

    print()
    print("ХОЛОДНАЯ СБОРКА ВНУТРИ ОДНОГО ОКНА")
    for name in sorted(inside, key=lambda n: rows[n]["cold_seconds"]):
        print("  %-16s %6.1f s" % (name, rows[name]["cold_seconds"]))
    print()
    print("  КРАТНОСТЬ ЗДЕСЬ НЕ ПЕЧАТАЕТСЯ НАМЕРЕННО: она не переживает")
    print("  смены сессии — и пара прогонов этого не показывает: она")
    print("  меряет повторяемость, а не разброс величины. Числа по")
    print("  сессиям ниже.")
    print("  Результат оси — ПОРЯДОК плеч, а не множитель; множитель,")
    print("  унесённый отсюда в статью, был бы числом, которое не")
    print("  переживает следующего запуска.")
    print("  Порядок в этом прогоне: %s"
          % " < ".join(sorted(inside, key=lambda n: rows[n]["cold_seconds"])))
    _print_session_drift()

    print()
    print("ПАРА system / system-prebaked: РОВНО МЕСТО УСТАНОВКИ")
    a, b = rows["system"], rows["system-prebaked"]
    for field in ("fmt_version", "fmt_version_declared", "fmt_linkage",
                  "binary_bytes", "cxx_version", "cmake_version"):
        mark = "совпадает" if a[field] == b[field] else "РАЗОШЛОСЬ"
        print("  %-21s %s: %r против %r" % (field, mark, a[field], b[field]))
    print("  холодная сборка: %.1f s против %.1f s — разница %.1f s и есть"
          % (a["cold_seconds"], b["cold_seconds"],
             a["cold_seconds"] - b["cold_seconds"]))
    print("  цена получения системной зависимости внутри замера.")

    print()
    print("КОНТРОЛИ")
    for name, (must_build, must_fail_at) in CONTROLS.items():
        r = rows[name]
        got = ("собралось за %.1f s" % r["cold_seconds"] if r["build_ok"]
               else "упало на шаге %r за %.1f s"
                    % (r["failure_stage"], r["cold_seconds"]))
        print("  %-15s %s" % (name, got))
        if r.get("fmt_version"):
            print("      измеренная версия fmt: %s" % r["fmt_version"])
        print("      %s" % expected["controls"][name])
    print("  Оговорка: «каждый контроль отличается от партнёра ровно одним»")
    print("  верна для system-offline и vcpkg-latest. У no-fmt отличий два —")
    print("  убран шаг получения зависимости И убран toolchain-файл; оба")
    print("  различия убирают зависимость, но читать это плечо как")
    print("  «изменили ровно одно» нельзя.")

    print()
    print("ИНСТРУМЕНТЫ ОДНИ И ТЕ ЖЕ У ВСЕХ ИЗМЕРЯЕМЫХ ПЛЕЧ")
    cxx = {rows[n]["cxx_version"] for n in MEASURED}
    cmake = {rows[n]["cmake_version"] for n in MEASURED}
    print("  компилятор: %s" % ("совпадает — " + cxx.pop() if len(cxx) == 1
                                else "РАЗОШЁЛСЯ: %r" % cxx))
    print("  cmake:      %s" % ("совпадает — " + cmake.pop() if len(cmake) == 1
                                else "РАЗОШЁЛСЯ: %r" % cmake))

    print()
    print("ВРЕМЕНА: что из них вообще что-то различает")
    for field, label, floor in (
            ("cold_seconds", "холодная сборка", None),
            ("warm_seconds", "тёплая (простой повтор)",
             RECORDED_FLOOR_SECONDS),
            ("rebuild_seconds", "пересборка своего кода",
             RECORDED_FLOOR_SECONDS)):
        _verdict(label,
                 {n: rows[n][field] for n in inside},
                 {n: run1[n][field] for n in inside} if run1 else None,
                 floor=floor)
    print()
    print("  Что про пересборку можно сказать УСТРОЙСТВОМ, а не числами:")
    print("  ARG SOURCE_STAMP стоит ПОСЛЕ шагов получения зависимости, и при")
    print("  смене метки Docker переиспользует эти слои, а заново выполняет")
    print("  только копирование исходника и сборку сервиса. Работы менеджера")
    print("  в этой величине нет по построению — не потому, что замер её не")
    print("  нашёл. Из «величина не различает плечи» само по себе следует")
    print("  только «этим замером различие не показано».")
    print()
    print("  Оговорка к пересборке: величина меряет повтор при СОХРАНЁННЫХ")
    print("  слоях Docker. Вне контейнера кэш пакетного менеджера ведёт себя")
    print("  иначе, и переносить отсюда вывод на ежедневную пересборку на")
    print("  рабочей машине стенд права не даёт.")

    print()
    print("РАЗМЕР И ЛИНКОВКА")
    pair_delta = abs(rows["vcpkg"]["binary_bytes"]
                     - rows["conan"]["binary_bytes"])
    print("  Контролируемая пара vcpkg/conan: одна ИЗМЕРЕННАЯ версия fmt (%s"
          % rows["conan"]["fmt_version"])
    print("  у обоих, снята из заголовков установленного пакета одним и тем")
    print("  же измерителем), один компилятор, одна статическая линковка.")
    print("  Расхождение в размере — %d Б из %d, то есть менеджер сам по себе"
          % (pair_delta, rows["conan"]["binary_bytes"]))
    print("  размер бинарника практически не меняет.")
    print()
    ratio = (rows["conan"]["binary_bytes"]
             / float(rows["system"]["binary_bytes"]))
    print("  Системное плечо: %d Б против %d Б — в %.1f раза меньше, потому"
          % (rows["system"]["binary_bytes"], rows["conan"]["binary_bytes"],
             ratio))
    print("  что fmt подключён динамически и в бинарник не попал. Точную")
    print("  дельту приписать линковке нельзя: у системного плеча ещё и")
    print("  другая версия fmt (%s против %s) — вторая переменная, которую"
          % (rows["system"]["fmt_version"], rows["conan"]["fmt_version"]))
    print("  стенд не отделяет.")
    print()
    print("  ПОЧЕМУ система линкует динамически: пакет libfmt-dev несёт %d"
          % rows["system"]["distro_fmt_static_archives"])
    print("  статических архивов (.a). Это измерено (dpkg -L), а не выведено")
    print("  из того, что ldd не показал libfmt: причина и следствие здесь")
    print("  разные утверждения, и проверка привязана к причине тоже.")

    print()
    print("ИЗМЕРЕННОЕ ПРОТИВ ЗАЯВЛЕННОГО")
    print("  Версия fmt у всех плеч снята ОДНИМ измерителем из заголовков")
    print("  установленного пакета. Раньше плечо conan брало её sed-ом из")
    print("  своего манифеста, и проверка «у vcpkg и conan одна версия»")
    print("  сравнивала измеренное с объявленным — то есть наполовину была")
    print("  тавтологией и прошла бы при сломанном разрешении зависимости.")
    for name in MEASURED:
        declared = rows[name].get("fmt_version_declared")
        if declared is None:
            continue
        mark = ("совпадает с измеренной"
                if declared.startswith(rows[name]["fmt_version"])
                else "РАСХОДИТСЯ с измеренной")
        print("  %-16s измерено %s, пакет объявляет %s — %s"
              % (name, rows[name]["fmt_version"], declared, mark))
    print("  Расхождение здесь не ошибка стенда: сравнения оси идут по")
    print("  измеренному числу, а заявленное записано, чтобы разница была")
    print("  видна, а не пропала.")

    print()
    print("РАСХОЖДЕНИЯ С ПРОГНОЗОМ (expected.json, блок build)")
    surprises = []
    for name in ("system", "system-prebaked"):
        if rows[name]["fmt_linkage"] != "dynamic":
            surprises.append("%s слинковал fmt не динамически" % name)
    for name in ("vcpkg", "conan"):
        if rows[name]["fmt_linkage"] != "static":
            surprises.append("%s слинковал fmt не статически" % name)
    order = sorted(inside, key=lambda n: rows[n]["cold_seconds"])
    if order != ["system", "conan", "vcpkg"]:
        surprises.append(
            "порядок холодной сборки оказался %s, а ждали "
            "system < conan < vcpkg" % " < ".join(order))
    for name in ("system", "system-prebaked"):
        declared = rows[name].get("fmt_version_declared")
        if declared and not declared.startswith(rows[name]["fmt_version"]):
            surprises.append(
                "%s: измеренная версия fmt (%s) не совпала с заявленной "
                "пакетом (%s)" % (name, rows[name]["fmt_version"], declared))
    if not surprises:
        print("  Прогон совпал с прогнозом по линковке и по порядку времён.")
    else:
        for text in surprises:
            print("  %s" % text)
        print("  ЭТО НАХОДКА: прогноз записан заранее и не подгоняется")
        print("  задним числом.")

    print()
    print("ПОВТОРЯЕМОСТЬ ВНУТРИ СЕССИИ (два прогона подряд)")
    print("  Это повторяемость, а не воспроизводимость: оба прогона сняты")
    print("  подряд на одной машине. Совпадение здесь НЕ означает, что")
    print("  величина устойчива вообще, — между сессиями времена и их")
    print("  кратности уезжают (см. fixtures/evidence/).")
    if not run1:
        print("  Второго прогона нет — повторяемость не проверена.")
    else:
        for name in MEASURED:
            a, b = run1[name]["cold_seconds"], rows[name]["cold_seconds"]
            worse, better = max(a, b), min(a, b)
            print("  %-16s холодная: %.1f s и %.1f s (в %.2f раза)"
                  % (name, a, b, worse / better if better else float("inf")))
            for field, label in (("binary_bytes", "размер"),
                                 ("fmt_version", "версия fmt"),
                                 ("fmt_linkage", "линковка")):
                if run1[name][field] != rows[name][field]:
                    print("     РАСХОЖДЕНИЕ по «%s»: %r против %r"
                          % (label, run1[name][field], rows[name][field]))


if __name__ == "__main__":
    main()
