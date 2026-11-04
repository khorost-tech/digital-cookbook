"""Ось 4: соседи по языкам. Сборка плеч и снятие их обмера.

ЧТО ЗДЕСЬ МЕРЯЕТСЯ И ЧТО НЕТ. Только СЧИТАЕМОЕ: байты бинарника, байты
образа, число разделяемых зависимостей, миллисекунды до первого ответа.
Пропускная способность и любые другие величины производительности не
меряются вовсе — решение R19, принятое автором статьи. Отсутствие таких
чисел не повод намекать на них словами.

ГЛАВНЫЙ РИСК ОСИ — сравнить три РАЗНЫЕ программы вместо трёх реализаций
одного сервиса. Поэтому у каждого плеча снимается не только размер, но и
ПОЛНЫЙ ОТВЕТ HTTP на /healthz (побайтно, вместе со строкой статуса и
заголовками) и тело /resolve. Разбор сравнивает их между плечами и
останавливается, если они разошлись.

УЛИКИ ПРОТИВ КЭША. Метка прогона пишется в образ ПЕРЕД шагом компиляции
и читается ОБРАТНО из собранного образа; отдельно снимается время
изменения самого бинарника внутри образа. Поле, скопированное из
спецификации, уликой не является — поэтому и метка, и время, и
фактически применённое лечение читаются из образа, а не объявляются
скриптом.
"""

import hashlib
import io
import json
import os
import statistics
import subprocess
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
IMAGES_DIR = os.path.join(HERE, "images")
SPEC_PATH = os.path.join(IMAGES_DIR, "sibling-variants.json")

FIRST_RESPONSE_ATTEMPTS = 11
READY_TIMEOUT = 30
PROBE_TIMEOUT = 20


def load_spec():
    with io.open(SPEC_PATH, encoding="utf-8") as handle:
        return json.loads(handle.read())


def find_arm(name):
    for arm in load_spec():
        if arm["name"] == name:
            return arm
    raise KeyError("нет плеча %r в sibling-variants.json" % name)


def _run(cmd, check=True):
    proc = subprocess.run(cmd, capture_output=True, text=True,
                          encoding="utf-8", errors="replace")
    if check and proc.returncode != 0:
        raise RuntimeError("команда %r вернула %d:\n%s"
                           % (cmd, proc.returncode,
                              (proc.stdout or "") + (proc.stderr or "")))
    return proc.returncode, (proc.stdout or "") + (proc.stderr or "")


def _capture(cmd):
    return _run(cmd)[1].strip()


# --------------------------------------------------------------------
# Сборка


def build_argv(arm, nonce, builder=False):
    """Полная командная строка docker build — она же попадёт в фикстуру.

    Командная строка СТРОИТСЯ из тех же значений, что записываются в
    строку: аргумент сборки, приписанный полю, обязан оказаться в
    команде, которая выполнялась. Уликой это всё равно не является —
    команду записывает тот же скрипт, который её и составил, — но
    расхождение между полем и командой такая запись ловит.
    """
    dockerfile = os.path.join(IMAGES_DIR, arm["dockerfile"])
    cmd = ["docker", "build", "-f", dockerfile]
    if builder:
        cmd += ["--target", "build"]
    cmd += ["--build-arg", "STAND_NONCE=%s" % nonce]
    for key, value in sorted(arm.get("build_args", {}).items()):
        cmd += ["--build-arg", "%s=%s" % (key, value)]
    tag = ("cpp-build-and-package-sibling-builder:%s" if builder
           else "cpp-build-and-package-sibling:%s") % arm["name"]
    cmd += ["-t", tag, HERE]
    return cmd, tag


def build_arm(arm, nonce):
    """Собирает образ сборки и финальный образ плеча.

    Две сборки на плечо — по той же причине, что и на оси упаковки:
    финальный образ (scratch) не несёт ни ldd, ни шелла, и всё, что
    читается изнутри (метка, лечение, время изменения, зависимости),
    снимается в ОБРАЗЕ СБОРКИ.
    """
    builder_argv, builder_tag = build_argv(arm, nonce, builder=True)
    final_argv, final_tag = build_argv(arm, nonce, builder=False)
    started = time.monotonic()
    _run(builder_argv)
    _run(final_argv)
    seconds = time.monotonic() - started
    return {
        "builder_tag": builder_tag,
        "final_tag": final_tag,
        "docker_argv": final_argv,
        "build_seconds": round(seconds, 1),
    }


# --------------------------------------------------------------------
# Чтение улик ИЗ ОБРАЗА


