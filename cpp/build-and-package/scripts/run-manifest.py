#!/usr/bin/env python3
r"""Сводка прогона: величины, которые публикуются, и на чём они сняты.

ЗАЧЕМ ЭТО ЕСТЬ.

Стенд снят на одной машине. Утверждение «на другой машине выйдет то же»
проверить по фикстурам НЕЛЬЗЯ: платформа в них не записана, а прогон
перезаписывает один и тот же набор файлов. Такое утверждение держалось бы
на памяти автора — ровно на том, против чего стенд и построен.

Теперь каждый прогон оставляет сводку в `fixtures/manifests/<платформа>.json`.
Сводки коммитятся, и сравнить их может кто угодно.

ПОЧЕМУ НЕ ХЕШИ ФИКСТУР.

Фикстуры несут метки прогона, времена сборки и времена изменения файлов —
они различаются при каждом прогоне даже на одной машине. Хеш файла не
доказал бы ничего и только создал бы видимость строгости. Сравнивать надо
ровно те величины, которые попадают в README и в статью.

ЧТО ЗДЕСЬ СРАВНИМО МЕЖДУ ПЛАТФОРМАМИ, А ЧТО НЕТ — СКАЗАНО ПРЯМО.

Сравнимо: размеры бинарников и образов, суммы sha256, версии тулчейнов и
fmt, исходы проб, способ линковки, разбор ELF. Всё это получается ВНУТРИ
контейнеров, из образов, закреплённых тегами; хостовая платформа на них
не влияет.

Не сравнимо и потому исключено ЯВНЫМ списком: время до первого ответа и
время сборки. Первое — это холодный запуск контейнера целиком, второе
зависит от диска, числа ядер и канала. Обе величины записываются в
фикстуры, но в сводку не попадают вовсе.

ПОЛЕ, ИСКЛЮЧЁННОЕ ИЗ СРАВНЕНИЯ, ОБЯЗАНО РАЗЛИЧАТЬСЯ.

Свидетель платформы — корень пути стенда, записанный в командных строках
`docker build` внутри самих фикстур (`/mnt/g/...` из WSL против `G:\...`
с хоста). Он и показывает, что фикстуры СНЯТЫ на разных платформах, а не
просто ПРОЧИТАНЫ дважды с одного диска. Если он совпал у двух сводок —
прогон был один, и вывод о двух платформах ложен. Это проверяет
`scripts/test_manifests_agree.py`.
"""

import io
import json
import os
import platform
import subprocess
import sys

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, HERE)

from scripts.console import force_utf8  # noqa: E402

force_utf8()

FIXTURES = os.path.join(HERE, "fixtures")
MANIFESTS = os.environ.get("MANIFESTS_DIR", os.path.join(FIXTURES, "manifests"))

# Величины, которые НЕ сравниваются между платформами. Список явный:
# молчаливое исключение превратило бы сравнение в фикцию. У каждого
# поля — причина.
NOT_COMPARED = {
    "first_response_ms": "время до первого ответа: холодный запуск "
                         "контейнера целиком, зависит от хоста",
    "first_response_min_ms": "то же",
    "first_response_max_ms": "то же",
    "first_response_spread_ratio": "то же",
    "spread_ms": "то же",
    "cold_seconds": "время сборки: зависит от диска, ядер и канала",
    "warm_seconds": "то же",
    "rebuild_seconds": "то же",
    "seconds_first": "то же",
    "seconds_second": "то же",
    "build_seconds": "то же",
    "binary_mtime": "время изменения файла внутри образа: улика того, что "
                    "сборок было две, а не одна; между прогонами обязано "
                    "различаться",
    "binary_mtime_first": "то же",
    "binary_mtime_second": "то же",
    "nonce_passed": "метка прогона: различается по построению",
    "nonce_read_back": "то же",
    "nonce_first": "то же",
    "nonce_second": "то же",
    "docker_argv": "командная строка несёт абсолютный путь стенда — из неё "
                   "берётся свидетель платформы, и сама она сравнению не "
                   "подлежит",
    "stand_root": "СВИДЕТЕЛЬ ПЛАТФОРМЫ: корень пути стенда в командах "
                  "docker build. Обязан РАЗЛИЧАТЬСЯ у разных сводок — "
                  "совпадение означает, что прогон был один",
}


