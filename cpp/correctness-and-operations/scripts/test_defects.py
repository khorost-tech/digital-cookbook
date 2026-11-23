"""Проверки оси 1 (задача 2 плана 30.09)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location(
    "analyze_defects", os.path.join(HERE, "scripts", "analyze-defects.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Defects(unittest.TestCase):
    def test_varying_cells_are_marked(self):
        # Клетка, чей исход различался между сессиями (fixtures/manifests),
        # обязана быть помечена nondeterministic. Сессии до строгого теста
        # гонки (EXPECT_LE) сравниваются только между собой: проверка была другой.
        import glob
        import json
        seen = {}
        for path in sorted(glob.glob(os.path.join(HERE, "fixtures", "manifests", "*.json"))):
            if os.path.basename(path) < "20261005T19":
                continue
            with open(path, encoding="utf-8") as f:
                for key, v in json.load(f)["published"]["defects"].items():
                    outcome = v[0] if isinstance(v, list) else v["outcome~"]
                    seen.setdefault(key, set()).add(outcome)
        marked = {"%s/%s-%s" % (r["program"], r["detector"], r["opt"])
                  for r in analyze.read() if r.get("nondeterministic")}
        for key, outcomes in seen.items():
            if len(outcomes) > 1:
                self.assertIn(key, marked, "%s: исход различался между сессиями %s" % (key, outcomes))

    def test_clean_program_trips_no_detector(self):
        rows = analyze.read()
        for row in analyze.rows_for(rows, program="clean"):
            self.assertNotEqual(row["outcome"], "caught",
                                "%s поймал чистую программу — шумит" % row["detector"])

    def test_every_defect_is_present_in_its_O0_build(self):
        rows = analyze.read()
        for row in analyze.rows_for(rows, opt="O0"):
            if row["program"] != "clean":
                self.assertTrue(row["defect_present"], row["program"])

    def test_detector_that_cannot_run_is_error(self):
        row = analyze.classify(returncode=None, stderr="", ran=False)
        self.assertEqual(row["outcome"], "error")

    def test_optimized_away_defect_is_not_a_miss(self):
        rows = [{"program": "leak", "detector": "asan", "opt": "O2",
                 "outcome": "missed", "detail": "x", "defect_present": False,
                 "kind": "defect", "variant": "leak", "probe": "asan-O2"}]
        self.assertEqual(analyze.table_misses(rows), [])

    def test_table_needs_both_controls(self):
        without_clean = [row for row in analyze.read() if row["program"] != "clean"]
        with self.assertRaises(SystemExit):
            analyze.check_controls(without_clean)

    def test_failed_gtest_is_not_a_sanitizer_report(self):
        # Код 1 одинаков у упавшего теста и у ASan: исход решает текст.
        out = "[  FAILED  ] Defect.leak\n"
        self.assertEqual(analyze.classify(1, out, True, detector="asan")["outcome"], "missed")
        self.assertEqual(analyze.classify(1, out, True, detector="gtest")["outcome"], "caught")


if __name__ == "__main__":
    unittest.main()


class DetectScript(unittest.TestCase):
    """Сборки детекторов читаются из detect.sh — без докера, по тексту."""

    def setUp(self):
        with open(os.path.join(HERE, "scripts", "detect.sh"), encoding="utf-8") as f:
            self.text = f.read()

    def line(self, detector):
        import re
        m = re.search(r"^\s*%s\)\s.*$" % detector, self.text, re.M)
        self.assertIsNotNone(m, detector)
        return m.group(0)

    def test_sanitizer_builds_have_their_flag(self):
        for det, flag in (("asan", "-fsanitize=address"), ("ubsan", "-fsanitize=undefined"),
                          ("tsan", "-fsanitize=thread")):
            self.assertIn(flag, self.line(det), det)

    def test_libfuzzer_has_nonzero_time_budget(self):
        import re
        m = re.search(r"-max_total_time=(\d+)", self.text)
        self.assertIsNotNone(m)
        self.assertGreater(int(m.group(1)), 0)
