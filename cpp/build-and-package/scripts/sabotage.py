#!/usr/bin/env python3
"""Диверсии оси сборки: применить, потребовать падения, откатить.

ЗАЧЕМ ЭТОТ ФАЙЛ ЛЕЖИТ В РЕПОЗИТОРИИ. В прошлой редакции 27 диверсий были
описаны в отчёте, а скрипты к ним жили во временном каталоге сессии и не
сохранились. Перепроверить их стало нечем: осталось честное слово. Стенд
про воспроизводимость не может держать свои проверки на честном слове —
поэтому диверсии здесь исполняемые.

ГЛАВНОЕ ПРАВИЛО. Диверсия, которая НЕ ПРИМЕНИЛАСЬ, — это ПРОВАЛЕННАЯ
проверка, а не пройденная. Правка по якорю, который не нашёлся или нашёлся
дважды, отвергается; сборка, не заметившая правки, засчитывается как
провал. Именно на этом стенд уже спотыкался: sed возвращал ошибку, якорь
оставался нетронутым, а диверсия выглядела успешной.

ГРУППЫ
  fixture   — мутация КОПИИ фикстуры, цель: уронить конкретную проверку.
              Файл в репозитории не трогается вовсе: проверки читают путь
              из переменной окружения BUILD_FIXTURE. Представительный
              набор, а не все проверки подряд: полнота здесь не окупается.
  analysis  — мутация КОПИИ фикстуры, цель: заставить разбор ОСТАНОВИТЬСЯ
              с внятным сообщением вместо печати таблицы поверх
              недействительных данных.
  siblings  — то же для оси соседей по языкам (задача 6): мутация КОПИИ
              fixtures/siblings.jsonl, цель — уронить проверку или
              остановить разбор. Отдельная группа: у оси своя фикстура и
              своя переменная окружения.
  repro     — то же для оси воспроизводимости (задача 5): мутация КОПИИ
              fixtures/repro.jsonl или fixtures/repro.run-1.jsonl, цель —
              уронить проверку или остановить разбор. Отдельная группа, а
              не часть fixture: у оси другая фикстура и другие переменные
              окружения. Диверсии над ПЕРВЫМ прогоном стоят отдельно: они
              показывают, что защиты дотягиваются и до него, а не только
              до последнего.
  expectations — правка того, что нельзя подменить через окружение, с
              побайтным откатом и восстановлением из снимка. Сюда входит
              expected.json (записанный ДО прогона) и исходники разбора и
              записи: путь к ним проверки из окружения не берут, подсунуть
              копию значило бы соврать о том, что они читают. Группа
              названа по первому своему случаю и шире него: сейчас
              большинство её диверсий правит analyze-repro.py и
              run-repro.py, а не expected.json.
  pinning   — правка того, чем закреплены внешние версии: дайджеста
              образа, суммы архива, суммы колеса, коммита реестра vcpkg.
              Цель — уронить проверку из test_pinning.py, которая
              это закрепление сторожит. Docker не нужен: правится
              текст, а не сборка.
  machinery — правка Dockerfile или флага сборки, цель: показать, что
              измеритель меряет, а не отвечает всегда одно и то же.
              Требует Docker и ключа --with-docker.

ЗАПУСК (из каталога стенда, в WSL):
  python3 scripts/sabotage.py list
  python3 scripts/sabotage.py run --group fixture
  python3 scripts/sabotage.py run --group analysis
  python3 scripts/sabotage.py run --group repro
  python3 scripts/sabotage.py run --group siblings
  python3 scripts/sabotage.py run --group machinery --with-docker
  python3 scripts/sabotage.py run --all --with-docker
  python3 scripts/sabotage.py run offline-control-failed

Код возврата 0 — все запрошенные диверсии применились и уронили свою
цель. Любой другой — где-то диверсия не применилась или цель устояла.
"""

import argparse
import hashlib
import io
import json
import os
import re
import shutil
import contextlib
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, HERE)

from scripts.console import force_utf8  # noqa: E402
from scripts.link import (MARKER as LINK_WARNING_MARKER,  # noqa: E402
                          LINK_STEP_MARKER)

# То же, что и в разборах: печать в консоль cp1251 иначе падает.
force_utf8()

IMAGES = os.path.join(HERE, "images")
FIXTURE = os.path.join(HERE, "fixtures", "build.jsonl")
FIXTURE_RUN1 = os.path.join(HERE, "fixtures", "build.run-1.jsonl")
REPRO_FIXTURE = os.path.join(HERE, "fixtures", "repro.jsonl")
REPRO_FIXTURE_RUN1 = os.path.join(HERE, "fixtures", "repro.run-1.jsonl")
SIBLINGS_FIXTURE = os.path.join(HERE, "fixtures", "siblings.jsonl")
SIBLINGS_FIXTURE_RUN1 = os.path.join(HERE, "fixtures", "siblings.run-1.jsonl")
PACKAGE_FIXTURE = os.path.join(HERE, "fixtures", "package.jsonl")
LINK_FIXTURE = os.path.join(HERE, "fixtures", "link.jsonl")
RUN_MARKER = os.path.join(HERE, "fixtures", "RUN-COMPLETE")

# Суммы, которые портят диверсии по закреплению. Записаны здесь, а не в
# теле диверсии: якорь обязан совпасть с тем, что лежит в закреплённом
# файле, и разойтись эти две записи не должны молча — несовпадение
# отвергает правку, а не проходит мимо.
FMT_ARCHIVE_SHA256 = "6cb1e6d37bdcb756dbbe59be438790db409cdb4868c66e888d5df9f13f7c027f"
SIX_WHEEL_SHA256 = "4721f391ed90541fddacab5acf947aa0d3dc7d27b2e1e8eda2be8970586c3274"


# --------------------------------------------------------------------
# Мелкая механика


def _read_rows(path):
    with io.open(path, encoding="utf-8") as handle:
        text = handle.read()
    return [json.loads(l) for l in text.split("\n") if l.strip().startswith("{")]


def _write_rows(path, rows):
    with io.open(path, "w", encoding="utf-8", newline="\n") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False, sort_keys=True))
            handle.write("\n")
        handle.write("COMPLETE\n")


def _run(cmd, env=None, cwd=None):
    """subprocess.run без конвейера.

    Конвейер `cmd | grep ...` вернул бы код grep, и диверсия, оборвавшая
    команду не там, где ожидалось, выглядела бы правильным результатом.
    Код возврата снимается отдельно от текста всегда.
    """
    full_env = dict(os.environ)
    full_env.update(env or {})
    proc = subprocess.run(cmd, capture_output=True, text=True,
                          encoding="utf-8", errors="replace",
                          env=full_env, cwd=cwd or HERE)
    return proc.returncode, (proc.stdout or "") + (proc.stderr or "")


class SabotageDidNotApply(Exception):
    """Диверсия не легла на своё место. Это провал, а не успех."""


@contextlib.contextmanager
def _marker_restored():
    """Маркер полного прогона обязан пережить набор диверсий.

    ПОЧЕМУ ЭТО ЕСТЬ. Диверсия над fixtures/RUN-COMPLETE правит маркер и
    возвращает его из побайтного снимка, но снимок живёт внутри одной
    диверсии. Прогон, оборванный между наложением и откатом — Docker не
    поднялся, машинерия упала, читатель нажал Ctrl+C, — оставлял маркер
    подменённым или не оставлял вовсе. Стенд после этого выглядит
    НЕПРОВЕРЕННЫМ, хотя маркер лежит в коммите и суммы фикстур
    совпадают: ровно на этом остановили публикацию.

    Снимок снимается ДО первой диверсии и возвращается ПОСЛЕ последней,
    что бы между ними ни случилось. Отсутствие маркера до прогона —
    допустимое состояние (`bench/run-all.sh` снимает его первым делом),
    и оно восстанавливается так же честно: не было — не будет.
    """
    before = None
    if os.path.exists(RUN_MARKER):
        before = io.open(RUN_MARKER, "rb").read()
    try:
        yield
    finally:
        if before is None:
            if os.path.exists(RUN_MARKER):
                os.remove(RUN_MARKER)
        else:
            io.open(RUN_MARKER, "wb").write(before)
            if io.open(RUN_MARKER, "rb").read() != before:
                raise RuntimeError(
                    "маркер полного прогона не вернулся к исходному виду")


def _tree_state():
    """Состояние рабочего дерева стенда глазами git.

    Сравнивается ДО и ПОСЛЕ прогона, а не с чистотой вообще: у читателя
    в дереве могут лежать свои правки, и требовать от него чистоты было
    бы проверкой не про диверсии. Значение имеет то, что оставил после
    себя НАБОР.
    """
    code, out = _run(["git", "status", "--porcelain", "--", "."], cwd=HERE)
    if code != 0:
        return None
    return sorted(line for line in out.splitlines() if line.strip())


def _patch_file(path, anchor, replacement):
    """Правка по якорю, который обязан найтись РОВНО ОДИН раз.

    Якорь, найденный дважды, — это не место правки, а совпадение: правка
    легла бы не туда, куда задумано, и результат диверсии ничего бы не
    значил. Такой случай отвергается.
    """
    text = io.open(path, encoding="utf-8").read()
    found = text.count(anchor)
    if found != 1:
        raise SabotageDidNotApply(
            "якорь %r найден %d раз(а) в %s — правка отвергнута"
            % (anchor[:60], found, os.path.basename(path)))
    io.open(path, "w", encoding="utf-8", newline="\n").write(
        text.replace(anchor, replacement))
    after = io.open(path, encoding="utf-8").read()
    if after == text:
        raise SabotageDidNotApply("файл %s не изменился" % path)


def _git_restore(relpath):
    code, out = _run(["git", "checkout", "--", relpath], cwd=HERE)
    if code != 0:
        raise RuntimeError("не откатить %s: %s" % (relpath, out))
    code, out = _run(["git", "diff", "--quiet", "--", relpath], cwd=HERE)
    if code != 0:
        raise RuntimeError("после отката %s остались изменения" % relpath)


# --------------------------------------------------------------------
# Диверсии над копией фикстуры


def _mutate(tmpdir, mutate_main=None, mutate_run1=None):
    """Копирует обе фикстуры во временный каталог и правит копии."""
    main = os.path.join(tmpdir, "build.jsonl")
    run1 = os.path.join(tmpdir, "build.run-1.jsonl")
    shutil.copyfile(FIXTURE, main)
    shutil.copyfile(FIXTURE_RUN1, run1)
    for path, mutate in ((main, mutate_main), (run1, mutate_run1)):
        if mutate is None:
            continue
        rows = _read_rows(path)
        before = json.dumps(rows, sort_keys=True)
        by_name = {r["variant"]: r for r in rows}
        result = mutate(by_name)
        rows = result if result is not None else list(by_name.values())
        if json.dumps(rows, sort_keys=True) == before:
            raise SabotageDidNotApply("копия фикстуры %s не изменилась"
                                      % os.path.basename(path))
        _write_rows(path, rows)
    return main, run1


def _fixture_case(mutate, test, mutate_run1=None):
    """Мутация копии обязана уронить названную проверку."""
    def action():
        with tempfile.TemporaryDirectory() as tmp:
            main, run1 = _mutate(tmp, mutate, mutate_run1)
            code, out = _run(
                [sys.executable, "-m", "unittest",
                 "scripts.test_build_analysis." + test],
                env={"BUILD_FIXTURE": main, "BUILD_FIXTURE_RUN1": run1})
            if code == 0:
                return False, ("проверка %s УСТОЯЛА под диверсией — она не "
                               "про то, что мутировано" % test)
            return True, "проверка %s упала, как и требовалось" % test
    return action


def _analysis_case(mutate, expect_text, mutate_run1=None, expect_ok=False):
    """Мутация копии обязана заставить разбор остановиться (или сказать)."""
    def action():
        with tempfile.TemporaryDirectory() as tmp:
            main, run1 = _mutate(tmp, mutate, mutate_run1)
            code, out = _run(
                [sys.executable, os.path.join(HERE, "scripts",
                                              "analyze-build.py")],
                env={"BUILD_FIXTURE": main, "BUILD_FIXTURE_RUN1": run1})
            if expect_ok:
                if code != 0:
                    return False, "разбор упал (код %d), а должен был " \
                                  "договорить: %s" % (code, out[-500:])
            elif code == 0:
                return False, ("разбор напечатал таблицу поверх "
                               "недействительных данных — код возврата 0")
            if expect_text not in out:
                return False, ("в выводе разбора нет %r; получено: %s"
                               % (expect_text, out[-500:]))
            return True, "разбор ответил %r" % expect_text
    return action


# --------------------------------------------------------------------
# Диверсии над машинерией замера (нужен Docker)


def _docker_build(dockerfile, tag, extra=()):
    cmd = ["docker", "build", "-f", os.path.join(IMAGES, dockerfile),
           "-t", tag] + list(extra) + [HERE]
    return _run(cmd)


def _machinery_libfmt_in_base():
    """Подложить libfmt-dev в базовый образ — контроль no-fmt обязан
    ПОЗЕЛЕНЕТЬ.

    Контроль, который не умеет позеленеть, не проверяет ничего: он мог бы
    падать по любой другой причине и выглядеть так же.
    """
    rel = os.path.join("images", "Dockerfile.build-base")
    path = os.path.join(HERE, rel)
    try:
        _patch_file(path, "      python3-pip python3-venv \\\n",
                    "      python3-pip python3-venv libfmt-dev \\\n")
        code, out = _docker_build("Dockerfile.build-base",
                                  "sabotage-base:tools")
        if code != 0:
            return False, "базовый образ с диверсией не собрался: %s" % out[-400:]
        code, out = _run(["docker", "run", "--rm", "sabotage-base:tools",
                          "ls", "/usr/include/fmt/core.h"])
        if code != 0:
            raise SabotageDidNotApply(
                "libfmt-dev не появился в образе — диверсия не применилась")
        # no-fmt поверх подложенного базового образа обязан СОБРАТЬСЯ.
        tmp_df = os.path.join(IMAGES, "Dockerfile.sabotage-nofmt")
        body = io.open(os.path.join(IMAGES, "Dockerfile.build-nofmt"),
                       encoding="utf-8").read()
        body = body.replace("FROM cpp-build-and-package-base:tools",
                            "FROM sabotage-base:tools")
        io.open(tmp_df, "w", encoding="utf-8", newline="\n").write(body)
        try:
            code, out = _docker_build("Dockerfile.sabotage-nofmt",
                                      "sabotage:nofmt")
        finally:
            os.remove(tmp_df)
        if code != 0:
            return False, ("контроль no-fmt не собрался даже с fmt в базовом "
                           "образе — значит он падает не по той причине, "
                           "которую заявляет: %s" % out[-400:])
        return True, ("контроль no-fmt умеет позеленеть — он про реальный "
                      "признак, а не про то, что всегда ложно")
    finally:
        _git_restore(rel)
        _run(["docker", "rmi", "-f", "sabotage-base:tools", "sabotage:nofmt"])
        text = io.open(path, encoding="utf-8").read()
        if "libfmt-dev" in text:
            raise RuntimeError("после отката в build-base остался libfmt-dev")


def _machinery_prebaked_touches_network():
    """Заставить плечо с готовой зависимостью ходить в сеть — контроль
    system-offline обязан УПАСТЬ."""
    rel = os.path.join("images", "Dockerfile.build-system-prebaked")
    path = os.path.join(HERE, rel)
    try:
        _patch_file(path, "\nARG SOURCE_STAMP=0\n",
                    "\nRUN curl -fsS -o /dev/null https://github.com/\n"
                    "\nARG SOURCE_STAMP=0\n")
        code, out = _docker_build("Dockerfile.build-system-prebaked",
                                  "sabotage:offline", ["--network=none"])
        if code == 0:
            return False, ("сборка с --network=none прошла, хотя в неё "
                           "добавлено сетевое обращение — контроль "
                           "system-offline ничего не ловит")
        if "could not resolve host" not in out.lower():
            return False, ("упало не на сети: %s" % out[-400:])
        return True, "контроль system-offline действительно ловит сеть"
    finally:
        _git_restore(rel)
        _run(["docker", "rmi", "-f", "sabotage:offline"])


def _machinery_conan_shared():
    """Заставить измеритель линковки ответить dynamic на менеджерском плече.

    Иначе «static» в фикстуре могло бы быть постоянным ответом измерителя:
    ldd не нашёл libfmt — значит static, независимо от того, что собрано.
    """
    rel = os.path.join("images", "Dockerfile.build-conan")
    path = os.path.join(HERE, rel)
    try:
        _patch_file(path, "      --build=missing -s build_type=Release\n",
                    "      --build=missing -s build_type=Release "
                    "-o \"fmt/*:shared=True\"\n")
        code, out = _docker_build("Dockerfile.build-conan", "sabotage:conan")
        if code != 0:
            return False, "плечо conan с shared=True не собралось: %s" % out[-400:]
        code, out = _run(["docker", "run", "--rm", "sabotage:conan",
                          "ldd", "/build/stand_service"])
        if code != 0:
            return False, "не снять ldd: %s" % out[-400:]
        if "libfmt" not in out:
            return False, ("ldd не показал libfmt даже при shared=True — "
                           "измеритель линковки отвечает static всегда")
        return True, "измеритель линковки умеет ответить dynamic"
    finally:
        _git_restore(rel)
        _run(["docker", "rmi", "-f", "sabotage:conan"])


def _machinery_vcpkg_without_network():
    """Классификатор шага падения обязан ответить network, а не compile.

    Правится флаг команды, не файл: откат не требуется.
    """
    sys.path.insert(0, HERE)
    from scripts import build as buildlib
    code, out = _docker_build("Dockerfile.build-vcpkg", "sabotage:vcpkgnet",
                              ["--network=none", "--no-cache"])
    _run(["docker", "rmi", "-f", "sabotage:vcpkgnet"])
    if code == 0:
        return False, "сборка vcpkg без сети прошла — это невозможно"
    stage = buildlib.failure_stage(out)
    if stage != "network":
        return False, ("классификатор ответил %r вместо 'network' — он не "
                       "различает шаги, и утверждение «vcpkg-latest упал "
                       "именно на компиляции» ничем не обеспечено" % stage)
    return True, "классификатор шага падения различает сеть и компиляцию"


