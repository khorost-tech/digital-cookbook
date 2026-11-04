"""Сборка и обмер плеч оси сборки из images/build-variants.json.

Отдельный модуль, а не дополнение к scripts/docker.py: там ось упаковки,
здесь ось сборки. У них разные вопросы (что получилось после упаковки
против того, откуда взялась зависимость) и разные наборы вариантов;
общего у них только каталог images/ и сам Docker.

ПОЧЕМУ КОД ВОЗВРАТА СНИМАЕТСЯ ОТДЕЛЬНО ОТ ТЕКСТА. Два плеча оси обязаны
ПАДАТЬ (no-fmt, vcpkg-latest), и для них важно не только «упало», но и
«упало там, где ожидалось». `docker build ... | grep ...` вернул бы код
grep вместо кода сборки, и сборка, оборвавшаяся не на том шаге,
выглядела бы правильным отказом. Поэтому здесь subprocess.run без
check=, returncode берётся из объекта, а текст — отдельным полем.
"""

import io
import json
import os
import re
import subprocess
import tempfile
import time

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
IMAGES_DIR = os.path.join(HERE, "images")
SPEC_PATH = os.path.join(IMAGES_DIR, "build-variants.json")


def load_spec():
    with io.open(SPEC_PATH, encoding="utf-8") as handle:
        return json.loads(handle.read())


def find_arm(name):
    for arm in load_spec()["arms"]:
        if arm["name"] == name:
            return arm
    raise KeyError("нет плеча %r в build-variants.json" % name)


def arm_tag(name):
    return "cpp-build-and-package-build:%s" % name


def prepare_bases():
    """Собирает общие образы инструментов.

    Не входит в замер ни одного плеча: это подготовка машины, а не
    сборка проекта. Собирается ОДИН раз на прогон — плечи отличаются
    друг от друга тем, что делают поверх этих образов, и если бы каждое
    ставило инструменты само, в замер попадала бы разная работа apt.
    """
    out = []
    for base in load_spec()["bases"]:
        dockerfile = os.path.join(IMAGES_DIR, base["dockerfile"])
        started = time.monotonic()
        proc = subprocess.run(
            ["docker", "build", "-f", dockerfile, "-t", base["tag"], HERE],
            capture_output=True, text=True,
            encoding="utf-8", errors="replace")
        if proc.returncode != 0:
            raise RuntimeError("не собрался базовый образ %s:\n%s"
                               % (base["tag"], proc.stderr[-4000:]))
        out.append({"tag": base["tag"],
                    "seconds": round(time.monotonic() - started, 3)})
    return out


def _timed_build(arm, tag, no_cache=False, extra_args=None):
    """Один прогон `docker build`, замеренный по стенным часам.

    Возвращает returncode, время и текст — все три отдельно. Ни одно из
    них не выводится из другого: плечо, которое обязано упасть,
    отличается от сломанного стенда именно тем, ГДЕ оно упало, и это
    видно только в тексте, а факт падения — только в коде возврата.
    """
    cmd = ["docker", "build",
           "-f", os.path.join(IMAGES_DIR, arm["dockerfile"]),
           "-t", tag]
    if no_cache:
        cmd.append("--no-cache")
    cmd += list(arm.get("build_args", []))
    cmd += list(extra_args or [])
    cmd.append(HERE)

    started = time.monotonic()
    proc = subprocess.run(cmd, capture_output=True, text=True,
                          encoding="utf-8", errors="replace")
    seconds = time.monotonic() - started
    return {
        "returncode": proc.returncode,
        "seconds": round(seconds, 3),
        "output": (proc.stdout or "") + (proc.stderr or ""),
        "cmd": cmd,
    }


def _run_in_image(tag, argv):
    proc = subprocess.run(["docker", "run", "--rm", tag] + argv,
                          capture_output=True, text=True,
                          encoding="utf-8", errors="replace")
    if proc.returncode != 0:
        raise RuntimeError("%s: %r вернул %d:\n%s"
                           % (tag, argv, proc.returncode, proc.stderr[-2000:]))
    return proc.stdout


def binary_bytes(tag):
    """Размер собранного бинарника, снятый копированием наружу.

    Через docker create + docker cp, а не `ls` внутри контейнера: способ
    один и тот же для любого образа, и он не зависит от того, какие
    утилиты в образе есть.
    """
    container = subprocess.run(["docker", "create", tag],
                               check=True, capture_output=True,
                               text=True,
                               encoding="utf-8", errors="replace").stdout.strip()
    try:
        with tempfile.TemporaryDirectory() as tmp:
            dest = os.path.join(tmp, "stand_service")
            subprocess.run(["docker", "cp",
                            "%s:/build/stand_service" % container, dest],
                           check=True, capture_output=True, text=True,
                           encoding="utf-8", errors="replace")
            return os.path.getsize(dest)
    finally:
        subprocess.run(["docker", "rm", "-f", container],
                       capture_output=True, text=True,
                       encoding="utf-8", errors="replace")


