"""Сколько сообщений рукопожатия нужно, чтобы договориться.

ЧТО СЧИТАЕТСЯ И ПОЧЕМУ ИМЕННО ЭТО.

Считаются СООБЩЕНИЯ рукопожатия — не время и не пакеты. Время зависит от
машины и от того, чем она сейчас занята; пакеты — от сети, от размера
записей и от того, как их сложила операционная система. Сообщения задаёт
протокол, и на одной и той же паре сторон их число одинаково всегда.

ГДЕ ПРОХОДИТ ГРАНИЦА СЧЁТА.

Считаем до тех пор, пока завершающее сообщение не пришлют ОБЕ стороны.

Сперва здесь стояла граница «до завершающего сообщения клиента», и она
давала неверное сравнение: в старой версии клиент заканчивает РАНЬШЕ
сервера, в новой — позже. У старой версии из счёта выпадал ответ
сервера, и обе версии выходили равными по числу сообщений. Числа были
верные, граница — нет.

Билеты на возобновление сервер присылает уже ПОСЛЕ, когда
соединение готово, и их число зависит от настроек сервера, а не от
версии протокола. Смешать их со счётом значило бы приписать версии то,
что ею не определяется, поэтому они считаются отдельно.

ПРОГОН ДЕЛАЕТСЯ ДВАЖДЫ.

Не ради усреднения — усреднять тут нечего. Ради проверки, что число не
плавает: если два прогона дали разное, значит считается не то, что мы
думаем, и никакие выводы делать нельзя.

ОЖИДАНИЯ ЗАПИСАНЫ ДО ПРОГОНА:
  полное 1.3 короче полного 1.2;
  возобновлённое короче полного, и короче оно ровно на сообщения,
  которыми передаётся и проверяется сертификат.
"""

import io
import json
import os
import re
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import clients

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
IMAGE = "tls-stand-openssl:1"
SERVER_PORT = 18870

ENV = dict(os.environ, MSYS_NO_PATHCONV="1")

LINE = re.compile(r"^(>>>|<<<) TLS [0-9.]+, Handshake \[length [0-9a-f]+\], (\w+)")

# Рукопожатие закрыто, когда завершающее сообщение прислали ОБЕ стороны.
CLOSING = "Finished"
TICKET = "NewSessionTicket"


def trace(extra_args, session_out="", session_in=""):
    """Один прогон openssl с печатью сообщений рукопожатия."""
    request = os.path.join(HERE, "fixtures/.request.txt")
    with io.open(request, "w", encoding="ascii", newline="") as handle:
        handle.write("GET / HTTP/1.1\r\nHost: stand.local\r\n\r\n")

    cmd = ["docker", "run", "--rm", "-i",
           # Каталог фикстур подключается на запись: сюда openssl кладёт
           # сохранённую сессию для пробы про возобновление.
           "-v", HERE.replace(os.sep, "/") + ":/stand",
           "-w", "/stand", IMAGE, "s_client", "-msg", "-ign_eof",
           "-connect", "host.docker.internal:%d" % SERVER_PORT,
           "-servername", "stand.local",
           "-verify_hostname", "stand.local",
           "-CAfile", "pki/root.pem"] + list(extra_args)
    if session_out:
        cmd += ["-sess_out", session_out]
    if session_in:
        cmd += ["-sess_in", session_in]

    with io.open(request, "rb") as handle:
        done = subprocess.run(cmd, stdin=handle, capture_output=True,
                              text=True, encoding="utf-8", errors="replace",
                              env=ENV)
    return done.stdout + done.stderr


def count(raw):
    """Разбирает печать на сообщения рукопожатия.

    Возвращает сообщения до тех пор, пока завершающее не пришлют ОБЕ
    стороны, и отдельно число билетов на возобновление.

    Граница важна. Сперва считалось «до завершающего сообщения клиента»,
    и сравнение выходило неверным: в старой версии клиент заканчивает
    РАНЬШЕ сервера, в новой — позже. У старой версии из счёта выпадал
    ответ сервера, и обе версии оказывались равными.
    """
    messages = []
    tickets = 0
    finished = set()
    for line in raw.split("\n"):
        found = LINE.match(line.strip())
        if not found:
            continue
        direction, name = found.group(1), found.group(2)
        if name == TICKET:
            tickets += 1
            continue
        if len(finished) == 2:
            continue
        messages.append(("клиент" if direction == ">>>" else "сервер", name))
        if name == CLOSING:
            finished.add(direction)
    return messages, tickets


def flights(messages):
    """Сколько раз слово переходит от одной стороны к другой.

    Это и есть та величина, ради которой новую версию хвалят за скорость.
    По СООБЩЕНИЯМ выигрыш оказался всего в одно — а вот пересылок,
    то есть ожиданий ответа, у новой версии на одну меньше, и именно они
    стоят кругового времени сети.

    Считается по смене направления в потоке сообщений, а не по времени:
    время зависит от машины и сети, а порядок задан протоколом.
    """
    count_flights = 0
    previous = None
    for side, _ in messages:
        if side != previous:
            count_flights += 1
            previous = side
    return count_flights


def probe(case, extra_args, session_out="", session_in=""):
    raw = trace(extra_args, session_out, session_in)
    messages, tickets = count(raw)
    return {
        "kind": "handshake",
        "case": case,
        "client": "openssl",
        "messages_total": len(messages),
        "messages": ["%s: %s" % (side, name) for side, name in messages],
        "from_server": sum(1 for side, _ in messages if side == "сервер"),
        "from_client": sum(1 for side, _ in messages if side == "клиент"),
        "flights": flights(messages),
        "tickets_after": tickets,
    }


def main():
    session = "fixtures/.session.pem"
    session_path = os.path.join(HERE, session)
    rows = []

    with clients.Server("valid", SERVER_PORT):
        for run_no in (1, 2):
            if os.path.exists(session_path):
                os.remove(session_path)

            full13 = probe("full_13", [], session_out=session)
            full12 = probe("full_12", ["-tls1_2"])
            # Возобновление опирается на сессию, сохранённую первой пробой.
            resumed = probe("resumed_13", [], session_in=session)

            for row in (full13, full12, resumed):
                row["run"] = run_no
                rows.append(row)
                sys.stderr.write(
                    "прогон %d  %-12s сообщений: %d (сервер %d, клиент %d), "
                    "пересылок: %d, билетов после: %d\n"
                    % (run_no, row["case"], row["messages_total"],
                       row["from_server"], row["from_client"],
                       row["flights"], row["tickets_after"]))

    # Контроль воспроизводимости: два прогона обязаны дать одно и то же.
    for case in ("full_13", "full_12", "resumed_13"):
        got = [r["messages"] for r in rows if r["case"] == case]
        if got[0] != got[1]:
            sys.stderr.write(
                "\nРАСХОЖДЕНИЕ между прогонами на случае %s — считается не "
                "то, что мы думаем:\n  %r\n  %r\n" % (case, got[0], got[1]))

    out = os.path.join(HERE, "fixtures/handshake.jsonl")
    with io.open(out, "w", encoding="utf-8", newline="\n") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False) + "\n")
        handle.write("COMPLETE\n")
    print("записано %d строк в %s" % (len(rows), out))


if __name__ == "__main__":
    main()