# ВЕЛИЧИНЫ, КОТОРЫЕ СРАВНИВАЮТСЯ С ДОПУСКОМ, А НЕ ПОБАЙТНО.
#
# ПОЧЕМУ ДОПУСК, А НЕ ОКРУГЛЕНИЕ. Раньше упакованный размер образа шёл в
# сводку округлённым до килобайта. Округление — не допуск: оно молчит,
# пока число не перешагнёт границу разряда, и краснеет, как только
# перешагнёт. Запас до этой границы у плеча cpp-nofmt был 21 байт при
# наблюдаемом межплатформенном сдвиге 2–5 байт: чуть иной набор файлов
# контекста — и сверка покраснела бы не из-за содержимого. Допуск же
# назван числом и сравнивается с измеренным разбросом напрямую.
#
# ПОЧЕМУ ОГРУБЛЕНИЕ ВООБЩЕ ДОПУСТИМО. Не потому, что «настоящее
# изменение сдвинуло бы размер сильнее»: это неверно и опровергнуто
# собственной диверсией стенда — дописанные к бинарнику 8 байт сдвинули
# упакованный размер на 2 байта, причём ВНИЗ (слой сжимается). Допустимо
# оно потому, что содержимое стерегут ТОЧНО сравниваемые размерные
# величины РЯДОМ: `siblings_image_unpacked_bytes` на оси соседей и
# `package_image_unpacked_bytes` на оси упаковки. Та самая диверсия
# сдвинула распакованный объём ровно на 8 байт, и он бы покраснел.
# Упакованный размер здесь — величина о ХРАНЕНИИ, а не о содержимом.
#
# ЧИСЛО, КОТОРЫМ ОБОСНОВАН ДОПУСК, СЧИТАЕТСЯ, А НЕ ВПИСАНО ПРОЗОЙ.
# Раньше «наибольшее измеренное расхождение — 5 Б» стояло в тексте
# руками: новое расхождение большее пяти байт не изменило бы ни это
# число, ни сам допуск. Теперь оно читается из уже записанных сводок —
# ровно из тех величин, что и сравнивает `test_manifests_agree.py`.
#
# ЧИТАЕТСЯ ИЗ КАНОНИЧЕСКОГО КАТАЛОГА, А НЕ ИЗ MANIFESTS_DIR.
# Диверсии над сводками (`sabotage.py`) подставляют MANIFESTS_DIR с
# порченой копией именно затем, чтобы проверка увидела расхождение и
# упала. Если бы допуск считался из того же переопределяемого каталога,
# диверсия, сдвигающая величину «за пределы допуска», сама раздвинула бы
# допуск под собственный сдвиг — и не была бы поймана никогда. Базовая
# линия допуска поэтому читается из зафиксированного пути к настоящим
# сводкам репозитория независимо от того, что подставлено на время теста.
_CANONICAL_MANIFESTS = os.path.join(FIXTURES, "manifests")


def _existing_manifests():
    if not os.path.isdir(_CANONICAL_MANIFESTS):
        return {}
    out = {}
    for name in sorted(os.listdir(_CANONICAL_MANIFESTS)):
        if not name.endswith(".json"):
            continue
        path = os.path.join(_CANONICAL_MANIFESTS, name)
        with io.open(path, encoding="utf-8") as handle:
            out[name[:-5]] = json.loads(handle.read())
    return out


def _max_observed_gap(field):
    """Наибольшее расхождение поля между уже записанными сводками."""
    manifests = _existing_manifests()
    names = sorted(manifests)
    best = 0
    for i, name_a in enumerate(names):
        for name_b in names[i + 1:]:
            values_a = manifests[name_a]["values"].get(field, {})
            values_b = manifests[name_b]["values"].get(field, {})
            for arm in values_a:
                if arm in values_b:
                    best = max(best, abs(values_a[arm] - values_b[arm]))
    return best


