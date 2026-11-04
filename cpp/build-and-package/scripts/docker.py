"""Сборка и обмер вариантов упаковки из images/variants.json.

Почему две сборки на вариант. Финальный образ (scratch/distroless) не
несёт инструментов — ни ldd, ни шелла. Библиотечные зависимости
бинарника снимаются в ОБРАЗЕ СБОРКИ, где ldd есть и матчится по libc с
самим бинарником (glibc-ldd против glibc-бинарника, musl-ldd против
musl-бинарника). Поэтому build_variant собирает multi-stage Dockerfile
дважды: с `--target build` (даёт образ сборки с /build/stand_service)
и целиком (даёт финальный образ для размера).
"""

import io
import json
import os
import subprocess
import tempfile

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
IMAGES_DIR = os.path.join(HERE, "images")


def _load_variants():
    with io.open(os.path.join(IMAGES_DIR, "variants.json"),
                 encoding="utf-8") as handle:
        return json.loads(handle.read())


def _find_variant(name):
    for variant in _load_variants():
        if variant["name"] == name:
            return variant
    raise KeyError("нет варианта %r в variants.json" % name)


def _run(cmd, **kwargs):
    return subprocess.run(cmd, check=True, capture_output=True, text=True,
                          encoding="utf-8", errors="replace",
                           **kwargs)


def image_size(tag):
    """Размер образа в байтах — docker image inspect --format '{{.Size}}'."""
    out = _run(["docker", "image", "inspect", tag,
                "--format", "{{.Size}}"]).stdout.strip()
    return int(out)


def image_digest(tag):
    """Локальный content-digest образа.

    Образ не пушится в registry (сборка только для замера), поэтому
    RepoDigests пуст — берём Id самого образа (sha256 конфига), это тот
    же способ, каким Docker отличает одно содержимое образа от другого
    локально.
    """
    out = _run(["docker", "image", "inspect", tag,
                "--format", "{{.Id}}"]).stdout.strip()
    return out


def dynamic_deps(builder_tag):
    """Список разделяемых зависимостей бинарника, собранного в builder_tag.

    Запускает ldd ВНУТРИ образа сборки (не финального) над
    /build/stand_service — только там гарантированно есть ldd, матчащий
    линковщик бинарника по libc. У статических бинарников ldd отвечает
    "not a dynamic executable" (glibc) или "Not a valid dynamic program"
    (musl) — это не ошибка вызова, а сам результат: пустой список.
    """
    proc = subprocess.run(
        ["docker", "run", "--rm", builder_tag,
         "ldd", "/build/stand_service"],
        capture_output=True, text=True,
        encoding="utf-8", errors="replace")
    output = (proc.stdout or "") + (proc.stderr or "")
    if "not a dynamic executable" in output.lower():
        return []
    if "not a valid dynamic program" in output.lower():
        return []
    deps = []
    for line in output.splitlines():
        line = line.strip()
        if not line:
            continue
        # "libfmt.so.10 => /usr/lib/x86_64-linux-gnu/libfmt.so.10 (0x...)"
        # "linux-vdso.so.1 (0x...)" — синтетическая запись ядра, не
        # библиотечная зависимость упаковки, её отбрасываем.
        name = line.split(" => ")[0].split()[0]
        if name.startswith("linux-vdso"):
            continue
        deps.append(name)
    return deps


def _binary_size(final_tag):
    """Размер бинарника внутри финального образа.

    Финальный образ может не иметь шелла (scratch, distroless) — размер
    снимается через docker create + docker cp, без exec внутри
    контейнера.
    """
    container = _run(["docker", "create", final_tag]).stdout.strip()
    try:
        with tempfile.TemporaryDirectory() as tmp:
            dest = os.path.join(tmp, "stand_service")
            _run(["docker", "cp",
                  "%s:/stand_service" % container, dest])
            return os.path.getsize(dest)
    finally:
        subprocess.run(["docker", "rm", "-f", container],
                        capture_output=True, text=True,
                        encoding="utf-8", errors="replace")