def fmt_linkage(tag):
    """static или dynamic — по тому, есть ли libfmt среди зависимостей.

    Смотрится именно libfmt, а не «динамический ли бинарник вообще»: все
    плечи оси динамически линкуются с libc и libstdc++, и по этому
    признаку они неразличимы. Вопрос оси — попала ли САМА ЗАВИСИМОСТЬ
    внутрь бинарника или осталась снаружи отдельным файлом.
    """
    out = _run_in_image(tag, ["ldd", "/build/stand_service"])
    for line in out.splitlines():
        if "libfmt" in line:
            return "dynamic"
    return "static"


def fmt_version(tag):
    """Версия fmt так, как её записало само плечо при сборке.

    Способ у всех плеч ОДИН И ТОТ ЖЕ: images/fmt-version.sh читает
    FMT_VERSION из заголовков установленного пакета. Раньше conan брал
    версию `sed`-ом из своего манифеста — то есть из файла, который
    написал автор стенда; проверка «у vcpkg и conan одна версия»
    сравнивала тогда измеренное число с объявленным и была наполовину
    тавтологией: она прошла бы и при сломанном разрешении зависимости.

    Числовой FMT_VERSION (110002) приводится к человеческому виду
    (11.0.2): у плеч vcpkg и conan одна и та же версия обязана выглядеть
    одинаково, иначе сравнение пары пришлось бы делать глазами.
    """
    return _decode_fmt_version(
        _run_in_image(tag, ["cat", "/fmt_version.txt"]).strip())


def _decode_fmt_version(raw):
    """110002 -> 11.0.2.

    Нераспознанное значение — не повод вернуть его как есть: строка,
    которая не является версией, в фикстуре выглядела бы измерением
    ровно так же, как настоящая версия.
    """
    if not re.fullmatch(r"\d{6}", raw):
        raise RuntimeError("измеритель версии вернул %r, а не FMT_VERSION"
                           % raw)
    return "%d.%d.%d" % (int(raw[:2]), int(raw[2:4]), int(raw[4:]))


def fmt_version_from_output(output):
    """Версия fmt из ЖУРНАЛА сборки — для плеча, которое упало.

    У упавшей сборки образа нет, и достать из него файл нечем. Но шаг
    измерения версии стоит ДО компиляции и печатает своё число в вывод;
    оттуда его и берём. Без этого утверждение «незакреплённый реестр
    подсовывает fmt такой-то версии» опиралось бы на доразведочную
    пробу, а в фикстуре у контроля стояло бы null.
    """
    found = re.findall(r"FMT_VERSION_MEASURED=(\d{6})", output)
    if not found:
        return None
    unique = sorted(set(found))
    if len(unique) != 1:
        raise RuntimeError("в журнале сборки несколько разных версий fmt: %r"
                           % unique)
    return _decode_fmt_version(unique[0])


def fmt_version_declared(tag):
    """Версия, которую ОБЪЯВЛЯЕТ пакет дистрибутива (dpkg-query).

    Держится рядом с измеренной намеренно и никогда её не подменяет.
    Заявленное и измеренное — разные утверждения: пакет может называться
    одной версией, а FMT_VERSION в заголовке, который увидит компилятор,
    говорить другую. Все сравнения оси идут по измеренной; заявленная
    записана, чтобы расхождение было видно, а не терялось.

    None у плеч, где системного пакета нет.
    """
    proc = subprocess.run(
        ["docker", "run", "--rm", tag, "cat", "/fmt_version_declared.txt"],
        capture_output=True, text=True,
        encoding="utf-8", errors="replace")
    if proc.returncode != 0:
        return None
    return proc.stdout.strip() or None


def distro_fmt_static_archives(tag):
    """Сколько .a несёт пакет libfmt-dev дистрибутива.

    Главная находка оси объясняется тем, что пакет дистрибутива несёт
    только .so, а стенд меряет лишь следствие — ldd не показывает
    libfmt. Если дистрибутив однажды положит в пакет статический архив,
    объяснение станет неверным, а следствие останется прежним, и стенд
    промолчит. Это число ловит такой день.

    Возвращает None у плеч, где системного пакета нет вовсе: отсутствие
    файла — не ноль архивов, а отсутствие предмета измерения.
    """
    proc = subprocess.run(
        ["docker", "run", "--rm", tag, "cat", "/fmt_static_archives.txt"],
        capture_output=True, text=True,
        encoding="utf-8", errors="replace")
    if proc.returncode != 0:
        return None
    return int(proc.stdout.strip())


