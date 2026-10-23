"""Проверки оси 4 (задача 5)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_reconnect",
                                               os.path.join(HERE, "scripts", "analyze-reconnect.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Reconnect(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()

    def test_controls(self):
        analyze.check(self.rows)

    def test_publish_happened_while_bus_down(self):
        for r in self.rows:
            if r["kind"] == "gap":
                self.assertTrue(r["bus_down_before_publish"] and r["bus_down_after_publish"], r["variant"])

    def test_cut_not_confirmed_is_refused(self):
        row = next(r for r in self.rows if r["kind"] == "gap" and r["outcome"] == "no-loss")
        with self.assertRaises(SystemExit):
            analyze.check([dict(row, bus_down_before_publish=False)])

    def test_barrier_really_held(self):
        rows = [r for r in self.rows if r["kind"] == "resume" and r.get("overlap")]
        self.assertEqual(len(rows), 6)
        for r in rows:
            self.assertTrue(r["barrier_seen"], r["variant"])
            self.assertEqual(r["received_at_publish_end"], r["n_before"], r["variant"])

    def test_barrier_control_can_fail(self):
        # Опыт со стыком, который не способен провалиться, ничего не доказывает:
        # подписка после истории обязана потерять события, опубликованные на барьере.
        for r in self.rows:
            if r["variant"].endswith("-barrier-late"):
                self.assertEqual(r["outcome"], "lost", r["variant"])
                self.assertEqual(r["missing"], r["n_after"], r["variant"])

    def test_cancel_releases_subscriber(self):
        rows = [r for r in self.rows if r["kind"] == "cancel"]
        self.assertEqual({r["server"] for r in rows}, {"go", "java"})
        for r in rows:
            self.assertEqual(r["outcome"], "released", r["variant"])

    def test_history_error_is_refused_not_hidden(self):
        rows = [r for r in self.rows if r["variant"].endswith("history-error")]
        self.assertEqual(len(rows), 2)
        for r in rows:
            self.assertEqual(r["outcome"], "refused", r["variant"])

    def test_journals_catch_up_and_pubsub_loses(self):
        out = {(r["server"], r["bus"]): r["outcome"] for r in self.rows if r["kind"] == "gap"}
        for server in ("go", "java"):
            self.assertEqual(out[(server, "redis-streams")], "no-loss")
            self.assertEqual(out[(server, "jetstream")], "no-loss")


if __name__ == "__main__":
    unittest.main()
