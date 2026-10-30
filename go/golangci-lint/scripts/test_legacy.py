"""Проверки оси 6."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_legacy", os.path.join(HERE, "scripts", "analyze-legacy.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)

NEW, TOUCHED, UNTOUCHED_SAME_FILE, OTHER_FILE = "legacy/a.go:14", "legacy/a.go:8", "legacy/a.go:9", "legacy/b.go:7"


class Legacy(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()
        self.by = {r["variant"]: r for r in self.rows}

    def test_controls(self):
        analyze.check(self.rows)

    def test_new_from_rev_adopts_touched_legacy_line(self):
        # правка старой строки делает её старую находку «новой»
        for v in ("new-from-rev", "new-from-merge-base"):
            self.assertEqual(sorted(self.by[v]["shown"]), sorted([TOUCHED, NEW]), v)

    def test_whole_files_shows_changed_file_only(self):
        self.assertEqual(sorted(self.by["whole-files"]["shown"]), sorted([TOUCHED, UNTOUCHED_SAME_FILE, NEW]))
        self.assertNotIn(OTHER_FILE, self.by["whole-files"]["shown"])

    def test_mixed_ranges_are_refused(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, feature="0" * 40) if r["variant"] == "whole-files" else r for r in self.rows])


if __name__ == "__main__":
    unittest.main()
