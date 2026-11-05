#!/usr/bin/env python3
"""Маркер полного прогона — и то, что он описывает.

ЗАЧЕМ ЭТО ЕСТЬ.

У каждой оси стенда своя фикстура и свой прогон. Пока маркера не было,
«стенд прогнан целиком» держалось на памяти: четыре фикстуры, снятые в
разное время и при разных сборках, выглядят ровно так же, как четыре
фикстуры одной сессии. Оборванный прогон отличался от полного только
числом строк в одном файле — и отличить их было нечем.

ЧТО МАРКЕР ДЕРЖИТ. Состояние, при котором прогон объявлен полным: суммы
sha256 всех пяти фикстур, число проверок в наборе и число диверсий.
`scripts/test_run_marker.py` сверяет их с текущим состоянием стенда:
правка фикстуры, новая проверка или новая диверсия делают маркер
КРАСНЫМ, а не тихо устаревшим.

ПОЧЕМУ НЕ ОДНА СТРОКА ВРЕМЕНИ. Отметка «прогон закончился тогда-то»
протухает молча: её никто не сверяет. Время в маркере есть, но оно
подпись, а не предмет — сверяется состояние.

ЧЕГО В МАРКЕРЕ НЕТ И ПОЧЕМУ. Сводок прогонов (`fixtures/manifests/`):
сводка прошлой сессии заморожена и в новый прогон не попадает, а сводка
текущего прогона переписывается каждый раз. Маркер, считающий их суммы,
краснел бы сам от себя. Сводки стережёт своя сверка —
`scripts/test_run_manifest.py`.

Маркер пишется ПОСЛЕДНИМ шагом `bench/run-all.sh` — после всех пяти
осей, всех разборов, сводки и всего набора проверок.
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
if HERE not in sys.path:
    sys.path.insert(0, HERE)

from scripts.console import force_utf8  # noqa: E402

force_utf8()

FIXTURES = os.path.join(HERE, "fixtures")
MARKER_PATH = os.environ.get("RUN_MARKER",
                             os.path.join(FIXTURES, "RUN-COMPLETE"))

# Фикстуры осей: то, ради чего прогон и делается. Ось 1 снимает две —
# занятость по настройкам и ожидание в базе.
FIXTURE_FILES = ("variants.jsonl", "db-wait.jsonl", "pool.jsonl",
                 "prepared.jsonl", "cancel.jsonl")


def fixture_digests():
    """Суммы фикстур — ПО БАЙТАМ ФАЙЛА, а не по разобранным строкам.

    Разбор пропустил бы правку в пробелах, в порядке строк и в маркере
    завершения, а маркер как раз и отвечает на вопрос «те ли это файлы».
    """
    out = {}
    for name in FIXTURE_FILES:
        path = os.path.join(FIXTURES, name)
        with io.open(path, "rb") as handle:
            out[name] = hashlib.sha256(handle.read()).hexdigest()
    return out


def check_count():
    """Число проверок — загрузкой набора, а не рукой.

    Вписанное руками число протухает при первой же новой проверке и
    остаётся зелёным.
    """
    loader = unittest.TestLoader()
    suite = loader.discover(os.path.join(HERE, "scripts"), pattern="test_*.py",
                            top_level_dir=HERE)
    if loader.errors:
        raise SystemExit("набор проверок не загрузился: %r" % loader.errors)
    return suite.countTestCases()


def sabotage_count():
    """Число диверсий — длиной списка в самом sabotage_source.py."""
    import importlib.util
    path = os.path.join(HERE, "scripts", "sabotage_source.py")
    spec = importlib.util.spec_from_file_location("sabotage_source_module",
                                                  path)
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