def _machinery_poison_conan_header():
    """Главная новая диверсия: измеритель версии обязан читать ЗАГОЛОВОК.

    Портится FMT_VERSION в заголовке УЖЕ УСТАНОВЛЕННОГО пакета, манифест
    conanfile.txt не трогается. Если измеритель читает заголовок, он
    вернёт испорченное число. Если бы он по-прежнему брал версию sed-ом из
    манифеста (как в первой редакции), он вернул бы 11.0.2 и диверсия
    прошла бы незамеченной — ровно поэтому её и не было.
    """
    rel = os.path.join("images", "Dockerfile.build-conan")
    path = os.path.join(HERE, rel)
    try:
        _patch_file(
            path,
            "RUN sh /images/fmt-version.sh /root/.conan2/p /fmt_version.txt\n",
            "RUN find /root/.conan2/p -type f -path '*/include/fmt/base.h' "
            "-exec sed -i 's/^#define FMT_VERSION .*/#define FMT_VERSION "
            "999999/' {} +\n"
            "RUN sh /images/fmt-version.sh /root/.conan2/p /fmt_version.txt\n")
        code, out = _docker_build("Dockerfile.build-conan",
                                  "sabotage:conanver", ["--no-cache"])
        _run(["docker", "rmi", "-f", "sabotage:conanver"])
        if "FMT_VERSION_MEASURED=999999" not in out:
            if "FMT_VERSION_MEASURED=110002" in out:
                return False, ("измеритель вернул 11.0.2 при испорченном "
                               "заголовке — он читает не заголовок, а "
                               "манифест: число объявлено, а не измерено")
            return False, ("в журнале сборки нет ни испорченного, ни "
                           "исходного числа: %s" % out[-400:])
        return True, ("измеритель версии читает заголовок установленного "
                      "пакета: испорченный заголовок дал испорченное число")
    finally:
        _git_restore(rel)


def _machinery_two_fmt_headers():
    """Измеритель обязан УПАСТЬ, найдя два разных FMT_VERSION.

    Молча выбранное из двух число выглядело бы измерением ровно так же,
    как верное.
    """
    rel = os.path.join("images", "Dockerfile.build-conan")
    path = os.path.join(HERE, rel)
    try:
        _patch_file(
            path,
            "RUN sh /images/fmt-version.sh /root/.conan2/p /fmt_version.txt\n",
            "RUN mkdir -p /root/.conan2/p/sabotage/include/fmt \\\n"
            " && echo '#define FMT_VERSION 123456' "
            "> /root/.conan2/p/sabotage/include/fmt/base.h\n"
            "RUN sh /images/fmt-version.sh /root/.conan2/p /fmt_version.txt\n")
        code, out = _docker_build("Dockerfile.build-conan",
                                  "sabotage:conan2hdr", ["--no-cache"])
        _run(["docker", "rmi", "-f", "sabotage:conan2hdr"])
        if code == 0:
            return False, ("сборка прошла при двух разных FMT_VERSION — "
                           "измеритель молча выбрал одно из чисел")
        if "различных значений" not in out:
            return False, "упало не на измерителе версии: %s" % out[-400:]
        return True, "измеритель версии падает на неоднозначности, а не гадает"
    finally:
        _git_restore(rel)


# --------------------------------------------------------------------
# Диверсии оси воспроизводимости (задача 5)


def _repro_mutate(tmpdir, mutate, which="main"):
    """Копирует обе фикстуры оси воспроизводимости и правит одну из них.

    which="run1" правит ПЕРВЫЙ прогон. Это отдельный случай, а не
    придирка: в первой редакции проверки читали только последний прогон,
    а разбор печатал первый прямо в таблицу устойчивости. Диверсия,
    которая портит только первый прогон, — единственный способ показать,
    что защиты до него вообще дотягиваются.
    """
    main = os.path.join(tmpdir, "repro.jsonl")
    run1 = os.path.join(tmpdir, "repro.run-1.jsonl")
    shutil.copyfile(REPRO_FIXTURE, main)
    shutil.copyfile(REPRO_FIXTURE_RUN1, run1)
    target = main if which == "main" else run1
    rows = _read_rows(target)
    before = json.dumps(rows, sort_keys=True)
    by_name = {r["variant"]: r for r in rows}
    mutate(by_name)
    rows = list(by_name.values())
    if json.dumps(rows, sort_keys=True) == before:
        raise SabotageDidNotApply("копия фикстуры %s не изменилась"
                                  % os.path.basename(target))
    _write_rows(target, rows)
    return main, run1


def _repro_fixture_case(mutate, test, which="main"):
    def action():
        with tempfile.TemporaryDirectory() as tmp:
            main, run1 = _repro_mutate(tmp, mutate, which)
            code, out = _run(
                [sys.executable, "-m", "unittest",
                 "scripts.test_repro." + test],
                env={"REPRO_FIXTURE": main, "REPRO_FIXTURE_RUN1": run1})
            if code == 0:
                return False, ("проверка %s УСТОЯЛА под диверсией — она не "
                               "про то, что мутировано" % test)
            return True, "проверка %s упала, как и требовалось" % test
    return action


def _repro_analysis_case(mutate, expect_text, which="main"):
    def action():
        with tempfile.TemporaryDirectory() as tmp:
            main, run1 = _repro_mutate(tmp, mutate, which)
            code, out = _run(
                [sys.executable, os.path.join(HERE, "scripts",
                                              "analyze-repro.py")],
                env={"REPRO_FIXTURE": main, "REPRO_FIXTURE_RUN1": run1})
            if code == 0:
                return False, ("разбор напечатал таблицу поверх "
                               "недействительных данных — код возврата 0")
            if expect_text not in out:
                return False, ("в выводе разбора нет %r; получено: %s"
                               % (expect_text, out[-500:]))
            return True, "разбор ответил %r" % expect_text
    return action


def _repro_converged_control(rows):
    """Отрицательный контроль объявлен сошедшимся.

    Суммы приводятся к одной ВМЕСТЕ с полем identical: иначе строка
    отвергается ещё проверкой согласованности, и упала бы не та защита,
    которую проверяем.
    """
    row = rows["source-changed"]
    row["sha256_second"] = row["sha256_first"]
    row["identical"] = True


def _repro_uncontrolled_pair(rows):
    for position in ("first", "second"):
        rows["pinned"]["treatment_%s" % position] = \
            rows["plain"]["treatment_%s" % position]


def _repro_uncontrolled_debug_pair(rows):
    for position in ("first", "second"):
        rows["debug-pinned"]["treatment_%s" % position] = \
            rows["debug-plain"]["treatment_%s" % position]


def _repro_converged_positive_control(rows):
    """Положительный контроль объявлен сошедшимся.

    Суммы приводятся к одной вместе с полем identical: иначе строка
    отвергается проверкой согласованности, и упала бы не та защита.
    Смысл диверсии — день, когда сборка с -g перестала расходиться:
    канала для лечения нет, и пара с -g не меряет ничего.
    """
    row = rows["debug-plain"]
    row["sha256_second"] = row["sha256_first"]
    row["identical"] = True


def _repro_shared_mtime_between_arms(rows):
    """Две сборки РАЗНЫХ плеч получают одно время изменения.

    Внутри каждого плеча всё остаётся безупречным: метки разные, времена
    разные. Именно этот случай прежние проверки пропускали.
    """
    rows["pinned"]["binary_mtime_first"] = \
        rows["plain"]["binary_mtime_first"]


def _repro_drop_no_cache_key(rows):
    """Поле no_cache осталось true, а ключа в команде нет.

    Ровно тот случай, ради которого командная строка попала в фикстуру:
    раньше поле бралось из спецификации и переживало любую правку кода.
    """
    row = rows["plain"]
    row["docker_argv_first"] = [a for a in row["docker_argv_first"]
                                if a != "--no-cache"]


def _repro_drop_network_key(rows):
    row = rows["plain"]
    row["docker_argv_first"] = [a for a in row["docker_argv_first"]
                                if not a.startswith("--network=")]


def _repro_invented_build_dir(rows):
    """Каталог сборки в поле не тот, который назвал шаг сборки.

    На это поле опирается нормализация лечения: она вырезает из строки
    ровно то, что в поле записано.
    """
    rows["pinned"]["build_dir_first"] = "/build-somewhere-else"


def _repro_cmake_split_within_arm(rows):
    rows["plain"]["cmake_version_second"] = "cmake version 3.30.0"


def _repro_debug_arm_lost_g(rows):
    """У плеча с -g лечение сообщает, что -g не применялся."""
    row = rows["debug-plain"]
    for position in ("first", "second"):
        key = "treatment_%s" % position
        row[key] = row[key].replace("FLAGS=[-g ", "FLAGS=[")


def _repro_strip_misreported(rows):
    row = rows["debug-plain"]
    for position in ("first", "second"):
        key = "treatment_%s" % position
        row[key] = row[key].replace("STRIP=[no]", "STRIP=[yes]")


def _repro_debug_binary_shrunk(rows):
    """Бинарник с -g того же размера, что стриппнутый.

    Значит -g не применился, и пара с отладочной информацией мерила бы
    ровно то же, что пара без неё.
    """
    row = rows["debug-plain"]
    for position in ("first", "second"):
        row["binary_bytes_%s" % position] = 18680


# --------------------------------------------------------------------
# Диверсии над записанным ДО прогона (expected.json)
#
# Правится файл в репозитории, откат через git checkout: путь к
# expected.json проверки не берут из окружения, и подсовывать им копию
# было бы враньём о том, что именно они читают.


def _drop_stale_bytecode(path):
    """Убрать скомпилированный кэш Python рядом с восстановленным файлом.

    ПОЧЕМУ ЭТО ЗДЕСЬ. Диверсия над исходником может заменить строку на
    другую ТОЙ ЖЕ ДЛИНЫ («if within >= between:» на «if between >=
    within:»), а откат вернуть байты в ту же секунду. Тогда у файла
    совпадают и размер, и время изменения с точностью, которую видит
    Python, — и он берёт .pyc, скомпилированный из ДИВЕРСИОННОГО
    исходника. Проверки после отката молча исполняют подменённый код.
    Ровно это здесь и случилось: правило про разброс осталось
    перевёрнутым в кэше и роняло два теста при чистом рабочем дереве.

    ЗЕРКАЛЬНЫЙ СЛУЧАЙ, ИЗ-ЗА КОТОРОГО ЭТО ЗОВЁТСЯ ДВАЖДЫ. Та же ловушка
    работает и в другую сторону: если правка легла в ту же секунду, что
    и предыдущая компиляция, Python возьмёт СТАРЫЙ .pyc при самой
    диверсии — исполнится нетронутый код, проверка устоит, и диверсия
    отчитается «УСТОЯЛА» на ровном месте. Поэтому кэш сбрасывается и
    после наложения, и после отката.
    """
    directory, name = os.path.split(path)
    stem = os.path.splitext(name)[0]
    cache = os.path.join(directory, "__pycache__")
    if not os.path.isdir(cache):
        return
    for entry in os.listdir(cache):
        if entry.startswith(stem + ".") and entry.endswith(".pyc"):
            os.remove(os.path.join(cache, entry))


def _source_case(relpath, anchor, replacement, test, module="test_repro"):
    """Правка ИСХОДНИКА скрипта с побайтным откатом.

    То же устройство, что у _expected_case, но целью может быть любой
    файл стенда. Нужна там, где проверяемое утверждение живёт не в
    данных, а в коде: печать оговорки в разборе и защита каталога
    назначения в run-repro.py не мутируются копией фикстуры никак.

    Откат — из снимка, снятого до правки, и снимок сверяется побайтно:
    git на разных машинах по-разному относится к концам строк, и «после
    отката чисто» означало бы тогда разное.
    """
    path = os.path.join(HERE, relpath)

    def action():
        before = io.open(path, "rb").read()
        _patch_file(path, anchor, replacement)
        # Кэш сбрасывается СРАЗУ ПОСЛЕ наложения, а не только после
        # отката. Правка той же длины, легшая в ту же секунду, что и
        # предыдущая компиляция, оставила бы Python'у прежний .pyc — и
        # диверсия исполняла бы НЕ ДИВЕРСИОННЫЙ код. Проверка тогда
        # устояла бы, и это выглядело бы как «проверка не про то, что
        # мутировано», то есть ложный красный на ровном месте.
        _drop_stale_bytecode(path)
        try:
            code, out = _run([sys.executable, "-m", "unittest",
                              "scripts.%s.%s" % (module, test)])
            if code == 0:
                return False, ("проверка %s УСТОЯЛА под диверсией" % test)
            return True, "проверка %s упала, как и требовалось" % test
        finally:
            io.open(path, "wb").write(before)
            if io.open(path, "rb").read() != before:
                raise RuntimeError("%s не вернулся к исходному виду"
                                   % relpath)
            _drop_stale_bytecode(path)
    return action


def _repro_stray_flag_in_mechanism_pair(rows):
    """Плечу механизма добавлен посторонний флаг компиляции.

    Плечо debug-stripped несёт вывод «канал закрывает strip», но до
    третьей проверки его флаги не сверялись ни с чем: лишний флаг прошёл
    бы весь набор проверок, а утверждение «отличается ровно шагом strip»
    осталось бы верным только на бумаге спецификации.
    """
    row = rows["debug-stripped"]
    for position in ("first", "second"):
        key = "treatment_%s" % position
        row[key] = row[key].replace("FLAGS=[-g", "FLAGS=[-g -fno-plt")


def _repro_mechanism_pair_same_strip(rows):
    """У плеча механизма шаг strip такой же, как у debug-plain.

    Тогда пара не меряет ничего: «отличается ровно шагом strip» верно
    ровно до тех пор, пока шаг strip плечи различает.
    """
    row = rows["debug-stripped"]
    for position in ("first", "second"):
        key = "treatment_%s" % position
        row[key] = row[key].replace("STRIP=[yes]", "STRIP=[no]")


def _expected_case(anchor, replacement, test, module="test_repro"):
    """Правка expected.json с побайтным откатом.

    Откат идёт не через git, а из снимка, снятого до правки, и снимок
    сверяется после отката побайтно: git на разных машинах по-разному
    относится к концам строк, и «после отката чисто» означало бы тогда
    разное. Файл возвращается ровно тем, чем был.
    """
    path = os.path.join(HERE, "expected.json")

    def action():
        before = io.open(path, "rb").read()
        _patch_file(path, anchor, replacement)
        try:
            code, out = _run([sys.executable, "-m", "unittest",
                              "scripts.%s.%s" % (module, test)])
            if code == 0:
                return False, ("проверка %s УСТОЯЛА под диверсией" % test)
            return True, "проверка %s упала, как и требовалось" % test
        finally:
            io.open(path, "wb").write(before)
            if io.open(path, "rb").read() != before:
                raise RuntimeError("expected.json не вернулся к исходному виду")
    return action


def _machinery_repro_cache_gives_identity_for_free():
    """Показать, что главная опасность оси НАСТОЯЩАЯ, а не выдуманная.

    Тот же Dockerfile собирается дважды БЕЗ --no-cache и с ОДНОЙ И ТОЙ ЖЕ
    меткой прогона. Docker переиспользует слои: метка и время изменения
    бинарника у двух «сборок» обязаны совпасть, а суммы — совпасть
    даром. Ровно этот случай и ловят улики оси; если бы он не
    воспроизводился, улики стерегли бы пустоту.
    """
    sys.path.insert(0, HERE)
    from scripts import repro as reprolib

    args = ["--network=none",
            "--build-arg", "BUILD_NONCE=sabotage-same",
            "--build-arg", "BUILD_DIR=/build-a",
            "--build-arg", "REPRO_PINNED=0",
            "--build-arg", "EXTRA_CXX_FLAGS="]
    tags = ["sabotage-repro:cache1", "sabotage-repro:cache2"]
    try:
        for tag in tags:
            code, out = _docker_build("Dockerfile.build-repro", tag, args)
            if code != 0:
                return False, "сборка не прошла: %s" % out[-400:]
        nonces = [reprolib.nonce_of(t) for t in tags]
        mtimes = [reprolib.binary_mtime(t) for t in tags]
        sums = [reprolib.binary_sha256_and_size(t)[0] for t in tags]
        if nonces[0] != nonces[1] or mtimes[0] != mtimes[1]:
            raise SabotageDidNotApply(
                "кэш НЕ сработал (метки %r, время %r) — диверсия не "
                "воспроизвела ту самую опасность" % (nonces, mtimes))
        if sums[0] != sums[1]:
            return False, ("суммы разошлись при переиспользованном слое — "
                           "измеритель не про файл в образе")
        return True, ("кэш действительно отдаёт совпадение сумм даром: "
                      "метка %r и время %d у обеих «сборок» одни и те же. "
                      "Улики оси стерегут настоящий случай"
                      % (nonces[0], mtimes[0]))
    finally:
        _run(["docker", "rmi", "-f"] + tags)


def _machinery_repro_treatment_is_read_from_the_image():
    """Лечение обязано браться из образа, а не из аргументов прогона.

    Шаг сборки заставляют записать в /repro_treatment.txt заведомую
    неправду при REPRO_PINNED=1. Если прогон читает файл, он вернёт эту
    неправду. Если бы он подставлял то, что сам передал в --build-arg,
    диверсия прошла бы незамеченной, и в фикстуре стояло бы объявление о
    лечении вместо измерения.
    """
    rel = os.path.join("images", "Dockerfile.build-repro")
    path = os.path.join(HERE, rel)
    sys.path.insert(0, HERE)
    from scripts import repro as reprolib
    tag = "sabotage-repro:treatment"
    try:
        # Подменяется ЗНАЧЕНИЕ поля, а не набор полей: строка лечения
        # обязана остаться той же по устройству, иначе упал бы разбор
        # строки, а не проверка того, откуда лечение взялось.
        _patch_file(
            path,
            '    echo "REPRO_TREATMENT BUILD_DIR=[$BUILD_DIR] '
            'FLAGS=[$FLAGS]" \\\n',
            '    echo "REPRO_TREATMENT BUILD_DIR=[$BUILD_DIR] '
            'FLAGS=[ЗАВЕДОМАЯ НЕПРАВДА]" \\\n')
        code, out = _docker_build(
            "Dockerfile.build-repro", tag,
            ["--network=none", "--no-cache",
             "--build-arg", "BUILD_NONCE=sabotage-treatment",
             "--build-arg", "BUILD_DIR=/build-a",
             "--build-arg", "REPRO_PINNED=1",
             "--build-arg", "EXTRA_CXX_FLAGS="])
        if code != 0:
            return False, "сборка с диверсией не прошла: %s" % out[-400:]
        treatment = reprolib.treatment_of(tag)
        if "ЗАВЕДОМАЯ НЕПРАВДА" not in treatment:
            return False, ("прогон вернул %r — он берёт лечение не из "
                           "образа, а из своих аргументов" % treatment)
        return True, ("лечение читается из образа: подменённый файл дал "
                      "подменённое значение")
    finally:
        _git_restore(rel)
        _run(["docker", "rmi", "-f", tag])


