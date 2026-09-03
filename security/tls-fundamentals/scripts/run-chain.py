"""Прогон матрицы цепочки: каждый случай — каждым клиентом.

Порт для каждого случая свой: забытый сервер прошлого случая держал бы
порт, клиент говорил бы со СТАРЫМ сервером и вернул зелёный результат.
Это уже случалось в этом стенде, и ложный успех выглядел ровно как
настоящий контрольный проход.

Фикстура заканчивается маркером COMPLETE. Оборванный прогон иначе
выглядел бы как полная картина — просто с меньшим числом строк.
"""

import io
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import clients
from scripts.outcome import parse_line

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BASE_PORT = 18500


def main():
    cases = json.loads(io.open(os.path.join(HERE, "pki/cases.json"),
                               encoding="utf-8").read())
    out_path = os.path.join(HERE, "fixtures/chain.jsonl")
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    lines = []
    for index, case in enumerate(cases):
        port = BASE_PORT + index
        name = case["case"]
        with clients.Server(name, port):
            for client in clients.CLIENTS:
                line = clients.run_client(client, name, port)
                parse_line(line)          # контракт проверяется сразу
                lines.append(line)
                sys.stderr.write(line + "\n")
    with io.open(out_path, "w", encoding="utf-8", newline="\n") as handle:
        handle.write("\n".join(lines) + "\nCOMPLETE\n")
    print("записано %d строк в %s" % (len(lines), out_path))


if __name__ == "__main__":
    main()
