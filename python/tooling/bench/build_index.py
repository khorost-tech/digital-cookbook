"""Локальный индекс пакетов: скачать один раз, дальше отдавать по HTTP.

ЗАЧЕМ. Сеть — самый крупный источник разброса в замере установки. PyPI
отвечает по-разному в разное время суток, зеркала кэшируют по-своему, и
разница между инструментами утонула бы в этом шуме. Локальный индекс
убирает сеть из уравнения, а инструменты продолжают работать своим
обычным путём: для них это просто другой индекс.

Плата названа честно: числа стенда — это «сколько стоит установка из
локального индекса», а не «из PyPI». Прогон против настоящего PyPI
остаётся отдельным плечом и помечается как невоспроизводимый.

ФОРМАТ. PEP 503: каталог `simple/<нормализованное-имя>/index.html` со
ссылками на файлы. Его понимают все три инструмента одинаково — это и
важно, иначе они делали бы разную работу.
"""

import hashlib
import html
import json
import re
import subprocess
import sys
from pathlib import Path

INDEX = Path("/index")
PACKAGES = INDEX / "packages"
SIMPLE = INDEX / "simple"


def normalize(name):
    """Нормализация имени по PEP 503."""
    return re.sub(r"[-_.]+", "-", name).lower()


# Платформенные пакеты, которых на Linux нет, но которые обязаны лежать в
# индексе. Poetry разрешает зависимости ДЛЯ ВСЕХ ПЛАТФОРМ сразу, и без них
# `poetry lock` падает с «Package colorama (0.4.6) not found», тогда как uv
# и pip разрешают под текущую платформу и не замечают их отсутствия.
#
# Это не костыль, а условие равенства: без этих колёс индекс годился бы для
# двух инструментов из трёх, и «одинаковые условия» были бы неправдой.
CROSS_PLATFORM = (
    ("colorama", "win_amd64"),
    ("pywin32", "win_amd64"),
    ("tzdata", "any"),
)


# Сборщик индекса обязан ходить в НАСТОЯЩИЙ PyPI, а не в тот индекс,
# который он строит. В окружении стенда PIP_INDEX_URL указывает на
# локальный, и без явного переопределения сборка индекса искала бы пакеты
# сама в себе: первый прогон проходил, а достройка падала с «not found».
UPSTREAM = "https://pypi.org/simple"


def download(requirements):
    PACKAGES.mkdir(parents=True, exist_ok=True)
    subprocess.run(
        ["pip", "download",
         "--requirement", str(requirements),
         "--dest", str(PACKAGES),
         "--index-url", UPSTREAM,
         # Только колёса: сборка из исходников добавила бы в замер работу
         # компилятора, которая к инструментам отношения не имеет и у всех
         # трёх одинакова.
         "--only-binary", ":all:"],
        check=True)

    for name, platform in CROSS_PLATFORM:
        args = ["pip", "download", name,
                "--dest", str(PACKAGES),
                "--index-url", UPSTREAM,
                "--no-deps", "--only-binary", ":all:"]
        if platform != "any":
            args += ["--platform", platform, "--python-version", "3.14"]
        subprocess.run(args, check=True)


def build_simple():
    """Строит индекс по тому, что реально лежит в каталоге."""
    if SIMPLE.exists():
        for path in sorted(SIMPLE.rglob("*"), reverse=True):
            path.unlink() if path.is_file() else path.rmdir()
    SIMPLE.mkdir(parents=True, exist_ok=True)

    by_project = {}
    for file in sorted(PACKAGES.iterdir()):
        if not file.is_file():
            continue
        # Имя проекта — до первого дефиса перед версией.
        stem = file.name
        if stem.endswith(".whl"):
            project = stem.split("-")[0]
        else:
            project = stem.rsplit("-", 1)[0]
        by_project.setdefault(normalize(project), []).append(file)

    for project, files in sorted(by_project.items()):
        directory = SIMPLE / project
        directory.mkdir(parents=True, exist_ok=True)
        links = []
        for file in sorted(files):
            digest = hashlib.sha256(file.read_bytes()).hexdigest()
            href = f"../../packages/{file.name}#sha256={digest}"
            links.append(f'    <a href="{href}">{html.escape(file.name)}</a>')
        body = "\n".join(links)
        (directory / "index.html").write_text(
            f"<!DOCTYPE html>\n<html><body>\n{body}\n</body></html>\n",
            encoding="utf-8")

    root = "\n".join(
        f'    <a href="{name}/">{name}</a>' for name in sorted(by_project))
    (SIMPLE / "index.html").write_text(
        f"<!DOCTYPE html>\n<html><body>\n{root}\n</body></html>\n",
        encoding="utf-8")
    return by_project


def main():
    requirements = Path(sys.argv[1] if len(sys.argv) > 1
                        else "/app/requirements.txt")
    download(requirements)
    projects = build_simple()
    total_files = sum(len(v) for v in projects.values())
    size = sum(f.stat().st_size for f in PACKAGES.iterdir() if f.is_file())
    print(json.dumps({
        "projects": len(projects),
        "files": total_files,
        "bytes": size,
        "mib": round(size / 1024 / 1024, 1),
    }, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
