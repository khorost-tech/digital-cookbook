"""Что в итоге установлено — снимок окружения, не зависящий от инструментов.

ПОЧЕМУ НЕ СПРАШИВАЕМ У ИНСТРУМЕНТА. Напрашивается `pip freeze` или
`uv pip freeze`, но тогда проверка равенства зависела бы от одного из
сравниваемых: его представление о содержимом окружения стало бы эталоном
для остальных. Здесь читаются каталоги `*.dist-info` прямо из
site-packages — то, что физически лежит на диске.

ЧТО ИСКЛЮЧЕНО И ПОЧЕМУ. Poetry при создании окружения кладёт в него `pip`,
`uv venv` не кладёт ничего. Это разница в СОЗДАНИИ окружения, а не в
установке зависимостей, и включать её в сравнение наборов нельзя: хеши
разошлись бы всегда, и блокирующая проверка превратилась бы в постоянно
горящую лампочку, которую перестают замечать.

Сама разница при этом не прячется: снимок отдельно сообщает, какие
загрузочные пакеты нашлись у кого. В статье это отдельный сюжет — Poetry
управляет окружением, uv и pip ставят в готовое.
"""

import hashlib
import json
import re
import sys
from pathlib import Path

# Пакеты, появляющиеся при СОЗДАНИИ окружения, а не при установке
# зависимостей. Из сравнения исключаются, но пересчитываются отдельно.
BOOTSTRAP = frozenset({"pip", "setuptools", "wheel", "pkg-resources"})


def normalize(name):
    """Нормализация имени по PEP 503: Flask, flask и FLASK — одно и то же."""
    return re.sub(r"[-_.]+", "-", name).lower()


def site_packages(venv):
    """Каталог site-packages внутри окружения.

    Ищется, а не вычисляется по шаблону: у разных способов создания
    окружения версия в пути может отличаться, и захардкоженный
    python3.14 однажды молча перестал бы находить что-либо — а пустой
    список выглядит как «ничего не установлено», то есть как успех
    сравнения двух пустых наборов.
    """
    found = sorted(Path(venv).glob("lib/python*/site-packages"))
    if not found:
        raise FileNotFoundError(f"в {venv} нет site-packages — окружение "
                                f"не создано или создано иначе")
    return found[0]


def snapshot(venv):
    """Снимок установленного: имена, версии, хеш."""
    packages = {}
    bootstrap = {}
    for info in sorted(site_packages(venv).glob("*.dist-info")):
        stem = info.name[: -len(".dist-info")]
        name, _, version = stem.rpartition("-")
        if not name:
            continue
        key = normalize(name)
        (bootstrap if key in BOOTSTRAP else packages)[key] = version

    listing = [f"{n}=={v}" for n, v in sorted(packages.items())]
    payload = "\n".join(listing)
    return {
        "count": len(listing),
        "sha256": hashlib.sha256(payload.encode("utf-8")).hexdigest()[:16],
        "packages": listing,
        # Не часть сравнения, но и не тайна: разницу в создании окружения
        # видно здесь.
        "bootstrap": [f"{n}=={v}" for n, v in sorted(bootstrap.items())],
    }


def main():
    if len(sys.argv) < 2:
        print("нужен путь к окружению", file=sys.stderr)
        return 2
    snap = snapshot(sys.argv[1])
    if "--full" not in sys.argv:
        snap.pop("packages")
    print(json.dumps(snap, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