def _machinery_repro_sum_follows_the_file():
    """Сумма обязана следовать за байтами файла.

    К готовому бинарнику дописывается метка ПОСЛЕ strip — компиляция при
    этом ровно та же. Сумма обязана измениться. Если бы измеритель брал
    digest образа или хешировал что-то другое, он ответил бы прежним
    числом.
    """
    rel = os.path.join("images", "Dockerfile.build-repro")
    path = os.path.join(HERE, rel)
    sys.path.insert(0, HERE)
    from scripts import repro as reprolib
    clean_tag = "sabotage-repro:clean"
    dirty_tag = "sabotage-repro:dirty"
    args = ["--network=none", "--no-cache",
            "--build-arg", "BUILD_NONCE=sabotage-sum",
            "--build-arg", "BUILD_DIR=/build-a",
            "--build-arg", "REPRO_PINNED=0",
            "--build-arg", "EXTRA_CXX_FLAGS="]
    try:
        code, out = _docker_build("Dockerfile.build-repro", clean_tag, args)
        if code != 0:
            return False, "чистая сборка не прошла: %s" % out[-400:]
        clean_sum, clean_size = reprolib.binary_sha256_and_size(clean_tag)

        _patch_file(path,
                    '    cp "$BUILD_DIR/stand_service" /stand_service; \\\n',
                    '    cp "$BUILD_DIR/stand_service" /stand_service; \\\n'
                    '    printf SABOTAGE >> /stand_service; \\\n')
        code, out = _docker_build("Dockerfile.build-repro", dirty_tag, args)
        if code != 0:
            return False, "сборка с диверсией не прошла: %s" % out[-400:]
        dirty_sum, dirty_size = reprolib.binary_sha256_and_size(dirty_tag)

        if dirty_size != clean_size + len("SABOTAGE"):
            raise SabotageDidNotApply(
                "размер не вырос на дописанные байты (%d против %d) — "
                "диверсия не легла" % (dirty_size, clean_size))
        if dirty_sum == clean_sum:
            return False, ("сумма не изменилась после дописывания байтов — "
                           "измеритель хеширует не файл")
        return True, ("сумма следует за байтами файла: дописанные %d Б "
                      "изменили её" % len("SABOTAGE"))
    finally:
        _git_restore(rel)
        _run(["docker", "rmi", "-f", clean_tag, dirty_tag])


# --------------------------------------------------------------------
# Реестр диверсий


# --------------------------------------------------------------------
# Диверсии оси соседей по языкам (задача 6)


def _siblings_mutate(tmpdir, mutate, which="both"):
    """Копирует ОБА прогона оси соседей и правит копии.

    Файлы в репозитории не трогаются: проверки и разбор берут пути из
    переменных окружения SIBLINGS_FIXTURE и SIBLINGS_FIXTURE_RUN1.

    ПОЧЕМУ ПРАВЯТСЯ ОБА. Проверки читают оба прогона, и правка только
    второго означала бы, что цель диверсии — «данные одного прогона», а
    не проверяемое утверждение. which="run2" оставлен для диверсий,
    которые про РАСХОЖДЕНИЕ прогонов между собой: там второй прогон
    обязан отличаться от первого.
    """
    targets = {}
    for key, source, name in (
            ("SIBLINGS_FIXTURE", SIBLINGS_FIXTURE, "siblings.jsonl"),
            ("SIBLINGS_FIXTURE_RUN1", SIBLINGS_FIXTURE_RUN1,
             "siblings.run-1.jsonl")):
        target = os.path.join(tmpdir, name)
        shutil.copyfile(source, target)
        targets[key] = target

    if which == "both":
        to_patch = list(targets.values())
    elif which == "run1":
        to_patch = [targets["SIBLINGS_FIXTURE_RUN1"]]
    else:
        to_patch = [targets["SIBLINGS_FIXTURE"]]
    for target in to_patch:
        rows = _read_rows(target)
        before = json.dumps(rows, sort_keys=True)
        by_name = {r["variant"]: r for r in rows}
        mutate(by_name)
        rows = list(by_name.values())
        if json.dumps(rows, sort_keys=True) == before:
            raise SabotageDidNotApply(
                "копия фикстуры %s не изменилась" % os.path.basename(target))
        _write_rows(target, rows)
    return targets


def _siblings_fixture_case(mutate, test, which="both"):
    def action():
        with tempfile.TemporaryDirectory() as tmp:
            targets = _siblings_mutate(tmp, mutate, which)
            code, out = _run(
                [sys.executable, "-m", "unittest",
                 "scripts.test_siblings." + test],
                env=targets)
            if code == 0:
                return False, ("проверка %s УСТОЯЛА под диверсией — она не "
                               "про то, что мутировано" % test)
            return True, "проверка %s упала, как и требовалось" % test
    return action


def _siblings_analysis_case(mutate, expect_text, which="both"):
    def action():
        with tempfile.TemporaryDirectory() as tmp:
            targets = _siblings_mutate(tmp, mutate, which)
            code, out = _run(
                [sys.executable, os.path.join(HERE, "scripts",
                                              "analyze-siblings.py")],
                env=targets)
            if code == 0:
                return False, ("разбор напечатал таблицу поверх "
                               "недействительных данных — код возврата 0")
            if expect_text not in out:
                return False, ("в выводе разбора нет %r; получено: %s"
                               % (expect_text, out[-500:]))
            return True, "разбор ответил %r" % expect_text
    return action


def _siblings_healthz_diverged(rows):
    """Одно из плеч отвечает на /healthz не тем же, что остальные.

    Сумма правится ВМЕСТЕ с ответом: иначе строка падала бы на проверке
    «сумма следует за ответом», и диверсия проверяла бы не то, что
    задумано.
    """
    row = rows["go"]
    response = row["healthz_response"][:-2] + "OK"
    row["healthz_response"] = response
    row["healthz_response_sha256"] = hashlib.sha256(
        response.encode("utf-8")).hexdigest()
    row["healthz_response_bytes"] = len(response.encode("utf-8"))


def _siblings_excluded_field_does_not_differ(rows):
    """Поле, объявленное исключением, у всех плеч одно.

    Тогда «различается ровно одно» проверено там, где не различается
    ничего: тела /resolve совпадают именно потому, что из них выброшено
    поле, которое и так у всех одинаково.
    """
    for name in ("cpp", "go", "rust"):
        rows[name]["resolve_reported_variant"] = "cpp"


def _siblings_control_converged(rows):
    """Отрицательный контроль совпал с плечом go."""
    for field in ("healthz_response", "healthz_response_sha256",
                  "healthz_response_bytes"):
        rows["go-divergent"][field] = rows["go"][field]


def _siblings_ldd_line_counted_as_dependency(rows):
    """Строка ldd с путём загрузчика засчитана за разделяемую зависимость.

    Ровно эта ошибка и была бы совершена, если бы зависимости мерились
    разбором текста ldd: у статического PIE ldd печатает путь загрузчика,
    и зависимостей стало бы «одна» там, где их нет.
    """
    rows["rust"]["dynamic_deps"] = ["/lib/ld-musl-x86_64.so.1"]
    rows["rust"]["dynamic_dep_count"] = 1


def _siblings_interpreter_appeared(rows):
    rows["rust"]["elf_has_interpreter"] = True


def _siblings_goproxy_on(rows):
    row = rows["go"]
    row["treatment"] = row["treatment"].replace(
        "GOPROXY=[off]", "GOPROXY=[https://proxy.golang.org,direct]")


def _siblings_external_crate_appeared(rows):
    row = rows["rust"]
    row["treatment"] = row["treatment"].replace(
        "EXTERNAL_CRATES=[0]", "EXTERNAL_CRATES=[1]")


def _siblings_control_pair_uncontrolled(rows):
    """Паре контроля добавлен второй различающийся аргумент сборки.

    После этого расхождение ответов объясняется двумя причинами сразу, и
    контроль перестаёт показывать то, ради чего заведён.
    """
    row = rows["go-divergent"]
    argv = list(row["docker_argv"])
    argv.insert(2, "--no-cache")
    row["docker_argv"] = argv


def _siblings_fmt_caveat_erased(rows):
    rows["cpp"]["external_libraries_declared"] = []


def _siblings_resolve_control_converged(rows):
    """Контроль ветки /resolve совпал с плечом go по сравниваемому телу.

    Тогда вторая ветка проверки тождества отвечает «совпало» при любых
    данных — ровно то, ради чего этот контроль и заведён.
    """
    rows["go-resolve-divergent"]["resolve_body_compared"] = \
        rows["go"]["resolve_body_compared"]


def _siblings_resolve_control_also_breaks_healthz(rows):
    """Контроль ветки /resolve разошёлся ещё и по /healthz.

    Тогда он падает на первой ветке, и вторая снова остаётся без живого
    контроля — при том, что «контроль разошёлся» формально верно.
    """
    row = rows["go-resolve-divergent"]
    response = row["healthz_response"][:-2] + "OK"
    row["healthz_response"] = response
    row["healthz_response_sha256"] = hashlib.sha256(
        response.encode("utf-8")).hexdigest()
    row["healthz_response_bytes"] = len(response.encode("utf-8"))


def _siblings_detail_back_in_comparison(rows):
    """Текст ошибки вернулся в сравниваемое тело /resolve.

    Тексты у трёх реализаций разные, и любой отказ разрешения имени
    после этого объявляет «это не один сервис» по причине, к тождеству
    отношения не имеющей.
    """
    for name in ("cpp", "go", "rust", "cpp-nofmt", "go-divergent",
                 "go-resolve-divergent"):
        row = rows[name]
        body = json.loads(row["resolve_body_compared"])
        body["detail"] = row["resolve_detail"]
        row["resolve_body_compared"] = json.dumps(
            body, ensure_ascii=False, sort_keys=True)


def _siblings_resolve_refused(rows):
    """Разрешение имени не прошло у одного плеча.

    Это не расхождение реализаций, и разбор обязан сказать именно это, а
    не «ЭТО НЕ ОДИН СЕРВИС».
    """
    rows["rust"]["resolve_outcome"] = "refused"
    rows["rust"]["resolve_detail"] = "EAI_AGAIN"


def _siblings_unpacked_smaller_than_binary(rows):
    """Распакованный объём образа меньше лежащего внутри бинарника.

    Так выглядит безымянная шкала сжатого содержимого, подписанная
    вторым именем: величина названа не тем, что она есть.
    """
    row = rows["go"]
    row["image_unpacked_bytes"] = row["binary_bytes"] - 1


def _siblings_two_different_files(rows):
    """Улики сняты с одного файла, байты — с другого."""
    rows["go"]["binary_sha256_in_final"] = "0" * 64


def _siblings_symtab_survived(rows):
    rows["go"]["elf_has_symtab"] = True


def _siblings_debug_sections_survived(rows):
    rows["cpp"]["elf_debug_sections"] = [".debug_info", ".debug_line"]


def _siblings_sections_were_not_read(rows):
    """Секций не прочитано ни одной — «отладочных секций нет» пусто."""
    rows["rust"]["elf_section_count"] = 0


def _siblings_cgo_enabled(rows):
    """У плеча go в лечении включён cgo.

    Улика того, что механизм разрешения имён у Go другой, держится на
    выключенном cgo: с включённым бинарник мог бы звать getaddrinfo, и
    названный механизм перестал бы следовать из замера.
    """
    row = rows["go"]
    row["treatment"] = row["treatment"].replace("CGO_ENABLED=[0]",
                                                "CGO_ENABLED=[1]")


def _siblings_go_claims_the_libc_resolver(rows):
    rows["go"]["name_resolution"] = (
        "getaddrinfo той же musl, с которой слинкован бинарник")


def _siblings_reference_arm_is_another_service(rows):
    """Справочное плечо отвечает не тем же, что плечо cpp.

    Тогда разность их байтов мерит разницу двух программ, а не цену
    библиотеки fmt, и справочное число печатать нельзя.
    """
    row = rows["cpp-nofmt"]
    response = row["healthz_response"][:-2] + "OK"
    row["healthz_response"] = response
    row["healthz_response_sha256"] = hashlib.sha256(
        response.encode("utf-8")).hexdigest()
    row["healthz_response_bytes"] = len(response.encode("utf-8"))


def _siblings_second_run_is_a_copy(rows):
    """Второй прогон — копия первого, а не второй замер.

    Метки прогона у одного плеча в двух фикстурах совпадают: файл
    скопирован. «Байты те же и во втором прогоне» становится
    тавтологией.
    """
    first = _read_rows(SIBLINGS_FIXTURE_RUN1)
    by_name = {r["variant"]: r for r in first}
    for name, row in rows.items():
        row["nonce_passed"] = by_name[name]["nonce_passed"]
        row["nonce_read_back"] = by_name[name]["nonce_read_back"]
        row["docker_argv"] = list(by_name[name]["docker_argv"])


def _siblings_spread_does_not_follow(rows):
    rows["go"]["spread_ms"] = 1.0


def _siblings_drop_marker_case():
    """Убрать маркер COMPLETE — чтение обязано отказать."""
    def action():
        with tempfile.TemporaryDirectory() as tmp:
            target = os.path.join(tmp, "siblings.jsonl")
            text = io.open(SIBLINGS_FIXTURE, encoding="utf-8").read()
            if "COMPLETE" not in text:
                raise SabotageDidNotApply("в фикстуре и не было маркера")
            io.open(target, "w", encoding="utf-8", newline="\n").write(
                text.replace("COMPLETE\n", ""))
            code, out = _run(
                [sys.executable, "-m", "unittest",
                 "scripts.test_siblings.FixtureShape"],
                env={"SIBLINGS_FIXTURE": target})
            if code == 0:
                return False, "оборванная фикстура прочиталась как полная"
            return True, "чтение отказало, как и требовалось"
    return action


def _machinery_siblings_cache_gives_the_same_nonce():
    """Показать, что опасность кэша НАСТОЯЩАЯ, а не выдуманная.

    Тот же Dockerfile собирается дважды БЕЗ смены метки и без --no-cache.
    Docker переиспользует слои: метка и время изменения бинарника у двух
    «сборок» обязаны совпасть. Ровно это и ловят улики оси; если бы
    случай не воспроизводился, улики стерегли бы пустоту.
    """
    tag = "cpp-build-and-package-sabotage-go:cache"
    extra = ["--target", "build", "--build-arg", "STAND_NONCE=odna-i-ta-zhe"]
    code, out = _docker_build("Dockerfile.sibling-go", tag, extra=extra)
    if code != 0:
        return False, "первая сборка не прошла:\n%s" % out[-800:]
    first_nonce = _run(["docker", "run", "--rm", tag,
                        "cat", "/nonce"])[1].strip()
    first_mtime = _run(["docker", "run", "--rm", tag, "stat", "-c", "%y",
                        "/build/stand_service"])[1].strip()
    code, out = _docker_build("Dockerfile.sibling-go", tag, extra=extra)
    if code != 0:
        return False, "вторая сборка не прошла:\n%s" % out[-800:]
    second_nonce = _run(["docker", "run", "--rm", tag,
                         "cat", "/nonce"])[1].strip()
    second_mtime = _run(["docker", "run", "--rm", tag, "stat", "-c", "%y",
                         "/build/stand_service"])[1].strip()
    _run(["docker", "image", "rm", "-f", tag])
    if first_nonce != second_nonce or first_mtime != second_mtime:
        return False, ("кэш НЕ переиспользовал слой (метки %r и %r, времена "
                       "%r и %r) — опасность, от которой стерегут улики, в "
                       "этих условиях не воспроизвелась"
                       % (first_nonce, second_nonce, first_mtime,
                          second_mtime))
    return True, ("кэш отдал ту же метку (%s) и то же время изменения (%s) "
                  "даром — улики оси стерегут настоящий случай"
                  % (first_nonce, first_mtime))


def _machinery_siblings_size_follows_the_file():
    """Показать, что измеритель РАЗМЕРА меряет файл, а не что-то рядом.

    Самые печатаемые величины оси — размер бинарника и размеры образа —
    были единственными, чей измеритель не роняли ничем. На оси
    воспроизводимости такая диверсия есть (дописать байты, сумма обязана
    измениться); здесь её аналог.

    К готовому бинарнику дописываются байты ПОСЛЕ шага strip:
    компиляция при этом ровно та же. Обязаны измениться все три
    величины — размер файла, сумма и распакованный объём образа, — и
    размер обязан вырасти ровно на дописанное. Если бы измеритель брал
    размер из метаданных образа, из спецификации или из объявления, он
    ответил бы прежним числом.

    Сжатое содержимое образа сюда НЕ включено намеренно: дописанные
    байты сжимаемы, и требовать от этой величины роста ровно на
    дописанное нельзя. Требуется только, чтобы она изменилась.
    """
    rel = os.path.join("images", "Dockerfile.sibling-go")
    path = os.path.join(HERE, rel)
    sys.path.insert(0, HERE)
    from scripts import siblings as siblib

    clean_tag = "cpp-build-and-package-sabotage-go:size-clean"
    dirty_tag = "cpp-build-and-package-sabotage-go:size-dirty"
    extra = ["--build-arg", "STAND_NONCE=sabotage-size"]
    marker = "SABOTAGE"
    try:
        code, out = _docker_build("Dockerfile.sibling-go", clean_tag, extra)
        if code != 0:
            return False, "чистая сборка не прошла:\n%s" % out[-800:]
        clean_size, clean_sum = siblib.binary_size_and_sha256_in_final(
            clean_tag)
        clean_unpacked = siblib.image_unpacked_bytes(clean_tag)
        clean_registry = siblib.image_registry_bytes(clean_tag)

        _patch_file(
            path,
            '      echo "STRIP=[ldflags -s -w]"; \\\n',
            '      echo "STRIP=[ldflags -s -w]"; \\\n'
            '      printf %s >> /build/stand_service; \\\n' % marker)
        code, out = _docker_build("Dockerfile.sibling-go", dirty_tag, extra)
        if code != 0:
            return False, "сборка с диверсией не прошла:\n%s" % out[-800:]
        dirty_size, dirty_sum = siblib.binary_size_and_sha256_in_final(
            dirty_tag)
        dirty_unpacked = siblib.image_unpacked_bytes(dirty_tag)
        dirty_registry = siblib.image_registry_bytes(dirty_tag)

        if dirty_size != clean_size + len(marker):
            raise SabotageDidNotApply(
                "размер бинарника не вырос на дописанные байты (%d против "
                "%d) — диверсия не легла" % (dirty_size, clean_size))
        if dirty_sum == clean_sum:
            return False, ("сумма не изменилась после дописывания байтов — "
                           "измеритель хеширует не файл")
        if dirty_unpacked != clean_unpacked + len(marker):
            return False, ("распакованный объём образа не вырос на "
                           "дописанные байты (%d против %d) — величина "
                           "снята не с содержимого образа"
                           % (dirty_unpacked, clean_unpacked))
        if dirty_registry == clean_registry:
            return False, ("сжатое содержимое образа не изменилось вовсе — "
                           "величина снята не с этого образа")
        return True, ("дописанные %d Б изменили сумму, подняли размер "
                      "бинарника %d → %d и распакованный объём образа "
                      "%d → %d; сжатое содержимое тоже изменилось "
                      "(%d → %d)"
                      % (len(marker), clean_size, dirty_size, clean_unpacked,
                         dirty_unpacked, clean_registry, dirty_registry))
    finally:
        _git_restore(rel)
        _run(["docker", "rmi", "-f", clean_tag, dirty_tag])