def build_builder(name):
    """Собирает ТОЛЬКО стадию сборки варианта и возвращает её тег.

    Отдельно от build_variant потому, что стадия сборки нужна и сама по
    себе: в ней живёт компоновщик, и только в ней можно перезапустить
    шаг линковки, чтобы перехватить его поток ошибок (relink_stderr).
    """
    variant = _find_variant(name)
    dockerfile = os.path.join(IMAGES_DIR, variant["dockerfile"])
    builder_tag = "cpp-build-and-package-builder:%s" % name
    _run(["docker", "build", "--target", "build",
          "-f", dockerfile, "-t", builder_tag, HERE])
    return builder_tag


def relink_stderr(builder_tag):
    """Перезапускает шаг линковки в образе сборки и отдаёт его потоки.

    ЗАЧЕМ ПЕРЕЗАПУСК, А НЕ ЧТЕНИЕ ЛОГА СБОРКИ. Вывод `docker build`
    смешивает потоки всех шагов и режется кэшем слоёв: у собранного
    ранее образа шага линковки в выводе не будет вовсе, и предупреждение
    пропало бы не потому, что его не печатают, а потому, что линковки не
    было. Здесь бинарник удаляется и собирается заново ВНУТРИ образа
    сборки — линковка происходит гарантированно, а её stderr достаётся
    отдельным потоком процесса.

    ПОЧЕМУ ЭТО ВЕЩЕСТВО, А НЕ ОБЪЯВЛЕНИЕ. Возвращается перехваченный
    вывод компоновщика как есть. Ни одна строка предупреждения здесь не
    записана: то, что печатает ld, приходит из ld.

    Улика того, что линковка действительно шла, — строка `Linking CXX
    executable` в stdout сборки. Без неё stderr без предупреждения
    означал бы лишь, что линковать было нечего.
    """
    proc = subprocess.run(
        ["docker", "run", "--rm", builder_tag, "sh", "-c",
         "rm -f /build/stand_service && cmake --build /build -j"],
        capture_output=True, text=True,
        encoding="utf-8", errors="replace")
    return {
        "returncode": proc.returncode,
        "stdout": proc.stdout or "",
        "stderr": proc.stderr or "",
    }


def _first_lines(text, count=2):
    """Первые непустые строки вывода, склеенные пробелом."""
    lines = [l.strip() for l in text.splitlines() if l.strip()]
    return " ".join(lines[:count])


def tool_versions(builder_tag):
    """Версии инструментов В ОБРАЗЕ СБОРКИ варианта упаковки.

    ЗАЧЕМ ЭТО ЗДЕСЬ. Ось упаковки не снимала версий вовсе: ни
    компилятора, ни libc, ни cmake — ни в images/variants.json, ни в
    fixtures/package.jsonl таких полей не было. Утверждение о том, чем
    различаются половины пары про устаревшее правило, держалось на ИМЕНИ
    ОБРАЗА в JSON, то есть на договорённости. Ось сборки такие версии
    снимает и объясняет почему: договорённость, которую никто не
    проверяет, ломается молча. Здесь то же самое и тем же способом.

    ЧТО ИМЕННО СНИМАЕТСЯ.
      cxx_version   — `g++ --version`, первая строка;
      cmake_version — `cmake --version`, первая строка;
      libc_version  — `ldd --version`, первые две непустые строки.

    ПОЧЕМУ ДВЕ СТРОКИ У libc. glibc печатает версию первой же строкой
    («ldd (Debian GLIBC 2.31-13+deb11u13) 2.31»), musl печатает имя и
    версию двумя строками («musl libc (x86_64)» / «Version 1.2.5») и
    завершается НЕНУЛЕВЫМ кодом. Требовать нулевого кода значило бы
    получить измеритель, работающий только на glibc; поэтому код
    возврата здесь не проверяется, а проверяется, что в измеренном есть
    цифра — пустая или бессодержательная строка величиной не является.

    ПОЧЕМУ В ОБРАЗЕ СБОРКИ, А НЕ В ФИНАЛЬНОМ. Финальный образ у
    статических вариантов — scratch: в нём нет ни компилятора, ни ldd, ни
    шелла. И libc, вшитая в статический бинарник, — это libc ОБРАЗА
    СБОРКИ, а не рантайма. Про libc рантайма динамических вариантов эта
    величина не говорит ничего.
    """
    def ask(argv, lines=1):
        proc = subprocess.run(["docker", "run", "--rm", builder_tag] + argv,
                              capture_output=True, text=True,
                              encoding="utf-8", errors="replace")
        return _first_lines((proc.stdout or "") + (proc.stderr or ""), lines)

    versions = {
        "cxx_version": ask(["g++", "--version"]),
        "cmake_version": ask(["cmake", "--version"]),
        "libc_version": ask(["ldd", "--version"], lines=2),
    }
    for field, value in sorted(versions.items()):
        if not any(ch.isdigit() for ch in value):
            raise RuntimeError(
                "%s: %s измерился как %r — в величине нет ни одной цифры, "
                "версией это не является" % (builder_tag, field, value))
    return versions


