"""Прогон оси «что видно до шифрования».

Между клиентом и сервером стоит посредник: он сохраняет первые байты,
которые прислал клиент, и передаёт соединение дальше. Сквозная передача
здесь не удобство — без неё клиент получал бы сбой, и осталось бы
непонятно, состоялось ли рукопожатие вообще.

Разбор байтов делается ОТДЕЛЬНО и не знает, какое имя мы передали
клиенту в аргументах. Иначе замер свёлся бы к печати того, что и так
известно.

Каждому клиенту передаётся СВОЁ имя. Если бы имя было общим, совпадение
разобранного с ожидаемым можно было бы получить и по случайности — а так
разбор обязан вернуть для каждого клиента ровно его имя.
"""

import io
import json
import os
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import clients
from scripts.wire_parse import parse_client_hello, name_at_offset, version_name

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# Своё имя каждому: общее имя допускало бы случайное совпадение.
NAMES = {
    "openssl": "openssl.stand.local",
    "curl": "curl.stand.local",
    "go": "go.stand.local",
    "java": "java.stand.local",
}

SERVER_PORT = 18810
RECORDER_PORT = 18820


def recorder_binary():
    for name in ("wire/recorder.exe", "wire/recorder"):
        path = os.path.join(HERE, name)
        if os.path.exists(path):
            return path
    raise RuntimeError("посредник не собран: нет wire/recorder[.exe]")


def record_one(client, server_port, recorder_port, dump_path):
    proc = subprocess.Popen(
        [recorder_binary(),
         # Слушаем на всех адресах: клиенты в контейнерах приходят
         # снаружи, и локальная петля им недоступна.
         "--listen=0.0.0.0:%d" % recorder_port,
         "--upstream=127.0.0.1:%d" % server_port, "--out=" + dump_path],
        cwd=HERE, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
        text=True, encoding="utf-8", errors="replace")
    deadline = time.time() + 15
    while time.time() < deadline:
        line = proc.stderr.readline()
        if not line:
            break
        if line.startswith("READY"):
            break
    else:
        proc.kill()
        raise RuntimeError("посредник не сообщил о готовности")

    try:
        # Имя не совпадает с сертификатом — клиент откажет, и это
        # неважно: мерится то, что он УЖЕ отправил до всякой проверки.
        clients.run_client(client, "wire", recorder_port, kind="wire",
                           servername=NAMES[client])
    finally:
        proc.kill()
        proc.wait()
        if proc.stderr is not None:
            proc.stderr.close()

    with io.open(dump_path, encoding="ascii") as handle:
        return bytes.fromhex(handle.read().strip())


def main():
    os.makedirs(os.path.join(HERE, "fixtures"), exist_ok=True)
    rows = []
    port = RECORDER_PORT
    with clients.Server("valid", SERVER_PORT):
        for client in clients.CLIENTS:
            dump = os.path.join(HERE, "fixtures/.hello-%s.hex" % client)
            data = record_one(client, SERVER_PORT, port, dump)
            port += 1

            parsed = parse_client_hello(data)
            asked = NAMES[client]
            # Имя берётся ИЗ БАЙТОВ по найденному смещению, а не из того,
            # что мы передали клиенту.
            from_bytes = name_at_offset(data, parsed["server_name_at"],
                                        len(parsed["server_name"]))
            rows.append({
                "kind": "wire",
                "client": client,
                "bytes_total": len(data),
                "server_name": parsed["server_name"],
                "server_name_at": parsed["server_name_at"],
                "server_name_from_bytes": from_bytes,
                "asked_name": asked,
                "record_version": version_name(parsed["record_version"]),
                "hello_version": version_name(parsed["hello_version"]),
                "supported_versions": [version_name(v)
                                       for v in parsed["supported_versions"]],
                "alpn": parsed["alpn"],
                "extensions": parsed["extensions"],
                "hex": data.hex(),
            })
            sys.stderr.write(
                "%-8s %4d байт  имя из байтов: %-22s смещение %d  версии: %s\n"
                % (client, len(data), from_bytes, parsed["server_name_at"],
                   ",".join(version_name(v)
                            for v in parsed["supported_versions"]) or "-"))

    out = os.path.join(HERE, "fixtures/wire.jsonl")
    with io.open(out, "w", encoding="utf-8", newline="\n") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False) + "\n")
        handle.write("COMPLETE\n")
    print("записано %d строк в %s" % (len(rows), out))


if __name__ == "__main__":
    main()