def _machinery_package_unpacked_size_follows_the_file():
    """Точный спутник упакованного размера обязан мерить содержимое.

    На оси упаковки упакованный размер образа сравнивается между
    платформами С ДОПУСКОМ, и стеречь содержимое он не может: та же
    проба на оси соседей показала, что дописанные к бинарнику 8 байт
    сдвигают упакованное число на 2 байта, причём вниз. Стережёт
    содержимое распакованный объём образа — и вот эта диверсия
    показывает, что он действительно снят с содержимого, а не отвечает
    всегда одно и то же.

    Измеритель здесь СВОЙ, не тот, что на оси соседей: образы упаковки
    стоят на debian и distroless, один и тот же файл (`etc/ld.so.cache`)
    встречается в двух слоях, и сумма по слоям посчитала бы его дважды.
    Слои применяются по порядку. Непроверенный измеритель — это
    утверждение без замера, поэтому диверсия у него обязана быть своя.

    К готовому бинарнику дописываются байты ПОСЛЕ шага strip.
    Распакованный объём обязан вырасти РОВНО на дописанное.
    """
    rel = os.path.join("images", "Dockerfile.glibc-dynamic")
    path = os.path.join(HERE, rel)
    sys.path.insert(0, HERE)
    from scripts import docker as dockerlib

    clean_tag = "cpp-build-and-package-sabotage-pkg:clean"
    dirty_tag = "cpp-build-and-package-sabotage-pkg:dirty"
    marker = "SABOTAGE"
    try:
        code, out = _docker_build("Dockerfile.glibc-dynamic", clean_tag)
        if code != 0:
            return False, "чистая сборка не прошла:\n%s" % out[-800:]
        clean_unpacked = dockerlib.image_unpacked_bytes(clean_tag)
        clean_packed = dockerlib.image_size(clean_tag)

        _patch_file(
            path,
            " && strip /build/stand_service\n",
            " && strip /build/stand_service \\\n"
            " && printf %s SABOTAGE >> /build/stand_service\n")
        code, out = _docker_build("Dockerfile.glibc-dynamic", dirty_tag)
        if code != 0:
            return False, "сборка с диверсией не прошла:\n%s" % out[-800:]
        dirty_unpacked = dockerlib.image_unpacked_bytes(dirty_tag)
        dirty_packed = dockerlib.image_size(dirty_tag)

        if dirty_unpacked != clean_unpacked + len(marker):
            return False, ("распакованный объём образа не вырос на "
                           "дописанные байты (%d против %d): величина "
                           "снята не с содержимого образа"
                           % (dirty_unpacked, clean_unpacked))
        return True, ("дописанные %d Б подняли распакованный объём образа "
                      "%d → %d ровно на столько же; упакованный при этом "
                      "сдвинулся на %d Б — он содержимое не стережёт"
                      % (len(marker), clean_unpacked, dirty_unpacked,
                         dirty_packed - clean_packed))
    finally:
        _git_restore(rel)
        _run(["docker", "rmi", "-f", clean_tag, dirty_tag])


def _machinery_siblings_ldd_would_have_miscounted_rust():
    """Показать, что отказ от разбора текста ldd — не придирка.

    Собирается плечо rust, и над одним и тем же бинарником снимаются два
    измерения: разбор текста ldd (как на оси упаковки) и записи NEEDED
    (как на этой оси). Первое обязано дать НЕ НОЛЬ, второе — ноль. Если
    оба дадут одно и то же, замена измерителя ничего не исправляла.
    """
    sys.path.insert(0, HERE)
    from scripts import docker as dockerlib
    from scripts import siblings as siblib

    tag = "cpp-build-and-package-sabotage-rust:elf"
    code, out = _docker_build("Dockerfile.sibling-rust", tag,
                              extra=["--target", "build"])
    if code != 0:
        return False, "сборка не прошла:\n%s" % out[-800:]
    by_ldd = dockerlib.dynamic_deps(tag)
    by_elf = siblib.read_elf_facts(tag)
    _run(["docker", "image", "rm", "-f", tag])
    if not by_ldd:
        return False, ("разбор текста ldd дал ноль — значит замена "
                       "измерителя ничего не исправляла, и обоснование "
                       "правки неверно")
    if by_elf["dynamic_dep_count"] != 0:
        return False, ("измеритель по ELF нашёл %d зависимостей (%r) — "
                       "бинарник не самодостаточен, и вся ось стоит не на "
                       "том" % (by_elf["dynamic_dep_count"],
                                by_elf["dynamic_deps"]))
    return True, ("ldd насчитал бы %r, записей NEEDED — ноль; замена "
                  "измерителя исправляла настоящую ошибку" % (by_ldd,))


def _set(name, **fields):
    def mutate(rows):
        rows[name].update(fields)
    return mutate


def _both_managers_failed(rows):
    for name in ("vcpkg", "conan"):
        rows[name].update(build_ok=False, failure_stage="network")


# Тёплая и пересборка, сведённые к разнице между плечами заведомо УЖЕ
# записанного порога (0,20 s против 1,0 s). Значения заданы здесь, а не
# списаны из фикстуры: раньше первый прогон получал времена ВТОРОГО, и
# ситуация «записанное правило работает в одиночку» держалась на том,
# каким вышел последний прогон. Переснятые фикстуры это и сломали —
# второй прогон разошёлся между плечами шире порога, диверсия покраснела
# не потому, что цель устояла, а потому, что диверсия перестала создавать
# ту обстановку, ради которой заведена. Теперь обстановку задаёт она сама.
_NARROW_WARM = {"system": 2.50, "vcpkg": 2.60, "conan": 2.70}
_NARROW_REBUILD = {"system": 4.50, "vcpkg": 4.60, "conan": 4.70}
# То же, но разница между плечами ШИРЕ порога (2,50 s и 2,40 s).
_WIDE_WARM = {"system": 2.50, "vcpkg": 2.60, "conan": 5.00}
_WIDE_REBUILD = {"system": 4.50, "vcpkg": 4.60, "conan": 6.90}


def _set_times(warm, rebuild):
    def mutate(rows):
        for name in warm:
            rows[name].update(warm_seconds=warm[name],
                              rebuild_seconds=rebuild[name])
    return mutate


# Оба прогона получают одни и те же узкие времена: межпрогонное правило
# после этого молчит (расхождение между прогонами равно нулю), прогоны о
# применимости записанного правила согласны, — и записанное правило
# обязано сработать В ОДИНОЧКУ. Промолчит разбор — значит применяется не
# оно.
_narrow_times = _set_times(_NARROW_WARM, _NARROW_REBUILD)
# Прогоны разведены по разные стороны порога, причём в ту сторону,
# КОТОРОЙ НЕТ В СНЯТЫХ ДАННЫХ: там правило снимает утверждение на первом
# прогоне и молчит на втором, здесь наоборот. Проверка, привязанная к
# одному направлению, была бы слепа ко второму.
_wide_times = _set_times(_WIDE_WARM, _WIDE_REBUILD)


def _drop_marker_case():
    """Убрать маркер COMPLETE — чтение обязано отказать."""
    def action():
        with tempfile.TemporaryDirectory() as tmp:
            main = os.path.join(tmp, "build.jsonl")
            run1 = os.path.join(tmp, "build.run-1.jsonl")
            shutil.copyfile(FIXTURE, main)
            shutil.copyfile(FIXTURE_RUN1, run1)
            text = io.open(main, encoding="utf-8").read()
            if "COMPLETE" not in text:
                raise SabotageDidNotApply("в копии фикстуры нет маркера")
            io.open(main, "w", encoding="utf-8", newline="\n").write(
                text.replace("COMPLETE\n", ""))
            code, out = _run(
                [sys.executable, "-m", "unittest",
                 "scripts.test_build_analysis.FixtureShape"
                 ".test_all_arms_present"],
                env={"BUILD_FIXTURE": main, "BUILD_FIXTURE_RUN1": run1})
            if code == 0:
                return False, ("оборванная фикстура прочиталась как полная")
            return True, "оборванная фикстура отвергнута при чтении"
    return action



def _machinery_package_tool_versions_follow_the_image():
    """Измеритель версий читает ОБРАЗ, а не отвечает всегда одно и то же.

    Снимает версии инструментов в двух образах сборки пары про
    устаревшее правило. Требуется двойное: у одного и того же образа два
    вызова обязаны дать одно и то же (измеритель устойчив), у двух
    разных образов пары — разные компилятор и libc (измеритель следует
    за веществом). Измеритель, отвечающий константой, проходит первую
    половину и валится на второй.
    """
    sys.path.insert(0, HERE)
    from scripts import docker as dockerlib

    new_tag = dockerlib.build_builder("glibc-static")
    old_tag = dockerlib.build_builder("glibc-static-old")
    first = dockerlib.tool_versions(new_tag)
    again = dockerlib.tool_versions(new_tag)
    if first != again:
        return False, ("два замера одного образа разошлись: %r против %r"
                       % (first, again))
    other = dockerlib.tool_versions(old_tag)
    if first["cxx_version"] == other["cxx_version"]:
        return False, ("компилятор у двух РАЗНЫХ образов сборки измерился "
                       "одинаково (%r) — измеритель отвечает константой"
                       % first["cxx_version"])
    if first["libc_version"] == other["libc_version"]:
        return False, ("libc у двух РАЗНЫХ образов сборки измерилась "
                       "одинаково (%r)" % first["libc_version"])
    if first["cmake_version"] != other["cmake_version"]:
        return False, ("cmake у половин пары разошёлся (%r против %r) — "
                       "равенство, удержанное вручную, сломалось"
                       % (first["cmake_version"], other["cmake_version"]))
    return True, ("измеритель устойчив на одном образе и различает два: "
                  "%s / %s" % (other["cxx_version"], first["cxx_version"]))


def _package_fixture_case(mutate, test):
    """Диверсия над КОПИЕЙ fixtures/package.jsonl.

    Проверки оси упаковки берут путь фикстуры из PACKAGE_FIXTURE — так
    же, как проверки прочих осей. Файл репозитория не трогается.
    """
    def action():
        rows = _read_rows(PACKAGE_FIXTURE)
        before = json.dumps(rows, sort_keys=True)
        mutate(rows)
        if json.dumps(rows, sort_keys=True) == before:
            raise SabotageDidNotApply("копия фикстуры не изменилась")
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "package.jsonl")
            _write_rows(path, rows)
            code, out = _run(
                [sys.executable, "-m", "unittest",
                 "scripts.test_package_analysis.%s" % test],
                env={"PACKAGE_FIXTURE": path})
            if code == 0:
                return False, ("проверка %s УСТОЯЛА под диверсией" % test)
            return True, "проверка %s упала, как и требовалось" % test
    return action


def _link_fixture_case(mutate, test):
    """Диверсия над КОПИЕЙ fixtures/link.jsonl.

    Проверки предупреждения компоновщика берут путь из LINK_FIXTURE — так
    же, как проверки прочих осей. Файл репозитория не трогается.
    """
    def action():
        rows = _read_rows(LINK_FIXTURE)
        before = json.dumps(rows, sort_keys=True)
        mutate(rows)
        if json.dumps(rows, sort_keys=True) == before:
            raise SabotageDidNotApply("копия фикстуры линковки не изменилась")
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "link.jsonl")
            _write_rows(path, rows)
            code, out = _run(
                [sys.executable, "-m", "unittest",
                 "scripts.test_link_warning.%s" % test],
                env={"LINK_FIXTURE": path})
            if code == 0:
                return False, ("проверка %s УСТОЯЛА под диверсией" % test)
            return True, "проверка %s упала, как и требовалось" % test
    return action


def _link_analysis_case(mutate, expect_text):
    """Мутация копии обязана ОСТАНОВИТЬ разбор оси упаковки.

    Печать про одинаковую строку при разных исходах не должна
    появляться поверх недействительных данных: если линковка не шла,
    отсутствие предупреждения ничего не значит.
    """
    def action():
        rows = _read_rows(LINK_FIXTURE)
        before = json.dumps(rows, sort_keys=True)
        mutate(rows)
        if json.dumps(rows, sort_keys=True) == before:
            raise SabotageDidNotApply("копия фикстуры линковки не изменилась")
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "link.jsonl")
            _write_rows(path, rows)
            code, out = _run(
                [sys.executable, os.path.join(HERE, "scripts",
                                              "analyze-package.py")],
                env={"LINK_FIXTURE": path})
            if code == 0:
                return False, ("разбор договорил до конца поверх "
                               "недействительных данных — код возврата 0")
            if expect_text not in out:
                return False, ("в выводе разбора нет %r; получено: %s"
                               % (expect_text, out[-500:]))
            return True, "разбор остановился: %r" % expect_text
    return action


def _link_row(rows, variant):
    for row in rows:
        if row.get("variant") == variant:
            return row
    raise SabotageDidNotApply("в фикстуре линковки нет плеча %r" % variant)


def _link_warning_declared_without_the_output(rows):
    """Предупреждение ОБЪЯВЛЕНО там, где его нет в перехваченном выводе.

    Ровно та болезнь, на которой стенд спотыкался дважды: измеритель
    отвечает по объявлению, а не по веществу.
    """
    _link_row(rows, "musl-static")["link_warning_present"] = True


def _link_warning_erased_from_the_pair(rows):
    """У одного плеча пары предупреждение исчезло вместе с выводом."""
    row = _link_row(rows, "glibc-static")
    row["link_warning_present"] = False
    row["link_warning_lines"] = []
    row["link_stderr"] = ""


def _link_warning_text_diverges(rows):
    """Тексты предупреждения у плеч пары разошлись.

    Признак при этом на месте и с выводом согласен — падает именно
    утверждение об ОДИНАКОВОМ предупреждении, а не о его наличии.
    """
    row = _link_row(rows, "glibc-static-old")
    anchor_text = "glibc version used for linking"
    if anchor_text not in row["link_stderr"]:
        raise SabotageDidNotApply("в выводе плеча нет якоря правки")
    row["link_stderr"] = row["link_stderr"].replace(
        anchor_text, anchor_text + " (bullseye)")
    row["link_warning_lines"] = [
        line.replace(anchor_text, anchor_text + " (bullseye)")
        for line in row["link_warning_lines"]]


def _link_step_did_not_run(rows):
    """Линковки не было — отсутствие предупреждения ничего не значит."""
    row = _link_row(rows, "musl-static")
    row["link_step_ran"] = False
    row["link_stdout"] = ""


def _link_warning_line_invented(rows):
    """К строкам предупреждения дописана та, которой нет в сыром выводе."""
    _link_row(rows, "glibc-static")["link_warning_lines"].append(
        "main.cpp:(.text.startup+0x<смещение>): warning: Using "
        "'gethostbyname' in statically linked applications requires at "
        "runtime the shared libraries from the glibc version used for linking")


def _link_variant_dropped(rows):
    """Плечо выпало из замера — молча выглядело бы плечом без предупреждения."""
    for index, row in enumerate(rows):
        if row.get("variant") == "distroless":
            del rows[index]
            return
    raise SabotageDidNotApply("в фикстуре линковки нет плеча distroless")


def _machinery_link_warning_follows_the_static_flag():
    """Собрать то же плечо БЕЗ -static — предупреждение обязано исчезнуть.

    Единственное утверждение блока link_warning в expected.json, которое
    до прогона не проверялось ничем. Без него признак мог бы отвечать
    «предупреждение есть» просто потому, что образ сборки такой: та же
    команда, тот же исходник, тот же компоновщик — меняется ровно
    статическая линковка.
    """
    rel = os.path.join("images", "Dockerfile.glibc-static")
    path = os.path.join(HERE, rel)
    tag = "cpp-build-and-package-sabotage-link:nostatic"
    try:
        _patch_file(path, "-DSTAND_STATIC=ON", "-DSTAND_STATIC=OFF")
        code, out = _run(["docker", "build", "--target", "build",
                          "-f", path, "-t", tag, HERE])
        if code != 0:
            return False, ("плечо без статической линковки не собралось: %s"
                           % out[-400:])
        code, out = _run(
            ["docker", "run", "--rm", tag, "sh", "-c",
             "rm -f /build/stand_service && cmake --build /build -j"])
        if code != 0:
            return False, "перелинковка не прошла: %s" % out[-400:]
        if LINK_STEP_MARKER not in out:
            raise SabotageDidNotApply(
                "линковка не состоялась (%r в выводе нет) — диверсия "
                "ничего не показала" % LINK_STEP_MARKER)
        if LINK_WARNING_MARKER in out:
            return False, ("предупреждение осталось и без статической "
                           "линковки — признак отвечает одно и то же "
                           "независимо от того, что собрано")
        return True, ("без -static предупреждение исчезло — признак следует "
                      "за веществом")
    finally:
        _git_restore(rel)
        _run(["docker", "rmi", "-f", tag])


def _package_unpacked_follows_the_packed(rows):
    """Распакованный объём образа объявлен равным упакованному.

    Так выглядела бы величина, снятая тем же измерителем, что и
    упакованный размер: спутник, который на деле стережёт не содержимое,
    а самого себя.
    """
    for row in rows:
        if row.get("probe") == "healthz":
            row["image_unpacked_bytes"] = row["image_bytes"]


def _package_unpacked_dropped(rows):
    """Распакованного объёма в фикстуре нет вовсе."""
    for row in rows:
        if row.get("probe") == "healthz":
            row.pop("image_unpacked_bytes", None)



def _package_tool_versions_dropped(rows):
    """Версий инструментов в фикстуре нет вовсе.

    Так ось упаковки и выглядела до вычитки: различия между вариантами
    держались на имени образа в variants.json.
    """
    for row in rows:
        if row.get("probe") == "healthz":
            for field in ("cxx_version", "cmake_version", "libc_version"):
                row.pop(field, None)


