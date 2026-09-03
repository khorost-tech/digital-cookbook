"""Виден ли сертификат сервера снаружи — это ЗАМЕР, а не пересказ RFC.

Утверждение «в новой версии сертификат зашифрован, в старой нет» можно
взять из описания протокола. Но посредник уже сохраняет нужные байты,
и проверить дешевле, чем пересказать. Проверка идёт по структуре записей,
а не поиском подстроки: зашифрованная запись тоже может случайно
содержать что угодно.

Ноль байт означает не «сертификата нет», а «снаружи его не прочитать» —
разница существенная, и путать её нельзя.
"""

import io
import json
import os
import unittest

from scripts.wire_parse import (parse_records, plaintext_certificate)

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def rows():
    with io.open(os.path.join(HERE, "fixtures/downgrade.jsonl"),
                 encoding="utf-8") as handle:
        text = handle.read()
    if "COMPLETE" not in text:
        raise AssertionError("фикстура оборвана")
    return {json.loads(l)["case"]: json.loads(l)
            for l in text.split("\n") if l.strip().startswith("{")}


class Visibility(unittest.TestCase):
    def test_old_version_shows_the_certificate_in_the_clear(self):
        row = rows()["negotiated_12"]
        self.assertGreater(
            row["plaintext_certificate_bytes"], 0,
            "в старой версии сертификат обязан читаться прямо с провода")

    def test_new_version_hides_it(self):
        row = rows()["negotiated_13"]
        self.assertEqual(
            row["plaintext_certificate_bytes"], 0,
            "в новой версии сертификат не должен читаться снаружи")

    def test_the_pair_differs_only_in_the_negotiated_version(self):
        data = rows()
        self.assertEqual(data["negotiated_12"]["negotiated_version"], "TLS 1.2")
        self.assertEqual(data["negotiated_13"]["negotiated_version"], "TLS 1.3")

    def test_read_again_from_the_bytes(self):
        # Не верим полю: перечитываем дамп и разбираем заново.
        for case, row in rows().items():
            data = bytes.fromhex(row["hex"])
            again = plaintext_certificate(parse_records(data))
            self.assertEqual(again, row["plaintext_certificate_bytes"], case)

    def test_hidden_does_not_mean_absent(self):
        """В новой версии сертификат есть — он внутри зашифрованной записи.

        Без этой проверки вывод «сертификата не видно» мог бы означать
        «сертификат не прислали», а это совсем другое утверждение.
        """
        row = rows()["negotiated_13"]
        encrypted = [r for r in row["records"]
                     if r["type_name"] == "прикладные данные"]
        self.assertTrue(encrypted, "зашифрованных записей нет вовсе")
        self.assertGreater(
            max(r["length"] for r in encrypted), 1000,
            "нет записи, в которую поместился бы сертификат — значит его "
            "и правда не прислали, и вывод надо строить заново")


class Diversions(unittest.TestCase):
    def test_encrypted_record_is_not_mistaken_for_a_certificate(self):
        # Запись с прикладными данными не должна разбираться как
        # рукопожатие, что бы в ней ни лежало.
        body = bytes([11, 0, 6, 160]) + b"\x00" * 1696
        record = bytes([23, 3, 3, (len(body) >> 8) & 0xFF, len(body) & 0xFF])
        self.assertEqual(plaintext_certificate(parse_records(record + body)), 0,
                         "содержимое зашифрованной записи принято за "
                         "сертификат")

    def test_truncated_record_does_not_invent_a_message(self):
        record = bytes([22, 3, 3, 0x06, 0xA1]) + bytes([11, 0, 6, 160])
        self.assertEqual(plaintext_certificate(parse_records(record)), 0,
                         "оборванная запись дала сообщение, которого нет")


if __name__ == "__main__":
    unittest.main()
