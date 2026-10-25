"""Ось 5: разбор. Контроль первым.

Контроль: без политики — ровно одна попытка. Повторы делает gRPC, а не проба:
у успешного вызова номер попытки из grpc-previous-rpc-attempts совпадает со
счётом попыток на сервере.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import RETRY  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "retry.jsonl")


def read(path=FIXTURE):
    return RETRY.read_fixture(path)


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["name"] in ("no-policy", "stream-no-policy") and r["server_attempts"] != 1:
            raise SystemExit("контроль %s: без политики попыток %d" % (r["variant"], r["server_attempts"]))
        if r["client_code"] == "OK" and r["success_attempt"] != r["server_attempts"]:
            raise SystemExit("%s: номер попытки %d не совпал со счётом сервера %d — повторял не gRPC"
                             % (r["variant"], r["success_attempt"], r["server_attempts"]))


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-26s %s" % (r["variant"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