def tool_versions(tag):
    """Версии компилятора и cmake в собранном образе плеча.

    Совпадение версий по всем плечам — условие того, что плечи вообще
    сравнимы. Оно обеспечено устройством (общий базовый образ), но
    обеспеченное устройством и проверенное — разные вещи: ARG или
    неудачно поставленный apt-get внутри плеча сломал бы это молча.
    """
    cxx = _run_in_image(tag, ["g++", "--version"]).splitlines()[0].strip()
    cmake = _run_in_image(tag, ["cmake", "--version"]).splitlines()[0].strip()
    return {"cxx_version": cxx, "cmake_version": cmake}


def failure_stage(output):
    """Где именно упала сборка — по тексту вывода Docker.

    Нужно только плечам-контролям, которые обязаны падать. Контроль,
    упавший не на своём шаге, ничего не доказывает: no-fmt, упавший на
    сети, и no-fmt, упавший на find_package, — это разные события с
    одинаковым кодом возврата.
    """
    text = output.lower()
    if "could not find a package configuration file provided by" in text:
        return "find_package"
    if "is not a member of 'fmt'" in text or "error: 'format' is not" in text:
        return "compile"
    if "could not resolve host" in text or "network is unreachable" in text \
            or "failed to connect" in text:
        return "network"
    return "unknown"


def measure_arm(arm_name, source_stamp):
    """Три замера одного плеча плюс обмер того, что получилось.

    cold  — docker build --no-cache: чистое состояние, зависимость
            добывается заново.
    warm  — повтор той же команды: Docker переиспользует слои. Величина
            задана заданием и заранее объявлена неинформативной —
            снимается, чтобы это можно было показать, а не утверждать.
    rebuild — повтор со СМЕНЁННЫМ SOURCE_STAMP: слои получения
            зависимости остаются в кэше, копирование исходника и сборка
            сервиса выполняются заново. Это и есть ежедневная пересборка
            своего кода.

    Порядок замеров существен: warm обязан идти сразу за cold, иначе
    между ними вклинится сборка с другим SOURCE_STAMP и «повтор»
    перестанет быть повтором.
    """
    arm = find_arm(arm_name)
    tag = arm_tag(arm_name)

    cold = _timed_build(arm, tag, no_cache=True)
    row = {
        "kind": "build" if arm["role"] == "measured" else "build-control",
        "variant": arm_name,
        "role": arm["role"],
        # Окно замера: входит ли получение зависимости в измеряемую
        # сборку. Плечи с разным окном по cold_seconds не сравнимы —
        # это две разные величины, и кратность между ними ничего не
        # значит. Поле пишется в КАЖДУЮ строку, а не только в описание
        # оси: разбор и проверки читают фикстуру, а не build-variants.
        "measurement_window": arm["measurement_window"],
        "dependency_from": arm["dependency_from"],
        "build_ok": cold["returncode"] == 0,
        "cold_returncode": cold["returncode"],
        "cold_seconds": cold["seconds"],
    }

    if cold["returncode"] != 0:
        # Плечо, которое обязано упасть. Тёплую и пересборку мерить не
        # у чего: образа нет. Записывается ГДЕ упало и последние строки
        # вывода — по ним разбор отличает правильный отказ от обрыва.
        row["failure_stage"] = failure_stage(cold["output"])
        row["failure_tail"] = "\n".join(
            [l for l in cold["output"].splitlines() if l.strip()][-6:])
        row["warm_seconds"] = None
        row["rebuild_seconds"] = None
        row["binary_bytes"] = None
        row["fmt_linkage"] = None
        # Версия снимается ДО компиляции и печатается в журнал сборки —
        # поэтому у плеча, упавшего на компиляции, она всё-таки есть.
        row["fmt_version"] = fmt_version_from_output(cold["output"])
        row["fmt_version_declared"] = None
        row["distro_fmt_static_archives"] = None
        return row

    warm = _timed_build(arm, tag, no_cache=False)
    if warm["returncode"] != 0:
        raise RuntimeError("%s: тёплая сборка упала, хотя холодная прошла:\n%s"
                           % (arm_name, warm["output"][-4000:]))

    rebuild = _timed_build(
        arm, tag, no_cache=False,
        extra_args=["--build-arg", "SOURCE_STAMP=%s" % source_stamp])
    if rebuild["returncode"] != 0:
        raise RuntimeError("%s: пересборка упала:\n%s"
                           % (arm_name, rebuild["output"][-4000:]))

    row.update({
        "warm_seconds": warm["seconds"],
        "rebuild_seconds": rebuild["seconds"],
        "binary_bytes": binary_bytes(tag),
        "fmt_linkage": fmt_linkage(tag),
        "fmt_version": fmt_version(tag),
        "fmt_version_declared": fmt_version_declared(tag),
        "distro_fmt_static_archives": distro_fmt_static_archives(tag),
        "failure_stage": None,
        "failure_tail": None,
    })
    row.update(tool_versions(tag))
    return row
