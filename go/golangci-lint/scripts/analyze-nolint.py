"""Ось 5: разбор. Контроль первым.

Контроль: без директив каждый размеченный дефект найден. Иначе «подавлено»
нельзя отличить от «не нашлось». Без nolintlint ни одна директива не отмечена —
отметки strict принадлежат ему, а не линтерам находок.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import NOLINT  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "nolint.jsonl")


def read(path=FIXTURE):
    return NOLINT.read_fixture(path)


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["config"] == "control" and (r["suppressed"] or r["shown"] != r["want"]):
            raise SystemExit("контроль %s: без директив найдено не всё" % r["variant"])
        if r["config"] in ("control", "plain") and r["nolintlint"]:
            raise SystemExit("%s: отметки nolintlint без nolintlint" % r["variant"])
    cases = {r["case"] for r in rows}
    for c in cases:
        plain = next(r for r in rows if r["variant"] == "plain/" + c)
        strict = next(r for r in rows if r["variant"] == "strict/" + c)
        if plain["suppressed"] != strict["suppressed"]:
            raise SystemExit("%s: nolintlint изменил, что подавлено" % c)


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-22s %s" % (r["variant"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