def _package_pair_compiler_declared_identical(rows):
    """У половин пары компилятор записан одинаковым.

    Ровно то, что четыре формулировки статьи утверждали словами:
    различается «ровно версия библиотеки». Фикстура обязана уметь этому
    возразить.
    """
    new = next(r for r in rows if r.get("probe") == "healthz"
               and r["variant"] == "glibc-static")
    old = next(r for r in rows if r.get("probe") == "healthz"
               and r["variant"] == "glibc-static-old")
    old["cxx_version"] = new["cxx_version"]


def _package_pair_cmake_diverges(rows):
    """Версия cmake у половин пары разошлась — третья переменная."""
    old = next(r for r in rows if r.get("probe") == "healthz"
               and r["variant"] == "glibc-static-old")
    old["cmake_version"] = "cmake version 3.18.4"


def _package_tool_versions_follow_the_builder(rows):
    """Вариант с чужим образом сборки получил чужие версии.

    glibc-static-old собран в debian:bullseye, а версии ему проставлены
    от gcc:15. Проверка «варианты с одним образом сборки называют одни
    версии» обязана это заметить.
    """
    new = next(r for r in rows if r.get("probe") == "healthz"
               and r["variant"] == "glibc-static")
    dyn = next(r for r in rows if r.get("probe") == "healthz"
               and r["variant"] == "glibc-dynamic")
    dyn["cxx_version"] = new["cxx_version"] + " (подменено)"


def _manifest_case(mutate, test, note):
    """Диверсия над сводками прогона: две сводки во временном каталоге.

    Сводки — данные, а не код, и в репозитории их пока одна. Чтобы
    диверсия могла что-то уронить, каталог сводок подставляется через
    MANIFESTS_DIR: настоящая сводка копируется под двумя именами, вторая
    портится, и проверка обязана покраснеть.

    Так же проверяется и обратное требование: свидетель платформы,
    СОВПАВШИЙ у двух сводок, означает, что прогон был один, и проверка
    обязана это заметить.
    """
    def action():
        source = os.path.join(HERE, "fixtures", "manifests")
        names = [n for n in sorted(os.listdir(source)) if n.endswith(".json")]
        if not names:
            raise SabotageDidNotApply(
                "в fixtures/manifests нет ни одной сводки: диверсии не над "
                "чем применяться")
        with io.open(os.path.join(source, names[0]), encoding="utf-8") as h:
            first = json.loads(h.read())
        second = json.loads(json.dumps(first))
        # Вторая сводка притворяется снятой на другой платформе: без
        # этого различия проверка совпадения даже не дошла бы до предмета
        # диверсии.
        second["environment"]["platform"] = "windows"
        second["environment"]["stand_root"] = r"G:\stand\build-and-package"
        before = json.dumps(second, sort_keys=True)
        mutate(second)
        if json.dumps(second, sort_keys=True) == before:
            raise SabotageDidNotApply("копия сводки не изменилась")
        temp = tempfile.mkdtemp(prefix="sabotage-manifests-")
        try:
            for name, data in (("linux.json", first), ("windows.json", second)):
                with io.open(os.path.join(temp, name), "w",
                             encoding="utf-8", newline="\n") as handle:
                    handle.write(json.dumps(data, ensure_ascii=False,
                                            indent=2, sort_keys=True))
            code, out = _run(
                [sys.executable, "-m", "unittest",
                 "scripts.test_manifests_agree.%s" % test],
                env={"MANIFESTS_DIR": temp})
            if code == 0:
                return False, ("проверка %s УСТОЯЛА под диверсией (%s)"
                               % (test, note))
            return True, "проверка %s упала, как и требовалось" % test
        finally:
            shutil.rmtree(temp, ignore_errors=True)
    return action


def _manifest_value_changed(manifest):
    """У второй сводки разошёлся размер бинарника плеча cpp."""
    values = manifest["values"]["siblings_binary_bytes"]
    values["cpp"] = values["cpp"] + 8


def _manifest_witness_equal(manifest):
    """Свидетель платформы у второй сводки тот же, что у первой."""
    manifest["environment"]["stand_root"] = (
        "/mnt/g/7/Projects/Khorost/digital-cookbook-staging/cpp/"
        "build-and-package")


def _manifest_tolerated_value_drifts_too_far(manifest):
    """Упакованный размер образа уехал дальше названного допуска.

    Допуск покрывает разницу УПАКОВКИ (биты прав доступа файлов
    контекста, единицы байт). Сдвиг за его пределы означает уже другое
    содержимое, и сверка обязана это увидеть — иначе допуск был бы
    просто выключенной проверкой.
    """
    values = manifest["values"]["package_image_bytes"]
    values["musl-static"] = values["musl-static"] + 4096


def _manifest_exact_companion_dropped(manifest):
    """У величины под допуском убрали точного спутника."""
    manifest["values"].pop("package_image_unpacked_bytes")


def _manifest_exclusion_unexplained(manifest):
    """Поле исключено из сравнения без объяснения."""
    manifest["not_compared"]["cold_seconds"] = "  "


# --------------------------------------------------------------------
# Диверсии по закреплению версий
#
# Проверки в scripts/test_pinning.py читают ФОРМУ: что образ назван
# дайджестом, что архив сверяется до распаковки, что реестр закреплён
# суммой коммита. Форма читается глазами, и потому эти проверки особенно
# легко написать так, что они не сторожат ничего. Каждая ниже роняется
# ровно тем изменением, против которого заведена, и откатывается
# побайтно.
#
# Две последние — над машинерией: они портят СУММУ и требуют, чтобы
# сборка встала. Без них весь набор доказывал бы только то, что в файле
# есть нужные слова: строчку `sha256sum -c` можно написать и так, что
# она ни на что не влияет, а ручка, которая никуда не подключена, уже
# попадалась стенду.


def _readme_count_drifts():
    """Число проверок в README разошлось с набором.

    Не _source_case: якорь пришлось бы записать числом, а число меняется
    от каждой новой проверки — диверсия протухала бы ровно так же, как
    протухала отметка, против которой она заведена. Здесь число читается
    из README и сдвигается на единицу, каким бы оно ни было.
    """
    path = os.path.join(HERE, "README.md")
    pattern = re.compile(r"(unittest discover[^\n#]*#\s*)(\d+)(\s+провер)")

    def action():
        before = io.open(path, "rb").read()
        text = before.decode("utf-8")
        found = pattern.findall(text)
        if len(found) != 1:
            raise SabotageDidNotApply(
                "строка с числом проверок найдена %d раз(а) — правка "
                "отвергнута" % len(found))
        shifted = pattern.sub(
            lambda m: "%s%d%s" % (m.group(1), int(m.group(2)) + 1, m.group(3)),
            text)
        if shifted == text:
            raise SabotageDidNotApply("README не изменился")
        io.open(path, "wb").write(shifted.encode("utf-8"))
        try:
            code, out = _run([
                sys.executable, "-m", "unittest",
                "scripts.test_readme_matches_fixtures.ReadmeCountsAreCurrent."
                "test_the_check_and_sabotage_counts_match_the_code"])
            if code == 0:
                return False, "проверка числа проверок УСТОЯЛА под диверсией"
            return True, "проверка упала, как и требовалось"
        finally:
            io.open(path, "wb").write(before)
            if io.open(path, "rb").read() != before:
                raise RuntimeError("README не вернулся к исходному виду")
    return action


def _machinery_pinning_archive_sum_is_connected():
    """Сумма архива испорчена — сборка обязана ВСТАТЬ на сверке.

    Проверка test_pinning читает текст: сумма есть, и стоит она раньше
    распаковки. Прочитать по тексту, ДЕЙСТВИТЕЛЬНО ли она что-то решает,
    нельзя: строчку можно написать и так, что её код возврата никто не
    смотрит. Здесь сумма подменяется заведомо неверной, и сборка обязана
    остановиться ДО того, как дойдёт до компилятора.
    """
    rel = os.path.join("images", "Dockerfile.musl-static")
    path = os.path.join(HERE, rel)
    before = io.open(path, "rb").read()
    try:
        _patch_file(path,
                    'echo "%s  /tmp/fmt.tar.gz"' % FMT_ARCHIVE_SHA256,
                    'echo "%s  /tmp/fmt.tar.gz"' % ("0" * 64))
        code, out = _docker_build("Dockerfile.musl-static",
                                  "sabotage-pinning:musl-static")
        if code == 0:
            return False, ("сборка прошла с ЗАВЕДОМО НЕВЕРНОЙ суммой архива: "
                           "сверка написана, но ни на что не влияет")
        if "sha256sum" not in out and "FAILED" not in out:
            return False, ("сборка упала, но не на сверке суммы: %s"
                           % out[-400:])
        if "Building CXX object" in out:
            return False, ("сборка дошла до компиляции — значит распаковка "
                           "случилась раньше сверки")
        return True, "сборка встала на сверке суммы, до компиляции"
    finally:
        io.open(path, "wb").write(before)
        if io.open(path, "rb").read() != before:
            raise RuntimeError("%s не вернулся к исходному виду" % rel)
        _run(["docker", "rmi", "-f", "sabotage-pinning:musl-static"])


def _machinery_pinning_wheel_hash_is_connected():
    """Сумма колеса испорчена — pip обязан ОТКАЗАТЬСЯ ставить.

    Тот же вопрос, что и у архива, но к другому механизму: --require-hashes
    объявлен в Dockerfile, а работает ли он — видно только по отказу.
    Портится сумма зависимости, а не самого conan: подмена в глубине
    списка проходит мимо взгляда легче всего.
    """
    rel = os.path.join("images", "conan-requirements.txt")
    path = os.path.join(HERE, rel)
    before = io.open(path, "rb").read()
    try:
        _patch_file(path, "--hash=sha256:%s" % SIX_WHEEL_SHA256,
                    "--hash=sha256:%s" % ("0" * 64))
        code, out = _docker_build("Dockerfile.build-base",
                                  "cpp-build-and-package-base:tools")
        if code != 0:
            raise SabotageDidNotApply(
                "базовый образ не собрался, диверсию не на чем проверить: %s"
                % out[-400:])
        code, out = _docker_build("Dockerfile.build-conan",
                                  "sabotage-pinning:conan")
        if code == 0:
            return False, ("pip поставил набор с ЗАВЕДОМО НЕВЕРНОЙ суммой: "
                           "--require-hashes ни на что не влияет")
        if "hash" not in out.lower():
            return False, ("сборка упала, но не на сверке сумм: %s"
                           % out[-400:])
        return True, "pip отказался ставить колесо с неверной суммой"
    finally:
        io.open(path, "wb").write(before)
        if io.open(path, "rb").read() != before:
            raise RuntimeError("%s не вернулся к исходному виду" % rel)
        _run(["docker", "rmi", "-f", "sabotage-pinning:conan"])


VCPKG_VERSION_STEP_BEFORE = 'RUN sh /images/fmt-version.sh \\\n      /manifest/vcpkg_installed/x64-linux/include /fmt_version.txt\n\nARG SOURCE_STAMP=0\nCOPY service /src\nRUN cmake -S /src -B /build -DCMAKE_BUILD_TYPE=Release \\\n      -DCMAKE_TOOLCHAIN_FILE=/opt/vcpkg/scripts/buildsystems/vcpkg.cmake \\\n      -DVCPKG_MANIFEST_MODE=OFF \\\n      -DCMAKE_PREFIX_PATH=/manifest/vcpkg_installed/x64-linux \\\n && cmake --build /build -j \\\n && strip /build/stand_service\n'
VCPKG_VERSION_STEP_AFTER = 'ARG SOURCE_STAMP=0\nCOPY service /src\nRUN cmake -S /src -B /build -DCMAKE_BUILD_TYPE=Release \\\n      -DCMAKE_TOOLCHAIN_FILE=/opt/vcpkg/scripts/buildsystems/vcpkg.cmake \\\n      -DVCPKG_MANIFEST_MODE=OFF \\\n      -DCMAKE_PREFIX_PATH=/manifest/vcpkg_installed/x64-linux \\\n && cmake --build /build -j \\\n && strip /build/stand_service\n\nRUN sh /images/fmt-version.sh \\\n      /manifest/vcpkg_installed/x64-linux/include /fmt_version.txt\n'