def build_variant(name):
    """Собирает вариант упаковки и возвращает его обмер.

    Ключи результата: image_tag, image_digest, image_bytes,
    image_unpacked_bytes, binary_bytes, dynamic_deps, cxx_version,
    cmake_version, libc_version.
    """
    variant = _find_variant(name)
    dockerfile = os.path.join(IMAGES_DIR, variant["dockerfile"])
    builder_tag = build_builder(name)
    final_tag = "cpp-build-and-package:%s" % name

    _run(["docker", "build", "-f", dockerfile, "-t", final_tag, HERE])

    measured = {
        "image_tag": final_tag,
        "image_digest": image_digest(final_tag),
        "image_bytes": image_size(final_tag),
        "image_unpacked_bytes": image_unpacked_bytes(final_tag),
        "binary_bytes": _binary_size(final_tag),
        "dynamic_deps": dynamic_deps(builder_tag),
    }
    # Версии инструментов снимаются ПОСЛЕ сборки, отдельными запусками
    # уже собранного образа сборки: в саму сборку они ничего не
    # добавляют, и размеры от их появления сдвинуться не могут.
    measured.update(tool_versions(builder_tag))
    return measured


def _resolve_run_args(run_args):
    """Подставляет {IMAGES_DIR} в аргументы запуска варианта.

    variants.json не может нести абсолютный путь (каталог стенда лежит
    по-разному на разных машинах), поэтому путь к каталогу images/
    держится плейсхолдером, а не готовой строкой.
    """
    return [arg.replace("{IMAGES_DIR}", IMAGES_DIR) for arg in run_args]


def run_variant(name, extra_docker_args=None, timeout=10):
    """Запускает финальный образ варианта с его run_args из variants.json.

    run_args — то самое плечо запуска у musl-no-resolv (подмена
    /etc/resolv.conf пустым файлом): у остальных вариантов оно пустое,
    и docker run для них ничем не отличается от обычного.

    -e STAND_VARIANT=<name> передаётся ЯВНО, для ВСЕХ вариантов, поверх
    того, что уже запечено в образ через ENV STAND_VARIANT в Dockerfile.
    Это не дублирование: musl-no-resolv делит один и тот же образ с
    musl-static (см. run_args выше) — запечённое в образ имя одно и то
    же для обоих плеч пары, и без явной переменной окружения оба плеча
    сообщали бы о себе первым именем. Запечённое в образ значение
    остаётся как есть — это запасной вариант для ручного `docker run`
    без обвязки стенда.

    Контейнер запускается отсоединённо (-d), чтобы можно было опросить
    его пробы, и снимается по имени в конце — вызывающий код отвечает
    за probe/остановку, здесь только формируется и исполняется команда.
    """
    variant = _find_variant(name)
    final_tag = "cpp-build-and-package:%s" % name
    cmd = ["docker", "run", "--rm", "-d", "-e", "STAND_VARIANT=%s" % name]
    cmd += _resolve_run_args(variant.get("run_args", []))
    if extra_docker_args:
        cmd += extra_docker_args
    cmd.append(final_tag)
    return _run(cmd).stdout.strip()


