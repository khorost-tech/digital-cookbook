"""Ось 3: корректное завершение по SIGTERM → fixtures/shutdown.jsonl.

Варианты (план 30.09 плюс один):
  control-no-signal — сервис с дослуживанием, сигнала нет: контроль;
  no-handler        — обработчика нет, сервис — PID 1 в контейнере;
  no-handler-init   — обработчика нет, контейнер с --init (tini — PID 1);
  drain-enough      — дослуживание, срок 5000 мс при работе 3000 мс;
  drain-short       — дослуживание, срок 1000 мс.
Вариант no-handler-init добавлен 05.10: PID 1 без обработчика ядро по SIGTERM
не завершает, и «без обработки» без --init мерило бы это, а не отсутствие
обработчика.

Большой ответ медленному читателю (по рецензиям 05.10): один /work с набивкой
BIG["bytes"], клиент читает с паузами, отправка длится дольше ожидания. Каждый
вариант повторяется REPEATS раз; в строке — сумма по повторам. Суффикс
-halfclose — клиент закрывает отправку (SHUT_WR) сразу после запроса.
  control-slow-reader[-halfclose]  — без сигнала: контроль, что клиент дочитывает;
  drain-slow-after-ack[-halfclose] — с учёта, когда очередь отправки сокета пуста;
  drain-slow-after-close[-halfclose] — с учёта по EOF от клиента;
  drain-slow-after-write           — с учёта по завершении async_write;
  drain-slow-before-write          — с учёта до отправки.
nondeterministic — исход, который различается между повторами и сессиями,
известен или предсказан ДО прогона (expected.json); счёт таких вариантов в
сравнение сессий не входит, но в фикстуру и статью идёт.

    python3 scripts/run-shutdown.py                → все варианты, фикстура
    python3 scripts/run-shutdown.py <вариант>      → один вариант, строка JSON
"""

import json
import os
import subprocess
import sys
import threading
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, SERVED  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "shutdown.jsonl")
SVC = "cpp-correctness-and-operations-svc:latest"
CLIENT = "python@sha256:3dd7cc108ec1493442514f5c2a871af6af0ec31d768ff6e378a93340c3b3db5f"
NET = "cpp-correctness-net"
SENT, WORK_MS, WAIT_EXIT_S = 4, 3000, 8
BIG = {"sent": 1, "work_ms": 1000, "bytes": 16000000, "repeats": 5}
DRAIN_BIG = {"STAND_SHUTDOWN": "drain", "STAND_DRAIN_MS": "20000"}


def big(release, signal=True, halfclose=False, nondeterministic=False):
    env = dict(DRAIN_BIG, STAND_INFLIGHT_RELEASE=release)
    return dict(BIG, env=env, signal=signal, halfclose=halfclose, nondeterministic=nondeterministic)


VARIANTS = {
    "control-no-signal": {"env": {"STAND_SHUTDOWN": "drain", "STAND_DRAIN_MS": "5000"}, "signal": False},
    "no-handler": {"env": {"STAND_SHUTDOWN": "none"}, "signal": True},
    "no-handler-init": {"env": {"STAND_SHUTDOWN": "none"}, "signal": True, "init": True},
    "drain-enough": {"env": {"STAND_SHUTDOWN": "drain", "STAND_DRAIN_MS": "5000"}, "signal": True},
    "drain-short": {"env": {"STAND_SHUTDOWN": "drain", "STAND_DRAIN_MS": "1000"}, "signal": True},
    "control-slow-reader": big("after-ack", signal=False),
    "control-slow-reader-halfclose": big("after-ack", signal=False, halfclose=True),
    "drain-slow-after-ack": big("after-ack"),
    "drain-slow-after-ack-halfclose": big("after-ack", halfclose=True),
    "drain-slow-after-close": big("after-close"),
    "drain-slow-after-close-halfclose": big("after-close", halfclose=True, nondeterministic=True),
    "drain-slow-after-write": big("after-write", nondeterministic=True),
    "drain-slow-before-write": big("before-write"),
}


def sh(*argv, check=True):
    p = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if check and p.returncode != 0:
        raise SystemExit("%s: %s" % (" ".join(argv), p.stdout.decode()))
    return p.stdout.decode().strip()


def build():
    docker.build_base()
    sh("docker", "build", "-q", "-t", SVC, "-f", os.path.join(docker.HERE, "images", "Dockerfile.svc"),
       docker.HERE)
    if NET not in sh("docker", "network", "ls", "--format", "{{.Name}}"):
        sh("docker", "network", "create", NET)