SABOTAGES = [
    # -------------------------------------------------- fixture
    ("offline-control-failed", "fixture",
     "контроль system-offline объявлен упавшим",
     _fixture_case(_set("system-offline", build_ok=False,
                        failure_stage="network"),
                   "Controls.test_prebaked_arm_builds_without_network")),
    ("pair-versions-split", "fixture",
     "контролируемая пара vcpkg/conan разведена по версии fmt",
     _fixture_case(_set("conan", fmt_version="10.2.1"),
                   "ArmsAreComparable.test_vcpkg_and_conan_share_the_fmt_"
                   "version")),
    ("cold-faster-than-warm", "fixture",
     "у vcpkg холодная сборка сделана быстрее тёплой",
     _fixture_case(_set("vcpkg", cold_seconds=0.5),
                   "ColdIsNotFasterThanWarm.test_cold_not_faster_than_warm")),
    ("system-linked-static", "fixture",
     "системному плечу приписана статическая линковка",
     _fixture_case(_set("system", fmt_linkage="static"),
                   "LinkageIsDecidedByTheSource.test_system_links_fmt_"
                   "dynamically")),
    ("system-pair-split", "fixture",
     "пара system/system-prebaked разведена по версии fmt",
     _fixture_case(_set("system-prebaked", fmt_version="11.0.2"),
                   "MeasurementWindow.test_system_pair_differs_only_by_"
                   "place_of_installation")),
    ("static-archive-appeared", "fixture",
     "в пакете libfmt-dev появился статический архив",
     _fixture_case(_set("system", distro_fmt_static_archives=1),
                   "LinkageIsDecidedByTheSource.test_distro_package_ships_"
                   "no_static_archive")),
    ("window-collapsed", "fixture",
     "у плеча system окно замера объявлено внешним",
     _fixture_case(_set("system", measurement_window="dependency_outside"),
                   "MeasurementWindow.test_compared_arms_share_one_window")),
    ("latest-version-erased", "fixture",
     "у контроля vcpkg-latest стёрта измеренная версия fmt",
     _fixture_case(_set("vcpkg-latest", fmt_version=None),
                   "Controls.test_unpinned_registry_version_is_measured_"
                   "not_remembered")),
    ("complete-marker-removed", "fixture",
     "из фикстуры убран маркер COMPLETE",
     _drop_marker_case()),

    # -------------------------------------------------- analysis
    ("analysis-offline-control-failed", "analysis",
     "контроль system-offline объявлен упавшим — разбор обязан встать",
     _analysis_case(_set("system-offline", build_ok=False,
                         failure_stage="network"),
                    "КОНТРОЛИ НЕ ДАЛИ ОЖИДАЕМОГО")),
    ("analysis-nofmt-wrong-stage", "analysis",
     "контроль no-fmt упал не на своём шаге",
     _analysis_case(_set("no-fmt", failure_stage="network"),
                    "упал на 'network' вместо 'find_package'")),
    ("analysis-cold-faster", "analysis",
     "у conan холодная сборка быстрее тёплой",
     _analysis_case(_set("conan", cold_seconds=0.4),
                    "ХОЛОДНАЯ СБОРКА БЫСТРЕЕ ТЁПЛОЙ")),
    ("analysis-both-managers-failed", "analysis",
     "оба менеджерских плеча объявлены упавшими",
     _analysis_case(_both_managers_failed,
                    "упали оба менеджера")),
    ("analysis-measured-arm-failed", "analysis",
     "измеряемое плечо conan объявлено упавшим — вердикт, а не TypeError",
     _analysis_case(_set("conan", build_ok=False, warm_seconds=None,
                         rebuild_seconds=None, binary_bytes=None),
                    "ИЗМЕРЯЕМОЕ ПЛЕЧО НЕ СОБРАЛОСЬ")),
    ("run1-warm-spread-widened", "fixture",
     "первому прогону раздвинуты тёплая и пересборка — прогоны перестают "
     "расходиться, и проверка, которая держит формулировку статьи, "
     "обязана покраснеть",
     _fixture_case(None,
                   "RecordedRuleIsTheOneApplied"
                   ".test_the_axis_fixtures_disagree_about_warm",
                   mutate_run1=_wide_times)),
    ("both-runs-wide-and-steady", "fixture",
     "оба прогона получают широкие и одинаковые времена — не срабатывает "
     "ни одно правило неинформативности, и проверка «вывода нет ни по "
     "одной величине» обязана покраснеть",
     _fixture_case(_wide_times,
                   "RecordedRuleIsTheOneApplied"
                   ".test_neither_warm_nor_rebuild_yields_a_verdict",
                   mutate_run1=_wide_times)),
    ("analysis-recorded-rule-fires-alone", "analysis",
     "оба прогона сведены к одним узким временам — записанное правило "
     "обязано сработать в одиночку",
     _analysis_case(_narrow_times, "Сработало правил: 1",
                    mutate_run1=_narrow_times, expect_ok=True)),
    ("analysis-runs-disagree-the-other-way", "analysis",
     "прогоны разведены по разные стороны порога в направлении, "
     "которого нет в снятых данных — расхождение обязано быть названо",
     _analysis_case(_narrow_times, "РАСХОДЯТСЯ",
                    mutate_run1=_wide_times, expect_ok=True)),

    # -------------------------------------------------- machinery
    ("libfmt-in-base", "machinery",
     "libfmt-dev подложен в базовый образ — контроль no-fmt обязан "
     "позеленеть",
     _machinery_libfmt_in_base),
    ("prebaked-touches-network", "machinery",
     "плечо с готовой зависимостью заставлено ходить в сеть",
     _machinery_prebaked_touches_network),
    ("conan-shared-fmt", "machinery",
     "conan собран с shared=True — измеритель линковки обязан ответить "
     "dynamic",
     _machinery_conan_shared),
    ("vcpkg-without-network", "machinery",
     "vcpkg собран без сети — классификатор обязан ответить network",
     _machinery_vcpkg_without_network),
    ("poison-conan-header", "machinery",
     "испорчен FMT_VERSION в заголовке установленного пакета — измеритель "
     "версии обязан вернуть испорченное число",
     _machinery_poison_conan_header),
    ("two-fmt-headers", "machinery",
     "подложен второй заголовок с другим FMT_VERSION — измеритель обязан "
     "упасть",
     _machinery_two_fmt_headers),

    # -------------------------------------------------- repro (ось 3)
    ("repro-nonce-equal", "repro",
     "метки прогона у двух сборок сведены к одной — улика кэша",
     _repro_fixture_case(
         lambda rows: rows["plain"].update(
             nonce_second=rows["plain"]["nonce_first"]),
         "CacheDidNotMeasureItself.test_nonces_differ_within_every_arm")),
    ("repro-mtime-equal", "repro",
     "время изменения бинарника у двух сборок сведено к одному",
     _repro_fixture_case(
         lambda rows: rows["plain"].update(
             binary_mtime_second=rows["plain"]["binary_mtime_first"]),
         "CacheDidNotMeasureItself.test_binary_mtime_differs_within_every_"
         "arm")),
    ("repro-mtime-shared-between-arms", "repro",
     "две сборки РАЗНЫХ плеч получают одно время изменения — внутри плеч "
     "всё безупречно",
     _repro_fixture_case(
         _repro_shared_mtime_between_arms,
         "CacheDidNotMeasureItself.test_all_binary_mtimes_in_a_run_are_"
         "pairwise_distinct")),
    ("repro-no-cache-cleared", "repro",
     "сборкам приписан запуск без --no-cache при ключе в команде",
     _repro_fixture_case(
         _set("plain", no_cache=False),
         "CacheDidNotMeasureItself.test_no_cache_declaration_matches_the_"
         "command_that_ran")),
    ("repro-no-cache-key-dropped", "repro",
     "ключ --no-cache убран из команды, а поле осталось true",
     _repro_fixture_case(
         _repro_drop_no_cache_key,
         "CacheDidNotMeasureItself.test_no_cache_declaration_matches_the_"
         "command_that_ran")),
    ("repro-network-key-dropped", "repro",
     "ключ --network убран из команды, а поле осталось",
     _repro_fixture_case(
         _repro_drop_network_key,
         "CacheDidNotMeasureItself.test_network_declaration_matches_the_"
         "command_that_ran")),
    ("repro-control-converged", "repro",
     "отрицательный контроль объявлен сошедшимся — измеритель не умеет "
     "сказать «различаются»",
     _repro_fixture_case(
         _repro_converged_control,
         "MeasurerCanSayDifferent.test_controls_diverge")),
    ("repro-positive-control-converged", "repro",
     "положительный контроль объявлен сошедшимся — у лечения нет канала",
     _repro_fixture_case(
         _repro_converged_positive_control,
         "MeasurerCanSayDifferent.test_controls_diverge")),
    ("repro-pair-uncontrolled", "repro",
     "плечу pinned приписано лечение плеча plain — пара перестаёт мерить",
     _repro_fixture_case(
         _repro_uncontrolled_pair,
         "ControlledPair.test_treatment_differs_between_arms_of_every_pair")),
    ("repro-debug-pair-uncontrolled", "repro",
     "плечу debug-pinned приписано лечение debug-plain — вторая пара "
     "перестаёт мерить",
     _repro_fixture_case(
         _repro_uncontrolled_debug_pair,
         "ControlledPair.test_treatment_differs_between_arms_of_every_pair")),
    ("repro-identical-disagrees", "repro",
     "поле identical разведено с суммами",
     _repro_fixture_case(
         _set("plain", identical=False),
         "IdenticalFollowsFromTheSums.test_identical_matches_the_sums")),
    ("repro-toolchain-split", "repro",
     "плечи пары разведены по версии компилятора",
     _repro_fixture_case(
         _set("pinned", cxx_version="g++ (GCC) 14.0.0"),
         "ControlledPair.test_pair_arms_share_toolchain_and_dependency")),
    ("repro-cmake-split-within-arm", "repro",
     "две сборки одного плеча разведены по версии cmake — раньше внутри "
     "плеча сверялся только компилятор",
     _repro_fixture_case(
         _repro_cmake_split_within_arm,
         "ControlledPair.test_toolchain_is_the_same_in_both_builds_of_an_"
         "arm")),
    ("repro-build-dir-invented", "repro",
     "каталог сборки в поле не тот, который назвал шаг сборки",
     _repro_fixture_case(
         _repro_invented_build_dir,
         "ControlledPair.test_build_dir_field_is_named_by_the_treatment")),
    ("repro-debug-arm-lost-g", "repro",
     "у плеча с отладочной информацией из лечения пропал -g",
     _repro_fixture_case(
         _repro_debug_arm_lost_g,
         "ControlledPair.test_debug_arms_really_carry_g")),
    ("repro-strip-misreported", "repro",
     "плечу без strip приписан выполненный strip",
     _repro_fixture_case(
         _repro_strip_misreported,
         "ControlledPair.test_strip_step_matches_the_arm")),
    ("repro-debug-binary-shrunk", "repro",
     "бинарник с -g того же размера, что стриппнутый — значит -g не "
     "применялся",
     _repro_fixture_case(
         _repro_debug_binary_shrunk,
         "ControlledPair.test_unstripped_binaries_are_larger_than_stripped_"
         "ones")),
    ("repro-run1-nonce-equal", "repro",
     "улика кэша испорчена ТОЛЬКО в первом прогоне — проверки обязаны "
     "дотягиваться и до него",
     _repro_fixture_case(
         lambda rows: rows["plain"].update(
             nonce_second=rows["plain"]["nonce_first"]),
         "CacheDidNotMeasureItself.test_nonces_differ_within_every_arm",
         which="run1")),

    # -------------------------------------------------- repro, разбор
    ("repro-analysis-cache", "repro",
     "улика кэша в фикстуре — разбор обязан встать до таблицы",
     _repro_analysis_case(
         lambda rows: rows["pinned"].update(
             nonce_second=rows["pinned"]["nonce_first"]),
         "СБОРКА БЫЛА ОДНА, А НЕ ДВЕ")),
    ("repro-analysis-mtime-shared", "repro",
     "одно время изменения на два плеча — разбор обязан встать",
     _repro_analysis_case(_repro_shared_mtime_between_arms,
                          "СБОРКА БЫЛА ОДНА, А НЕ ДВЕ")),
    ("repro-analysis-control-converged", "repro",
     "отрицательный контроль сошёлся — разбор обязан объявить ось "
     "недействительной",
     _repro_analysis_case(_repro_converged_control,
                          "ИЗМЕРИТЕЛЬ НЕ УМЕЕТ ОТВЕТИТЬ")),
    ("repro-analysis-positive-control", "repro",
     "положительный контроль сошёлся — разбор обязан сказать, что канала "
     "для лечения нет",
     _repro_analysis_case(_repro_converged_positive_control,
                          "КАНАЛА ДЛЯ ЛЕЧЕНИЯ НЕТ")),
    ("repro-analysis-pair", "repro",
     "пара получила одно лечение — разбор обязан встать",
     _repro_analysis_case(_repro_uncontrolled_pair,
                          "ПАРА НЕ КОНТРОЛИРУЕМА")),
    ("repro-analysis-debug-pair", "repro",
     "пара с -g получила одно лечение — разбор обязан встать",
     _repro_analysis_case(_repro_uncontrolled_debug_pair,
                          "ПАРА НЕ КОНТРОЛИРУЕМА")),
    ("repro-analysis-run1-cache", "repro",
     "улика кэша ТОЛЬКО в первом прогоне — разбор обязан встать и на нём",
     _repro_analysis_case(
         _repro_shared_mtime_between_arms,
         "СБОРКА БЫЛА ОДНА, А НЕ ДВЕ — ТАБЛИЦЫ НЕТ (прогон 1)",
         which="run1")),

    ("repro-mechanism-pair-stray-flag", "repro",
     "плечу механизма добавлен посторонний флаг — «отличается ровно шагом "
     "strip» перестаёт быть верным",
     _repro_fixture_case(
         _repro_stray_flag_in_mechanism_pair,
         "ControlledPair.test_mechanism_pair_differs_only_by_the_strip_"
         "step")),
    ("repro-analysis-mechanism-pair", "repro",
     "тот же посторонний флаг — разбор обязан встать до таблицы",
     _repro_analysis_case(_repro_stray_flag_in_mechanism_pair,
                          "ПАРА НЕ КОНТРОЛИРУЕМА")),
    ("repro-analysis-mechanism-strip-equal", "repro",
     "шаг strip у пары механизма одинаков — разбор обязан встать",
     _repro_analysis_case(_repro_mechanism_pair_same_strip,
                          "ПАРА НЕ КОНТРОЛИРУЕМА")),

    # -------------------------------------------------- repro, ожидания
    ("repro-probe-filed-as-prediction", "expectations",
     "исход плеча, известный из пробы, переложен в блок гипотез — знание "
     "выдано за предвидение",
     _expected_case(
         '"debug_arms_are_really_built_with_g": {',
         '"debug-plain": {"expected_identical": false},\n'
         '      "debug_arms_are_really_built_with_g": {',
         "ExpectationsWereRecordedBeforeTheRun.test_probe_results_are_not_"
         "filed_as_predictions")),
    ("repro-claim-scope-widened", "expectations",
     "область утверждения возвращена к исходнику вместо конфигурации",
     _expected_case(
         "вывод формулируется с указанием КОНФИГУРАЦИИ, а не исходника: "
         "«в этой конфигурации сборки — Release, без -g, со strip — флаги "
         "закрепления ничего не меняют: совпадение получено и без них»",
         "вывод формулируется так: «на этом исходнике закреплять нечего». "
         "Это утверждение про данный исходник",
         "ExpectationsWereRecordedBeforeTheRun.test_claim_scope_names_the_"
         "configuration_not_the_source")),

    ("repro-hypotheses-caveat-dropped", "expectations",
     "из печати разбора убрана оговорка о том, что гипотезами были не все "
     "три пункта — знание снова читалось бы как предвидение",
     _source_case(
         "scripts/analyze-repro.py",
         '    "  ОГОВОРКА: ГИПОТЕЗАМИ БЫЛИ НЕ ВСЕ ТРИ.",',
         '    "  Все три пункта ниже были предсказаниями.",',
         "HypothesesCaveatIsPrinted.test_caveat_names_the_single_firm_"
         "prognosis")),
    ("repro-caveat-hides-the-inverted-justification", "expectations",
     "из оговорки убрано указание на перевёрнутое обоснование в "
     "expected.json",
     _source_case(
         "scripts/analyze-repro.py",
         '    "  Обоснование в expected.json при этом перевёрнуто буквально:",',
         '    "  Обоснование в expected.json этому не противоречит:",',
         "HypothesesCaveatIsPrinted.test_caveat_says_the_justification_is_"
         "inverted_and_not_edited")),
    ("repro-caveat-loses-the-source-of-the-numbers", "expectations",
     "из оговорки убрана ссылка на первую редакцию нашей же фикстуры — "
     "нечем показать, что равенство было выводимо",
     _source_case(
         "scripts/analyze-repro.py",
         '    "  0c510779… для plain (git show 7936180:…/fixtures/repro.jsonl).",',
         '    "  0c510779… для plain.",',
         "HypothesesCaveatIsPrinted.test_caveat_names_both_derived_items_"
         "and_their_source")),
    ("repro-destination-check-looks-elsewhere", "expectations",
     "проверка каталога назначения смотрит не на тот каталог — ошибка "
     "вызова снова стоила бы двенадцати сборок",
     _source_case(
         "scripts/run-repro.py",
         "    parent = os.path.dirname(out_path)",
         "    parent = FIXTURES_DIR",
         "DestinationIsCheckedBeforeTheBuilds.test_missing_destination_"
         "directory_is_refused")),

    # -------------------------------------------------- repro, машинерия
    ("repro-cache-gives-identity", "machinery",
     "две сборки без --no-cache и с одной меткой — кэш обязан отдать "
     "совпадение сумм даром",
     _machinery_repro_cache_gives_identity_for_free),
    ("repro-treatment-poisoned", "machinery",
     "шаг сборки записал в файл лечения заведомую неправду — прогон обязан "
     "вернуть её",
     _machinery_repro_treatment_is_read_from_the_image),
    ("repro-sum-follows-file", "machinery",
     "к готовому бинарнику дописаны байты — сумма обязана измениться",
     _machinery_repro_sum_follows_the_file),
    # -------------------------------------------------- siblings (ось 4)
    ("siblings-healthz-diverged", "siblings",
     "одно плечо отвечает на /healthz не тем же, что остальные — "
     "сравнивались бы разные программы",
     _siblings_fixture_case(
         _siblings_healthz_diverged,
         "TheServiceIsTheSame.test_healthz_response_is_byte_identical_"
         "across_measured_arms")),
    ("siblings-excluded-field-does-not-differ", "siblings",
     "исключённое поле variant у всех плеч одно — «различается ровно "
     "одно» проверено там, где не различается ничего",
     _siblings_fixture_case(
         _siblings_excluded_field_does_not_differ,
         "TheServiceIsTheSame.test_the_excluded_field_really_differs")),
    ("siblings-resolve-body-diverged", "siblings",
     "тело /resolve у плеча rust отличается — плечи делают разное",
     _siblings_fixture_case(
         _set("rust", resolve_body_without_variant='{"probe": "other"}'),
         "TheServiceIsTheSame.test_resolve_body_is_identical_apart_from_"
         "the_variant_field")),
    ("siblings-arm-reports-someone-elses-name", "siblings",
     "плечо называет себя чужим именем — плечи go и go-divergent делят "
     "образ, и без явного имени оба звались бы первым",
     _siblings_fixture_case(
         _set("go-divergent", resolve_reported_variant="go"),
         "TheServiceIsTheSame.test_every_arm_reports_its_own_name")),
    ("siblings-control-converged", "siblings",
     "отрицательный контроль совпал с плечом go — проверка тождества "
     "отвечает «совпало» при любых данных",
     _siblings_fixture_case(
         _siblings_control_converged,
         "MeasurerCanSayDifferent.test_the_control_diverges_from_its_twin")),
    ("siblings-control-pair-uncontrolled", "siblings",
     "паре контроля добавлен второй различающийся аргумент сборки",
     _siblings_fixture_case(
         _siblings_control_pair_uncontrolled,
         "MeasurerCanSayDifferent.test_the_control_pair_differs_by_exactly_"
         "one_build_argument")),
    ("siblings-nonce-not-read-back", "siblings",
     "метка, прочитанная из образа, не та, что передана сборке",
     _siblings_fixture_case(
         _set("go", nonce_read_back="метка-прошлого-прогона"),
         "CacheDidNotMeasureItself.test_the_nonce_read_back_matches_the_"
         "one_passed")),
    ("siblings-mtime-shared-between-arms", "siblings",
     "два плеча получили одно время изменения бинарника",
     _siblings_fixture_case(
         lambda rows: rows["go"].update(
             binary_mtime=rows["rust"]["binary_mtime"]),
         "CacheDidNotMeasureItself.test_binary_mtimes_are_pairwise_"
         "distinct")),
    ("siblings-ldd-line-counted-as-dependency", "siblings",
     "строка ldd с путём загрузчика засчитана за зависимость — ровно та "
     "ошибка, ради которой измеритель переведён на записи NEEDED",
     _siblings_fixture_case(
         _siblings_ldd_line_counted_as_dependency,
         "StaticLinkageMakesSizesComparable.test_no_arm_has_shared_"
         "dependencies")),
    ("siblings-interpreter-appeared", "siblings",
     "у бинарника появился PT_INTERP — он ждёт загрузчик, которого в "
     "scratch нет",
     _siblings_fixture_case(
         _siblings_interpreter_appeared,
         "StaticLinkageMakesSizesComparable.test_no_arm_expects_a_program_"
         "interpreter")),
    ("siblings-goproxy-on", "siblings",
     "сборка Go шла с включённым реестром модулей — отсутствие "
     "зависимостей перестаёт быть доказанным",
     _siblings_fixture_case(
         _siblings_goproxy_on,
         "DependenciesAreMeasuredNotDeclared.test_go_was_built_with_the_"
         "module_proxy_off")),
    ("siblings-external-crate-appeared", "siblings",
     "в разрешении Cargo появился внешний крейт",
     _siblings_fixture_case(
         _siblings_external_crate_appeared,
         "DependenciesAreMeasuredNotDeclared.test_the_siblings_pulled_in_"
         "nothing_external")),
    ("siblings-fmt-caveat-erased", "siblings",
     "у плеча cpp стёрта запись про статически слинкованный fmt — "
     "разница размеров осталась бы без главной оговорки",
     _siblings_fixture_case(
         _siblings_fmt_caveat_erased,
         "DependenciesAreMeasuredNotDeclared.test_the_cpp_arm_declares_its_"
         "external_library")),
    ("siblings-spread-does-not-follow", "siblings",
     "разброс не следует за записанными min и max",
     _siblings_fixture_case(
         _siblings_spread_does_not_follow,
         "SpreadIsRecordedNextToTheNumber.test_spread_follows_the_min_and_"
         "max")),
    ("siblings-complete-marker-removed", "siblings",
     "из фикстуры убран маркер COMPLETE",
     _siblings_drop_marker_case()),
    ("siblings-resolve-control-converged", "siblings",
     "контроль ветки /resolve совпал с плечом go — вторая ветка проверки "
     "тождества отвечает «совпало» при любых данных",
     _siblings_fixture_case(
         _siblings_resolve_control_converged,
         "MeasurerCanSayDifferent.test_the_resolve_control_diverges_from_"
         "its_twin")),
    ("siblings-resolve-control-also-breaks-healthz", "siblings",
     "контроль ветки /resolve разошёлся ещё и по /healthz — он падал бы "
     "на первой ветке, и вторая снова осталась бы без живого контроля",
     _siblings_fixture_case(
         _siblings_resolve_control_also_breaks_healthz,
         "MeasurerCanSayDifferent.test_the_resolve_control_diverges_from_"
         "its_twin")),
    ("siblings-detail-back-in-comparison", "siblings",
     "текст ошибки вернулся в сравниваемое тело /resolve — любой отказ "
     "DNS объявлял бы плечи разными сервисами",
     _siblings_fixture_case(
         _siblings_detail_back_in_comparison,
         "TheServiceIsTheSame.test_the_compared_body_carries_no_"
         "implementation_specific_text")),
    ("siblings-resolve-refused", "siblings",
     "у плеча rust разрешение имени не прошло",
     _siblings_fixture_case(
         _siblings_resolve_refused,
         "TheServiceIsTheSame.test_resolve_succeeded_everywhere")),
    ("siblings-unpacked-smaller-than-binary", "siblings",
     "распакованный объём образа меньше лежащего внутри бинарника — так "
     "выглядит безымянная шкала сжатого содержимого под вторым именем",
     _siblings_fixture_case(
         _siblings_unpacked_smaller_than_binary,
         "ImageSizeHasTwoNamedScales.test_the_unpacked_scale_contains_the_"
         "binary")),
    ("siblings-two-different-files", "siblings",
     "сумма бинарника в финальном образе не та, что в образе сборки — "
     "улики сняты с одного файла, байты с другого",
     _siblings_fixture_case(
         _siblings_two_different_files,
         "TheMeasuredBinaryIsTheOneInTheImage.test_the_two_sums_agree")),
    ("siblings-symtab-survived", "siblings",
     "в бинарнике осталась таблица символов — шаг strip объявлен, а "
     "результата нет",
     _siblings_fixture_case(
         _siblings_symtab_survived,
         "StripIsMeasuredNotDeclared.test_no_arm_keeps_a_symbol_table")),
    ("siblings-debug-sections-survived", "siblings",
     "в бинарнике остались секции .debug_*",
     _siblings_fixture_case(
         _siblings_debug_sections_survived,
         "StripIsMeasuredNotDeclared.test_no_arm_keeps_debug_sections")),
    ("siblings-sections-were-not-read", "siblings",
     "секций не прочитано ни одной — «отладочных секций нет» проходит на "
     "пустоте",
     _siblings_fixture_case(
         _siblings_sections_were_not_read,
         "StripIsMeasuredNotDeclared.test_the_sections_were_actually_read")),
    ("siblings-cgo-enabled", "siblings",
     "у плеча go включён cgo — механизм разрешения имён перестаёт "
     "следовать из замера",
     _siblings_fixture_case(
         _siblings_cgo_enabled,
         "NameResolutionMechanismIsNamed.test_go_cannot_call_getaddrinfo")),
    ("siblings-go-claims-the-libc-resolver", "siblings",
     "плечу go приписан резолвер libc — разница механизма замолчана",
     _siblings_fixture_case(
         _siblings_go_claims_the_libc_resolver,
         "NameResolutionMechanismIsNamed.test_go_is_not_described_as_using_"
         "the_libc_resolver")),
    ("siblings-reference-arm-is-another-service", "siblings",
     "справочное плечо отвечает не тем же, что cpp — разность байтов "
     "мерила бы разницу двух программ, а не цену fmt",
     _siblings_fixture_case(
         _siblings_reference_arm_is_another_service,
         "TheFmtShareHasAReferenceMeasurement.test_the_reference_arm_is_"
         "the_same_service")),
    ("siblings-second-run-is-a-copy", "siblings",
     "второй прогон несёт метки первого — это копия файла, а не второй "
     "замер",
     _siblings_fixture_case(
         _siblings_second_run_is_a_copy,
         "BothRunsAreCheckedTheSameWay.test_the_two_runs_are_distinct_"
         "measurements",
         which="run2")),
    ("siblings-first-run-not-guarded", "siblings",
     "испорчен ПЕРВЫЙ прогон — защиты обязаны применяться к каждому, а "
     "не к последнему",
     _siblings_fixture_case(
         _siblings_healthz_diverged,
         "BothRunsAreCheckedTheSameWay.test_analysis_guards_accept_both_"
         "runs",
         which="run1")),

    # -------------------------------------------------- siblings, разбор
    ("siblings-analysis-healthz-diverged", "siblings",
     "плечи отвечают по-разному — разбор обязан встать до таблицы",
     _siblings_analysis_case(_siblings_healthz_diverged,
                             "ЭТО НЕ ОДИН СЕРВИС НА ТРЁХ ЯЗЫКАХ")),
    ("siblings-analysis-excluded-field", "siblings",
     "исключённое поле не различает плечи — разбор обязан встать",
     _siblings_analysis_case(_siblings_excluded_field_does_not_differ,
                             "ЭТО НЕ ОДИН СЕРВИС НА ТРЁХ ЯЗЫКАХ")),
    ("siblings-analysis-control-converged", "siblings",
     "отрицательный контроль сошёлся — разбор обязан объявить ось "
     "недействительной",
     _siblings_analysis_case(_siblings_control_converged,
                             "ОТРИЦАТЕЛЬНЫЙ КОНТРОЛЬ СОШЁЛСЯ")),
    ("siblings-analysis-nonce", "siblings",
     "метка не вернулась из образа — разбор обязан встать",
     _siblings_analysis_case(
         _set("go", nonce_read_back="метка-прошлого-прогона"),
         "ОБРАЗ СОБРАН НЕ В ЭТОМ ПРОГОНЕ")),
    ("siblings-analysis-interpreter", "siblings",
     "плечу приписан загрузчик — разбор обязан встать",
     _siblings_analysis_case(_siblings_interpreter_appeared,
                             "ПЛЕЧО СОБРАНО НЕ САМОДОСТАТОЧНО")),
    ("siblings-analysis-ldd-line", "siblings",
     "строка загрузчика засчитана за зависимость — разбор обязан встать",
     _siblings_analysis_case(_siblings_ldd_line_counted_as_dependency,
                             "ПЛЕЧО СОБРАНО НЕ САМОДОСТАТОЧНО")),
    ("siblings-analysis-resolve-control-converged", "siblings",
     "контроль ветки /resolve сошёлся — разбор обязан объявить ось "
     "недействительной",
     _siblings_analysis_case(_siblings_resolve_control_converged,
                             "ОТРИЦАТЕЛЬНЫЙ КОНТРОЛЬ СОШЁЛСЯ")),
    ("siblings-analysis-resolve-control-breaks-healthz", "siblings",
     "контроль ветки /resolve разошёлся и по /healthz — разбор обязан "
     "сказать, что он роняет не ту ветку",
     _siblings_analysis_case(_siblings_resolve_control_also_breaks_healthz,
                             "КОНТРОЛЬ ВЕТКИ /resolve РАЗОШЁЛСЯ И ПО "
                             "/healthz")),
    ("siblings-analysis-resolve-refused", "siblings",
     "разрешение имени не прошло — разбор обязан назвать это отказом "
     "разрешения, а не расхождением реализаций",
     _siblings_analysis_case(_siblings_resolve_refused,
                             "РАЗРЕШЕНИЕ ИМЕНИ НЕ ПРОШЛО")),
    ("siblings-analysis-two-different-files", "siblings",
     "улики и байты сняты с разных файлов — разбор обязан встать",
     _siblings_analysis_case(_siblings_two_different_files,
                             "УЛИКИ И БАЙТЫ СНЯТЫ С РАЗНЫХ ФАЙЛОВ")),
    ("siblings-analysis-debug-sections", "siblings",
     "остались отладочные секции — разбор обязан встать",
     _siblings_analysis_case(_siblings_debug_sections_survived,
                             "ПЛЕЧО СОБРАНО НЕ САМОДОСТАТОЧНО")),
    ("siblings-analysis-reference-arm", "siblings",
     "справочное плечо — другой сервис, разбор обязан встать до "
     "справочного числа",
     _siblings_analysis_case(_siblings_reference_arm_is_another_service,
                             "ДРУГОЙ СЕРВИС")),
    ("siblings-analysis-first-run-not-guarded", "siblings",
     "испорчен ПЕРВЫЙ прогон — разбор обязан встать на нём, а не "
     "напечатать таблицу второго",
     _siblings_analysis_case(_siblings_healthz_diverged,
                             "(прогон 1)", which="run1")),

    # -------------------------------------------------- siblings, ожидания
    ("siblings-time-rule-relaxed", "expectations",
     "правило про разброс перевёрнуто — утверждение по времени "
     "осталось бы при том же разбросе",
     _source_case(
         "scripts/analyze-siblings.py",
         "    if within >= between:",
         "    if between >= within:",
         "TimeRuleIsAppliedLiterally.test_wide_spread_withdraws_the_claim",
         module="test_siblings")),
    ("siblings-time-rule-made-strict", "expectations",
     "записанное «больше или равен» подменено на «больше» — эквивалентное "
     "правило вместо предрегистрированного, расходится только на границе",
     _source_case(
         "scripts/analyze-siblings.py",
         "    if within >= between:",
         "    if within > between:",
         "TimeRuleIsAppliedLiterally.test_equal_spread_and_gap_withdraws_"
         "the_claim",
         module="test_siblings")),
    ("siblings-time-rule-check-looks-for-anything", "expectations",
     "сверка формулировки правила ищет слово, которое есть в любом "
     "тексте, — связь кода с предрегистрацией становится формальной",
     _source_case(
         "scripts/analyze-siblings.py",
         'TIME_RULE_LITERAL = "больше или равен"',
         'TIME_RULE_LITERAL = "разброс"',
         "TimeRuleIsAppliedLiterally.test_the_rule_in_the_code_is_checked_"
         "against_the_recorded_one",
         module="test_siblings")),
    ("siblings-prevalence-caveat-not-printed", "expectations",
     "из печати разбора убран запрет на утверждения о распространённости "
     "— в отчёте он есть, до читателя стенда не доезжает",
     _source_case(
         "scripts/analyze-siblings.py",
         '    print("  3. Ничего про распространённость.")\n'
         '    print("     %s" % claims["about_prevalence"])\n',
         '',
         "AnalysisPrintsWhatTheReportKnows.test_the_caveats_forbid_"
         "prevalence_claims",
         module="test_siblings")),
    ("siblings-runs-caveat-not-printed", "expectations",
     "из печати разбора убрана область прогонов",
     _source_case(
         "scripts/analyze-siblings.py",
         '    print("     %s" % _runs_note())\n',
         '',
         "AnalysisPrintsWhatTheReportKnows.test_the_caveats_name_the_number_"
         "of_runs",
         module="test_siblings")),
    ("siblings-caveats-retold-instead-of-quoted", "expectations",
     "печать пересказывает предрегистрацию вместо цитаты — расхождение "
     "печати с записанным перестаёт быть видимым",
     _source_case(
         "scripts/analyze-siblings.py",
         '    print("     %s" % claims["about_the_fmt_confounder"])',
         '    print("     Разница размера не только про язык.")',
         "AnalysisPrintsWhatTheReportKnows.test_the_caveats_are_quoted_from_"
         "the_pre_registration",
         module="test_siblings")),
    ("siblings-image-ratios-not-printed", "expectations",
     "кратности по образам не печатаются кодом — в отчёте они были, а "
     "пересчитать их было нечем",
     _source_case(
         "scripts/analyze-siblings.py",
         '    _ratios(rows, "image_registry_bytes",\n'
         '            "КРАТНОСТИ ПО ОБРАЗАМ (СЖАТОЕ СОДЕРЖИМОЕ)",\n'
         '            "Это кратности цены хранения в реестре, а не на диске, '
         'и они "\n'
         '            "несут в себе разную сжимаемость трёх бинарников.")\n',
         '',
         "AnalysisPrintsWhatTheReportKnows.test_image_ratios_are_printed_by_"
         "the_code",
         module="test_siblings")),
    ("siblings-ratio-precision-hidden", "expectations",
     "третий знак кратностей не печатается — в прозе он был, а пересчитан "
     "кодом не был",
     _source_case(
         "scripts/analyze-siblings.py",
         '        print("  %s / %s = %.1f (посчитано %.3f)"\n'
         '              % (name, smallest, value, value))',
         '        print("  %s / %s = %.1f" % (name, smallest, value))',
         "AnalysisPrintsWhatTheReportKnows.test_ratios_are_printed_with_the_"
         "precision_the_prose_uses",
         module="test_siblings")),
    ("siblings-resolver-difference-not-printed", "expectations",
     "разница механизма разрешения имён не печатается — она названа в "
     "данных и в отчёте, но не в разборе",
     _source_case(
         "scripts/analyze-siblings.py",
         '    print("РАЗРЕШЕНИЕ ИМЁН: ПЛЕЧИ РАБОТАЮТ НЕ ОДИНАКОВО")',
         '    print("ЗАМЕЧАНИЕ")',
         "AnalysisPrintsWhatTheReportKnows.test_the_resolver_difference_is_"
         "printed",
         module="test_siblings")),
    ("siblings-fmt-reference-not-printed", "expectations",
     "справочное число fmt не печатается рядом с кратностью — оговорка "
     "снова стоит без величины",
     _source_case(
         "scripts/analyze-siblings.py",
         '    print("СПРАВОЧНО, ВНЕ ОСИ: ЦЕНА fmt В ПЛЕЧЕ cpp")',
         '    print("СПРАВОЧНО: ПЛЕЧО ВНЕ ОСИ")\n'
         '    if True:\n'
         '        return',
         "AnalysisPrintsWhatTheReportKnows.test_the_fmt_reference_number_is_"
         "printed_next_to_the_ratio",
         module="test_siblings")),
    ("siblings-ldd-replacement-not-disclosed", "expectations",
     "печать вердикта dynamic_deps_is_zero_everywhere больше не называет "
     "замену ldd на записи NEEDED — до читателя доезжает только цитата "
     "записанного, без причины замены",
     _source_case(
         "scripts/analyze-siblings.py",
         '    print("    ИНСТРУМЕНТ ЗАМЕНЁН. Записанная строка называет '
         'ldd; величина")\n'
         '    print("    снимается записями NEEDED динамической секции '
         '(плюс")\n'
         '    print("    PT_INTERP), потому что ldd над статическим PIE '
         'Rust печатает")\n'
         '    print("    путь загрузчика и засчитал бы его за '
         'разделяемую зависимость")\n'
         '    print("    — «у Rust одна зависимость» было бы просто '
         'неверно.")\n',
         '',
         "AnalysisPrintsWhatTheReportKnows.test_the_ldd_replacement_is_"
         "disclosed_next_to_the_verdict",
         module="test_siblings")),
    ("siblings-binary-ratio-pointer-dropped", "expectations",
     "из блока «КРАТНОСТИ ПО РАЗМЕРУ БИНАРНИКА» убран указатель на "
     "справочное число fmt — вырезанный отдельно блок снова читается без "
     "оговорки",
     _source_case(
         "scripts/analyze-siblings.py",
         '            pointer="Справочное число ниже (цена fmt в плече '
         'cpp) меняет "\n'
         '            "чтение этой кратности на противоположное: '
         'вырезанная "\n'
         '            "отдельно, эта таблица читается без него.")',
         '            pointer=None)',
         "AnalysisPrintsWhatTheReportKnows.test_the_binary_ratio_block_"
         "points_to_the_fmt_reference",
         module="test_siblings")),
    ("siblings-reference-arm-joins-measured-axis", "expectations",
     "плечо cpp-nofmt добавлено в MEASURED — ни одна проверка кратностей "
     "на той же фикстуре этого не ловит, потому что считает ожидание тем "
     "же MEASURED, что и код",
     _source_case(
         "scripts/analyze-siblings.py",
         'MEASURED = ("cpp", "go", "rust")',
         'MEASURED = ("cpp", "go", "rust", "cpp-nofmt")',
         "TheFmtShareHasAReferenceMeasurement.test_the_reference_arm_is_"
         "excluded_from_the_measured_axis",
         module="test_siblings")),
    ("siblings-fmt-caveat-dropped-from-expected", "expectations",
     "из записанного до прогона убрано имя библиотеки, смешивающей цену "
     "языка с ценой зависимости",
     _expected_case(
         "в бинарнике C++ лежит статически слинкованный fmt, у соседей его "
         "аналога нет",
         "у плеч разные внешние библиотеки",
         "ExpectationsWereRecordedBeforeTheRun.test_the_fmt_confounder_is_"
         "named_before_the_run",
         module="test_siblings")),
    ("siblings-probe-block-added", "expectations",
     "в предрегистрационный файл задним числом добавлен блок «известно "
     "из проб» — знание выдавалось бы за предвидение",
     _expected_case(
         '    "hypothesis_before_run": {\n      "healthz_is_identical"',
         '    "known_from_pre_run_probes": {"binary_bytes": "видели до '
         'прогона"},\n    "hypothesis_before_run": {\n      '
         '"healthz_is_identical"',
         "ExpectationsWereRecordedBeforeTheRun.test_no_probe_results_are_"
         "filed_as_predictions",
         module="test_siblings")),

    # ---------------------------------------------- README и сводки прогона
    ("console-utf8-not-forced", "expectations",
     "из разбора убрано переключение вывода в UTF-8 — на консоли cp1251 "
     "печать знака «не равно» роняет прогон",
     _source_case(
         "scripts/analyze-repro.py",
         "force_utf8()",
         "pass  # UTF-8 не включается",
         "ConsoleEncoding.test_every_printing_script_forces_utf8",
         module="test_console_encoding")),
    ("readme-number-invented", "expectations",
     "в README подставлено число, которого нет ни в одной фикстуре",
     _source_case(
         "README.md",
         "разность двух конкретных бинарников",
         "разность двух конкретных бинарников (порядка 391 килобайта)",
         "ReadmeNumbers.test_every_number_is_backed_by_a_fixture",
         module="test_readme_matches_fixtures")),
    ("readme-limit-dropped", "expectations",
     "из README убрана граница «в какой версии glibc правило изменилось — "
     "не измерено»",
     _source_case(
         "README.md",
         "**Границу версий стенд не ищет:**",
         "Про версии:",
         "ReadmeSaysWhatCannotBeClaimed.test_limits_section_names_the_limits",
         module="test_readme_matches_fixtures")),
    ("manifest-values-disagree", "expectations",
     "у второй сводки разошёлся размер бинарника — сверка между "
     "платформами обязана это увидеть",
     _manifest_case(_manifest_value_changed,
                    "Manifests.test_published_values_agree_across_platforms",
                    "величина разошлась, а сверка промолчала")),
    ("manifest-witness-equal", "expectations",
     "свидетель платформы совпал у двух сводок — прогон был один, и "
     "вывод о двух платформах ложен",
     _manifest_case(_manifest_witness_equal,
                    "Manifests.test_the_excluded_witness_really_differs",
                    "исключённое поле совпало, а проверка промолчала")),
    ("manifest-exclusion-unexplained", "expectations",
     "поле исключено из сравнения без объяснения",
     _manifest_case(
         _manifest_exclusion_unexplained,
         "Manifests.test_the_exclusion_list_explains_every_field",
         "молчаливое исключение прошло")),

    ("console-utf8-not-forced-in-package", "expectations",
     "из scripts/__init__.py убрано переключение вывода в UTF-8 — набор "
     "проверок, запущенный напрямую на консоли cp1251, умирает на печати "
     "собственного сообщения",
     _source_case(
         "scripts/__init__.py",
         "force_utf8()\n",
         "pass  # UTF-8 не включается\n",
         "ConsoleEncoding.test_the_package_forces_utf8_for_the_test_suite",
         module="test_console_encoding")),
    ("subprocess-encoding-left-to-the-locale", "expectations",
     "чтение вывода подпроцесса отдано кодовой странице консоли — на "
     "cp1251 русский вывод роняет читателя UnicodeDecodeError",
     _source_case(
         "scripts/docker.py",
         '                          encoding="utf-8", errors="replace",\n',
         '',
         "ConsoleEncoding.test_every_captured_subprocess_names_its_encoding",
         module="test_console_encoding")),
    ("build-ratio-printed-bare", "expectations",
     "разбор сборки снова печатает голый множитель времени — тот, кто "
     "пишет статью, унесёт его отсюда",
     _source_case(
         "scripts/analyze-build.py",
         '        print("  %-16s %6.1f s" % (name, rows[name]["cold_seconds"]))',
         '        print("  %-16s %6.1f s  — дороже system в %.1f раза"\n'
         '              % (name, rows[name]["cold_seconds"],\n'
         '                 rows[name]["cold_seconds"]\n'
         '                 / rows["system"]["cold_seconds"]))',
         "PrintedTimesCarryTheirLimit.test_the_cold_build_block_prints_"
         "seconds_without_a_ratio",
         module="test_build_analysis")),
    ("build-ratio-evidence-block-dropped", "expectations",
     "из печати разбора убран блок межсессионного разброса — отсылка к "
     "свидетельству по сессиям пропадает, а оговорка «не переживает "
     "смены сессии» печатается строкой раньше и диверсию не ловит",
     _source_case(
         "scripts/analyze-build.py",
         "    _print_session_drift()",
         "    pass  # разброс по сессиям не печатается",
         "PrintedTimesCarryTheirLimit.test_the_cold_build_block_says_the_"
         "ratio_does_not_survive",
         module="test_build_analysis")),
    ("evidence-session-number-changed", "expectations",
     "в свидетельстве по сессиям подменено время холодной сборки — оно "
     "перестаёт совпадать с фикстурами своего коммита",
     _source_case(
         "fixtures/evidence/cold-seconds-by-session.json",
         '"conan": 18.225',
         '"conan": 16.700',
         "Evidence.test_recorded_sessions_match_the_history_they_name",
         module="test_cold_seconds_evidence")),
    ("run-marker-describes-other-fixtures", "expectations",
     "в маркере полного прогона подменена сумма фикстуры — маркер "
     "описывает состояние, которого нет",
     _source_case(
         "fixtures/RUN-COMPLETE",
         '"build.jsonl": "',
         '"build.jsonl": "0',
         "RunMarker.test_marker_matches_the_fixtures_lying_next_to_it",
         module="test_run_marker")),
    ("manifest-field-unclassified", "expectations",
     "поле фикстуры выпало из разметки сводки — величину можно убрать "
     "из публикации молча",
     _source_case(
         "scripts/run-manifest.py",
         '        "language": "подпись плеча словом",\n',
         '',
         "Manifests.test_every_fixture_field_is_published_or_explained",
         module="test_manifests_agree")),
    ("manifest-tolerance-too-wide", "expectations",
     "величина под допуском уехала дальше допуска — сверка обязана это "
     "увидеть, иначе допуск был бы выключенной проверкой",
     _manifest_case(_manifest_tolerated_value_drifts_too_far,
                    "Manifests.test_published_values_agree_across_platforms",
                    "сдвиг за пределы допуска прошёл молча")),
    ("manifest-exact-companion-dropped", "expectations",
     "у величины под допуском убран точный спутник — содержимое образа "
     "перестаёт стеречь кто-либо",
     _manifest_case(
         _manifest_exact_companion_dropped,
         "Manifests.test_every_tolerated_value_has_an_exact_size_companion",
         "спутник пропал, а проверка промолчала")),
    ("manifest-value-dropped-from-publication", "expectations",
     "величина выпала из сводки — раньше это ловило только несовпадение "
     "наборов ключей у двух сводок",
     _manifest_case(
         _manifest_exact_companion_dropped,
         "Manifests.test_the_manifest_publishes_everything_the_analyses_"
         "print",
         "величина пропала из публикации, а проверка промолчала")),
    ("package-unpacked-follows-the-packed", "fixture",
     "распакованный объём образа объявлен равным упакованному — спутник "
     "стережёт самого себя",
     _package_fixture_case(
         _package_unpacked_follows_the_packed,
         "ExactSizeCompanion.test_the_unpacked_size_is_not_the_packed_one")),
    ("package-unpacked-dropped", "fixture",
     "распакованного объёма образа в фикстуре нет вовсе",
     _package_fixture_case(
         _package_unpacked_dropped,
         "ExactSizeCompanion.test_every_variant_reports_the_unpacked_image_"
         "size")),

    ("package-tool-versions-dropped", "fixture",
     "версий инструментов в фикстуре нет вовсе — различия вариантов снова "
     "держатся на имени образа в variants.json",
     _package_fixture_case(
         _package_tool_versions_dropped,
         "ToolVersions.test_every_variant_reports_its_tool_versions")),
    ("package-pair-compiler-declared-identical", "fixture",
     "у половин пары компилятор записан одинаковым — ровно то, что "
     "утверждали четыре формулировки статьи",
     _package_fixture_case(
         _package_pair_compiler_declared_identical,
         "ControlledPair.test_the_pair_differs_in_compiler_and_libc")),
    ("package-pair-cmake-diverges", "fixture",
     "версия cmake у половин пары разошлась — у пары появилась третья "
     "переменная",
     _package_fixture_case(
         _package_pair_cmake_diverges,
         "ControlledPair.test_the_pair_shares_the_cmake_version")),
    ("package-tool-versions-not-from-the-builder", "fixture",
     "варианту проставлены версии чужого образа сборки — измеренное "
     "разошлось с тем, в чём сборка шла",
     _package_fixture_case(
         _package_tool_versions_follow_the_builder,
         "ToolVersions.test_variants_sharing_a_builder_report_the_same_"
         "versions")),

    # -------------------------------------------------- siblings, машинерия
    ("siblings-cache-gives-the-same-nonce", "machinery",
     "две сборки с одной меткой и без --no-cache — кэш обязан отдать ту "
     "же метку и то же время изменения даром",
     _machinery_siblings_cache_gives_the_same_nonce),
    ("siblings-size-follows-the-file", "machinery",
     "к готовому бинарнику дописаны байты — размер, сумма и распакованный "
     "объём образа обязаны измениться",
     _machinery_siblings_size_follows_the_file),
    ("package-tool-versions-follow-the-image", "machinery",
     "версии инструментов сняты в двух образах сборки пары — измеритель "
     "обязан быть устойчив на одном образе и различать два",
     _machinery_package_tool_versions_follow_the_image),
    ("package-unpacked-size-follows-the-file", "machinery",
     "к бинарнику варианта упаковки дописаны байты — распакованный объём "
     "образа обязан вырасти ровно на дописанное",
     _machinery_package_unpacked_size_follows_the_file),
    # ------------------------------------- предупреждение компоновщика
    ("link-warning-declared-without-the-output", "fixture",
     "предупреждение объявлено там, где его нет в перехваченном выводе — "
     "измеритель отвечает по объявлению, а не по веществу",
     _link_fixture_case(
         _link_warning_declared_without_the_output,
         "TheFlagIsReadFromTheSubstance.test_the_flag_agrees_with_the_"
         "captured_stderr")),
    ("link-warning-line-invented", "fixture",
     "к строкам предупреждения дописана та, которой в сыром выводе нет",
     _link_fixture_case(
         _link_warning_line_invented,
         "TheFlagIsReadFromTheSubstance.test_every_warning_line_comes_from_"
         "the_raw_output")),
    ("link-warning-erased-from-the-pair", "fixture",
     "у плеча пары предупреждение исчезло — наблюдение про одинаковую "
     "строку обязано покраснеть",
     _link_fixture_case(
         _link_warning_erased_from_the_pair,
         "TheFlagDiscriminates.test_the_pair_about_the_rule_both_warn")),
    ("link-warning-text-diverges", "fixture",
     "тексты предупреждения у плеч пары разошлись — наблюдения про "
     "одинаковую строку больше нет",
     _link_fixture_case(
         _link_warning_text_diverges,
         "TheSameWarningWithDifferentOutcomes.test_both_arms_print_the_"
         "identical_warning")),
    ("link-step-did-not-run", "fixture",
     "линковки не было — отсутствие предупреждения перестаёт что-либо "
     "значить",
     _link_fixture_case(
         _link_step_did_not_run,
         "TheLinkReallyHappened.test_every_row_reports_a_link_step")),
    ("link-variant-dropped", "fixture",
     "плечо выпало из замера — молча выглядело бы плечом без "
     "предупреждения",
     _link_fixture_case(
         _link_variant_dropped,
         "Coverage.test_every_distinct_dockerfile_is_measured")),
    ("link-analysis-prints-over-a-link-that-never-happened", "analysis",
     "разбор обязан ОСТАНОВИТЬСЯ, а не печатать наблюдение про "
     "одинаковую строку поверх несостоявшейся линковки",
     _link_analysis_case(_link_step_did_not_run, "ЛИНКОВКА НЕ ШЛА")),
    ("link-analysis-retraction-dropped", "analysis",
     "из разбора убран отзыв причинной трактовки — читатель увидел бы "
     "наблюдение и не увидел бы, что записанный до прогона вывод отозван",
     _source_case(
         os.path.join("scripts", "analyze-package.py"),
         "    print_retraction()\n",
         "    pass  # отзыв не печатается\n",
         "TheAnalysisCarriesTheRetraction.test_the_analysis_prints_the_"
         "retraction",
         module="test_link_warning")),
    ("link-analysis-states-the-retracted-cause", "analysis",
     "разбор снова печатает отозванный причинный вывод рядом с "
     "наблюдением — отзыв стоит, а утверждение уносится читателем",
     _source_case(
         os.path.join("scripts", "analyze-package.py"),
         '        print("  НЕ СЛЕДУЕТ.")\n',
         '        print("  НЕ СЛЕДУЕТ. Предупреждение пережило свою '
         'причину.")\n',
         "TheAnalysisCarriesTheRetraction.test_the_analysis_never_states_"
         "the_retracted_cause",
         module="test_link_warning")),
    ("link-warning-follows-the-static-flag", "machinery",
     "то же плечо собрано БЕЗ статической линковки — предупреждение "
     "обязано исчезнуть",
     _machinery_link_warning_follows_the_static_flag),

    ("siblings-ldd-would-have-miscounted-rust", "machinery",
     "над одним бинарником rust сняты два измерения — разбор текста ldd "
     "обязан дать не ноль, записи NEEDED обязаны дать ноль",
     _machinery_siblings_ldd_would_have_miscounted_rust),

    # -------------------------------------------------- закрепление версий
    ("pinning-base-image-back-to-a-tag", "pinning",
     "у образа соседа снят дайджест — остался подвижный тег",
     _source_case(
         os.path.join("images", "Dockerfile.sibling-cpp-nofmt"),
         "FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce AS build",
         "FROM alpine:3.22 AS build",
         "BaseImages.test_every_external_base_image_is_pinned_by_digest",
         module="test_pinning")),
    ("pinning-one-tag-two-digests", "pinning",
     "gcc:15 закреплён в distroless другим дайджестом — под одним именем "
     "собирались бы два разных компилятора",
     _source_case(
         os.path.join("images", "Dockerfile.distroless"),
         "FROM gcc:15@sha256:fb2568a8cc0609134396aeb1eae1f3a91c8ceec54aaee67c798d9602593abd66 AS build",
         "FROM gcc:15@sha256:0000000000000000000000000000000000000000000000000000000000000000 AS build",
         "BaseImages.test_the_same_image_is_pinned_to_one_digest_everywhere",
         module="test_pinning")),
    ("pinning-archive-unpacked-without-a-sum", "pinning",
     "у архива fmt в musl-static убрана сверка суммы — распаковка идёт "
     "вслепую",
     _source_case(
         os.path.join("images", "Dockerfile.musl-static"),
         ' && echo "6cb1e6d37bdcb756dbbe59be438790db409cdb4868c66e888d5df9f13f7c027f  /tmp/fmt.tar.gz" | sha256sum -c - \\\n',
         "",
         "Archives.test_every_downloaded_archive_is_checked_before_it_is_"
         "unpacked",
         module="test_pinning")),
    ("pinning-one-archive-two-sums", "pinning",
     "архив fmt в glibc-static-old сверяется другой суммой — половина пары "
     "молча брала бы другой файл",
     _source_case(
         os.path.join("images", "Dockerfile.glibc-static-old"),
         'echo "6cb1e6d37bdcb756dbbe59be438790db409cdb4868c66e888d5df9f13f7c027f  /tmp/fmt.tar.gz"',
         'echo "1111111111111111111111111111111111111111111111111111111111111111  /tmp/fmt.tar.gz"',
         "Archives.test_the_same_archive_carries_the_same_sum_everywhere",
         module="test_pinning")),
    ("pinning-pip-without-require-hashes", "pinning",
     "у pip снят --require-hashes — файл требований превращается в список "
     "пожеланий",
     _source_case(
         os.path.join("images", "Dockerfile.build-conan"),
         "RUN pip3 install --break-system-packages --quiet --require-hashes \\\n",
         "RUN pip3 install --break-system-packages --quiet \\\n",
         "PipPackage.test_conan_is_installed_from_a_hashed_requirements_file",
         module="test_pinning")),
    ("pinning-requirement-without-a-version", "pinning",
     "у зависимости six снята точная версия — закрепление набора неполно",
     _source_case(
         os.path.join("images", "conan-requirements.txt"),
         "six==1.17.0 \\\n",
         "six \\\n",
         "PipPackage.test_every_requirement_is_pinned_with_a_version_and_a_"
         "hash",
         module="test_pinning")),
    ("pinning-vcpkg-registry-back-to-a-tag", "pinning",
     "реестр vcpkg закреплён тегом вместо суммы коммита — тег можно "
     "перевесить",
     _source_case(
         os.path.join("images", "Dockerfile.build-vcpkg"),
         "ARG VCPKG_REF=ce613c41372b23b1f51333815feb3edd87ef8a8b\n",
         "ARG VCPKG_REF=2025.04.09\n",
         "VcpkgRegistry.test_the_registry_default_is_a_commit_sha",
         module="test_pinning")),
    ("pinning-measured-arm-back-to-a-tag", "pinning",
     "измеряемое плечо vcpkg передаёт тег вместо суммы коммита",
     _source_case(
         os.path.join("images", "build-variants.json"),
         '"VCPKG_REF=ce613c41372b23b1f51333815feb3edd87ef8a8b"',
         '"VCPKG_REF=2025.04.09"',
         "VcpkgRegistry.test_the_measured_arm_takes_the_registry_by_commit",
         module="test_pinning")),
    ("pinning-swallows-its-own-control", "pinning",
     "контроль vcpkg-latest закреплён «за компанию» — находка про "
     "незакреплённый реестр исчезает",
     _source_case(
         os.path.join("images", "build-variants.json"),
         '"VCPKG_REF=master"',
         '"VCPKG_REF=ce613c41372b23b1f51333815feb3edd87ef8a8b"',
         "VcpkgRegistry.test_the_unpinned_control_stays_unpinned",
         module="test_pinning")),
    ("pinning-version-taken-after-the-compile", "pinning",
     "снятие версии fmt переставлено ПОСЛЕ компиляции — у падающего "
     "контроля поле осталось бы пустым",
     _source_case(
         os.path.join("images", "Dockerfile.build-vcpkg"),
         VCPKG_VERSION_STEP_BEFORE, VCPKG_VERSION_STEP_AFTER,
         "VcpkgRegistry.test_the_version_is_taken_before_the_compile",
         module="test_pinning")),
    ("readme-check-count-went-stale", "pinning",
     "число проверок в README разошлось с набором — ровно так протухала "
     "прежняя отметка прогона",
     _readme_count_drifts()),

    ("pinning-archive-sum-is-connected", "machinery",
     "сумма архива fmt подменена неверной — сборка обязана встать на "
     "сверке, а не дойти до компилятора",
     _machinery_pinning_archive_sum_is_connected),
    ("pinning-wheel-hash-is-connected", "machinery",
     "сумма колеса six подменена неверной — pip обязан отказаться ставить",
     _machinery_pinning_wheel_hash_is_connected),
]


