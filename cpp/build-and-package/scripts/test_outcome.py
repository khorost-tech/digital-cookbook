"""Контракт строки замера. Единственное место, где он задан.

Три исхода, и третий отделён от второго намеренно:

  resolved — имя разрешилось;
  refused  — система отказала, и сказала почему;
  error    — проба не смогла запуститься.

Последний нужен, чтобы НАША поломка не попадала в таблицу как свойство
линковки. Отказ обязан объяснять себя: без объяснения его нечем отличить
от отказа по другой причине.
"""

import os
import tempfile
import unittest

from scripts.outcome import OUTCOMES, parse_line, read_fixture, read_measurements


def _write_temp(text):
    """Пишет text во временный файл, возвращает его путь."""
    handle = tempfile.NamedTemporaryFile(
        mode="w", suffix=".jsonl", delete=False, encoding="utf-8")
    try:
        handle.write(text)
    finally:
        handle.close()
    return handle.name


class Contract(unittest.TestCase):
    def test_three_outcomes_and_no_more(self):
        self.assertEqual(OUTCOMES, ("resolved", "refused", "error"))

    def test_missing_field_is_rejected(self):
        with self.assertRaises(ValueError):
            parse_line('{"kind":"package","variant":"musl-static",'
                       '"outcome":"resolved","detail":""}')

    def test_unknown_outcome_is_rejected(self):
        with self.assertRaises(ValueError):
            parse_line('{"kind":"package","variant":"a","probe":"resolve",'
                       '"outcome":"ok","detail":""}')

    def test_refusal_must_explain_itself(self):
        with self.assertRaises(ValueError):
            parse_line('{"kind":"package","variant":"a","probe":"resolve",'
                       '"outcome":"refused","detail":"  "}')

    def test_good_line_parses(self):
        row = parse_line('{"kind":"package","variant":"musl-static",'
                         '"probe":"resolve","outcome":"resolved","detail":""}')
        self.assertEqual(row["variant"], "musl-static")


class MeasurementRows(unittest.TestCase):
    """read_measurements: строки без исхода, но не без контракта.

    У времени сборки исхода нет, и требовать его значило бы выдумывать —
    это проверяется первым тестом. Остального контракт лишаться не должен:
    маркер завершения и поля kind/variant обязательны так же, как у
    фикстуры с исходом.
    """

    def setUp(self):
        self._paths = []
        self.addCleanup(self._cleanup)

    def _cleanup(self):
        for path in self._paths:
            os.unlink(path)

    def _path(self, text):
        path = _write_temp(text)
        self._paths.append(path)
        return path

    def test_row_without_outcome_reads_fine(self):
        path = self._path(
            '{"kind":"build","variant":"musl-static","seconds":12.3}\n'
            "COMPLETE\n")
        rows = read_measurements(path)
        self.assertEqual(len(rows), 1)
        self.assertNotIn("outcome", rows[0])
        self.assertEqual(rows[0]["variant"], "musl-static")

    def test_missing_end_marker_is_rejected(self):
        path = self._path(
            '{"kind":"build","variant":"musl-static","seconds":12.3}\n')
        with self.assertRaises(ValueError):
            read_measurements(path)

    def test_row_without_kind_is_rejected(self):
        path = self._path(
            '{"variant":"musl-static","seconds":12.3}\n'
            "COMPLETE\n")
        with self.assertRaises(ValueError):
            read_measurements(path)

    def test_row_without_variant_is_rejected(self):
        path = self._path(
            '{"kind":"build","seconds":12.3}\n'
            "COMPLETE\n")
        with self.assertRaises(ValueError):
            read_measurements(path)

    def test_marker_inside_a_value_does_not_count(self):
        # COMPLETE встретился внутри значения поля, а не как маркер строки
        # завершения — файл всё равно должен считаться оборванным.
        path = self._path(
            '{"kind":"build","variant":"musl-static",'
            '"note":"still not COMPLETE yet"}\n')
        with self.assertRaises(ValueError):
            read_measurements(path)

    def test_marker_without_data_rows_is_rejected(self):
        path = self._path("COMPLETE\n")
        with self.assertRaises(ValueError):
            read_measurements(path)

    def test_empty_file_is_rejected(self):
        path = self._path("")
        with self.assertRaises(ValueError):
            read_measurements(path)


class FixtureRows(unittest.TestCase):
    """read_fixture: те же две дыры находки 3, но со строками исхода."""

    def setUp(self):
        self._paths = []
        self.addCleanup(self._cleanup)

    def _cleanup(self):
        for path in self._paths:
            os.unlink(path)

    def _path(self, text):
        path = _write_temp(text)
        self._paths.append(path)
        return path

    def test_marker_inside_a_value_does_not_count(self):
        path = self._path(
            '{"kind":"package","variant":"a","probe":"resolve",'
            '"outcome":"refused","detail":"still not COMPLETE yet"}\n')
        with self.assertRaises(ValueError):
            read_fixture(path)

    def test_marker_without_data_rows_is_rejected(self):
        path = self._path("COMPLETE\n")
        with self.assertRaises(ValueError):
            read_fixture(path)

    def test_empty_file_is_rejected(self):
        path = self._path("")
        with self.assertRaises(ValueError):
            read_fixture(path)
