#!/usr/bin/env python3
"""Маркер полного прогона — и то, что он описывает.

ЗАЧЕМ ЭТО ЕСТЬ.

Раньше маркер был строкой времени: «прогон закончился тогда-то». Такая
отметка протухает МОЛЧА. Она была датирована коммитом, после которого
набор проверок вырос на пятнадцать проверок, а фикстуры переснимались
ещё дважды, — и ни одна проверка её не читала. Отметка, которую никто не
сверяет, доказывает ровно то же, что честное слово.

ЧТО МАРКЕР ДЕРЖИТ ТЕПЕРЬ.

Состояние, при котором прогон был объявлен полным: суммы sha256 всех
фикстур осей, число проверок в наборе и число диверсий. Проверка
`test_run_marker.py` сверяет их с текущим состоянием стенда: правка
фикстуры или новая проверка делают маркер красным, а не тихо устаревшим.

ЧЕГО В МАРКЕРЕ НЕТ И ПОЧЕМУ. Сводок прогонов (`fixtures/manifests/`):
сводка второй платформы появляется в репозитории ПОСЛЕ прогона первой,
и маркер, считающий её, краснел бы у обеих платформ по очереди. Сводки
стережёт своя сверка.

Маркер пишется ПОСЛЕДНИМ шагом `bench/run-all.sh` — после всех осей,
всех разборов, сводки и всего набора проверок.
"""

import hashlib
import io
import json
import os
import platform
import sys
import time
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, HERE)

from scripts.console import force_utf8  # noqa: E402

force_utf8()

FIXTURES = os.path.join(HERE, "fixtures")
MARKER_PATH = os.environ.get("RUN_MARKER",
                             os.path.join(FIXTURES, "RUN-COMPLETE"))

# Фикстуры осей: то, ради чего прогон и делается.
FIXTURE_FILES = ("package.jsonl", "link.jsonl",
                 "build.jsonl", "build.run-1.jsonl",
                 "repro.jsonl", "repro.run-1.jsonl",
                 "siblings.jsonl", "siblings.run-1.jsonl")


def fixture_digests():
    out = {}
    for name in FIXTURE_FILES:
        path = os.path.join(FIXTURES, name)
        with io.open(path, "rb") as handle:
            out[name] = hashlib.sha256(handle.read()).hexdigest()
    return out


def check_count():
    """Число проверок в наборе — считается загрузкой набора, а не рукой."""
    loader = unittest.TestLoader()
    suite = loader.discover(os.path.join(HERE, "scripts"), pattern="test_*.py",
                            top_level_dir=HERE)
    if loader.errors:
        raise SystemExit("набор проверок не загрузился: %r" % loader.errors)
    return suite.countTestCases()


def sabotage_count():
    """Число диверсий — считается длиной списка в самом sabotage.py."""
    import importlib.util
    path = os.path.join(HERE, "scripts", "sabotage.py")
    spec = importlib.util.spec_from_file_location("sabotage_module", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return len(module.SABOTAGES)


def state():
    return {
        "finished_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "platform": platform.system().lower(),
        "fixtures_sha256": fixture_digests(),
        "checks": check_count(),
        "sabotages": sabotage_count(),
    }


def main():
    marker = state()
    with io.open(MARKER_PATH, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(json.dumps(marker, ensure_ascii=False, indent=2,
                                sort_keys=True) + "\n")
    print("маркер полного прогона: %s" % MARKER_PATH)
    print("  фикстур: %d, проверок: %d, диверсий: %d"
          % (len(marker["fixtures_sha256"]), marker["checks"],
             marker["sabotages"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
