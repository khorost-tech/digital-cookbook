"""Корректное завершение: измеряемый сценарий, а не декларация.

«Graceful shutdown» обычно обещают. Здесь он измеряется: под постоянной
нагрузкой на /wait процессу посылается SIGTERM, и считается судьба запросов,
БЫВШИХ В ПОЛЁТЕ в момент сигнала.

Именно в полёте, а не всех подряд. Первая версия считала потерей каждый
незавершившийся запрос из плана нагрузчика — и выдала, что gunicorn теряет
96% и при штатном завершении, и при обрыве. На деле сервер с двумя
синхронными воркерами успевал обслужить около 290 запросов из 4000 за три
секунды до сигнала, а остальные 3700 не терялись: они просто не начинались.
Так сравнивалась пропускная способность, а не качество завершения.

КОНТРОЛЬ ОБЯЗАТЕЛЕН. Без него «ноль потерянных запросов» ничего не значит:
неизвестно, теряются ли они вообще при этой нагрузке и на этой машине.
Поэтому каждый сервер гасится дважды — SIGTERM (штатное завершение) и
SIGKILL (обрыв без шанса на дренаж). Если при SIGKILL потерь тоже ноль,
значит сценарий не умеет их ловить, и разбор обязан это отвергнуть, а не
записать в заслугу серверу.

Третий вопрос: что происходит с запросом, который длиннее таймаута дренажа.
Для этого есть плечо с ожиданием заведомо дольше таймаута.

СИГНАЛЫ ШЛЮТСЯ ПО-РАЗНОМУ, и это не мелочь. SIGTERM идёт мастеру — так
поступает оркестратор, и так работает штатный дренаж. А SIGKILL идёт ВСЕЙ
ГРУППЕ процессов: у gunicorn воркеры это дети мастера, они переживают его
смерть и продолжают отвечать. Первая версия слала SIGKILL только мастеру, и
контроль показал 0 потерь из 4000 — то есть сценарий вообще не умел ловить
потери, о чём разбор и сообщил, отказавшись считать остальное.
"""

import json
import os
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request

PY = "/opt/py/web/bin/python"
APP_DIR = "/app"
BENCH_DIR = os.path.dirname(os.path.abspath(__file__))

TARGET_CPUS = "0-3"
LOADER_CPUS = "4-15"
PORT = 8011
CONCURRENCY = 32
WAIT_SECONDS = 0.02

# Развёртка по числу воркеров у gunicorn проверяет гипотезу: уцелевают ровно
# те запросы, которые ОБРАБАТЫВАЮТСЯ, а не те, что приняты. У синхронной
# модели это разные вещи — воркер держит один запрос, остальные стоят в
# очереди принятых соединений, и дренаж их не касается.
#
# Если гипотеза верна, число уцелевших при SIGTERM должно расти вместе с
# числом воркеров и примерно ему равняться. Если нет — объяснение другое,
# и его придётся искать, а не додумывать.
SERVERS = [
    ("uvicorn", ["-m", "uvicorn", "app_fastapi:app", "--port", str(PORT),
                 "--log-level", "warning"]),
] + [
    (f"gunicorn-w{w}",
     ["-m", "gunicorn", "-b", f"127.0.0.1:{PORT}", "-w", str(w),
      "--log-level", "warning", "app_flask:app"])
    for w in (1, 2, 4, 8)
]


def start(argv):
    # Своя группа процессов: иначе SIGKILL мастеру gunicorn не трогает его
    # воркеров, и «обрыв» оказывается не обрывом.
    proc = subprocess.Popen(
        [PY, f"{BENCH_DIR}/pin.py", TARGET_CPUS, "--", PY] + argv,
        cwd=APP_DIR, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        start_new_session=True)
    url = f"http://127.0.0.1:{PORT}/ping"
    deadline = time.time() + 40
    while time.time() < deadline:
        if proc.poll() is not None:
            return None
        try:
            with urllib.request.urlopen(url, timeout=5) as resp:
                if resp.status == 200:
                    time.sleep(0.5)
                    return proc
        except (OSError, urllib.error.HTTPError):
            pass
        time.sleep(0.2)
    proc.terminate()
    return None