def read_nonce(builder_tag):
    """Метка прогона, прочитанная обратно из собранного образа."""
    return _capture(["docker", "run", "--rm", builder_tag, "cat", "/nonce"])


def read_treatment(builder_tag):
    """Фактически применённое лечение — так, как его назвал сам шаг сборки.

    Не «то, что мы собирались сделать», а то, что напечатал шаг сборки
    из своих же переменных окружения. Объявление в Dockerfile этим не
    заменяется: значение переменной, забытой в ENV, здесь стало бы
    видно.
    """
    text = _capture(["docker", "run", "--rm", builder_tag,
                     "cat", "/treatment.txt"])
    return " ".join(line.strip() for line in text.splitlines() if line.strip())


def read_binary_mtime(builder_tag):
    """Время изменения бинарника ВНУТРИ образа, до наносекунд.

    Секундной точности мало: четыре плеча собираются подряд, и два из
    них могут уложиться в одну секунду. Совпадение времени тогда
    означало бы не переиспользованный слой, а грубость измерителя.
    """
    return _capture(["docker", "run", "--rm", builder_tag,
                     "stat", "-c", "%y", "/build/stand_service"])


def read_elf_facts(builder_tag):
    """Разделяемые зависимости и устройство ELF — из самого бинарника.

    ПОЧЕМУ НЕ ldd. На оси упаковки зависимости снимались ответом ldd, и
    там это работало: все её плечи — либо обычные динамические бинарники,
    либо статические не-PIE, у которых ldd честно отвечает «not a valid
    dynamic program». У соседей так не выходит. Rust на musl по умолчанию
    даёт СТАТИЧЕСКИЙ PIE, и ldd печатает для него строку с путём
    загрузчика — разбор текста ldd засчитал бы её за зависимость,
    которой нет. Это не придирка к формату: вывод «у Rust одна
    разделяемая зависимость» был бы просто неверен.

    Поэтому меряется вещество: записи NEEDED в динамической секции (это и
    есть разделяемые зависимости) и наличие PT_INTERP (программы-
    загрузчика). У самодостаточного бинарника нет ни того, ни другого.
    Второе, независимое подтверждение самодостаточности даёт сам прогон:
    все плечи запускаются в scratch, где нет ни загрузчика, ни libc.

    ЗАЧЕМ ЗДЕСЬ ЖЕ СЕКЦИИ. Отладочная информация у C++ и Rust убирается
    утилитой strip, у Go — ключами линковщика -s -w. Поле strip_step
    фиксирует ОБЪЯВЛЕНИЕ шага, прочитанное из образа, и ничего не
    говорит о том, что в бинарнике осталось: «убрано сопоставимо» было
    рассуждением. Список секций отвечает на это замером — ни .symtab, ни
    .debug_*. Число секций записывается рядом: «секций .debug_* нет»
    прошло бы и на бинарнике, у которого не прочитано ни одной секции
    вовсе.
    """
    text = _capture(["docker", "run", "--rm", builder_tag, "sh", "-c",
                     "readelf -d /build/stand_service 2>/dev/null; "
                     "echo '---'; readelf -l /build/stand_service; "
                     "echo '---'; readelf -h /build/stand_service; "
                     "echo '---'; readelf -S /build/stand_service"])
    dynamic, program, header, sections = (text.split("---") + ["", "", ""])[:4]
    deps = []
    for line in dynamic.splitlines():
        if "(NEEDED)" not in line:
            continue
        start = line.find("[")
        end = line.find("]", start)
        if start >= 0 and end > start:
            deps.append(line[start + 1:end])
    elf_type = ""
    for line in header.splitlines():
        if line.strip().startswith("Type:"):
            elf_type = line.split(":", 1)[1].strip()
    names = _section_names(sections)
    return {
        "dynamic_deps": deps,
        "dynamic_dep_count": len(deps),
        "elf_has_interpreter": "INTERP" in program,
        "elf_type": elf_type,
        "elf_section_count": len(names),
        "elf_has_symtab": ".symtab" in names,
        "elf_debug_sections": sorted(n for n in names
                                     if n.startswith(".debug")),
    }


def _section_names(text):
    """Имена секций из вывода `readelf -S`.

    Строка выглядит так: «  [ 4] .text             PROGBITS  ...». Имя —
    первое поле после закрывающей скобки номера. Разбор намеренно тупой:
    ничего, кроме имён, отсюда не берётся, а числа секций хватает, чтобы
    отличить пустой список от непрочитанного вывода.
    """
    names = []
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped.startswith("["):
            continue
        end = stripped.find("]")
        if end < 0:
            continue
        rest = stripped[end + 1:].split()
        if rest and rest[0].startswith("."):
            names.append(rest[0])
    return names


