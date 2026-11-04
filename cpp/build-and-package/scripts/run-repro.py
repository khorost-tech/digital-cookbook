#!/usr/bin/env python3
"""Снимает fixtures/repro.jsonl — прогон оси воспроизводимости.

Порядок работы:
  1. собрать базовый образ с зависимостью (вне замера: это подготовка
     машины, а не сборка проекта);
  2. на каждое из шести плеч выполнить ДВЕ сборки одного и того же
     исходника, каждую с --no-cache и --network=none;
  3. у каждой сборки снять sha256 бинарника, его размер, метку прогона,
     время изменения файла, фактически применённое лечение и командную
     строку, которой она запускалась;
  4. записать строки, проверив контракт ПРИ ЗАПИСИ — и построчно, и по
     прогону целиком.

Контракт проверяется при записи, а не только проверками над готовой
фикстурой: строка с уликой кэша (совпавшие метки или совпавшее время
изменения) не должна вообще оказаться на диске — она выглядит
полноценной, а значит ничего.

ПОЧЕМУ КОНТРАКТ ЕЩЁ И ПО ПРОГОНУ. Улики кэша проверялись внутри плеча, а
между плечами — нет. Метка прогона у всех плеч одна и та же на позицию
(так задумано), каталоги сборки общие, а у пары plain-первая и
source-changed-первая совпадает даже весь набор аргументов сборки. То
есть шесть плеч, каждое из которых внутри себя безупречно, могли бы
оказаться одним плечом, повторённым шесть раз, и ни одна проверка этого
не заметила бы. Различность ВСЕХ двенадцати binary_mtime — единственное,
что это ловит.

Имя выходного файла можно переопределить первым аргументом: ось
снимается дважды, и второй прогон не должен затирать первый до того, как
их сравнили.

ПОЧЕМУ ПУТЬ РАЗБИРАЕТСЯ ДО ПЕРВОЙ СБОРКИ. Относительное имя
присоединяется к каталогу фикстур, поэтому аргумент вида
`fixtures/repro.jsonl` даёт `fixtures/fixtures/repro.jsonl` — каталога с
таким именем нет, и запись падает. Раньше это выяснялось ПОСЛЕ всех
двенадцати сборок: цена ошибки вызова равнялась полному прогону оси, и
она уже была заплачена один раз. Теперь каталог назначения проверяется
первым же действием, до подготовки базового образа.
"""

import io
import json
import os
import sys
import time

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, HERE)

from scripts import repro as reprolib  # noqa: E402
from scripts.console import force_utf8  # noqa: E402

# Потоки переключаются в UTF-8 первым делом: на консоли cp1251
# печать «≠» роняла разбор с UnicodeEncodeError. Найдено прогоном
# на втором хосте.
force_utf8()

FIXTURES_DIR = os.path.join(HERE, "fixtures")
DEFAULT_FIXTURE = os.path.join(FIXTURES_DIR, "repro.jsonl")

REQUIRED = (
    "kind", "variant", "role", "network", "no_cache",
    "docker_argv_first", "docker_argv_second",
    "build_dir_first", "build_dir_second",
    "nonce_first", "nonce_second",
    "binary_mtime_first", "binary_mtime_second",
    "treatment_first", "treatment_second",
    "sha256_first", "sha256_second", "identical",
    "binary_bytes_first", "binary_bytes_second",
    "cxx_version_first", "cxx_version_second",
    "cmake_version_first", "cmake_version_second",
    "fmt_version_first", "fmt_version_second",
    "cxx_version", "cmake_version", "fmt_version",
    "seconds_first", "seconds_second",
)

POSITIONS = ("first", "second")