_TOLERATED_FIELDS = (
    "package_image_bytes",
    "siblings_image_registry_bytes",
    "siblings_run1_image_registry_bytes",
)
# Пока сводка одна (или ни одной), расхождение измерить нечем: тогда
# берётся прежний проверенный запас явным числом, а не нулём — нулевой
# допуск покраснел бы на первом же байте различия упаковки.
_OBSERVED_GAP = max((_max_observed_gap(f) for f in _TOLERATED_FIELDS), default=0) or 5
TOLERANCE_BYTES = 5 * _OBSERVED_GAP
TOLERANCES = {
    "package_image_bytes": (
        TOLERANCE_BYTES,
        "упакованный размер образа: между платформами расходится на "
        "единицы байт. Измерено: в слой попадают биты прав доступа файлов "
        "контекста сборки — drvfs отдаёт 0777, клиент Windows 0755, и tar "
        "пишет разный режим (проверено tar -tvf над слоями двух образов). "
        "Допуск — пятикратный запас к наибольшему ИЗМЕРЕННОМУ расхождению "
        "(%d Б у пары сводок), а не круглое число: набор файлов контекста "
        "может отличаться, и сдвиг растёт вместе с ним. Содержимое "
        "стережёт не эта величина, а package_image_unpacked_bytes рядом — "
        "она сравнивается точно" % _OBSERVED_GAP),
    "siblings_image_registry_bytes": (
        TOLERANCE_BYTES,
        "то же самое на оси соседей; точный спутник — "
        "siblings_image_unpacked_bytes"),
    "siblings_run1_image_registry_bytes": (
        TOLERANCE_BYTES, "то же для первого прогона оси соседей"),
}


# КАЖДОЕ ПОЛЕ ФИКСТУРЫ ЛИБО ПУБЛИКУЕТСЯ, ЛИБО ОБЪЯСНЕНО.
#
# Без этой разметки величина, выпавшая из сводки, пропадала бы молча:
# сверка сравнивает то, что в сводках есть, и об отсутствующем не знает
# ничего. Пока сводок две и одна из них заморожена, пропажу ловит
# несовпадение наборов ключей; после полного прогона на обеих платформах
# эта защита исчезнет — обе сводки потеряют величину разом.
#
# PUBLISHED_FROM: поле фикстуры → имя группы в сводке.
# NOT_PUBLISHED:  поле фикстуры → почему его в сводке нет.
# Поля из NOT_COMPARED объяснены там и здесь не повторяются.
PUBLISHED_FROM = {
    "package": {
        "variant": "package_variants",
        "image_bytes": "package_image_bytes",
        "image_unpacked_bytes": "package_image_unpacked_bytes",
        "binary_bytes": "package_binary_bytes",
        "dynamic_deps": "package_dynamic_dep_count",
        "outcome": "package_resolve_outcome",
        "detail": "package_resolve_detail",
    },
    "build": {
        "build_ok": "build_ok",
        "binary_bytes": "build_binary_bytes",
        "fmt_version": "build_fmt_version",
        "fmt_version_declared": "build_fmt_version_declared",
        "fmt_linkage": "build_fmt_linkage",
        "failure_stage": "build_failure_stage",
        "measurement_window": "build_measurement_window",
        "distro_fmt_static_archives": "build_static_archives",
        "cxx_version": "build_cxx_version",
        "cmake_version": "build_cmake_version",
    },
    "repro": {
        "identical": "repro_identical",
        "sha256_first": "repro_sha256",
        "sha256_second": "repro_sha256",
        "binary_bytes_first": "repro_binary_bytes",
        "binary_bytes_second": "repro_binary_bytes",
        "cxx_version": "repro_toolchain",
        "cmake_version": "repro_toolchain",
        "fmt_version": "repro_toolchain",
        "no_cache": "repro_no_cache",
        "network": "repro_network",
    },
    "siblings": {
        "binary_bytes": "siblings_binary_bytes",
        "image_registry_bytes": "siblings_image_registry_bytes",
        "image_unpacked_bytes": "siblings_image_unpacked_bytes",
        "binary_sha256_in_final": "siblings_binary_sha256",
        "toolchain": "siblings_toolchain",
        "elf_type": "siblings_elf",
        "elf_has_interpreter": "siblings_elf",
        "elf_has_symtab": "siblings_elf",
        "elf_section_count": "siblings_elf",
        "dynamic_dep_count": "siblings_elf",
        "resolve_outcome": "siblings_resolve_outcome",
        "healthz_response_sha256": "siblings_healthz_sha256",
    },
}

