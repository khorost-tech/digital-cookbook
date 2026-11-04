#!/usr/bin/env python3
"""Снимает fixtures/package.jsonl — прогон оси упаковки.

На каждый из шести вариантов:
  1. собрать образ (scripts.docker.build_variant) — размеры, digest,
     зависимости и версии инструментов образа сборки (компилятор, cmake,
     libc): без них утверждение о том, чем различаются половины пары про
     устаревшее правило, держалось бы на имени образа в variants.json;
  2. поднять контейнер, дождаться строки READY в его stderr — не спать
     по таймеру, иначе прогон будет плавающим;
  3. дёрнуть /healthz и /resolve, записать по строке на каждую пробу;
  4. отдельно снять время до первого ответа: 11 независимых холодных
     запусков контейнера, время от команды `docker run` до первого
     успешного ответа /healthz. Это единственная временная величина
     стенда — медиана печатается вместе с разбросом (min/max), и если
     разброс перекрывает разницу между вариантами, по ней нельзя будет
     делать вывод — это оценивает разбор, не этот скрипт.

Контейнер каждый раз поднимается заново (--rm, без переиспользования) —
и для проб исхода, и для замера времени: иначе тёплый кеш файловой
системы/страниц исказил бы именно то время, которое мы хотим измерить у
холодного старта.
"""

import io
import json
import os
import statistics
import subprocess
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, HERE)

from scripts import docker as dockerlib  # noqa: E402
from scripts.console import force_utf8  # noqa: E402

# Потоки переключаются в UTF-8 первым делом: на консоли cp1251
# печать «≠» роняла разбор с UnicodeEncodeError. Найдено прогоном
# на втором хосте.
force_utf8()

FIXTURES_DIR = os.path.join(HERE, "fixtures")
FIXTURE_PATH = os.path.join(FIXTURES_DIR, "package.jsonl")

NAMES = ["glibc-dynamic", "glibc-static", "glibc-static-old",
         "musl-static", "musl-no-resolv", "distroless"]

FIRST_RESPONSE_ATTEMPTS = 11
READY_TIMEOUT = 30
PROBE_TIMEOUT = 20


def _log(msg):
    print(msg, file=sys.stderr)
    sys.stderr.flush()


