"""Проверки оси 6 (задача 7)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_keepalive", os.path.join(HERE, "scripts", "analyze-keepalive.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Keepalive(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()

    def test_controls(self):
        analyze.check(self.rows)

    def test_default_policy_goaway(self):
        for r in self.rows:
            if r["kind"] == "pings" and r["policy"] == "default":
                self.assertEqual(r["outcome"], "goaway", r["variant"])

    def test_proxy_without_pings_closes(self):
        for r in self.rows:
            if r["kind"] == "idle" and r["path"] != "direct" and r["ka"] == "off":
                self.assertEqual(r["outcome"], "reconnected", r["variant"])

    def test_silent_stream_l7_survives_l4_needs_pings(self):
        rows = [r for r in self.rows if r["kind"] == "silent"]
        self.assertEqual(len(rows), 20)
        for r in rows:
            if r["path"].endswith("-l7") or r["path"] == "direct" or r["ka"] == "on":
                self.assertEqual(r["outcome"], "survived", r["variant"])
            else:
                self.assertEqual(r["outcome"], "dropped", r["variant"])

    def test_survived_on_new_connection_is_refused(self):
        row = next(r for r in self.rows if r["kind"] == "idle" and r["outcome"] == "survived")
        with self.assertRaises(SystemExit):
            analyze.check([dict(row, same_conn=False)])

    def test_survived_without_ready_is_refused(self):
        row = next(r for r in self.rows if r["kind"] == "idle" and r["outcome"] == "survived")
        with self.assertRaises(SystemExit):
            analyze.check([dict(row, state_before_second="IDLE")])


if __name__ == "__main__":
    unittest.main()
