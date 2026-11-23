"""Ось 1: засеянные дефекты против детекторов — классификация и контроли.

Исход строки решает `classify` по выводу детектора, а не по коду возврата:
код 1 одинаков у упавшего теста, у ASan и у UBSan. `error` — детектор не
запустился (нет маркера «--- запуск ---» — сборка не дошла до запуска).

Контроли (без них таблица не печатается — SystemExit):
  - отрицательный: на чистой программе не срабатывает ни один детектор;
  - положительный: каждый дефект присутствует в своей сборке -O0.
Строка с defect_present == False в таблицу как `missed` не входит:
пропустить то, чего нет в бинарнике, — не свойство детектора.
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import DEFECTS  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "defects.jsonl")
RAN = "--- запуск ---"

CAUGHT_SIGNS = {
    "asan": ("ERROR: AddressSanitizer", "ERROR: LeakSanitizer"),
    "ubsan": ("runtime error:",),
    "tsan": ("WARNING: ThreadSanitizer",),
    "libfuzzer": ("SUMMARY: AddressSanitizer", "SUMMARY: libFuzzer"),
}


def classify(returncode, stderr, ran, detector="plain", stdout=""):
    """Исход одной строки. Возвращает {'outcome', 'detail'}."""
    text = (stdout or "") + (stderr or "")
    if not ran:
        return {"outcome": "error", "detail": "детектор не запустился (код %s)" % returncode}
    if detector in CAUGHT_SIGNS:
        for sign in CAUGHT_SIGNS[detector]:
            if sign in text:
                line = next(l for l in text.splitlines() if sign in l)
                return {"outcome": "caught", "detail": line.strip()[:200]}
        return {"outcome": "missed", "detail": "отчёта нет (код %s)" % returncode}
    if detector == "valgrind":
        if returncode == 99:
            first = next((l for l in text.splitlines() if l.startswith("==") and
                          ("Invalid" in l or "uninitialised" in l or "lost" in l)), "")
            return {"outcome": "caught", "detail": first.strip()[:200] or "код 99"}
        return {"outcome": "missed", "detail": "ошибок нет (код %s)" % returncode}
    if detector == "gtest":
        if "[  FAILED  ]" in text:
            return {"outcome": "caught", "detail": "тест упал"}
        if "[  PASSED  ] 1 test" in text:
            return {"outcome": "missed", "detail": "тест прошёл"}
        return {"outcome": "error", "detail": "нет итога GoogleTest (код %s)" % returncode}
    # plain: «сообщил об ошибке» — аварийное завершение или ненулевой код.
    if returncode != 0:
        return {"outcome": "caught", "detail": "код %s" % returncode}
    return {"outcome": "missed", "detail": "код 0"}


def read(path=FIXTURE):
    return DEFECTS.read_fixture(path)


def rows_for(rows, **match):
    return [r for r in rows if all(r.get(k) == v for k, v in match.items())]


def table_misses(rows):
    """Пропуски, которые идут в таблицу: только там, где дефект в бинарнике есть."""
    return [r for r in rows if r["outcome"] == "missed" and r.get("defect_present") is True]


def check_controls(rows):
    clean = rows_for(rows, program="clean")
    if not clean:
        raise SystemExit("нет строк чистой программы — отрицательного контроля нет")
    noisy = [r for r in clean if r["outcome"] == "caught"]
    if noisy:
        raise SystemExit("детектор сработал на чистой программе: %s"
                         % ", ".join("%s-%s" % (r["detector"], r["opt"]) for r in noisy))
    absent_O0 = [r for r in rows_for(rows, opt="O0")
                 if r["program"] != "clean" and r.get("defect_present") is not True]
    if absent_O0:
        raise SystemExit("дефект отсутствует в сборке -O0: %s"
                         % ", ".join("%s/%s" % (r["program"], r["detector"]) for r in absent_O0))


def matrix(rows):
    """{программа: {детектор-уровень: исход или 'absent' при defect_present=False}}."""
    out = {}
    for r in rows:
        cell = r["outcome"]
        if r.get("defect_present") is False:
            cell = "нет в бинарнике"
        out.setdefault(r["program"], {})["%s-%s" % (r["detector"], r["opt"])] = cell
    return out


def main():
    rows = read()
    check_controls(rows)
    m = matrix(rows)
    cols = sorted({c for v in m.values() for c in v})
    print(json.dumps({"rows": len(rows), "errors": len(rows_for(rows, outcome="error")),
                      "table_misses": len(table_misses(rows))}, ensure_ascii=False))
    print("%-16s" % "" + " ".join("%-13s" % c for c in cols))
    for prog in sorted(m):
        print("%-16s" % prog + " ".join("%-13s" % m[prog].get(c, "—") for c in cols))
    return 0


if __name__ == "__main__":
    sys.exit(main())
