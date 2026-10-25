"""Задача 1: один контракт и одно поведение по умолчанию у Go и Java."""

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import PROTOCOL  # noqa: E402

FIXTURE = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "fixtures", "protocol.jsonl")


class Protocol(unittest.TestCase):
    def setUp(self):
        self.rows = PROTOCOL.read_fixture(FIXTURE)

    def test_four_pairs(self):
        self.assertEqual({r["variant"] for r in self.rows}, {"go->go", "java->go", "go->java", "java->java"})

    def test_same_observations(self):
        base = self.rows[0]["observations"]
        for r in self.rows:
            self.assertEqual(r["observations"], base, r["variant"])

    def test_not_empty(self):
        # Совпадение пустых наборов ничего не доказывает.
        for r in self.rows:
            o = r["observations"]
            self.assertEqual(o["server_stream"], [1, 2, 3], r["variant"])
            self.assertEqual(o["unary_details"], ["NotFound", "RECORD_NOT_FOUND"], r["variant"])
            self.assertEqual(o["unary_plain"][0], "Unknown", r["variant"])


if __name__ == "__main__":
    unittest.main()