def read_binary_sha256_in_builder(builder_tag):
    """Сумма бинарника ТАМ, где снимаются факты ELF, метка и время.

    Факты ELF, метка прогона и время изменения читаются в образе
    СБОРКИ, а байты и размер — в ФИНАЛЬНОМ образе. Между ними стоит
    COPY, и что это один и тот же файл, до сих пор не показывалось
    ничем: правка Dockerfile'а могла бы развести их, и улики
    относились бы к одному файлу, а размер — к другому. Сумма снимается
    в обоих местах, и разбор их сверяет.
    """
    out = _capture(["docker", "run", "--rm", builder_tag,
                    "sha256sum", "/build/stand_service"])
    return out.split()[0]


# --------------------------------------------------------------------
# Размер образа: ДВЕ величины, и каждая названа тем, что она есть
#
# `docker image inspect --format {{.Size}}` на хранилище containerd
# отдаёт объём СЖАТОГО содержимого образа плюс конфиг. Отсюда и число,
# меньшее лежащего внутри бинарника (414 797 против 1 022 128 у cpp), и
# расхождение кратностей: по образам одно, по бинарникам другое —
# сжимаемость у трёх языков разная.
#
# Величина от этого не портится, но она отвечает на другой вопрос: цена
# хранения и передачи в реестре. Цена на диске — вопрос отдельный, и у
# него отдельный ответ. Безымянную шкалу читатель называет сам, и
# называет её «размером образа на диске»; поэтому величин здесь две, и
# каждая несёт своё имя и свой способ снятия.

IMAGE_REGISTRY_METHOD = (
    "docker image inspect --format {{.Size}} — объём сжатого содержимого "
    "образа плюс конфиг, то есть то, что поехало бы в реестр")
IMAGE_UNPACKED_METHOD = (
    "docker image save, слои из manifest.json распакованы, сумма размеров "
    "обычных файлов во всех слоях — распакованное содержимое образа без "
    "заголовков и выравнивания tar")


def image_registry_bytes(final_tag):
    """Объём сжатого содержимого образа: то, что поехало бы в реестр."""
    return int(_capture(["docker", "image", "inspect", final_tag,
                         "--format", "{{.Size}}"]))


def image_unpacked_bytes(final_tag):
    """Распакованный объём содержимого образа.

    ПОЧЕМУ НЕ `docker image save | wc -c`. На хранилище containerd слои
    лежат СЖАТЫМИ, и save отдаёт их как есть: у плеча cpp это 429 056
    байт при бинарнике в 1 022 128 — та же безымянная шкала плюс расходы
    tar. Но сам поток save годится: слои в нём названы в manifest.json,
    и их можно распаковать.

    ПОЧЕМУ НЕ `docker export`. Он отдаёт файловую систему КОНТЕЙНЕРА, а
    не образа: Docker подставляет туда свои /etc/resolv.conf, /etc/hosts
    и /etc/hostname поверх файлов образа, добавляет /.dockerenv, /dev,
    /proc и /sys. У этих образов из-за этого пропадали ровно те 19
    байтов /etc/resolv.conf, которые в образе лежат, а вместо них
    появлялось семь пустых записей контейнерного рантайма. Величина
    называлась бы объёмом образа, а мерила бы объём контейнера.

    ЧТО СЧИТАЕТСЯ. Сумма размеров ОБЫЧНЫХ ФАЙЛОВ во всех слоях образа.
    Не длина потока: tar несёт заголовки по 512 байт на запись и
    выравнивание хвоста, и это расходы транспорта, а не содержимое.
    Каталоги и символические ссылки размера не несут.

    ЧЕГО ЭТОТ СЧЁТ НЕ УМЕЕТ. Файл, перекрытый или удалённый в следующем
    слое, был бы посчитан дважды или зря. У образов этой оси такого нет
    — каждый слой добавляет свои файлы, — и это не предполагается, а
    проверяется: записи-удаления (.wh.) и повторные имена роняют замер.
    """
    import gzip
    import tarfile
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        archive_path = os.path.join(tmp, "image.tar")
        _run(["docker", "image", "save", final_tag, "-o", archive_path])
        root = os.path.join(tmp, "unpacked")
        with tarfile.open(archive_path) as archive:
            archive.extractall(root)
        with io.open(os.path.join(root, "manifest.json"),
                     encoding="utf-8") as handle:
            manifest = json.loads(handle.read())
        layers = manifest[0]["Layers"]
        total = 0
        seen = set()
        for layer in layers:
            path = os.path.join(root, *layer.split("/"))
            with io.open(path, "rb") as raw:
                head = raw.read(2)
            opener = gzip.open if head == b"\x1f\x8b" else io.open
            with opener(path, "rb") as stream:
                with tarfile.open(fileobj=stream, mode="r|") as layer_tar:
                    for member in layer_tar:
                        name = os.path.basename(member.name)
                        if name.startswith(".wh."):
                            raise RuntimeError(
                                "слой %s удаляет файл (%s): сумма по слоям "
                                "перестала быть объёмом образа"
                                % (layer, member.name))
                        if not member.isreg():
                            continue
                        if member.name in seen:
                            raise RuntimeError(
                                "файл %s встречается в двух слоях: сумма по "
                                "слоям посчитала бы его дважды" % member.name)
                        seen.add(member.name)
                        total += member.size
        return total


