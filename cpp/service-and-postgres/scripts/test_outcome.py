"""Контракт строки замера. Единственное место, где он задан.

Три исхода, и третий отделён от второго намеренно:

  served  — сервис обслужил запрос;
  refused — сервис отказал, и сказал почему;
  error   — проба не смогла выполниться.

Последний нужен, чтобы НАША поломка не попадала в таблицу как свойство
фреймворка. Отказ обязан объяснять себя: без объяснения его нечем
отличить от отказа по другой причине.
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
    def test_outcomes_are_three_and_error_is_separate(self):
        # error — это «наша проба сломалась», и он обязан быть отделён от
        # refused: иначе поломка замера попадёт в таблицу как свойство
        # фреймворка.
        self.assertEqual(OUTCOMES, ("served", "refused", "error"))

    def test_missing_field_is_rejected(self):
        with self.assertRaises(ValueError):
            parse_line('{"kind":"block","variant":"beast",'
                       '"outcome":"served","detail":""}')

    def test_unknown_outcome_is_rejected(self):
        with self.assertRaises(ValueError):
            parse_line('{"kind":"block","variant":"beast","probe":"healthz",'
                       '"outcome":"ok","detail":""}')

    def test_refusal_must_explain_itself(self):
        with self.assertRaises(ValueError):
            parse_line('{"kind":"block","variant":"beast","probe":"healthz",'
                       '"outcome":"refused","detail":""}')

    def test_blank_detail_is_not_an_explanation(self):
        with self.assertRaises(ValueError):
            parse_line('{"kind":"block","variant":"beast","probe":"healthz",'
                       '"outcome":"refused","detail":"   "}')

    def test_broken_probe_must_explain_itself_too(self):
        # error без объяснения неотличим от error по любой другой причине,
        # а именно по объяснению разбор и решает, чинить стенд или писать
        # строку в таблицу.
        with self.assertRaises(ValueError):
            parse_line('{"kind":"cancel","variant":"crow","probe":"q",'
                       '"outcome":"error","detail":""}')

    def test_empty_variant_is_rejected(self):
        # Весь смысл требования к имени варианта — чтобы два варианта не
        # склеились в одну строку фикстуры. Наличие поля этого не даёт:
        # пустое имя склеивает ровно так же, как отсутствующее, только
        # молча.
        with self.assertRaises(ValueError):
            parse_line('{"kind":"block","variant":"","probe":"healthz",'
                       '"outcome":"served","detail":"200"}')

    def test_blank_variant_is_rejected(self):
        with self.assertRaises(ValueError):
            parse_line('{"kind":"block","variant":"  ","probe":"healthz",'
                       '"outcome":"served","detail":"200"}')

    def test_empty_kind_is_rejected(self):
        with self.assertRaises(ValueError):
            parse_line('{"kind":"","variant":"beast","probe":"healthz",'
                       '"outcome":"served","detail":"200"}')

    def test_empty_probe_is_rejected(self):
        with self.assertRaises(ValueError):
            parse_line('{"kind":"block","variant":"beast","probe":"",'
                       '"outcome":"served","detail":"200"}')

    def test_good_line_parses(self):
        row = parse_line('{"kind":"block","variant":"beast","probe":"healthz",'
                         '"outcome":"served","detail":"200"}')
        self.assertEqual(row["variant"], "beast")


class MeasurementRows(unittest.TestCase):
    """read_measurements: строки без исхода, но не без контракта.

    У числа соединений исхода нет, и требовать его значило бы выдумывать —
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
            '{"kind":"connections","variant":"drogon","backends":8}\n'
            "COMPLETE\n")
        rows = read_measurements(path)
        self.assertEqual(len(rows), 1)
        self.assertNotIn("outcome", rows[0])
        self.assertEqual(rows[0]["variant"], "drogon")

    def test_missing_end_marker_is_rejected(self):
        path = self._path(
            '{"kind":"connections","variant":"drogon","backends":8}\n')
        with self.assertRaises(ValueError):
            read_measurements(path)

    def test_row_without_kind_is_rejected(self):
        path = self._path(
            '{"variant":"drogon","backends":8}\n'
            "COMPLETE\n")
        with self.assertRaises(ValueError):
            read_measurements(path)

    def test_row_without_variant_is_rejected(self):
        path = self._path(
            '{"kind":"connections","backends":8}\n'
            "COMPLETE\n")
        with self.assertRaises(ValueError):
            read_measurements(path)

    def test_row_with_empty_variant_is_rejected(self):
        # Та же склейка, что и у строк с исходом: величина без имени
        # варианта не относится ни к одному из них.
        path = self._path(
            '{"kind":"connections","variant":"","backends":8}\n'
            "COMPLETE\n")
        with self.assertRaises(ValueError):
            read_measurements(path)

    def test_marker_inside_a_value_does_not_count(self):
        # COMPLETE встретился внутри значения поля, а не как маркер строки
        # завершения — файл всё равно должен считаться оборванным.
        path = self._path(
            '{"kind":"connections","variant":"drogon",'
            '"note":"still not COMPLETE yet"}\n')
        with self.assertRaises(ValueError):
            read_measurements(path)

    def test_marker_in_the_middle_does_not_count(self):
        # Маркер обязан стоять В КОНЦЕ. Строки, дописанные после него, —
        # это прогон, продолжившийся после объявленного завершения, и
        # считать такой файл полным нельзя.
        path = self._path(
            '{"kind":"connections","variant":"drogon","backends":8}\n'
            "COMPLETE\n"
            '{"kind":"connections","variant":"crow","backends":1}\n')
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
    """read_fixture: то же самое, но со строками исхода."""

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

    def test_good_fixture_reads(self):
        path = self._path(
            '{"kind":"block","variant":"beast","probe":"healthz",'
            '"outcome":"served","detail":"200"}\n'
            "COMPLETE\n")
        rows = read_fixture(path)
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["outcome"], "served")

    def test_row_without_outcome_is_rejected(self):
        # Обратная сторона разделения двух функций: строка величины не
        # должна проезжать через read_fixture, иначе исход перестанет быть
        # обязательным там, где он есть.
        path = self._path(
            '{"kind":"connections","variant":"drogon","backends":8}\n'
            "COMPLETE\n")
        with self.assertRaises(ValueError):
            read_fixture(path)

    def test_missing_end_marker_is_rejected(self):
        path = self._path(
            '{"kind":"block","variant":"beast","probe":"healthz",'
            '"outcome":"served","detail":"200"}\n')
        with self.assertRaises(ValueError):
            read_fixture(path)

    def test_marker_inside_a_value_does_not_count(self):
        path = self._path(
            '{"kind":"block","variant":"beast","probe":"healthz",'
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
