#!/usr/bin/env python3
"""report.py — матрица «инструмент × СУБД» из fixtures/results.tsv.

    python3 scripts/report.py   → fixtures/matrix.md

Ячейка — исходы шагов apply1 / journal / apply2 / introspect / rollback.
Отказы сопровождаются первой строкой ошибки из вывода инструмента.
"""
import csv
import os

here = os.path.join(os.path.dirname(__file__), "..", "fixtures")
rows = list(csv.DictReader(open(os.path.join(here, "results.tsv")), delimiter="\t"))
tools = ["goose", "flyway", "liquibase", "atlas", "alembic"]
engines = ["pg", "crdb", "pico"]
names = {"pg": "PostgreSQL", "crdb": "CockroachDB", "pico": "Picodata"}
steps = ["apply1", "journal", "apply2", "introspect", "rollback"]

cell = {}
errors = []
for r in rows:
    cell[(r["tool"], r["engine"], r["step"])] = r["verdict"]
    if r["verdict"] not in ("ok", "n/a") and r["step"] != "journal" and r["message"]:
        errors.append((r["tool"], r["engine"], r["step"], r["verdict"], r["message"]))

out = ["# Матрица: инструмент × СУБД", "",
       "Шаги в ячейке: apply1 / journal / apply2 / introspect / rollback.", "",
       "| Инструмент | " + " | ".join(names[e] for e in engines) + " |",
       "|---|" + "---|" * len(engines)]
for t in tools:
    cells = [" / ".join(cell.get((t, e, s), "—") for s in steps) for e in engines]
    out.append(f"| {t} | " + " | ".join(cells) + " |")
out += ["", "## Отказы — первая строка ошибки", "",
        "| Инструмент | СУБД | Шаг | Исход | Сообщение |", "|---|---|---|---|---|"]
for t, e, s, v, m in errors:
    out.append(f"| {t} | {names[e]} | {s} | {v} | `{m}` |")
open(os.path.join(here, "matrix.md"), "w").write("\n".join(out) + "\n")
print("\n".join(out))
