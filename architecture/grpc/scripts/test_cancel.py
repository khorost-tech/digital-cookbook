"""Проверки оси 2 (задача 3)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_cancel", os.path.join(HERE, "scripts", "analyze-cancel.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Cancel(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()

    def test_controls(self):
        analyze.check(self.rows)

    def test_four_pairs_three_modes(self):
        # три режима во всех парах и ещё три способа отправки у сервера Java
        self.assertEqual(len({(r["client"], r["server"], r["mode"]) for r in self.rows}), 18)

    def test_java_delivery_of_cancel(self):
        # Цикл внутри обработчика: onNext молчит. Цикл после возврата обработчика:
        # onNext бросает CANCELLED, пока нет обработчика отмены; обработчик
        # отмены вызывается до конца цикла, только если обработчик вызова вернулся.
        for r in self.rows:
            if r["server"] != "java" or r["mode"] in ("check", "control"):
                continue
            errors = r["stream_send_errors"] > 0
            self.assertEqual(errors, r["mode"] == "async", r["variant"])
            if r["mode"].endswith("-handler"):
                self.assertEqual(r["cancel_handler_calls"], 1, r["variant"])
                self.assertEqual(r["cancel_handler_before_end"] > 0, r["mode"] == "async-handler", r["variant"])

    def test_handler_mode_without_handler_call_is_refused(self):
        row = next(r for r in self.rows if r["mode"] == "sync-handler")
        with self.assertRaises(SystemExit):
            analyze.check([dict(row, cancel_handler_calls=0)])

    def test_check_stops(self):
        for r in self.rows:
            if r["mode"] == "check":
                self.assertEqual(r["outcome"], "stopped", r["variant"])

    def test_no_check_that_stops_is_refused(self):
        row = next(r for r in self.rows if r["mode"] == "no-check")
        with self.assertRaises(SystemExit):
            analyze.check([dict(row, outcome="stopped")])


if __name__ == "__main__":
    unittest.main()