NOT_PUBLISHED = {
    "package": {
        "kind": "служебное поле фикстуры: имя оси",
        "probe": "служебное: имя пробы, по нему строки группируются",
        # Версии инструментов оси упаковки досняты ПОЗЖЕ, чем заморожена
        # сводка второй платформы: тот прогон снимался в отдельной копии
        # стенда, и переснять его нечем. Опубликовать величину в одной
        # сводке из двух нельзя — наборы величин разойдутся, и сверка
        # между платформами перестанет читаться целиком, чтобы показать
        # три новых поля. Величина при этом не бесхозная: она
        # сравнивается ВНУТРИ прогона — ToolVersions и ControlledPair в
        # scripts/test_package_analysis.py. При следующем полном прогоне
        # на обеих платформах поля переезжают в PUBLISHED_FROM.
        "cxx_version": "версия компилятора образа сборки: снята после "
                       "заморозки сводки второй платформы, сравнивается "
                       "внутри прогона (ToolVersions, ControlledPair), а "
                       "не между платформами",
        "cmake_version": "то же",
        "libc_version": "то же",
    },
    "build": {
        "kind": "служебное поле фикстуры: имя оси",
        "role": "служебное: измеряемое плечо или контроль",
        "variant": "имя плеча — ключ всех групп сводки, а не величина",
        "cold_returncode": "код возврата холодной сборки: то же самое, что "
                           "build_ok, только числом",
        "failure_tail": "хвост вывода упавшей сборки: сырой текст с путями, "
                        "между платформами различается по построению",
        "dependency_from": "описание источника зависимости словами, а не "
                           "измеренная величина",
    },
    "repro": {
        "kind": "служебное поле фикстуры: имя оси",
        "role": "служебное: измеряемое плечо или контроль",
        "variant": "имя плеча — ключ всех групп сводки, а не величина",
        "build_dir_first": "путь каталога сборки внутри образа: улика "
                           "того, что сборок было две, а не одна",
        "build_dir_second": "то же",
        "cxx_version_first": "версии тулчейна публикуются одной тройкой "
                             "repro_toolchain; посборочные копии её дублируют",
        "cxx_version_second": "то же",
        "cmake_version_first": "то же",
        "cmake_version_second": "то же",
        "fmt_version_first": "то же",
        "fmt_version_second": "то же",
        "treatment_first": "строка лечения сборки: сырой текст",
        "treatment_second": "то же",
        "docker_argv_first": "командная строка несёт абсолютный путь стенда: "
                             "из неё берётся свидетель платформы, сравнению "
                             "она не подлежит",
        "docker_argv_second": "то же",
    },
    "siblings": {
        "kind": "служебное поле фикстуры: имя оси",
        "role": "служебное: измеряемое плечо или контроль",
        "variant": "имя плеча — ключ всех групп сводки, а не величина",
        "language": "подпись плеча словом",
        "treatment": "строка лечения сборки: сырой текст с флагами и "
                     "меткой прогона",
        "dockerfile": "имя файла сборки, а не величина",
        "strip_step": "описание шага strip словами",
        "image_registry_method": "описание способа замера, а не результат",
        "image_unpacked_method": "то же",
        "external_libraries_declared": "объявленные библиотеки: сырой список "
                                       "из манифестов плеч",
        "name_resolution": "описание механизма разрешения имён словами",
        "dynamic_deps": "список имён; в сводку идёт их ЧИСЛО, в siblings_elf",
        "elf_debug_sections": "список имён секций; в сводку идёт их наличие "
                              "через elf_has_symtab и общее число секций",
        "binary_sha256_in_builder": "сумма в образе СБОРКИ; в сводку идёт "
                                    "сумма в финальном образе — тот же файл "
                                    "после упаковки",
        "healthz_response": "сырое тело ответа; в сводке его сумма",
        "healthz_response_bytes": "длина тела ответа; сумма строже длины",
        "resolve_detail": "текст объяснения исхода",
        "resolve_body_compared": "какие поля тела сравнивались: описание "
                                 "метода",
        "resolve_excluded_fields": "какие поля исключены из сравнения: "
                                   "описание метода",
        "resolve_reported_variant": "имя, которым плечо назвало себя само; "
                                    "улика подмены, а не публикуемая величина",
        "first_response_samples": "отдельные замеры времени: время в сводку "
                                  "не идёт вовсе",
    },
}


def _rows(name):
    with io.open(os.path.join(FIXTURES, name), encoding="utf-8") as handle:
        text = handle.read()
    if "COMPLETE" not in text:
        raise SystemExit("%s: фикстура оборвана — сводку снимать не с чего"
                         % name)
    rows = [json.loads(l) for l in text.split("\n")
            if l.strip().startswith("{")]
    if not rows:
        raise SystemExit("%s: нет ни одной строки данных" % name)
    return rows


