"""Проверки оси 3."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_truncate", os.path.join(HERE, "scripts", "analyze-truncate.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Truncate(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()
        self.by = {r["variant"]: r for r in self.rows}

    def test_controls(self):
        analyze.check(self.rows)

    def test_default_limits(self):
        # max-same-issues 3, max-issues-per-linter 50, одна находка на строку
        self.assertEqual((self.by["same/default"]["shown"], self.by["same/default"]["found"]), (3, 5))
        self.assertEqual((self.by["local/default"]["shown"], self.by["local/default"]["found"]), (3, 60))
        self.assertEqual((self.by["many/default"]["shown"], self.by["many/default"]["found"]), (50, 60))
        self.assertEqual((self.by["line/default"]["shown"], self.by["line/default"]["found"]), (1, 2))

    def test_exit_code_still_fails(self):
        for r in self.rows:
            self.assertEqual(r["rc"], 1, r["variant"])

    def test_untruncated_default_is_refused(self):
        row = self.by["same/default"]
        with self.assertRaises(SystemExit):
            analyze.check([dict(row, outcome="all-shown")])


if __name__ == "__main__":
    unittest.main()
