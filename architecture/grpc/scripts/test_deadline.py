"""Проверки оси 1 (задача 2)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_deadline", os.path.join(HERE, "scripts", "analyze-deadline.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Deadline(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()

    def test_controls(self):
        analyze.check(self.rows)

    def test_all_eight_combinations(self):
        combos = {(r["client"], r["a"], r["b"]) for r in self.rows}
        self.assertEqual(len(combos), 8)

    def test_fresh_context_wastes_work(self):
        # Отрицательный контроль: без передачи контекста исполнитель обязан
        # доделать работу — иначе «бросил» у передачи ничего не доказывает.
        for r in self.rows:
            if r["mode"] == "fresh" and r["deadline_ms"] < 1000:
                self.assertEqual(r["outcome"], "wasted", r["variant"])

    def test_stopped_without_stopping_is_refused(self):
        row = next(r for r in self.rows if r["outcome"] == "stopped")
        with self.assertRaises(SystemExit):
            analyze.check([dict(row, b_steps_done=10)])


if __name__ == "__main__":
    unittest.main()
