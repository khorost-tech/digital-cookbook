"""Имя обязано быть ПРОЧИТАНО из байтов, а не переписано из аргументов.

Самый вероятный самообман этой оси — напечатать то, что мы сами передали
клиенту, и назвать это наблюдением. Защита двойная: разбор возвращает
смещение, а проверка отдельно смотрит, что лежит в байтах по этому
смещению.

Проверку мало написать — её надо уронить. Здесь она роняется трижды:
порчей байтов имени, сдвигом смещения и подменой ожидаемого имени. Если
хоть одна диверсия проходит незамеченной, проверка ничего не стоит.
"""

import io
import json
import os
import unittest

from scripts.wire_parse import (WireError, name_at_offset, parse_client_hello)

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def rows():
    with io.open(os.path.join(HERE, "fixtures/wire.jsonl"),
                 encoding="utf-8") as handle:
        text = handle.read()
    if "COMPLETE" not in text:
        raise AssertionError("фикстура оборвана")
    return [json.loads(l) for l in text.split("\n") if l.strip().startswith("{")]


class NameComesFromBytes(unittest.TestCase):
    def test_every_client_has_an_offset(self):
        for row in rows():
            self.assertGreater(
                row["server_name_at"], 0,
                "%s: смещение не найдено — значит имя взято не из байтов"
                % row["client"])

    def test_bytes_at_the_offset_really_spell_the_name(self):
        for row in rows():
            data = bytes.fromhex(row["hex"])
            actual = name_at_offset(data, row["server_name_at"],
                                    len(row["asked_name"]))
            self.assertEqual(actual, row["asked_name"],
                             "%s: по смещению %d лежит не то имя"
                             % (row["client"], row["server_name_at"]))

    def test_each_client_asked_for_its_own_name(self):
        # Общее имя допускало бы случайное совпадение: разбор мог бы
        # вернуть чужое и остаться незамеченным.
        names = [r["asked_name"] for r in rows()]
        self.assertEqual(len(names), len(set(names)))

    def test_parse_finds_the_same_name_independently(self):
        for row in rows():
            data = bytes.fromhex(row["hex"])
            self.assertEqual(parse_client_hello(data)["server_name"],
                             row["asked_name"], row["client"])


class Diversions(unittest.TestCase):
    """Проверка обязана краснеть на подделке."""

    def _first(self):
        row = rows()[0]
        return row, bytearray(bytes.fromhex(row["hex"]))

    def test_corrupted_name_bytes_are_noticed(self):
        row, data = self._first()
        at = row["server_name_at"]
        data[at] = data[at] ^ 0x20            # меняем одну букву имени
        spoiled = name_at_offset(bytes(data), at, len(row["asked_name"]))
        self.assertNotEqual(
            spoiled, row["asked_name"],
            "порча байтов имени осталась незамеченной — значит проверка "
            "смотрит не в байты")

    def test_shifted_offset_is_noticed(self):
        row, data = self._first()
        shifted = name_at_offset(bytes(data), row["server_name_at"] + 1,
                                 len(row["asked_name"]))
        self.assertNotEqual(shifted, row["asked_name"],
                            "имя нашлось и по сдвинутому смещению — значит "
                            "смещение ничего не подтверждает")

    def test_substituted_expected_name_is_noticed(self):
        row, data = self._first()
        actual = name_at_offset(bytes(data), row["server_name_at"],
                                len("нечто.иное"))
        self.assertNotEqual(actual, "нечто.иное")

    def test_parser_refuses_bytes_that_are_not_a_client_hello(self):
        # Разбор не должен «что-нибудь находить» в произвольных байтах.
        with self.assertRaises(WireError):
            parse_client_hello(b"\x17\x03\x03" + b"\x00" * 100)


if __name__ == "__main__":
    unittest.main()