def run_variant(name):
    spec = VARIANTS[name]
    runs = [run_once(name, spec) for _ in range(spec.get("repeats", 1))]
    row = runs[0]
    if len(runs) > 1:
        for k in ("sent", "in_flight_at_signal", "served", "dropped", "truncated"):
            row[k] = sum(r[k] for r in runs)
        row["accepted_after_signal"] = any(r["accepted_after_signal"] for r in runs)
        codes = {r["exit_code"] for r in runs}
        row["exit_code"] = codes.pop() if len(codes) == 1 else sorted(codes, key=str)
        row["exited_by_itself"] = all(r["exited_by_itself"] for r in runs)
        row["exit_after_signal_s"] = [r["exit_after_signal_s"] for r in runs]
        row["per_repeat"] = ["served" if r["served"] == r["sent"] else
                             "truncated" if r["truncated"] else "dropped" for r in runs]
    row["repeats"] = len(runs)
    row["halfclose"] = bool(spec.get("halfclose"))
    row["nondeterministic"] = bool(spec.get("nondeterministic"))
    if row["dropped"] == 0 and row["served"] == row["sent"]:
        row["outcome"], row["detail"] = "served", "дослужены все %d" % row["sent"]
    else:
        row["outcome"] = "refused" if row["served"] == 0 else "mixed"
        row["detail"] = "дослужено %d из %d, оборвано %d (из них обрезано посреди отправки %d)" % (
            row["served"], row["sent"], row["dropped"], row["truncated"])
    SERVED.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def run_once(name, spec):
    sent, work_ms = spec.get("sent", SENT), spec.get("work_ms", WORK_MS)
    box = "svc-" + name
    sh("docker", "rm", "-f", box, check=False)
    argv = ["docker", "run", "-d", "--name", box, "--network", NET,
            "-e", "STAND_PORT=8080", "-e", "STAND_TIMEOUT_MS=1000"]
    if spec.get("init"):
        argv.append("--init")
    for k, v in spec["env"].items():
        argv += ["-e", "%s=%s" % (k, v)]
    sh(*argv, SVC)
    time.sleep(0.5)
    client = subprocess.Popen(
        ["docker", "run", "--rm", "-i", "--network", NET, "-v",
         "%s:/s:ro" % os.path.join(docker.HERE, "scripts"), CLIENT,
         "python", "/s/shutdown_client.py", box, str(sent), str(work_ms), str(spec.get("bytes", 0)),
         "halfclose" if spec.get("halfclose") else "full"],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    ready = client.stdout.readline().strip()
    signalled_at = None
    # Выход контейнера ловит `docker wait` параллельно с клиентом: клиент может
    # дочитывать ещё долго после того, как сервис вышел, и время выхода по его
    # окончанию было бы временем клиента.
    exited = {}

    def watch():
        p = subprocess.run(["docker", "wait", box], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        if p.returncode == 0 and p.stdout.strip():
            exited["code"], exited["at"] = int(p.stdout.strip()), time.time()

    watcher = threading.Thread(target=watch, daemon=True)
    watcher.start()
    if spec["signal"]:
        sh("docker", "kill", "--signal=SIGTERM", box)
        signalled_at = time.time()
    client.stdin.write("go\n")
    client.stdin.flush()
    tail = client.stdout.read()
    client.wait()
    result = json.loads(tail.strip().splitlines()[-1])
    watcher.join(WAIT_EXIT_S)
    exit_code, exited_by_itself = exited.get("code"), "code" in exited
    exit_s = round(exited["at"] - signalled_at, 1) if (signalled_at and exited_by_itself) else None
    sh("docker", "rm", "-f", box, check=False)
    row = {"kind": "shutdown", "variant": name,
           "probe": "sigterm" if spec["signal"] else "no-signal",
           "cmd": "python3 scripts/run-shutdown.py %s" % name,
           "ready": ready, "work_ms": work_ms, "drain_ms": int(spec["env"].get("STAND_DRAIN_MS", 0)),
           "bytes": spec.get("bytes", 0),
           "release": spec["env"].get("STAND_INFLIGHT_RELEASE", "after-ack"),
           "exit_code": exit_code, "exited_within_s": WAIT_EXIT_S if exited_by_itself else None,
           "exited_by_itself": exited_by_itself, "exit_after_signal_s": exit_s}
    row.update(result)
    return row


def main():
    build()
    names = sys.argv[1:] or list(VARIANTS)
    rows = []
    for name in names:
        row = run_variant(name)
        rows.append(row)
        print(json.dumps({k: row[k] for k in ("variant", "outcome", "sent", "in_flight_at_signal", "served",
                                               "dropped", "truncated", "accepted_after_signal", "exit_code",
                                               "exited_by_itself", "exit_after_signal_s")},
                         ensure_ascii=False))
    if not sys.argv[1:]:
        with open(FIXTURE, "w", encoding="utf-8") as f:
            for row in rows:
                f.write(json.dumps(row, ensure_ascii=False) + "\n")
            f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
