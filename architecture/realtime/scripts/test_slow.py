"""Проверки оси 5 (задача 6)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_slow", os.path.join(HERE, "scripts", "analyze-slow.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Slow(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()

    def test_controls(self):
        analyze.check(self.rows)

    def test_backlog_exceeds_socket_buffers(self):
        for r in self.rows:
            self.assertGreaterEqual(r["n"] * r["payload"], 2 * analyze.SOCKET_BUFFERS, r["variant"])

    def test_small_backlog_is_refused(self):
        row = dict(self.rows[0], n=10)
        with self.assertRaises(SystemExit):
            analyze.check([row])

    def test_fast_client_never_loses(self):
        for r in self.rows:
            self.assertTrue(r["fast_all"] and r["fast_missing"] == 0, r["variant"])


if __name__ == "__main__":
    unittest.main()