def run_case(server_name, argv, how, round_no):
    """Поднять сервер, начать нагрузку, послать сигнал, посчитать потери."""
    proc = start(argv)
    if proc is None:
        return None

    # Нагрузка идёт в отдельном процессе и НЕ ждёт завершения: сервер будет
    # убит посреди неё, и нам важно, что увидит клиент.
    # Запросов заведомо больше, чем сервер успеет обслужить до сигнала.
    # Фиксированные 4000 у uvicorn заканчивались на 1.5 секунде, то есть
    # раньше сигнала на 3.0 — гасить было уже нечего, и в полёте не
    # оказывалось ни одного запроса. Лишние запросы после обрыва отваливаются
    # мгновенно и в подсчёт потерь не идут: они начались после сигнала.
    count = 40000
    loader = subprocess.Popen(
        [PY, "load.py", "--url", f"http://127.0.0.1:{PORT}/wait",
         "--framework", f"{server_name}/{how}", "--endpoint", "wait",
         "--concurrency", str(CONCURRENCY), "--count", str(count),
         "--warmup", "100", "--procs", "4", "--round", str(round_no)],
        cwd=BENCH_DIR, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
        text=True, encoding="utf-8",
        env=dict(os.environ, LOADER_CPUS=LOADER_CPUS,
                 PYTHONPATH=BENCH_DIR, EMIT_SAMPLES="1"))

    # Даём нагрузке выйти на режим, затем гасим сервер посреди работы.
    time.sleep(3.0)
    sent_at = time.time()
    if how == "sigterm":
        # Штатный путь: сигнал мастеру, дальше он сам гасит воркеров.
        proc.send_signal(signal.SIGTERM)
    else:
        # Обрыв: вся группа разом, иначе воркеры переживут смерть мастера.
        os.killpg(os.getpgid(proc.pid), signal.SIGKILL)

    try:
        proc.wait(timeout=30)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
        except ProcessLookupError:
            pass
        proc.wait(timeout=10)
    # После штатного завершения мастера воркеры могут ещё доживать: ждём,
    # пока порт освободится, иначе следующий случай попадёт на чужой сервер.
    deadline = time.time() + 20
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{PORT}/ping",
                                        timeout=2):
                time.sleep(0.3)
        except (OSError, urllib.error.HTTPError):
            break
    shutdown_s = time.time() - sent_at

    out, _ = loader.communicate(timeout=120)
    lines = (out or "").strip().splitlines()
    if not lines:
        return None
    data = json.loads(lines[-1])

    # Судьба запросов, бывших в полёте в момент сигнала: начались раньше
    # сигнала и должны были закончиться позже. Запросы, начавшиеся после
    # сигнала, потерями не считаются — их сервер имеет право не принимать.
    inflight = inflight_ok = started_after = 0
    # Имя переменной НЕ start: оно затенило бы функцию start() на всю
    # область видимости функции, и падало бы ещё до цикла.
    for began, dur, ok in data.get("samples", []):
        if began < sent_at:
            if began + dur >= sent_at:
                inflight += 1
                inflight_ok += ok
        else:
            started_after += 1

    data.update({
        "server": server_name,
        "how": how,
        "round": round_no,
        "signal_at": round(sent_at, 6),
        "shutdown_s": round(shutdown_s, 3),
        "inflight": inflight,
        "inflight_ok": inflight_ok,
        "inflight_lost": inflight - inflight_ok,
        "started_after_signal": started_after,
        "requests_seen": data["count"],
    })
    # Пофразовые отметки в фикстуру не кладём: они нужны только для этого
    # подсчёта и раздули бы файл в сотни раз.
    data.pop("samples", None)
    return json.dumps(data, sort_keys=True)


def main():
    rounds = int(os.environ.get("ROUNDS", "3"))
    out_path = os.environ.get("OUT", "/fixtures/shutdown.txt")

    with open(out_path, "a", encoding="utf-8") as out:
        for r in range(1, rounds + 1):
            for server_name, argv in SERVERS:
                # Порядок «сначала контроль, потом штатное» чередуется по
                # кругам: иначе один из способов всегда попадал бы на более
                # холодную машину.
                order = (["sigkill", "sigterm"] if r % 2
                         else ["sigterm", "sigkill"])
                for how in order:
                    line = run_case(server_name, argv, how, r)
                    if line is None:
                        print(f"ОТКАЗ: {server_name}/{how} круг {r} "
                              "не дал результата", file=sys.stderr)
                        return 2
                    out.write(line + "\n")
                    out.flush()
                print(f"круг {r}: {server_name} готов", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
