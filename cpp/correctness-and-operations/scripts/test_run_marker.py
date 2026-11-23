"""Проверки маркера полного прогона и сводки."""

import hashlib
import importlib.util
import json
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FX = os.path.join(HERE, "fixtures")


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"),
                                                  os.path.join(HERE, "scripts", name + ".py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


PENDING = os.environ.get("STAND_MARKER_PENDING") == "1"


@unittest.skipIf(PENDING, "маркер ставится прямо сейчас (run-marker.py)")
class Marker(unittest.TestCase):
    def test_marker_hashes_match_fixtures(self):
        # Фикстура, изменённая после маркера, — уже не тот прогон.
        with open(os.path.join(FX, "RUN-COMPLETE"), encoding="utf-8") as f:
            marker = json.load(f)
        for name, digest in marker["fixtures"].items():
            with open(os.path.join(FX, name), "rb") as f:
                self.assertEqual(hashlib.sha256(f.read()).hexdigest(), digest, name)

    def test_marker_tests_count_matches_suite(self):
        # Число проверок в маркере — посчитанное, а не вписанное: равно
        # числу проверок, которые находит загрузчик unittest.
        import unittest as u
        suite = u.defaultTestLoader.discover(os.path.join(HERE, "scripts"), pattern="test_*.py",
                                             top_level_dir=HERE)
        with open(os.path.join(FX, "RUN-COMPLETE"), encoding="utf-8") as f:
            self.assertEqual(json.load(f)["tests"], suite.countTestCases())

    def test_marker_counts_are_positive(self):
        with open(os.path.join(FX, "RUN-COMPLETE"), encoding="utf-8") as f:
            marker = json.load(f)
        self.assertGreater(marker["tests"], 0)
        self.assertGreater(marker["sabotages"], 0)


class RunAll(unittest.TestCase):
    def test_run_all_stops_on_failure_and_marks_last(self):
        with open(os.path.join(HERE, "bench", "run-all.sh"), encoding="utf-8") as f:
            text = f.read()
        self.assertIn("set -euo pipefail", text)
        last = [l for l in text.splitlines() if l.strip() and not l.startswith("#")][-1]
        self.assertTrue(last.startswith("python3 scripts/run-marker.py"), last)


class Manifest(unittest.TestCase):
    def setUp(self):
        self.m = load("run-manifest")

    def test_excluded_field_must_differ(self):
        a = {"config": {"naive/empty": {"outcome": "x", "effective_port@empty": 1}}}
        b = {"config": {"naive/empty": {"outcome": "x", "effective_port@empty": 2}}}
        self.assertEqual(self.m.compare(a, b), ([], True))
        self.assertEqual(self.m.compare(a, a), ([], False))

    def test_published_difference_is_reported(self):
        a = {"fds": {"raw/fail=1": 100}}
        b = {"fds": {"raw/fail=1": 99}}
        diffs, _ = self.m.compare(a, b)
        self.assertEqual(len(diffs), 1)


if __name__ == "__main__":
    unittest.main()
