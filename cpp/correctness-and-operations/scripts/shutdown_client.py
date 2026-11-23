"""Клиент пробы оси 3 — работает В СЕТИ DOCKER, рядом с сервисом.

Не на хосте: проброс портов Docker Desktop идёт через прокси, который сам
принимает соединения и закрывает их, — «принял ли сервис соединение после
сигнала» мерил бы прокси, а не сервис.

Порядок: отправить `sent` запросов /work?ms=N; опрашивать /healthz, пока
inflight не станет равен sent; напечатать READY и inflight; ждать строку на
stdin (хост в это время шлёт SIGTERM или не шлёт — контроль); попробовать
новое соединение; дождаться ответов на /work; напечатать итог одной строкой JSON.

Аргумент bytes > 0 — большой ответ медленному читателю: /work просит набивку
в bytes байт, клиент держит маленький приёмный буфер и читает с паузами.
Отправка ответа длится дольше ожидания, и видно, ждёт ли сервис её конца.

Аргумент halfclose — клиент закрывает отправку (SHUT_WR) сразу после запроса
и читает ответ дальше: HTTP это допускает (RFC 9112 §9.6), и сервер видит EOF
задолго до того, как ответ прочитан.

Ответ дослужен, только если пришёл код 200 и тело длиной ровно Content-Length:
обрезанный посреди отправки ответ — оборван, а не дослужен.
"""

import json
import socket
import sys
import threading
import time

host, sent, ms = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
pad = int(sys.argv[4]) if len(sys.argv) > 4 else 0
halfclose = len(sys.argv) > 5 and sys.argv[5] == "halfclose"
PORT = 8080
SLOW_RCVBUF, SLOW_CHUNK, SLOW_PAUSE_S, SLOW_IDLE_S = 16384, 65536, 0.01, 5


def request(path, timeout, slow=False):
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    if slow:
        s.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, SLOW_RCVBUF)   # до connect
    s.settimeout(timeout)
    s.connect((host, PORT))
    s.sendall(("GET %s HTTP/1.1\r\nHost: x\r\n\r\n" % path).encode())
    if slow and halfclose:
        s.shutdown(socket.SHUT_WR)       # запрос отправлен — отправку закрыть, читать дальше
    parts = []
    try:
        while True:
            try:
                chunk = s.recv(SLOW_CHUNK if slow else 4096)
            except (socket.timeout, ConnectionResetError):
                # Сервис ушёл, не закрыв соединение по-хорошему: остановленный
                # контейнер не всегда шлёт FIN. Полученное до обрыва — сохраняется,
                # и обрезанный ответ виден как обрезанный, а не как пустой.
                break
            if not chunk:
                break
            parts.append(chunk)
            if slow:
                s.settimeout(SLOW_IDLE_S)
                time.sleep(SLOW_PAUSE_S)
    finally:
        s.close()
    return b"".join(parts).decode("utf-8", "replace")


def complete(text):
    head, sep, body = text.partition("\r\n\r\n")
    if not sep or not head.startswith("HTTP/1.1 200"):
        return False
    for line in head.split("\r\n"):
        if line.lower().startswith("content-length:"):
            return len(body.encode("utf-8")) == int(line.split(":", 1)[1])
    return False


results = [None] * sent


def work(i):
    try:
        path = "/work?ms=%d" % ms + ("&bytes=%d" % pad if pad else "")
        text = request(path, timeout=ms / 1000 + 30, slow=pad > 0)
        if complete(text) and '"kind":"work"' in text:
            results[i] = "served"
        else:
            results[i] = "truncated" if text.startswith("HTTP/1.1 200") else "dropped"
    except OSError:
        results[i] = "dropped"


threads = [threading.Thread(target=work, args=(i,)) for i in range(sent)]
for t in threads:
    t.start()

inflight = 0
deadline = time.time() + 5
while time.time() < deadline:
    try:
        body = request("/healthz", timeout=1)
        inflight = json.loads(body.split("\r\n\r\n", 1)[1])["inflight"]
    except (OSError, ValueError, IndexError, KeyError):
        pass
    if inflight == sent:
        break
    time.sleep(0.05)
print("READY %d" % inflight, flush=True)
sys.stdin.readline()                     # хост послал сигнал (или не послал)

time.sleep(0.2)
try:
    text = request("/healthz", timeout=1)
    accepted = text.startswith("HTTP/1.1")
except OSError:
    accepted = False

for t in threads:
    t.join()
print(json.dumps({"sent": sent, "in_flight_at_signal": inflight,
                  "served": results.count("served"),
                  "dropped": results.count("dropped") + results.count("truncated"),
                  "truncated": results.count("truncated"),
                  "accepted_after_signal": accepted}), flush=True)
