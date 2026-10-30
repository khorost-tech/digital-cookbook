"""Ось 8: смысл разметки — дефект, на который указывает находка, есть в поведении.

Зелёный линтер доказывает только выполнение правил конфига. Здесь `go test`
пакета semantics/ проверяет само поведение: фиктивный драйвер database/sql с
одним соединением меряет Stats().InUse после полного прохода rows без Close
(контроль), после sample/db.First (ранний выход без Close) и после
fixed/db.First; отдельный тест — что json.Encoder.Encode падает ещё до записи
ответа; контракты Merge, Normalize и IDs из комментариев sample/ — на исходном
коде и исправленной паре (и на наивном исправлении IDs). Каждый тест печатает
«measure <имя> <значение>».

    python3 scripts/run-semantics.py           → все тесты, фикстура
    python3 scripts/run-semantics.py TestSampleFirstHoldsConnection   → один тест
"""

import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, SEMANTICS  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "semantics.jsonl")
TESTS = ("TestDrainedRowsReleaseConnection", "TestSampleFirstHoldsConnection", "TestFixedFirstReleasesConnection",
         "TestEncodeFailsBeforeWrite", "TestMergeAliasing", "TestNormalizeTrims", "TestIDsEmptyContract")


def measure(test):
    rc, out, err = docker.run_in(docker.HERE, "test", "-count=1", "-v", "-run", "^%s$" % test, "./semantics/...",
                                 binary="go")
    text = out + err
    values = {m.group(1): int(m.group(2)) for m in re.finditer(r"measure (\w+) (-?\d+)", text)}
    passed = re.search(r"--- PASS: %s\b" % test, text) is not None
    row = {"kind": "semantics", "variant": test, "probe": "semantics",
           "cmd": "python3 scripts/run-semantics.py %s" % test,
           "go_cmd": "go test -count=1 -v -run '^%s$' ./semantics/..." % test, "passed": passed, **values}
    if not passed or not values:
        row["outcome"], row["detail"] = "error", "тест не прошёл: %s" % text[-300:]
    else:
        row["outcome"] = "confirmed"
        row["detail"] = ", ".join("%s=%d" % kv for kv in values.items())
    SEMANTICS.parse_line(json.dumps(row, ensure_ascii=False))
    return row


SHOW = ("variant", "outcome", "detail")


def main():
    if sys.argv[1:]:
        print(json.dumps({k: measure(sys.argv[1])[k] for k in SHOW}, ensure_ascii=False))
        return
    docker.pull()
    rows = []
    for t in TESTS:
        rows.append(measure(t))
        print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