def platform_key():
    return platform.system().lower()


def stand_root():
    """Корень пути стенда, прочитанный ИЗ ФИКСТУР, а не из окружения.

    Именно он свидетельствует, что фикстуры сняты на этой платформе.
    Взятый из `os.getcwd()`, он говорил бы лишь о том, где запущен этот
    скрипт, — и сводка, снятая над чужими фикстурами, выглядела бы
    вторым прогоном.

    Берётся ПОСЛЕДНИЙ аргумент команды `docker build` — контекст сборки.
    Первая редакция искала в командной строке каталог с именем
    `build-and-package`, и это была ошибка: на втором хосте стенд лежал
    в каталоге с другим именем, корней нашлось ноль, и сводка не
    снялась. Имя каталога — не свойство стенда: читатель волен
    клонировать репозиторий куда угодно.
    """
    roots = set()
    for name in ("repro.jsonl", "repro.run-1.jsonl",
                 "siblings.jsonl", "siblings.run-1.jsonl"):
        for row in _rows(name):
            for key in ("docker_argv", "docker_argv_first",
                        "docker_argv_second"):
                argv = row.get(key)
                if not argv:
                    continue
                context = argv[-1]
                if not ("/" in context or "\\" in context):
                    raise SystemExit(
                        "последний аргумент docker build не похож на путь "
                        "контекста: %r. Свидетель платформы снять не с "
                        "чего." % context)
                roots.add(context)
    if len(roots) != 1:
        raise SystemExit(
            "в фикстурах найдено %d различных корней пути стенда (%r). "
            "Ожидался ровно один: иначе фикстуры собраны из разных "
            "прогонов и сводка описывала бы не один замер."
            % (len(roots), sorted(roots)))
    return roots.pop()


