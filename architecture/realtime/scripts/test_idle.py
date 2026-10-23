"""Проверки оси 6 (задача 7)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_idle", os.path.join(HERE, "scripts", "analyze-idle.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Idle(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()

    def test_controls(self):
        analyze.check(self.rows)

    def test_noisy_base_is_refused(self):
        row = dict(self.rows[0], base_drift_kb=10 ** 7)
        with self.assertRaises(SystemExit):
            analyze.check([row])

    def test_jvm_heap_is_fixed(self):
        for r in self.rows:
            if r["server"] == "java":
                self.assertEqual(r["jvm_heap"], "-Xms256m -Xmx256m")


if __name__ == "__main__":
    unittest.main()
