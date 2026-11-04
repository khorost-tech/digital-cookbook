"""Обмер оси воспроизводимости из images/repro-variants.json.

ЧТО ЗДЕСЬ МЕРЯЕТСЯ ФИЗИЧЕСКИ, А НЕ ОБЪЯВЛЯЕТСЯ.

  * Сумма — по байтам самого файла. Бинарник выкапывается из образа
    (docker create + docker cp) и хешируется снаружи контейнера. Не
    берётся ни digest образа (он про образ, а не про файл), ни вывод
    sha256sum внутри контейнера (тогда мерил бы контейнер).
  * Лечение — из файла, который написал САМ ШАГ СБОРКИ
    (/repro_treatment.txt). Скрипт знает, какие аргументы он передал, но
    в фикстуру идёт то, что применилось, а не то, что передавалось.
    Оттуда же берутся каталог сборки и признак того, выполнялся ли strip.
  * Метка прогона — читается обратно из собранного образа. Совпадение
    меток означает, что второй образ есть первый образ.
  * Время изменения бинарника — stat внутри образа. Если Docker
    переиспользовал слой, оно совпадёт до секунды, и «две сборки»
    окажутся одной.

ЧТО ЗДЕСЬ ОБЪЯВЛЯЕТСЯ, А НЕ МЕРЯЕТСЯ. Поля no_cache и network. Раньше
ключи --no-cache и --network=none были вписаны в build_once жёстко, а
поля брались из спецификации: убрав ключ из кода, можно было оставить
поле true, и все проверки прошли бы. Теперь ключи СТРОЯТСЯ из тех же
значений спецификации, и в строку фикстуры кладётся полная командная
строка docker build (docker_argv_first/second) — поле обязано отвечать
тому, что в команде есть. Уликой это всё равно не называется: командную
строку записывает тот же скрипт, который её и составил. Уликами
остаются метка и время изменения бинарника.

ПОЧЕМУ КОД ВОЗВРАТА СНИМАЕТСЯ ОТДЕЛЬНО ОТ ТЕКСТА. Конвейер
`docker build ... | grep ...` вернул бы код grep, и оборвавшаяся сборка
выглядела бы успешной. Здесь subprocess.run без check=, returncode
берётся из объекта.
"""

import hashlib
import io
import json
import os
import re
import subprocess
import tempfile
import time

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
IMAGES_DIR = os.path.join(HERE, "images")
SPEC_PATH = os.path.join(IMAGES_DIR, "repro-variants.json")

POSITIONS = ("first", "second")

# Поля лечения печатает сам шаг сборки, вида KEY=[значение].
_TREATMENT_FIELD_RE = r"(?:^|\s)%s=\[([^\]]*)\]"


def load_spec():
    with io.open(SPEC_PATH, encoding="utf-8") as handle:
        return json.loads(handle.read())


def find_arm(name):
    for arm in load_spec()["arms"]:
        if arm["name"] == name:
            return arm
    raise KeyError("нет плеча %r в repro-variants.json" % name)


def arm_tag(name, position):
    return "cpp-build-and-package-repro:%s-%s" % (name, position)


def _run(cmd):
    proc = subprocess.run(cmd, capture_output=True, text=True,
                          encoding="utf-8", errors="replace")
    return proc.returncode, (proc.stdout or "") + (proc.stderr or "")


def _run_in_image(tag, argv):
    code, out = _run(["docker", "run", "--rm", tag] + argv)
    if code != 0:
        raise RuntimeError("%s: %r вернул %d:\n%s" % (tag, argv, code,
                                                      out[-2000:]))
    return out


def build_command(arm, position, nonce):
    """Командная строка одной сборки. Ключи строятся из спецификации.

    --no-cache и метка прогона делают одно и то же дело двумя
    независимыми способами. Это не дублирование: ключ можно забыть при
    правке, и тогда прогон продолжит выглядеть правильным — метка,
    прочитанная обратно из образа, поймает такой день.

    Ключи не вписаны здесь буквами: они берутся из тех же полей
    спецификации, которые попадают в фикстуру. Разойтись объявлению и
    команде теперь негде, а сама команда кладётся в строку фикстуры и
    сверяется проверками.
    """
    spec = load_spec()
    build_dir = spec["build_dirs"][position]
    cmd = ["docker", "build",
           "-f", os.path.join(IMAGES_DIR, spec["dockerfile"]),
           "-t", arm_tag(arm["name"], position)]
    if spec["no_cache"]:
        cmd.append("--no-cache")
    cmd.append("--network=%s" % spec["network"])
    cmd += [
        "--build-arg", "BUILD_NONCE=%s" % nonce,
        "--build-arg", "BUILD_DIR=%s" % build_dir,
        "--build-arg", "REPRO_PINNED=%s" % arm["repro_pinned"],
        "--build-arg", "REPRO_DEBUG=%s" % arm["repro_debug"],
        "--build-arg", "REPRO_STRIP=%s" % arm["repro_strip"],
        "--build-arg", "EXTRA_CXX_FLAGS=%s" % arm["extra_cxx_flags"][position],
        HERE,
    ]
    return cmd