def image_unpacked_bytes(tag):
    """Объём файлов ИТОГОВОЙ файловой системы образа, в байтах.

    ЗАЧЕМ ЭТА ВЕЛИЧИНА НУЖНА ИМЕННО ЗДЕСЬ. Упакованный размер образа
    (`image_bytes`) между платформами расходится на единицы байт и
    поэтому сравнивается с допуском. Величина, сравниваемая с допуском,
    сама по себе содержимое не стережёт: диверсия
    `siblings-size-follows-the-file` показала, что дописанные к бинарнику
    8 байт сдвигают упакованный размер всего на 2 байта, причём вниз.
    Содержимое стережёт ТОЧНО сравниваемая размерная величина рядом — на
    оси соседей это распакованный объём образа, и на оси упаковки такая
    же нужна.

    ПОЧЕМУ НЕ СУММА ПО СЛОЯМ, как на оси соседей. Там образы собраны из
    scratch, каждый слой добавляет свои файлы, и сумма по слоям равна
    объёму итоговой файловой системы; наивный счёт там ронялся на
    повторном имени намеренно. Здесь образы стоят на debian и
    distroless: `etc/ld.so.cache` встречается в двух слоях, и сумма
    посчитала бы его дважды. Поэтому слои применяются ПО ПОРЯДКУ —
    файл из верхнего слоя вытесняет одноимённый из нижнего, запись
    удаления (`.wh.`) убирает файл, — и суммируются размеры того, что
    осталось. Это и есть объём распакованного образа, а не объём
    архива.

    Каталоги, ссылки и жёсткие ссылки размера не несут: у них в tar
    нулевой размер, и содержимое лежит по первому имени.
    """
    import gzip
    import tarfile
    import tempfile

    with tempfile.TemporaryDirectory() as tmp:
        archive_path = os.path.join(tmp, "image.tar")
        _run(["docker", "image", "save", tag, "-o", archive_path])
        root = os.path.join(tmp, "unpacked")
        with tarfile.open(archive_path) as archive:
            archive.extractall(root)
        with io.open(os.path.join(root, "manifest.json"),
                     encoding="utf-8") as handle:
            manifest = json.loads(handle.read())
        sizes = {}
        for layer in manifest[0]["Layers"]:
            path = os.path.join(root, *layer.split("/"))
            with io.open(path, "rb") as raw:
                head = raw.read(2)
            opener = gzip.open if head == b"\x1f\x8b" else io.open
            with opener(path, "rb") as stream:
                with tarfile.open(fileobj=stream, mode="r|") as layer_tar:
                    for member in layer_tar:
                        name = member.name.lstrip("./")
                        base = os.path.basename(name)
                        if base == ".wh..wh..opq":
                            raise RuntimeError(
                                "слой %s помечает каталог непрозрачным "
                                "(%s): применение слоёв здесь этого не "
                                "умеет, и величина перестала бы быть "
                                "объёмом образа" % (layer, member.name))
                        if base.startswith(".wh."):
                            removed = os.path.join(
                                os.path.dirname(name), base[len(".wh."):])
                            removed = removed.replace("\\", "/")
                            for key in [k for k in sizes
                                        if k == removed
                                        or k.startswith(removed + "/")]:
                                del sizes[key]
                            continue
                        if not member.isreg():
                            continue
                        sizes[name] = member.size
        return sum(sizes.values())
