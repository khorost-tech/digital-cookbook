"""Ось 8: разбор. Контроль первым.

Контроль: полный проход rows без Close освобождает соединение — иначе «утечка»
в sample/db.First не отличима от свойства фиктивного драйвера.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import SEMANTICS  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "semantics.jsonl")


def read(path=FIXTURE):
    return SEMANTICS.read_fixture(path)


def check(rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error" or not r["passed"]:
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
    if by["TestDrainedRowsReleaseConnection"]["in_use"] != 0:
        raise SystemExit("контроль: полный проход не освободил соединение")
    if by["TestSampleFirstHoldsConnection"]["in_use"] != 1:
        raise SystemExit("sample/db.First не держит соединение — разметка sqlclosecheck не подтверждена")
    if by["TestFixedFirstReleasesConnection"]["in_use"] != 0:
        raise SystemExit("исправленная пара держит соединение")
    m, nz, ids = by["TestMergeAliasing"], by["TestNormalizeTrims"], by["TestIDsEmptyContract"]
    if (m["sample_aliases"], m["fixed_aliases"]) != (1, 0):
        raise SystemExit("контракт Merge не подтверждён")
    if (nz["sample_trims"], nz["fixed_trims"]) != (0, 1):
        raise SystemExit("контракт Normalize не подтверждён")
    if (ids["sample_null"], ids["fixed_null"], ids["naive_null"]) != (1, 1, 0):
        raise SystemExit("исправленная пара IDs меняет результат на пустом входе")
    enc = by["TestEncodeFailsBeforeWrite"]
    if enc["encode_failed"] != 1 or enc["body_bytes"] != 0:
        raise SystemExit("Encode не упал до записи")


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-36s %s" % (r["variant"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