def build_once(arm, position, nonce):
    """Одна сборка плеча."""
    spec = load_spec()
    cmd = build_command(arm, position, nonce)
    started = time.monotonic()
    code, out = _run(cmd)
    seconds = round(time.monotonic() - started, 3)
    if code != 0:
        raise RuntimeError("%s/%s: сборка вернула %d:\n%s"
                           % (arm["name"], position, code, out[-4000:]))
    return {"tag": arm_tag(arm["name"], position), "seconds": seconds,
            "build_dir": spec["build_dirs"][position], "argv": cmd,
            "output": out}


def binary_sha256_and_size(tag):
    """Сумма и размер САМОГО ФАЙЛА, снятые снаружи контейнера."""
    code, out = _run(["docker", "create", tag])
    if code != 0:
        raise RuntimeError("не создать контейнер из %s: %s" % (tag, out))
    container = out.strip().splitlines()[-1]
    try:
        with tempfile.TemporaryDirectory() as tmp:
            dest = os.path.join(tmp, "stand_service")
            code, out = _run(["docker", "cp",
                              "%s:/stand_service" % container, dest])
            if code != 0:
                raise RuntimeError("не достать бинарник из %s: %s" % (tag, out))
            digest = hashlib.sha256()
            with open(dest, "rb") as handle:
                for chunk in iter(lambda: handle.read(1 << 20), b""):
                    digest.update(chunk)
            return digest.hexdigest(), os.path.getsize(dest)
    finally:
        _run(["docker", "rm", "-f", container])


def nonce_of(tag):
    """Метка прогона, ПРОЧИТАННАЯ ОБРАТНО из собранного образа."""
    return _run_in_image(tag, ["cat", "/nonce"]).strip()


def treatment_of(tag):
    """Лечение так, как его записал сам шаг сборки."""
    text = _run_in_image(tag, ["cat", "/repro_treatment.txt"]).strip()
    if not text.startswith("REPRO_TREATMENT "):
        raise RuntimeError("%s: файл лечения не похож на себя: %r"
                           % (tag, text[:120]))
    text = text[len("REPRO_TREATMENT "):]
    for field in ("BUILD_DIR", "FLAGS", "SOURCE_DATE_EPOCH", "STRIP"):
        treatment_field(text, field)
    return text


def treatment_field(treatment, name):
    """Одно поле лечения, напечатанного шагом сборки.

    Поле, которого нет, — это не пустое поле: строка лечения тогда не та,
    за которую себя выдаёт, и разбирать её дальше нельзя.
    """
    found = re.findall(_TREATMENT_FIELD_RE % re.escape(name), treatment)
    if len(found) != 1:
        raise ValueError("в лечении %r поле %s встречается %d раз(а)"
                         % (treatment[:160], name, len(found)))
    return found[0]


def binary_mtime(tag):
    """Время изменения бинарника внутри образа, в секундах эпохи."""
    return int(_run_in_image(tag, ["stat", "-c", "%Y", "/stand_service"])
               .strip())


def fmt_version(tag):
    from scripts.build import _decode_fmt_version
    return _decode_fmt_version(
        _run_in_image(tag, ["cat", "/fmt_version.txt"]).strip())


def tool_versions(tag):
    cxx = _run_in_image(tag, ["g++", "--version"]).splitlines()[0].strip()
    cmake = _run_in_image(tag, ["cmake", "--version"]).splitlines()[0].strip()
    return cxx, cmake