def _select(args):
    if args.name:
        chosen = [s for s in SABOTAGES if s[0] == args.name]
        if not chosen:
            raise SystemExit("нет диверсии %r" % args.name)
        return chosen
    if args.group:
        return [s for s in SABOTAGES if s[1] == args.group]
    if args.all:
        return list(SABOTAGES)
    return [s for s in SABOTAGES if s[1] != "machinery"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("list")
    run = sub.add_parser("run")
    run.add_argument("name", nargs="?")
    run.add_argument("--group",
                     choices=("fixture", "analysis", "repro", "siblings",
                              "expectations", "pinning", "machinery"))
    run.add_argument("--all", action="store_true")
    run.add_argument("--with-docker", action="store_true",
                     help="разрешить диверсии над машинерией замера")
    args = parser.parse_args()

    if args.command == "list":
        for name, group, description, _ in SABOTAGES:
            print("%-32s %-10s %s" % (name, group, description))
        return 0

    chosen = _select(args)
    if any(s[1] == "machinery" for s in chosen) and not args.with_docker:
        raise SystemExit("диверсии над машинерией требуют Docker и ключа "
                         "--with-docker")

    failed = []
    tree_before = _tree_state()
    with _marker_restored():
        for name, group, description, action in chosen:
            print("=== %s (%s)" % (name, group))
            print("    %s" % description)
            try:
                ok, note = action()
            except SabotageDidNotApply as error:
                # Неприменившаяся диверсия — ПРОВАЛЕННАЯ проверка.
                print("    ДИВЕРСИЯ НЕ ПРИМЕНИЛАСЬ: %s" % error)
                print("    Засчитано как ПРОВАЛ.")
                failed.append(name)
                continue
            print("    %s %s" % ("OK  " if ok else "ПРОВАЛ", note))
            if not ok:
                failed.append(name)

    print()
    print("итого: %d из %d" % (len(chosen) - len(failed), len(chosen)))

    # След в рабочем дереве — такой же провал, как устоявшая проверка.
    # Диверсия, не убравшая за собой, оставляет стенд в состоянии, о
    # котором никто не договаривался: следующий прогон пойдёт по чужим
    # данным, а `git status` покажет правки, которых автор не делал.
    tree_after = _tree_state()
    if tree_before is None or tree_after is None:
        print("состояние рабочего дерева не снято: git недоступен")
    elif tree_after != tree_before:
        left = [line for line in tree_after if line not in tree_before]
        gone = [line for line in tree_before if line not in tree_after]
        print("НАБОР ОСТАВИЛ СЛЕД В РАБОЧЕМ ДЕРЕВЕ:")
        for line in left:
            print("  появилось: %s" % line)
        for line in gone:
            print("  пропало:   %s" % line)
        print("Диверсия обязана возвращать всё, что тронула.")
        failed.append("рабочее дерево изменено набором")

    if failed:
        print("провалились: %s" % ", ".join(failed))
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
