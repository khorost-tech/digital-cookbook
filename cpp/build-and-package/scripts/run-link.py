#!/usr/bin/env python3
"""Снимает fixtures/link.jsonl — предупреждение компоновщика на оси упаковки.

ЗАЧЕМ ЭТА ВЕЛИЧИНА.

Ось упаковки снимает два наблюдения. Первое — исход разрешения имён у
пары glibc-static / glibc-static-old различается: старое плечо имя не
разрешает, новое разрешает (fixtures/package.jsonl). Второе — что при
этом печатает компоновщик: у обоих плеч пары строка предупреждения о
нужде в разделяемых библиотеках той же версии совпадает дословно.

Второе наблюдение до сих пор стендом не снималось: его воспроизводили
вручную. Утверждение, снятое рукой один раз, живёт в отчёте, а не в
фикстуре, и перепроверить его нечем.

ЧТО ИЗ ЭТИХ ДВУХ НАБЛЮДЕНИЙ НЕ СЛЕДУЕТ. Причинное объяснение: почему
исходы разошлись и что стало с причиной предупреждения. Плечи пары
различаются образом сборки целиком (и glibc, и компилятор), а код
возврата EAI_SYSTEM называет род ошибки, а не механизм. Трактовка,
записанная до прогона в expected.json, отозвана — отзыв печатает
scripts/analyze-package.py вплотную к наблюдению.

ЧТО ИМЕННО СНИМАЕТСЯ.

На каждый РАЗЛИЧНЫЙ Dockerfile оси (пять из шести вариантов: у
musl-no-resolv тот же файл сборки, что и у musl-static, — это плечо
ЗАПУСКА, шаг линковки у него тот же самый):

  1. собрать стадию сборки варианта (`docker build --target build`);
  2. удалить готовый бинарник ВНУТРИ этого образа и собрать заново —
     линковка происходит гарантированно, а не берётся из кэша слоёв;
  3. записать перехваченные stdout и stderr процесса ЦЕЛИКОМ, признак
     присутствия предупреждения, нормализованные строки предупреждения и
     способ нормализации.

Ни одной строки предупреждения этот скрипт не знает: он записывает то,
что напечатал компоновщик. Признак — короткая подстрока (scripts/link.py:
MARKER) — тоже ложится в фикстуру, чтобы читатель видел, что именно
сопоставлялось.

ПОЧЕМУ ОТДЕЛЬНАЯ ФИКСТУРА, А НЕ ПОЛЕ В package.jsonl.

package.jsonl снят на двух платформах, и его величины сверяются между
сводками прогонов (fixtures/manifests/). Новое поле в нём означало бы
новую величину в сводке, а сводка второй платформы заморожена: пришлось
бы либо переснять обе платформы целиком, либо объявить величину
сравнённой между ними, не сравнив. Здесь она снята на ОДНОЙ платформе,
в одном прогоне, и файл отдельный именно затем, чтобы это было видно.
"""

import io
import json
import os
import sys

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, HERE)

from scripts import docker as dockerlib  # noqa: E402
from scripts import link as linklib  # noqa: E402
from scripts.console import force_utf8  # noqa: E402

force_utf8()

FIXTURES_DIR = os.path.join(HERE, "fixtures")
FIXTURE_PATH = os.path.join(FIXTURES_DIR, "link.jsonl")
IMAGES_DIR = os.path.join(HERE, "images")


def _log(msg):
    print(msg, file=sys.stderr)
    sys.stderr.flush()


def variants_to_measure():
    """По одному варианту на КАЖДЫЙ различный Dockerfile оси.

    Список не выписан руками: рукой написанный он молча разошёлся бы с
    variants.json — вариант, добавленный в ось, не попал бы в замер, и
    отсутствие строки выглядело бы как отсутствие предупреждения.
    Порядок — порядок variants.json.
    """
    with io.open(os.path.join(IMAGES_DIR, "variants.json"),
                 encoding="utf-8") as handle:
        variants = json.loads(handle.read())
    seen = {}
    order = []
    for variant in variants:
        key = variant["dockerfile"]
        if key in seen:
            continue
        seen[key] = variant["name"]
        order.append(variant)
    return order


def run_one(variant):
    name = variant["name"]
    _log("=== %s (%s)" % (name, variant["dockerfile"]))
    _log("  сборка стадии build...")
    builder_tag = dockerlib.build_builder(name)

    _log("  перезапуск шага линковки, перехват stderr...")
    result = dockerlib.relink_stderr(builder_tag)

    lines = linklib.warning_lines(result["stderr"])
    row = {
        "kind": "link",
        "variant": name,
        "dockerfile": variant["dockerfile"],
        "builder": variant["builder"],
        "libc": variant["libc"],
        "linkage": variant["linkage"],
        "link_returncode": result["returncode"],
        "link_step_ran": linklib.link_step_ran(result["stdout"]),
        "link_stdout": result["stdout"],
        "link_stderr": result["stderr"],
        "link_warning_present": linklib.has_warning(result["stderr"]),
        "link_warning_marker": linklib.MARKER,
        "link_warning_lines": lines,
        "link_warning_normalization": linklib.NORMALIZATION,
    }
    _log("  линковка шла: %s, код возврата %d, предупреждение: %s"
         % (row["link_step_ran"], row["link_returncode"],
            "ЕСТЬ" if row["link_warning_present"] else "нет"))
    for line in lines:
        _log("    %s" % line)
    return row


def main():
    os.makedirs(FIXTURES_DIR, exist_ok=True)
    rows = [run_one(variant) for variant in variants_to_measure()]

    with io.open(FIXTURE_PATH, "w", encoding="utf-8", newline="\n") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False, sort_keys=True))
            handle.write("\n")
        handle.write("COMPLETE\n")

    _log("записано %d строк в %s" % (len(rows), FIXTURE_PATH))
    return 0


if __name__ == "__main__":
    sys.exit(main())
