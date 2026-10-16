"""Пять плеч обязаны вернуть побайтово одинаковый результат.

Проверка блокирующая: не сошлись — замер не запускается. Причина та же,
что в волне 2, где она поймала три расхождения подряд. Разница здесь в
том, ЧТО именно расходится: не тела HTTP-ответов, а представление типов.
Numeric с разным числом нулей, timestamptz в разных поясах, float вместо
Decimal — всё это даёт правдоподобные числа и несравнимые плечи.

Печатает по строке JSON на плечо и сводку. Код возврата: 0 — сошлись,
2 — нет, 3 — плечо не отработало.
"""

import json
import sys

import arms
import common


def main():
    results = {}
    for cls in arms.ARMS:
        arm = cls()
        row = {"arm": cls.name}
        try:
            for query in common.SQL:
                rows = arms.execute(arm, query)
                row[query] = common.fingerprint(query, rows)
        except Exception as exc:  # noqa: BLE001 — отчёт важнее трассировки
            row["error"] = f"{type(exc).__name__}: {exc}"
            print(json.dumps(row, ensure_ascii=False, sort_keys=True))
            print(f"CHECK FAIL: плечо {cls.name} не отработало", file=sys.stderr)
            return 3
        results[cls.name] = row
        print(json.dumps(row, ensure_ascii=False, sort_keys=True))

    print()
    bad = []
    for query in common.SQL:
        hashes = {name: r[query]["sha256"] for name, r in results.items()}
        counts = {name: r[query]["rows"] for name, r in results.items()}
        distinct = set(hashes.values())
        mark = "ОК" if len(distinct) == 1 else "РАСХОЖДЕНИЕ"
        print(f"  {query:<10} строк {sorted(set(counts.values()))}, "
              f"различных хешей {len(distinct)}  {mark}")
        if len(distinct) != 1:
            bad.append(query)
            for name in sorted(hashes):
                print(f"      {name:<20} {hashes[name]}  строк {counts[name]}")

    print()
    if bad:
        print("CHECK FAIL: плечи вернули разное на " + ", ".join(bad),
              file=sys.stderr)
        return 2

    print("CHECK OK: пять плеч отдают побайтово одинаковый результат")
    print("  версии: " + json.dumps(common.versions(), sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
