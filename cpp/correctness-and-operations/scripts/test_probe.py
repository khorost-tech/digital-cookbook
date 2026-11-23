"""Проверки фикстуры проб (задача 1): то, на что опираются ожидания."""

import json
import os
import unittest

from scripts.outcome import PROBE

FIXTURE = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                       "fixtures", "probe.jsonl")


def rows():
    return {(r["variant"], r["probe"]): r for r in PROBE.read_fixture(FIXTURE)}


class ProbeFixture(unittest.TestCase):
    def test_every_row_has_command(self):
        for row in rows().values():
            self.assertTrue(row["cmd"].startswith("docker run --rm"), row["probe"])

    def test_versions_are_measured_not_declared(self):
        versions = json.loads(rows()[("base", "tool_versions")]["detail"])
        for tool in ("g++", "clang++", "valgrind", "perf", "llvm-cov", "gtest", "benchmark"):
            self.assertTrue(versions.get(tool), tool)

    def test_tools_the_axes_rely_on_were_observed(self):
        # Оси 9.1, 9.2, 9.3, 8.1–8.4 стоят на этих пробах. Если какая-то
        # перестала давать находку, ожидания для её оси писать нельзя.
        r = rows()
        for key in [("libfuzzer", "known-crash"), ("asan+gtest", "heap-overflow"),
                    ("ubsan+gtest", "signed-overflow"), ("tsan+gtest", "race"),
                    ("plain+gtest", "all-green"), ("llvm-cov", "mcdc"),
                    ("benchmark", "json"), ("perf", "stacks-O2-fp")]:
            self.assertEqual(r[key]["outcome"], "observed", key)

    def test_hardware_counters_are_absent(self):
        # Опора статьи 8 — callgrind именно потому, что этого нет. Если
        # появится, спека и статья обязаны это учесть, а не молча остаться.
        self.assertEqual(rows()[("perf", "hw-instructions")]["outcome"], "absent")


if __name__ == "__main__":
    unittest.main()