def binary_size_and_sha256_in_final(final_tag):
    """Размер и сумма бинарника ВНУТРИ финального образа.

    Финальный образ — scratch: ни шелла, ни sha256sum там нет, поэтому
    файл выкапывается наружу (docker create + docker cp) и хешируется
    здесь. Сумма считается по тем же байтам, размер которых идёт в
    таблицу, — иначе сверка с суммой из образа сборки показывала бы
    совпадение двух чисел, снятых с разных файлов.
    """
    import tempfile
    container = _capture(["docker", "create", final_tag])
    try:
        with tempfile.TemporaryDirectory() as tmp:
            dest = os.path.join(tmp, "stand_service")
            _run(["docker", "cp", "%s:/stand_service" % container, dest])
            with io.open(dest, "rb") as handle:
                data = handle.read()
            return len(data), hashlib.sha256(data).hexdigest()
    finally:
        subprocess.run(["docker", "rm", "-f", container],
                       capture_output=True, text=True,
                       encoding="utf-8", errors="replace")


def treatment_field(treatment, key):
    """Значение поля KEY=[...] из строки лечения."""
    needle = "%s=[" % key
    pos = treatment.find(needle)
    if pos < 0:
        return None
    start = pos + len(needle)
    end = treatment.find("]", start)
    if end < 0:
        return None
    return treatment[start:end]


# --------------------------------------------------------------------
# Пробы сервиса


def _wait_for_ready(container, timeout=READY_TIMEOUT):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        proc = subprocess.run(["docker", "logs", container],
                              capture_output=True, text=True,
                              encoding="utf-8", errors="replace")
        combined = (proc.stdout or "") + (proc.stderr or "")
        for line in combined.splitlines():
            if line.strip().startswith("READY "):
                return
        time.sleep(0.05)
    raise RuntimeError("%s не напечатал READY за %ss" % (container, timeout))


def _host_port(container, attempts=40):
    for _ in range(attempts):
        proc = subprocess.run(["docker", "port", container, "8080/tcp"],
                              capture_output=True, text=True,
                              encoding="utf-8", errors="replace")
        line = (proc.stdout or "").strip()
        if line:
            return int(line.splitlines()[0].rsplit(":", 1)[-1])
        time.sleep(0.05)
    raise RuntimeError("%s: нет проброшенного порта 8080" % container)


def start_container(arm):
    """Запускает финальный образ плеча.

    Имя варианта передаётся ЯВНО через -e STAND_VARIANT: плечи go и
    go-divergent делят один Dockerfile, и запечённое в образ имя у них
    одно и то же. Без явной переменной оба называли бы себя первым
    именем, и проверка «имена различаются» проверяла бы совпадение.
    """
    tag = "cpp-build-and-package-sibling:%s" % arm["name"]
    container = _capture([
        "docker", "run", "--rm", "-d",
        "-e", "STAND_VARIANT=%s" % arm["name"],
        "-p", "0:8080", tag])
    return container


def stop_container(container):
    subprocess.run(["docker", "stop", container],
                   capture_output=True, text=True,
                   encoding="utf-8", errors="replace")


def raw_http_get(host_port, path, timeout=PROBE_TIMEOUT):
    """Полный ответ HTTP — со строкой статуса и заголовками, побайтно.

    urllib отдал бы только тело, и проверка тождества сравнивала бы
    «ok» с «ok», не замечая расхождения в заголовках. Заголовки здесь
    часть контракта: Content-Length вычисляется каждой реализацией
    самостоятельно.
    """
    import socket
    request = ("GET %s HTTP/1.1\r\nHost: 127.0.0.1\r\n"
               "Connection: close\r\n\r\n" % path).encode("ascii")
    sock = socket.create_connection(("127.0.0.1", host_port), timeout=timeout)
    try:
        sock.sendall(request)
        chunks = []
        while True:
            data = sock.recv(4096)
            if not data:
                break
            chunks.append(data)
        return b"".join(chunks)
    finally:
        sock.close()


