"""Проверки оси 1 (задача 2)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_wire", os.path.join(HERE, "scripts", "analyze-wire.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Wire(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()

    def test_controls(self):
        analyze.check(self.rows)

    def test_per_event_bytes_by_difference(self):
        # Делением всего на N рукопожатие и заголовки потока попали бы в цену
        # события; разность двух прогонов их сокращает.
        for r in self.rows:
            self.assertEqual(len(r["runs"]), 2, r["variant"])
            self.assertEqual(analyze.per_event(r, "bytes_down"), r["down_per_event"], r["variant"])

    def test_both_servers_every_variant(self):
        names = {r["name"] for r in self.rows}
        for server in ("go", "java"):
            self.assertEqual({r["name"] for r in self.rows if r["server"] == server}, names, server)

    def test_empty_polls_only_when_polling_faster_than_events(self):
        for r in self.rows:
            if r["name"] == "poll-50ms":
                self.assertGreater(r["empty_per_event"], 0, r["variant"])
            if r["name"] == "poll-500ms":
                self.assertLess(r["requests_per_event"], 1, r["variant"])

    def test_broken_byte_count_is_refused(self):
        row = dict(self.rows[0], down_per_event=1.0, compress=False)
        row["runs"] = {"20": dict(self.rows[0]["runs"]["20"], bytes_down=0),
                       "120": dict(self.rows[0]["runs"]["120"], bytes_down=100)}
        with self.assertRaises(SystemExit):
            analyze.check([row])


if __name__ == "__main__":
    unittest.main()
