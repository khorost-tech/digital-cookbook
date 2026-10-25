"""Оси 3 и 8: разбор. Контроль первым.

Контроль: вызов без ошибки — OK напрямую и HTTP 200 через шлюз, сервер жив.
«Сервер упал» засчитывается только при согласии двух признаков: контейнер не
работает и health-вызов не прошёл. Без перехватчика паника обязана уронить
сервер Go — иначе перехватчику нечего показать.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import ERRORS  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "errors.jsonl")


def read(path=FIXTURE):
    return ERRORS.read_fixture(path)


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["kind"] == "errors":
            if r["health_after"] != r["container_running"]:
                raise SystemExit("%s: признаки жизни сервера расходятся" % r["variant"])
            if r["mode"] == "panic" and r["server"] == "go" and r["container_running"]:
                raise SystemExit("%s: паника без перехватчика не уронила сервер Go" % r["variant"])
            if r["mode"] == "panic-recover" and not r["container_running"]:
                raise SystemExit("%s: перехватчик не спас сервер" % r["variant"])


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-28s %s" % (r["variant"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
