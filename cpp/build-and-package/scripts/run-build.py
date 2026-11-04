#!/usr/bin/env python3
"""Снимает fixtures/build.jsonl — прогон оси сборки.

Порядок работы:
  1. собрать общие образы инструментов (вне замера — это подготовка
     машины, а не сборка проекта);
  2. на каждое плечо снять три времени: холодную сборку (--no-cache),
     тёплую (повтор) и пересборку своего кода (повтор со сменённым
     SOURCE_STAMP);
  3. у собравшихся плеч снять размер бинарника, способ линковки fmt,
     версию fmt и версии инструментов;
  4. у плеч-контролей, которые обязаны упасть, записать код возврата и
     ШАГ, на котором упало.

Строки пишутся через read_measurements (scripts/outcome.py): у времени
сборки нет исхода, и поля outcome в этих строках нет. Маркер COMPLETE
ставится в конце — оборванный прогон не должен выглядеть полным.

Имя выходного файла можно переопределить первым аргументом: фикстура
снимается дважды, и второй прогон не должен затирать первый до того,
как их сравнили.
"""

import io
import json
import os
import sys
import time

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, HERE)

from scripts import build as buildlib  # noqa: E402
from scripts.console import force_utf8  # noqa: E402

# Потоки переключаются в UTF-8 первым делом: на консоли cp1251
# печать «≠» роняла разбор с UnicodeEncodeError. Найдено прогоном
# на втором хосте.
force_utf8()

FIXTURES_DIR = os.path.join(HERE, "fixtures")
DEFAULT_FIXTURE = os.path.join(FIXTURES_DIR, "build.jsonl")

# Контракт строки оси сборки, проверяемый ПРИ ЗАПИСИ.
#
# Раньше обязательность этих полей держалась только проверками над уже
# снятой фикстурой. Разница существенная: проверка над фикстурой ловит
# пропуск поля через час после прогона, когда пересъёмка стоит ещё один
# прогон, а тут прогон падает сразу и не оставляет после себя файл,
# который выглядит полным.
REQUIRED_ALWAYS = (
    "kind", "variant", "role", "measurement_window", "dependency_from",
    "build_ok", "cold_returncode", "cold_seconds",
)
REQUIRED_WHEN_BUILT = (
    "warm_seconds", "rebuild_seconds", "binary_bytes", "fmt_linkage",
    "fmt_version", "cxx_version", "cmake_version",
)


def check_row_contract(row):
    """Строка обязана нести всё, на чём стоят утверждения оси.

    Пустая строка (None или пробелы) не считается заполненной: поле,
    существующее со значением null, не отличается от забытого ничем,
    кроме вида в файле.
    """
    missing = [f for f in REQUIRED_ALWAYS
               if f not in row or row[f] is None
               or (isinstance(row[f], str) and not row[f].strip())]
    if row.get("build_ok"):
        missing += [f for f in REQUIRED_WHEN_BUILT
                    if f not in row or row[f] is None
                    or (isinstance(row[f], str) and not row[f].strip())]
    if missing:
        raise SystemExit(
            "плечо %r: строка не удовлетворяет контракту, не заполнено %s — "
            "прогон не записан" % (row.get("variant"), ", ".join(missing)))


def _log(msg):
    print(msg, file=sys.stderr)
    sys.stderr.flush()


def main():
    out_path = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_FIXTURE
    if not os.path.isabs(out_path):
        out_path = os.path.join(FIXTURES_DIR, out_path)
    os.makedirs(FIXTURES_DIR, exist_ok=True)

    # Метка исходника уникальна для прогона: иначе на втором прогоне
    # «пересборка своего кода» попала бы в кэш первого и померила бы ноль.
    source_stamp = str(int(time.time()))
    _log("метка исходника для пересборки: %s" % source_stamp)

    _log("подготовка общих образов инструментов (вне замера)...")
    for base in buildlib.prepare_bases():
        _log("  %s — %.1f s" % (base["tag"], base["seconds"]))

    rows = []
    for arm in buildlib.load_spec()["arms"]:
        name = arm["name"]
        _log("=== %s (%s) ===" % (name, arm["role"]))
        row = buildlib.measure_arm(name, source_stamp)
        if row["build_ok"]:
            _log("  холодная %.1f s, тёплая %.1f s, пересборка %.1f s"
                 % (row["cold_seconds"], row["warm_seconds"],
                    row["rebuild_seconds"]))
            _log("  бинарник %d B, fmt %s, линковка %s"
                 % (row["binary_bytes"], row["fmt_version"],
                    row["fmt_linkage"]))
        else:
            _log("  сборка упала (код %d) на шаге %r за %.1f s"
                 % (row["cold_returncode"], row["failure_stage"],
                    row["cold_seconds"]))
        rows.append(row)

    for row in rows:
        check_row_contract(row)

    with io.open(out_path, "w", encoding="utf-8") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False, sort_keys=True))
            handle.write("\n")
        handle.write("COMPLETE\n")
    _log("записано %d строк в %s" % (len(rows), out_path))


if __name__ == "__main__":
    main()