def _wait_for_ready(container, timeout=READY_TIMEOUT):
    """Ждёт строку READY <порт> в логах контейнера — не спит по таймеру."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        proc = subprocess.run(["docker", "logs", container],
                               capture_output=True, text=True,
                               encoding="utf-8", errors="replace")
        combined = (proc.stdout or "") + (proc.stderr or "")
        for line in combined.splitlines():
            line = line.strip()
            if line.startswith("READY "):
                return
        time.sleep(0.05)
    raise RuntimeError(
        "%s не напечатал READY за %ss" % (container, timeout))


def _host_port(container, container_port="8080/tcp", attempts=20):
    """docker port может быть недоступен в самый первый момент после -d."""
    for _ in range(attempts):
        proc = subprocess.run(["docker", "port", container, container_port],
                               capture_output=True, text=True,
                               encoding="utf-8", errors="replace")
        line = (proc.stdout or "").strip()
        if line:
            return int(line.splitlines()[0].rsplit(":", 1)[-1])
        time.sleep(0.05)
    raise RuntimeError("%s: нет проброшенного порта %s"
                        % (container, container_port))


def _stop(container):
    subprocess.run(["docker", "stop", container],
                    capture_output=True, text=True,
                    encoding="utf-8", errors="replace")


def _http_get(url, timeout=PROBE_TIMEOUT):
    with urllib.request.urlopen(url, timeout=timeout) as resp:
        return resp.read().decode("utf-8")


def _start_container(name):
    container = dockerlib.run_variant(name, extra_docker_args=["-p", "0:8080"])
    _wait_for_ready(container)
    host_port = _host_port(container)
    return container, host_port


def probe_healthz(name, host_port):
    url = "http://127.0.0.1:%d/healthz" % host_port
    try:
        body = _http_get(url)
    except (urllib.error.URLError, ConnectionError, TimeoutError) as exc:
        return {"kind": "package", "variant": name, "probe": "healthz",
                "outcome": "error", "detail": "healthz не ответил: %r" % exc}
    if body.strip() != "ok":
        return {"kind": "package", "variant": name, "probe": "healthz",
                "outcome": "error",
                "detail": "неожиданное тело /healthz: %r" % body}
    return {"kind": "package", "variant": name, "probe": "healthz",
            "outcome": "resolved", "detail": ""}


def probe_resolve(name, host_port):
    url = "http://127.0.0.1:%d/resolve?host=example.com" % host_port
    try:
        raw = _http_get(url)
    except (urllib.error.URLError, ConnectionError, TimeoutError) as exc:
        return {"kind": "package", "variant": name, "probe": "resolve",
                "outcome": "error", "detail": "resolve не ответил: %r" % exc}
    try:
        body = json.loads(raw)
    except ValueError as exc:
        return {"kind": "package", "variant": name, "probe": "resolve",
                "outcome": "error",
                "detail": "тело /resolve не JSON: %r (%s)" % (raw, exc)}
    # Контракт уже проставлен сервисом (kind/probe/outcome/detail) — имя
    # варианта и kind переустанавливаются здесь явно, а не берутся из
    # ответа: разбор доверяет тому, ЧТО МЫ ЗАПУСТИЛИ, а не тому, что
    # сервис сказал о себе сам (сервис мог унаследовать чужое имя из
    # запечённого ENV, если бы -e STAND_VARIANT не передали).
    row = dict(body)
    row["kind"] = "package"
    row["variant"] = name
    row["probe"] = "resolve"
    return row


def measure_first_response_ms(name, attempts=FIRST_RESPONSE_ATTEMPTS):
    """Медиана времени от `docker run` до первого успешного /healthz.

    Каждый замер — отдельный холодный запуск контейнера: тёплый диск/кеш
    страниц исказил бы именно то, что здесь меряется.
    """
    samples = []
    for i in range(attempts):
        start = time.monotonic()
        container = dockerlib.run_variant(name, extra_docker_args=["-p", "0:8080"])
        try:
            host_port = _host_port(container)
            deadline = time.monotonic() + READY_TIMEOUT
            elapsed_ms = None
            while time.monotonic() < deadline:
                try:
                    body = _http_get(
                        "http://127.0.0.1:%d/healthz" % host_port, timeout=2)
                except (urllib.error.URLError, ConnectionError, TimeoutError):
                    time.sleep(0.02)
                    continue
                if body.strip() == "ok":
                    elapsed_ms = (time.monotonic() - start) * 1000.0
                    break
            if elapsed_ms is None:
                raise RuntimeError(
                    "%s: /healthz не ответил за %ss (замер %d/%d)"
                    % (name, READY_TIMEOUT, i + 1, attempts))
            samples.append(elapsed_ms)
        finally:
            _stop(container)
    median_ms = statistics.median(samples)
    spread = {
        "min_ms": min(samples),
        "max_ms": max(samples),
        "spread_ratio": (max(samples) / min(samples)) if min(samples) > 0
        else None,
    }
    _log("  %s: время до первого ответа — медиана %.1f ms, "
         "разброс %.1f..%.1f ms (%d запусков)"
         % (name, median_ms, spread["min_ms"], spread["max_ms"], attempts))
    return median_ms, spread


def run_one(name):
    _log("=== %s ===" % name)
    _log("  сборка образа...")
    build = dockerlib.build_variant(name)
    _log("  image_bytes=%d image_unpacked_bytes=%d binary_bytes=%d "
         "dynamic_deps=%r"
         % (build["image_bytes"], build["image_unpacked_bytes"],
            build["binary_bytes"], build["dynamic_deps"]))
    _log("  инструменты образа сборки: cxx=%r cmake=%r libc=%r"
         % (build["cxx_version"], build["cmake_version"],
            build["libc_version"]))

    _log("  запуск для проб healthz/resolve...")
    container, host_port = _start_container(name)
    try:
        healthz_row = probe_healthz(name, host_port)
        resolve_row = probe_resolve(name, host_port)
    finally:
        _stop(container)

    _log("  замер времени до первого ответа (%d холодных запусков)..."
         % FIRST_RESPONSE_ATTEMPTS)
    median_ms, spread = measure_first_response_ms(name)

    healthz_row.update({
        "image_bytes": build["image_bytes"],
        "image_unpacked_bytes": build["image_unpacked_bytes"],
        "binary_bytes": build["binary_bytes"],
        "dynamic_deps": build["dynamic_deps"],
        "first_response_ms": round(median_ms, 1),
        "first_response_min_ms": round(spread["min_ms"], 1),
        "first_response_max_ms": round(spread["max_ms"], 1),
        "first_response_spread_ratio":
            round(spread["spread_ratio"], 3) if spread["spread_ratio"]
            else None,
        # Версии инструментов образа сборки. Раньше ось упаковки не
        # снимала их вовсе, и различие половин пары про устаревшее
        # правило держалось на имени образа в variants.json — на
        # договорённости, которую никто не проверял.
        "cxx_version": build["cxx_version"],
        "cmake_version": build["cmake_version"],
        "libc_version": build["libc_version"],
    })

    _log("  healthz: %s" % healthz_row["outcome"])
    _log("  resolve: %s (%s)" % (resolve_row["outcome"],
                                  resolve_row.get("detail") or "-"))
    return [healthz_row, resolve_row]


def main():
    os.makedirs(FIXTURES_DIR, exist_ok=True)
    all_rows = []
    for name in NAMES:
        all_rows.extend(run_one(name))

    with io.open(FIXTURE_PATH, "w", encoding="utf-8") as handle:
        for row in all_rows:
            handle.write(json.dumps(row, ensure_ascii=False, sort_keys=True))
            handle.write("\n")
        handle.write("COMPLETE\n")

    _log("записано %d строк в %s" % (len(all_rows), FIXTURE_PATH))


if __name__ == "__main__":
    main()
