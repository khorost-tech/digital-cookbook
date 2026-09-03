"""Первый ответ сервера: метка о понижении и то, что с него читается.

Одна запись — две величины, снятые с ОДНИХ И ТЕХ ЖЕ байт.

Первая — метка о понижении версии.

Вторая — виден ли снаружи сертификат сервера. В старой версии протокола
он едет открытым текстом и читается любым, кто смотрит на провод; в новой
уезжает внутрь зашифрованной записи, и снаружи видна только её длина.
Утверждать это по описанию протокола можно, но проверить дешевле:
посредник уже сохраняет нужные байты.

Сервер, который УМЕЕТ новую версию, но договорился на старую, обязан
положить восемь опознавательных байт в конец случайного поля своего
приветствия. Клиент, умеющий новую версию, увидит их и поймёт, что
понижение могло быть навязано посредником, а не выбрано честно.

ПАРА С КОНТРОЛЕМ, И РАЗЛИЧИЕ РОВНО ОДНО.

  сервер умеет 1.3, клиент предлагает 1.3  -> метки быть не должно;
  сервер умеет 1.3, клиент предлагает 1.2  -> метка обязана быть.

Сервер В ОБОИХ случаях один и тот же и остаётся способным на 1.3.
Понижать потолок сервера было бы ошибкой: сервер, не умеющий новой
версии, метку и не кладёт — и мы намерили бы её отсутствие, объявив
это отсутствием защиты.
"""

import io
import json
import os
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import clients
from scripts.wire_parse import (parse_records, parse_server_hello,
                                plaintext_certificate, version_name)

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

SERVER_PORT = 18840
RECORDER_PORT = 18850

# (метка случая, потолок версии у клиента, ожидание — записано ДО прогона)
PROBES = [
    ("negotiated_13", "", False),
    ("negotiated_12", "1.2", True),
]


def recorder_binary():
    for name in ("wire/recorder.exe", "wire/recorder"):
        path = os.path.join(HERE, name)
        if os.path.exists(path):
            return path
    raise RuntimeError("посредник не собран")


def record(case, max_version, recorder_port):
    dump = os.path.join(HERE, "fixtures/.server-%s.hex" % case)
    proc = subprocess.Popen(
        [recorder_binary(), "--listen=0.0.0.0:%d" % recorder_port,
         "--upstream=127.0.0.1:%d" % SERVER_PORT,
         "--out=" + os.path.join(HERE, "fixtures/.hello-%s.hex" % case),
         "--out-server=" + dump],
        cwd=HERE, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
        text=True, encoding="utf-8", errors="replace")
    deadline = time.time() + 15
    while time.time() < deadline:
        line = proc.stderr.readline()
        if not line or line.startswith("READY"):
            break
    try:
        clients.run_client("openssl", case, recorder_port, kind="downgrade",
                           max_version=max_version)
    finally:
        proc.kill()
        proc.wait()
        if proc.stderr is not None:
            proc.stderr.close()
    with io.open(dump, encoding="ascii") as handle:
        return bytes.fromhex(handle.read().strip())


def main():
    rows = []
    port = RECORDER_PORT
    # Сервер один и тот же для обеих проб: он всегда способен на 1.3.
    with clients.Server("valid", SERVER_PORT):
        for case, max_version, want_mark in PROBES:
            data = record(case, max_version, port)
            port += 1
            parsed = parse_server_hello(data)
            records = parse_records(data)
            cert_bytes = plaintext_certificate(records)
            row = {
                "kind": "downgrade",
                "case": case,
                "client": "openssl",
                "client_max_version": max_version or "без ограничения",
                # Два поля, а не одно: в приветствии сервера версия
                # ВСЕГДА говорит 1.2, настоящая лежит в расширении.
                # С одним полем таблица показывала бы 1.2 в обеих
                # строках, и вывод «оба договорились на старую» был бы
                # неверным.
                "server_hello_version": version_name(parsed["hello_version"]),
                "negotiated_version": version_name(parsed["negotiated_version"]),
                "random_at": parsed["random_at"],
                "random": parsed["random"],
                "downgrade_tail": parsed["downgrade_tail"],
                "downgrade_mark": parsed["downgrade_to_12"],
                "expected_mark": want_mark,
                "hex": data.hex(),
                # Сертификат, читаемый прямо с провода. Ноль означает не
                # «сертификата нет», а «снаружи его не прочитать».
                "plaintext_certificate_bytes": cert_bytes,
                "records": [
                    {"type_name": r["type_name"], "length": r["length"],
                     "handshake": [m["name"] for m in r["handshake"]]}
                    for r in records
                ],
            }
            rows.append(row)
            mark = "<- РАСХОЖДЕНИЕ" if row["downgrade_mark"] != want_mark else ""
            sys.stderr.write(
                "%-15s клиент до %-16s метка: %-5s (ждали %s) хвост %s %s\n"
                % (case, row["client_max_version"], row["downgrade_mark"],
                   want_mark, parsed["downgrade_tail"], mark))

    out = os.path.join(HERE, "fixtures/downgrade.jsonl")
    with io.open(out, "w", encoding="utf-8", newline="\n") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False) + "\n")
        handle.write("COMPLETE\n")
    print("записано %d строк в %s" % (len(rows), out))


if __name__ == "__main__":
    main()