# Поля тела /resolve, которые из сравнения между плечами ИСКЛЮЧЕНЫ, и
# причины у них разные.
#
#   variant — исключён потому, что обязан различаться: это имя плеча.
#     «Различается ровно одно» проверяется отдельно и требует, чтобы
#     значения действительно различались попарно.
#   detail — исключён потому, что НЕ СРАВНИМ. Текст ошибки каждая
#     реализация пишет своими словами: musl отдаёт символьное имя кода
#     getaddrinfo, Go — текст net.LookupHost, Rust — текст
#     std::io::Error. Пока detail входил в сравниваемое тело, ЛЮБОЙ
#     отказ разрешения имени печатал «ЭТО НЕ ОДИН СЕРВИС» по причине, к
#     тождеству реализаций отношения не имеющей. Требовать, чтобы это
#     поле различалось, нельзя и не нужно: при успешном разрешении оно
#     пусто у всех. Сам исход /resolve сравнивается отдельной проверкой
#     и по-прежнему обязан совпасть.
RESOLVE_EXCLUDED_FIELDS = ("detail", "variant")


def probe_service(arm, host_port):
    """Снимает всё, на чём стоит проверка «это один и тот же сервис»."""
    healthz = raw_http_get(host_port, "/healthz")
    resolve = raw_http_get(host_port, "/resolve?host=example.com")
    resolve_body = resolve.split(b"\r\n\r\n", 1)[-1].decode("utf-8")
    body = json.loads(resolve_body)
    compared = {k: v for k, v in body.items()
                if k not in RESOLVE_EXCLUDED_FIELDS}
    return {
        "healthz_response": healthz.decode("utf-8"),
        "healthz_response_sha256": hashlib.sha256(healthz).hexdigest(),
        "healthz_response_bytes": len(healthz),
        "resolve_outcome": body.get("outcome"),
        "resolve_detail": body.get("detail"),
        "resolve_reported_variant": body.get("variant"),
        "resolve_excluded_fields": sorted(RESOLVE_EXCLUDED_FIELDS),
        "resolve_body_compared": json.dumps(
            compared, ensure_ascii=False, sort_keys=True),
    }


def measure_first_response_ms(arm, attempts=FIRST_RESPONSE_ATTEMPTS):
    """Медиана времени от `docker run` до первого успешного /healthz.

    Каждый замер — отдельный холодный запуск контейнера. Величина
    покрывает холодный старт целиком: создание контейнера, старт
    процесса, первый ответ; долю языка в ней стенд отделять не умеет.
    Разброс возвращается вместе с медианой — по решению, записанному до
    прогона, именно он решает, останется ли по этой величине вывод.
    """
    samples = []
    tag = "cpp-build-and-package-sibling:%s" % arm["name"]
    for i in range(attempts):
        start = time.monotonic()
        container = _capture([
            "docker", "run", "--rm", "-d",
            "-e", "STAND_VARIANT=%s" % arm["name"],
            "-p", "0:8080", tag])
        try:
            host_port = _host_port(container)
            deadline = time.monotonic() + READY_TIMEOUT
            elapsed_ms = None
            while time.monotonic() < deadline:
                try:
                    with urllib.request.urlopen(
                            "http://127.0.0.1:%d/healthz" % host_port,
                            timeout=2) as resp:
                        resp.read()
                except (urllib.error.URLError, ConnectionError, TimeoutError):
                    time.sleep(0.02)
                    continue
                elapsed_ms = (time.monotonic() - start) * 1000.0
                break
            if elapsed_ms is None:
                raise RuntimeError(
                    "%s: /healthz не ответил за %ss (замер %d/%d)"
                    % (arm["name"], READY_TIMEOUT, i + 1, attempts))
            samples.append(elapsed_ms)
        finally:
            stop_container(container)
    # Разброс считается из ЗАПИСАННЫХ min и max, а не из сырых замеров:
    # иначе он не следует за числами, которые лежат в строке рядом с ним,
    # и расходится с ними в последнем знаке. Число в фикстуре обязано
    # быть выводом из соседних чисел той же фикстуры.
    low = round(min(samples), 1)
    high = round(max(samples), 1)
    return {
        "first_response_ms": round(statistics.median(samples), 1),
        "first_response_min_ms": low,
        "first_response_max_ms": high,
        "spread_ms": round(high - low, 1),
        "first_response_samples": len(samples),
    }
