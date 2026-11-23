"""Проверки версий инструментов (задача 1 старого плана)."""

import json
import os
import re
import unittest

from scripts.outcome import read_measurements

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "tools.jsonl")
BASE_JSON = os.path.join(HERE, "images", "base.json")
DOCKERFILE = os.path.join(HERE, "images", "Dockerfile.base")


class Tools(unittest.TestCase):
    def test_every_tool_version_is_measured_not_declared(self):
        rows = read_measurements(FIXTURE)
        self.assertEqual({row["tool"] for row in rows},
                         {"gcc", "clang", "valgrind", "gtest", "benchmark", "boost", "cmake"})
        for row in rows:
            self.assertTrue(row["measured"].strip(), "%s: версия не снята" % row["tool"])

    def test_measured_version_is_in_the_tools_own_output(self):
        # raw — сырой вывод инструмента. Версия, которой в нём нет, взята не
        # у инструмента, а из объявления пакета.
        for row in read_measurements(FIXTURE):
            self.assertIn(row["measured"], row["raw"], row["tool"])

    def test_digest_in_dockerfile_matches_base_json(self):
        # Первая строка FROM, а не первая строка файла: над ней комментарий,
        # объясняющий закрепление (отступление от плана 30.09 — там FROM
        # стоял первой строкой).
        with open(BASE_JSON, encoding="utf-8") as f:
            pinned = json.load(f)["image"]
        with open(DOCKERFILE, encoding="utf-8") as f:
            first_from = re.search(r"^FROM .*$", f.read(), re.M).group(0).strip()
        self.assertEqual(first_from, "FROM " + pinned)
        self.assertIn("@sha256:", pinned)


if __name__ == "__main__":
    unittest.main()
