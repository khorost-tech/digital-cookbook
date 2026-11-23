"""Ось 4: отказ на старте при битой конфигурации → fixtures/config.jsonl.

Девять значений STAND_PORT × два способа чтения (naive — atoi, strict —
from_chars + std::expected). Запуск контейнера на 3 секунды:
  refused          — вышел с кодом 2 и назвал причину в stderr;
  served           — корректное значение, жив и отвечает /healthz;
  started-silently — некорректное значение, но жив и отвечает /healthz на
                     порту, который сам напечатал («порт N»);
  failed-later     — стартовал, но умер или не отвечает;
  error            — проба сломалась.
/healthz спрашивает клиент в сети Docker, как в оси 3.

    python3 scripts/run-config.py                       → фикстура
    python3 scripts/run-config.py <naive|strict> <вход>  → одна строка
"""

import json
import os
import re
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import CONFIG, END_MARKER  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "config.jsonl")
SVC = "cpp-correctness-and-operations-svc:latest"
CLIENT = "python@sha256:3dd7cc108ec1493442514f5c2a871af6af0ec31d768ff6e378a93340c3b3db5f"
NET = "cpp-correctness-net"

# имя входа: значение STAND_PORT (None — переменной нет)
INPUTS = [("missing", None), ("empty", ""), ("letters", "abc"), ("trailing-garbage", "80x"),
          ("out-of-range", "70000"), ("negative", "-1"), ("leading-space", " 8080"),
          ("plus-sign", "+80"), ("valid", "8080")]

HEALTHZ = ("import socket,sys\n"
           "s=socket.create_connection((sys.argv[1],int(sys.argv[2])),timeout=2)\n"
           "s.sendall(b'GET /healthz HTTP/1.1\\r\\n\\r\\n');d=b''\n"
           "while True:\n c=s.recv(4096)\n if not c: break\n d+=c\n"
           "print(d.decode().split('\\r\\n')[0])")


def sh(*argv):
    p = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    return p.returncode, p.stdout.decode("utf-8", "replace").strip()


def probe(reader, name, value):
    box = "cfg-%s-%s" % (reader, name)
    sh("docker", "rm", "-f", box)
    argv = ["docker", "run", "-d", "--name", box, "--network", NET,
            "-e", "STAND_CONFIG=" + reader, "-e", "STAND_TIMEOUT_MS=1000"]
    if value is not None:
        argv += ["-e", "STAND_PORT=" + value]
    rc, out = sh(*argv, SVC)
    if rc != 0:
        return {"outcome": "error", "detail": "контейнер не запустился: " + out[-200:]}, None, None
    time.sleep(3)
    _, state = sh("docker", "inspect", "-f", "{{.State.Status}} {{.State.ExitCode}}", box)
    status, code = state.split()
    _, logs = sh("docker", "logs", box)
    m = re.search(r"порт (\d+)", logs)
    port = int(m.group(1)) if m else None
    if status == "exited":
        sh("docker", "rm", "-f", box)
        if int(code) == 2 and "конфигурация:" in logs:
            reason = next(l for l in logs.splitlines() if "конфигурация:" in l)
            return {"outcome": "refused", "detail": reason.strip()}, int(code), port
        return {"outcome": "failed-later", "detail": "вышел с кодом %s: %s" % (code, logs[-200:])}, int(code), port
    rc, line = sh("docker", "run", "--rm", "--network", NET, CLIENT, "python", "-c", HEALTHZ,
                  box, str(port or 0))
    sh("docker", "rm", "-f", box)
    if rc == 0 and line.startswith("HTTP/1.1 200"):
        outcome = "served" if name == "valid" else "started-silently"
        return {"outcome": outcome, "detail": "жив, /healthz отвечает на порту %s" % port}, None, port
    return {"outcome": "failed-later", "detail": "жив, но /healthz не отвечает на порту %s" % port}, None, port


def row_for(reader, name, value):
    verdict, code, port = probe(reader, name, value)
    row = {"kind": "config", "variant": reader, "probe": name,
           "cmd": "python3 scripts/run-config.py %s %s" % (reader, name),
           "reader": reader, "input_name": name, "value": value,
           "exit_code": code, "effective_port": port}
    row.update(verdict)
    CONFIG.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def main():
    docker.build_base()
    sh("docker", "build", "-q", "-t", SVC, "-f", os.path.join(docker.HERE, "images", "Dockerfile.svc"),
       docker.HERE)
    rc, nets = sh("docker", "network", "ls", "--format", "{{.Name}}")
    if NET not in nets.split():
        sh("docker", "network", "create", NET)
    if len(sys.argv) == 3:
        value = dict(INPUTS)[sys.argv[2]]
        print(json.dumps(row_for(sys.argv[1], sys.argv[2], value), ensure_ascii=False))
        return
    rows = []
    for reader in ("naive", "strict"):
        for name, value in INPUTS:
            row = row_for(reader, name, value)
            rows.append(row)
            print("%-7s %-17s %-8r %-17s порт=%-6s %s" % (reader, name, value, row["outcome"],
                                                          row["effective_port"], row["detail"][:70]))
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