def published_values():
    """Ровно те величины, которые попадают в README и в статью.

    Ни одного времени здесь нет — по построению, а не по недосмотру:
    времена перечислены в NOT_COMPARED.
    """
    out = {}

    package = {}
    for row in _rows("package.jsonl"):
        package.setdefault(row["variant"], {})[row["probe"]] = row
    out["package_variants"] = sorted(package)
    # Упакованный размер образа публикуется ТОЧНЫМ ЧИСЛОМ, а сравнивается
    # с допуском (TOLERANCES): между платформами он расходится на единицы
    # байт из-за битов прав доступа файлов контекста. Точное число здесь
    # нужно затем, чтобы допуск можно было проверить по самой сводке, а
    # не поверить ему на слово.
    out["package_image_bytes"] = {
        name: probes["healthz"]["image_bytes"]
        for name, probes in package.items()}
    # Спутник, сравниваемый ТОЧНО: объём файлов итоговой файловой системы
    # образа. Именно он стережёт содержимое там, где упакованный размер
    # сравнивается с допуском.
    out["package_image_unpacked_bytes"] = {
        name: probes["healthz"]["image_unpacked_bytes"]
        for name, probes in package.items()}
    out["package_binary_bytes"] = {
        name: probes["healthz"]["binary_bytes"] for name, probes in package.items()}
    out["package_dynamic_dep_count"] = {
        name: len(probes["healthz"]["dynamic_deps"])
        for name, probes in package.items()}
    out["package_resolve_outcome"] = {
        name: probes["resolve"]["outcome"] for name, probes in package.items()}
    out["package_resolve_detail"] = {
        name: probes["resolve"]["detail"] for name, probes in package.items()}

    build = {r["variant"]: r for r in _rows("build.jsonl")}
    build1 = {r["variant"]: r for r in _rows("build.run-1.jsonl")}
    for label, table in (("build", build), ("build_run1", build1)):
        out["%s_ok" % label] = {n: r["build_ok"] for n, r in table.items()}
        out["%s_binary_bytes" % label] = {
            n: r["binary_bytes"] for n, r in table.items()}
        out["%s_fmt_version" % label] = {
            n: r["fmt_version"] for n, r in table.items()}
        out["%s_fmt_version_declared" % label] = {
            n: r["fmt_version_declared"] for n, r in table.items()}
        out["%s_fmt_linkage" % label] = {
            n: r["fmt_linkage"] for n, r in table.items()}
        out["%s_failure_stage" % label] = {
            n: r["failure_stage"] for n, r in table.items()}
        out["%s_measurement_window" % label] = {
            n: r["measurement_window"] for n, r in table.items()}
        out["%s_static_archives" % label] = {
            n: r["distro_fmt_static_archives"] for n, r in table.items()}
        out["%s_cxx_version" % label] = {
            n: r["cxx_version"] for n, r in table.items() if r.get("cxx_version")}
        out["%s_cmake_version" % label] = {
            n: r["cmake_version"] for n, r in table.items()
            if r.get("cmake_version")}

    for label, name in (("repro", "repro.jsonl"),
                        ("repro_run1", "repro.run-1.jsonl")):
        table = {r["variant"]: r for r in _rows(name)}
        out["%s_identical" % label] = {
            n: r["identical"] for n, r in table.items()}
        out["%s_sha256" % label] = {
            n: [r["sha256_first"], r["sha256_second"]]
            for n, r in table.items()}
        out["%s_binary_bytes" % label] = {
            n: [r["binary_bytes_first"], r["binary_bytes_second"]]
            for n, r in table.items()}
        out["%s_toolchain" % label] = {
            n: [r["cxx_version"], r["cmake_version"], r["fmt_version"]]
            for n, r in table.items()}
        out["%s_no_cache" % label] = {
            n: r["no_cache"] for n, r in table.items()}
        out["%s_network" % label] = {n: r["network"] for n, r in table.items()}

    for label, name in (("siblings", "siblings.jsonl"),
                        ("siblings_run1", "siblings.run-1.jsonl")):
        table = {r["variant"]: r for r in _rows(name)}
        out["%s_binary_bytes" % label] = {
            n: r["binary_bytes"] for n, r in table.items()}
        out["%s_image_registry_bytes" % label] = {
            n: r["image_registry_bytes"] for n, r in table.items()}
        out["%s_image_unpacked_bytes" % label] = {
            n: r["image_unpacked_bytes"] for n, r in table.items()}
        out["%s_binary_sha256" % label] = {
            n: r["binary_sha256_in_final"] for n, r in table.items()}
        out["%s_toolchain" % label] = {
            n: r["toolchain"] for n, r in table.items()}
        out["%s_elf" % label] = {
            n: [r["elf_type"], r["elf_has_interpreter"], r["elf_has_symtab"],
                r["elf_section_count"], r["dynamic_dep_count"]]
            for n, r in table.items()}
        out["%s_resolve_outcome" % label] = {
            n: r["resolve_outcome"] for n, r in table.items()}
        out["%s_healthz_sha256" % label] = {
            n: r["healthz_response_sha256"] for n, r in table.items()}

    return out


def environment():
    def ask(cmd):
        try:
            done = subprocess.run(cmd, capture_output=True, text=True,
                                  encoding="utf-8", errors="replace")
            return (done.stdout + done.stderr).strip().split("\n")[0]
        except Exception as error:  # noqa: BLE001 — сводка не должна падать
            return "не определить: %s" % error

    return {
        "platform": platform_key(),
        "platform_full": platform.platform(),
        "python": sys.version.split()[0],
        "docker": ask(["docker", "--version"]),
        # Свидетель платформы: прочитан из фикстур, а не из окружения.
        "stand_root": stand_root(),
    }


def main():
    if not os.path.isdir(MANIFESTS):
        os.makedirs(MANIFESTS)
    manifest = {
        "environment": environment(),
        "not_compared": NOT_COMPARED,
        "compared_with_tolerance": {
            name: {"tolerance_bytes": limit, "why": why}
            for name, (limit, why) in TOLERANCES.items()},
        "values": published_values(),
    }
    path = os.path.join(MANIFESTS, platform_key() + ".json")
    with io.open(path, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(json.dumps(manifest, ensure_ascii=False, indent=2,
                                sort_keys=True) + "\n")
    print("сводка прогона записана: %s" % path)
    print("свидетель платформы: %s" % manifest["environment"]["stand_root"])

    stored = sorted(n for n in os.listdir(MANIFESTS) if n.endswith(".json"))
    print("сводок на месте: %d (%s)" % (len(stored), ", ".join(stored)))
    if len(stored) < 2:
        print("ПОКА ОДНА ПЛАТФОРМА: о совпадении величин между платформами "
              "утверждать нечем, пока не появится вторая сводка.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