def normalized_treatment(row, position):
    """Лечение, приведённое к виду, не зависящему от каталога сборки.

    -ffile-prefix-map по устройству НАЗЫВАЕТ каталог сборки, а каталоги у
    двух сборок одного плеча разные намеренно — это и есть тот источник
    расхождения, который флаг закрепляет. Дословное сравнение строк
    лечения объявляло бы такую пару «двумя разными сборками», то есть
    ловило бы не подмену лечения, а само устройство флага.

    Здесь путь каталога заменяется меткой, и сравнение ловит именно
    подмену: другой набор флагов, другое SOURCE_DATE_EPOCH, лишний флаг
    компиляции, пропущенный strip. Заменяется РОВНО путь каталога сборки
    этой же сборки и ничего больше: замена «всего, что похоже на путь»
    стёрла бы и различия, которые ловить надо.

    Заменяемый путь берётся из поля build_dir_*, а поле пишет тот же
    скрипт, который потом и вырезает. Чтобы нормализация не верила полю
    на слово, каталог печатает сам шаг сборки, и отдельная проверка
    требует совпадения поля с напечатанным.
    """
    treatment = row["treatment_%s" % position]
    return treatment.replace(row["build_dir_%s" % position], "<BUILD_DIR>")


def measure_arm(arm_name, run_stamp):
    """Две сборки плеча и обмер обеих.

    Метка прогона складывается из общей метки прогона и НОМЕРА СБОРКИ:
    внутри плеча метки разные (удар по кэшу), между плечами одинаковые
    (иначе плечи различались бы не только лечением). Различность сборок
    между плечами доказывает не метка, а время изменения бинарника — оно
    проверяется по прогону целиком, в run-repro.py.
    """
    arm = find_arm(arm_name)
    spec = load_spec()

    measured = {}
    for position in POSITIONS:
        built = build_once(arm, position, "%s-%s" % (run_stamp, position))
        tag = built["tag"]
        sha, size = binary_sha256_and_size(tag)
        cxx, cmake = tool_versions(tag)
        measured[position] = {
            "seconds": built["seconds"],
            "build_dir": built["build_dir"],
            "argv": built["argv"],
            "sha256": sha,
            "bytes": size,
            "nonce": nonce_of(tag),
            "treatment": treatment_of(tag),
            "mtime": binary_mtime(tag),
            "cxx": cxx,
            "cmake": cmake,
            "fmt_version": fmt_version(tag),
        }

    a, b = measured["first"], measured["second"]
    row = {
        "kind": arm["kind"],
        "variant": arm_name,
        "role": arm["role"],
        "network": spec["network"],
        "no_cache": spec["no_cache"],
        "docker_argv_first": a["argv"],
        "docker_argv_second": b["argv"],
        "build_dir_first": a["build_dir"],
        "build_dir_second": b["build_dir"],
        "nonce_first": a["nonce"],
        "nonce_second": b["nonce"],
        "binary_mtime_first": a["mtime"],
        "binary_mtime_second": b["mtime"],
        "treatment_first": a["treatment"],
        "treatment_second": b["treatment"],
        "sha256_first": a["sha256"],
        "sha256_second": b["sha256"],
        # identical ВЫВОДИТСЯ из сумм здесь же, одной строкой. Записанное
        # отдельным решением, оно могло бы разойтись с суммами, и таблица
        # показывала бы одно, а суммы говорили другое.
        "identical": a["sha256"] == b["sha256"],
        "binary_bytes_first": a["bytes"],
        "binary_bytes_second": b["bytes"],
        "cxx_version_first": a["cxx"],
        "cxx_version_second": b["cxx"],
        "cmake_version_first": a["cmake"],
        "cmake_version_second": b["cmake"],
        "fmt_version_first": a["fmt_version"],
        "fmt_version_second": b["fmt_version"],
        "cxx_version": a["cxx"],
        "cmake_version": a["cmake"],
        "fmt_version": a["fmt_version"],
        "seconds_first": a["seconds"],
        "seconds_second": b["seconds"],
    }
    # Инструмент, подменившийся между двумя сборками плеча, объясняет
    # расхождение сумм не хуже подмены исходника — и объясняет неверно.
    # Сверяются все три версии, а не одна: раньше cmake внутри плеча не
    # сверялся вовсе.
    for field, human in (("cxx", "компилятора"), ("cmake", "cmake"),
                         ("fmt_version", "fmt")):
        if a[field] != b[field]:
            raise RuntimeError(
                "%s: у двух сборок разная версия %s (%s против %s) — это не "
                "воспроизводимость, это разный инструмент"
                % (arm_name, human, a[field], b[field]))
    return row