def check_row_contract(row):
    """Строка обязана нести всё, на чём стоят утверждения оси.

    Пустое значение не считается заполненным: поле, существующее со
    значением null, не отличается от забытого ничем, кроме вида в файле.
    Отдельно проверяется то, без чего суммы не значат ничего:

      * метки прогона у двух сборок различаются — иначе второй образ
        есть первый образ;
      * время изменения бинарника различается — иначе слой был
        переиспользован и компиляция во второй раз не выполнялась;
      * identical согласуется с суммами — вывод, а не мнение;
      * объявленные no_cache и network отвечают команде, которая
        выполнялась;
      * каталог сборки из поля назван самим шагом сборки в лечении;
      * версии компилятора, cmake и fmt внутри плеча одни и те же.
    """
    missing = [f for f in REQUIRED
               if f not in row or row[f] is None
               or (isinstance(row[f], str) and not row[f].strip())]
    if missing:
        raise SystemExit(
            "плечо %r: строка не удовлетворяет контракту, не заполнено %s — "
            "прогон не записан" % (row.get("variant"), ", ".join(missing)))

    name = row["variant"]
    if row["nonce_first"] == row["nonce_second"]:
        raise SystemExit(
            "плечо %r: метка прогона у обеих сборок одна (%r) — второй образ "
            "есть первый образ, сравнивать нечего. Прогон не записан."
            % (name, row["nonce_first"]))
    if row["binary_mtime_first"] == row["binary_mtime_second"]:
        raise SystemExit(
            "плечо %r: бинарник внутри образа имеет одно и то же время "
            "изменения (%r) — слой переиспользован, второй сборки не было. "
            "Прогон не записан." % (name, row["binary_mtime_first"]))
    if row["identical"] != (row["sha256_first"] == row["sha256_second"]):
        raise SystemExit(
            "плечо %r: поле identical=%r расходится с суммами — оно обязано "
            "быть выводом из них. Прогон не записан."
            % (name, row["identical"]))

    for position in POSITIONS:
        argv = row["docker_argv_%s" % position]
        if ("--no-cache" in argv) != bool(row["no_cache"]):
            raise SystemExit(
                "плечо %r, сборка %s: поле no_cache=%r не отвечает команде, "
                "которая выполнялась. Объявление без команды — не улика и "
                "даже не объявление. Прогон не записан."
                % (name, position, row["no_cache"]))
        if "--network=%s" % row["network"] not in argv:
            raise SystemExit(
                "плечо %r, сборка %s: поле network=%r не отвечает команде. "
                "Прогон не записан." % (name, position, row["network"]))
        named = reprolib.treatment_field(row["treatment_%s" % position],
                                         "BUILD_DIR")
        if named != row["build_dir_%s" % position]:
            raise SystemExit(
                "плечо %r, сборка %s: каталог сборки в поле (%r) и каталог, "
                "который назвал сам шаг сборки (%r), — разные. На это поле "
                "опирается нормализация лечения. Прогон не записан."
                % (name, position, row["build_dir_%s" % position], named))

    for field, human in (("cxx_version", "компилятора"),
                         ("cmake_version", "cmake"),
                         ("fmt_version", "fmt")):
        if row["%s_first" % field] != row["%s_second" % field]:
            raise SystemExit(
                "плечо %r: между двумя сборками различается версия %s (%r "
                "против %r) — это не воспроизводимость, это разный "
                "инструмент. Прогон не записан."
                % (name, human, row["%s_first" % field],
                   row["%s_second" % field]))


def check_run_contract(rows):
    """Проверки, которые нельзя сделать над одной строкой.

    Все времена изменения бинарников прогона обязаны быть попарно
    различны. Совпадение любых двух означает, что две сборки прогона были
    одной, и неважно, в одном они плече или в разных.
    """
    seen = {}
    for row in rows:
        for position in POSITIONS:
            mtime = row["binary_mtime_%s" % position]
            where = "%s/%s" % (row["variant"], position)
            if mtime in seen:
                raise SystemExit(
                    "время изменения бинарника %r встречается дважды (%s и "
                    "%s) — две сборки прогона были одной. Прогон не записан."
                    % (mtime, seen[mtime], where))
            seen[mtime] = where


