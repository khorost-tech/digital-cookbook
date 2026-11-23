"""Проверки оси 4 (задача 5 плана 30.09)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location(
    "analyze_config", os.path.join(HERE, "scripts", "analyze-config.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Config(unittest.TestCase):
    def test_valid_value_starts_for_both_readers(self):
        for reader in ("naive", "strict"):
            self.assertEqual(analyze.outcome(analyze.read(), reader, "valid"), "served")

    def test_whitespace_and_plus_are_in_the_inputs(self):
        names = {row["input_name"] for row in analyze.read()}
        self.assertTrue({"leading-space", "plus-sign"} <= names)

    def test_strict_refusal_names_a_reason(self):
        for row in analyze.rows_for(analyze.read(), reader="strict"):
            if row["outcome"] == "refused":
                self.assertTrue(row["detail"].strip())
                self.assertIn("STAND_PORT", row["detail"])

    def test_silent_start_is_not_served(self):
        # started-silently засчитанный как served спрятал бы всю находку оси.
        for row in analyze.read():
            if row["input_name"] != "valid":
                self.assertNotEqual(row["outcome"], "served", row["probe"])


if __name__ == "__main__":
    unittest.main()


class StrictReaderSource(unittest.TestCase):
    """Строгий разбор читается по тексту config.cpp — без докера."""

    def setUp(self):
        with open(os.path.join(HERE, "service", "config.cpp"), encoding="utf-8") as f:
            self.text = f.read()

    def test_whole_string_must_be_consumed(self):
        # Без проверки end строгий разбор принял бы «80x» как 80 — ровно то,
        # что делает atoi.
        self.assertIn("end != text.data() + text.size()", self.text)

    def test_range_is_checked(self):
        self.assertIn("value < lo || value > hi", self.text)
