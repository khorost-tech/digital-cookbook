"""Проверки тренажёра."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_trainer", os.path.join(HERE, "scripts", "analyze-trainer.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Trainer(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()
        self.by = {r["variant"]: r for r in self.rows}

    def test_controls(self):
        analyze.check(self.rows)

    def test_levels_cover_their_topics(self):
        self.assertEqual(self.by["l1/start"]["problems"], ["bodyclose", "errcheck", "ineffassign"])
        self.assertIn("gosec:G101", self.by["l2/start"]["problems"])
        self.assertIn("l3:no-linter", self.by["l3/start"]["problems"])
        self.assertEqual(self.by["l4/start"]["problems"], ["l4:test"])

    def test_green_start_is_refused(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, outcome="green") if r["variant"] == "l2/start" else r for r in self.rows])


if __name__ == "__main__":
    unittest.main()