def resolve_out_path(argv):
    """Куда писать — решается ДО того, как запущена первая сборка.

    Относительное имя присоединяется к каталогу фикстур. Это удобно
    (`run-repro.py repro.run-1.jsonl`) и ровно поэтому опасно: вызов
    `run-repro.py fixtures/repro.jsonl` выглядит правильным, а даёт
    `fixtures/fixtures/repro.jsonl`. Каталога с таким именем нет, запись
    падает — и падала она в самом конце, после двенадцати сборок. Ошибка
    вызова стоила полного прогона оси; здесь она стоит одной секунды.

    Проверяется и право записи: каталог, в который нельзя писать, тоже
    выяснился бы только на последнем шаге.
    """
    given = argv[0] if argv else DEFAULT_FIXTURE
    out_path = given
    if not os.path.isabs(out_path):
        out_path = os.path.join(FIXTURES_DIR, out_path)
    parent = os.path.dirname(out_path)
    if not os.path.isdir(parent):
        raise SystemExit(
            "каталог назначения %r не существует. Аргумент %r разобран в "
            "%r: относительное имя присоединяется к каталогу фикстур %r. "
            "Сборки НЕ запускались." % (parent, given, out_path, FIXTURES_DIR))
    if not os.access(parent, os.W_OK):
        raise SystemExit(
            "в каталог назначения %r нельзя писать. Сборки НЕ запускались."
            % parent)
    return out_path


def _log(msg):
    print(msg, file=sys.stderr)
    sys.stderr.flush()


def _prepare_base():
    """Базовый образ с зависимостью — вне замера.

    Собирается тем же Dockerfile, что и на оси сборки: ось
    воспроизводимости не заводит своей версии зависимости, иначе она
    мерила бы другой предмет.
    """
    spec = reprolib.load_spec()
    for dockerfile, tag in (("Dockerfile.build-base",
                             "cpp-build-and-package-base:tools"),
                            ("Dockerfile.build-sysdeps", spec["base"])):
        code, out = reprolib._run(
            ["docker", "build", "-f",
             os.path.join(reprolib.IMAGES_DIR, dockerfile),
             "-t", tag, HERE])
        if code != 0:
            raise SystemExit("не собрался базовый образ %s:\n%s"
                             % (tag, out[-3000:]))
        _log("  базовый образ готов: %s" % tag)


def main():
    # Каталог назначения разбирается ПЕРВЫМ действием: ошибка вызова,
    # обнаруженная после двенадцати сборок, стоит полного прогона оси.
    os.makedirs(FIXTURES_DIR, exist_ok=True)
    out_path = resolve_out_path(sys.argv[1:])
    _log("писать будем в %s" % out_path)

    # Метка прогона уникальна: на втором прогоне оси метки обязаны
    # отличаться от первого, иначе второй прогон мог бы попасть в кэш
    # первого целиком.
    run_stamp = str(int(time.time()))
    _log("метка прогона: %s" % run_stamp)

    _log("подготовка базового образа (вне замера)...")
    _prepare_base()

    rows = []
    for arm in reprolib.load_spec()["arms"]:
        name = arm["name"]
        _log("=== %s (%s) ===" % (name, arm["role"]))
        row = reprolib.measure_arm(name, run_stamp)
        _log("  сборки: %.1f s и %.1f s"
             % (row["seconds_first"], row["seconds_second"]))
        _log("  метки:  %s и %s" % (row["nonce_first"], row["nonce_second"]))
        _log("  время:  %d и %d" % (row["binary_mtime_first"],
                                    row["binary_mtime_second"]))
        _log("  байт:   %d и %d" % (row["binary_bytes_first"],
                                    row["binary_bytes_second"]))
        _log("  суммы:  %s и %s — %s"
             % (row["sha256_first"][:16], row["sha256_second"][:16],
                "совпали" if row["identical"] else "РАЗОШЛИСЬ"))
        rows.append(row)

    for row in rows:
        check_row_contract(row)
    check_run_contract(rows)

    with io.open(out_path, "w", encoding="utf-8", newline="\n") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False, sort_keys=True))
            handle.write("\n")
        handle.write("COMPLETE\n")
    _log("записано %d строк в %s" % (len(rows), out_path))


if __name__ == "__main__":
    main()
