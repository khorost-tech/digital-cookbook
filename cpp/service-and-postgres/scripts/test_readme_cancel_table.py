# -*- coding: utf-8 -*-
"""Таблица обрыва клиента в README сверяется с фикстурой.

ЧЕТВЁРТЫЙ ЯРУС ПРОВЕРОК — ТЕКСТ. Завёлся он не из любви к симметрии:
перезамер уже сдвигал число в README, а README продолжал утверждать
прежнее, и расхождение прошло молча — поле не читала ни одна проверка.
Читатель статьи видит именно текст.
"""

import io
import json
import os
import re
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "cancel.jsonl")
README = os.path.join(HERE, "README.md")

VARIANTS = ("beast", "crow", "drogon")


def _rows():
    out = {}
    with io.open(FIXTURE, encoding="utf-8") as handle:
        for line in handle:
            line = line.strip()
            if line.startswith("{"):
                row = json.loads(line)
                out[(row["variant"], row["arm"])] = row
    return out


def _table():
    """Строки таблицы обрыва: вариант → четыре числа."""
    with io.open(README, encoding="utf-8") as handle:
        text = handle.read()
    head = text.index("## Переживает ли запрос уход клиента")
    tail = text.index("\n## ", head + 10)
    out = {}
    for line in text[head:tail].splitlines():
        match = re.match(r"^\|\s*(\w+)\s*\|(.+)\|\s*$", line)
        if not match or match.group(1) not in VARIANTS:
            continue
        cells = [cell.strip() for cell in match.group(2).split("|")]
        out[match.group(1)] = [int(cell) for cell in cells]
    return out


class ReadmeMatchesTheCancelFixture(unittest.TestCase):

    def setUp(self):
        self.rows = _rows()
        self.table = _table()

    def test_every_variant_is_in_the_table(self):
        self.assertEqual(sorted(self.table), sorted(VARIANTS),
                         "в таблице обрыва не все варианты — или таблица не "
                         "нашлась вовсе")

    def test_numbers_match(self):
        for variant in VARIANTS:
            awaited = self.rows[(variant, "awaited")]
            abandoned = self.rows[(variant, "abandoned")]
            expected = [awaited["active_after_reply"],
                        abandoned["active_before_close"],
                        abandoned["active_after_close"],
                        abandoned["active_after_window"]]
            self.assertEqual(
                self.table[variant], expected,
                "таблица обрыва в README разошлась с фикстурой у %s: "
                "текст утверждает %r, замер даёт %r"
                % (variant, self.table[variant], expected))
