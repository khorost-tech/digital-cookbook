"""Переворот ответа должен объясняться отзывом, а не включённой проверкой.

Проба 2×2. Ключевая клетка — НЕотозванный сертификат со списком: если бы
он тоже получал отказ, вывод «клиент увидел отзыв» был бы ложным, а
таблица выглядела бы осмысленной.

Это не гипотетика. Первая попытка передать список падала на неизвестной
опции: openssl выходил с ненулевым кодом, и отказ получали обе строки.
Различие «отозван / не отозван» тогда не мерилось вовсе.
"""

import os
import unittest

from scripts.outcome import read_fixture

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures/revocation.jsonl")


def by_case():
    return {r["case"]: r for r in read_fixture(FIXTURE)}


class Revocation(unittest.TestCase):
    def test_fixture_has_all_four_cells(self):
        self.assertEqual(
            sorted(by_case()),
            ["revoked", "revoked_with_crl", "valid", "valid_with_crl"])

    def test_every_row_is_the_revocation_probe(self):
        # Вид пробы раньше приходил из окружения и до клиента не доходил:
        # строки получали вид по умолчанию и смешивались бы с матрицей.
        for row in read_fixture(FIXTURE):
            self.assertEqual(row["kind"], "revocation", row)

    def test_revoked_connects_when_state_is_not_given(self):
        # Отзыв не живёт в сертификате. Не дав клиенту состояние, спросить
        # его не о чем.
        self.assertEqual(by_case()["revoked"]["outcome"], "connected")

    def test_revoked_is_refused_when_state_is_given(self):
        row = by_case()["revoked_with_crl"]
        self.assertEqual(row["outcome"], "rejected")
        self.assertIn("revoked", row["detail"].lower(),
                      "отказ есть, но не про отзыв: %r" % row["detail"])

    def test_control_still_connects_with_the_same_list(self):
        """Без этой клетки вывод был бы ложным."""
        self.assertEqual(
            by_case()["valid_with_crl"]["outcome"], "connected",
            "неотозванный сертификат тоже отвергнут: значит переворот "
            "объясняется не отзывом, а самой проверкой")


if __name__ == "__main__":
    unittest.main()
