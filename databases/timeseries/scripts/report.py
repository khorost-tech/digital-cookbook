#!/usr/bin/env python3
"""report.py — сводка fixtures/bytes.tsv в fixtures/bytes.md: байт на точку."""
import csv
from collections import defaultdict

rows = list(csv.DictReader(open("fixtures/bytes.tsv"), delimiter="\t"))
systems = ["prometheus", "victoriametrics", "timescaledb", "timescaledb-compressed"]
table = defaultdict(dict)
for r in rows:
    table[r["shape"]][r["system"]] = int(r["bytes"]) / int(r["samples"])
with open("fixtures/bytes.md", "w") as out:
    out.write("Байт на точку (данные + индекс), 200 рядов × сутки × 15 с = 1 152 000 точек.\n\n")
    out.write("| Форма | " + " | ".join(systems) + " |\n|---|" + "---|" * len(systems) + "\n")
    for shape in table:
        out.write(f"| {shape} | " + " | ".join(f"{table[shape].get(s, float('nan')):.2f}" for s in systems) + " |\n")
print(open("fixtures/bytes.md").read())
