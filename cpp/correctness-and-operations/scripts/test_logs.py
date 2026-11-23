"""Проверки оси 6 (задача 5.2, изменения 05.10)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location(
    "analyze_logs", os.path.join(HERE, "scripts", "analyze-logs.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Logs(unittest.TestCase):
    def test_control_plain_message_parses_for_both(self):
        analyze.check_controls(analyze.read())

    def test_parser_does_not_accept_anything(self):
        # Разбор, принимающий любую строку, спрятал бы всю ось.
        self.assertEqual(analyze.judge(b'{"msg":"a"b"}\n', "quote")["parsed"], 0)
        self.assertEqual(analyze.judge(b'not json\n', "plain")["parsed"], 0)

    def test_two_lines_are_not_a_round_trip(self):
        out = '{"msg":"строка один\nстрока два"}\n'.encode()
        self.assertFalse(analyze.judge(out, "newline")["round_trip"])

    def test_invalid_utf8_is_named(self):
        r = analyze.judge(b'{"msg":"\xff"}\n', "byte-ff")
        self.assertFalse(r["valid_utf8"])

    def test_every_logger_and_message_present(self):
        rows = analyze.read()
        for logger in ("pattern", "escaped"):
            for message in analyze.MESSAGES:
                analyze.cell(rows, logger, message)


if __name__ == "__main__":
    unittest.main()
