"""Проверки оси 2 (задача 3)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_proxy", os.path.join(HERE, "scripts", "analyze-proxy.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Proxy(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()

    def test_controls(self):
        analyze.check(self.rows)

    def test_sse_window_closes_before_stream_end(self):
        # Буфер прокси сливается при закрытии потока: счёт после закрытия
        # засчитал бы задержанные события как доставленные.
        for r in self.rows:
            if r["kind"] == "sse-window":
                self.assertTrue(r["stream_open_at_count"], r["variant"])

    def test_every_case_on_both_servers(self):
        cases = lambda s: {r["variant"].split("/", 1)[1] for r in self.rows if r["server"] == s}
        self.assertEqual(cases("go"), cases("java"))

    def test_heartbeat_is_the_only_difference_in_idle(self):
        for r in self.rows:
            if r["kind"] == "idle" and r["path"] == "short-idle-hb":
                self.assertGreater(r["heartbeat_ms"], 0)
            if r["kind"] == "idle" and r["path"] in ("short-idle", "direct"):
                self.assertEqual(r["heartbeat_ms"], 0)

    def test_broken_control_is_refused(self):
        row = next(r for r in self.rows if r["path"] == "direct" and r["kind"] == "idle")
        with self.assertRaises(SystemExit):
            analyze.check([dict(row, outcome="closed", closed_during_idle=True)])


if __name__ == "__main__":
    unittest.main()
