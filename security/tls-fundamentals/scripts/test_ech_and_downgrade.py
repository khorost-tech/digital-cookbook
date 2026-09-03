"""Поддержка шифрованного приветствия и метка о понижении версии.

Обе величины легко подделать не злым умыслом, а невнимательностью:
поддержку — посчитав по наличию флага, метку — объявив её по названию
случая, а не по байтам. Поэтому:

  поддержка обязана следовать из ДОКАЗАТЕЛЬСТВА, записанного рядом;
  метка обязана быть теми самыми восемью байтами, а не признаком True.

У curl этот вопрос не умозрительный: флаг --ech в справке есть, а
библиотека его не поддерживает. Счёт по флагам завысил бы поддержку
ровно на одного клиента из четырёх.
"""

import io
import json
import os
import unittest

from scripts.wire_parse import (DOWNGRADE_TO_12, WireError, parse_server_hello)

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def load(name):
    with io.open(os.path.join(HERE, "fixtures", name), encoding="utf-8") as h:
        text = h.read()
    if "COMPLETE" not in text:
        raise AssertionError("%s: фикстура оборвана" % name)
    return [json.loads(l) for l in text.split("\n") if l.strip().startswith("{")]


class EchSupport(unittest.TestCase):
    def test_all_four_clients_checked(self):
        self.assertEqual(sorted(r["client"] for r in load("ech.jsonl")),
                         ["curl", "go", "java", "openssl"])

    def test_every_verdict_names_how_it_was_obtained(self):
        for row in load("ech.jsonl"):
            self.assertTrue(row["how"].strip(),
                            "%s: не сказано, как получен вывод" % row["client"])
            self.assertTrue(str(row["evidence"]).strip(),
                            "%s: вывод без доказательства" % row["client"])

    def test_a_present_flag_alone_is_not_support(self):
        """Главная защита от завышения счёта.

        У curl флаг есть, а возможности нет. Если вывод «умеет» уживётся
        с доказательством «библиотека этого не поддерживает», значит счёт
        ведётся по флагам, и он завышен.
        """
        for row in load("ech.jsonl"):
            if "does not support" in str(row["evidence"]).lower():
                self.assertFalse(
                    row["supported"],
                    "%s объявлен умеющим, а доказательство говорит "
                    "обратное" % row["client"])

    def test_support_and_evidence_do_not_contradict(self):
        for row in load("ech.jsonl"):
            if row["supported"]:
                self.assertNotIn("нет", str(row["evidence"]).lower()[:10],
                                 "%s: объявлен умеющим при пустом "
                                 "доказательстве" % row["client"])


class DowngradeMark(unittest.TestCase):
    def test_pair_differs_in_one_thing(self):
        rows = {r["case"]: r for r in load("downgrade.jsonl")}
        self.assertEqual(sorted(rows), ["negotiated_12", "negotiated_13"])
        # Сервер один и тот же в обеих пробах: понижать его потолок было
        # бы ошибкой — сервер, не умеющий новой версии, метку и не кладёт.
        self.assertEqual(rows["negotiated_13"]["client_max_version"],
                         "без ограничения")
        self.assertEqual(rows["negotiated_12"]["client_max_version"], "1.2")

    def test_mark_is_those_exact_bytes(self):
        rows = {r["case"]: r for r in load("downgrade.jsonl")}
        self.assertEqual(rows["negotiated_12"]["downgrade_tail"],
                         DOWNGRADE_TO_12.hex(),
                         "метка объявлена, но байты другие")
        self.assertNotEqual(rows["negotiated_13"]["downgrade_tail"],
                            DOWNGRADE_TO_12.hex(),
                            "метка нашлась там, где понижения не было")

    def test_the_pair_really_negotiated_different_versions(self):
        """Иначе пара не про понижение, а про два одинаковых соединения.

        Версия в приветствии сервера в обоих случаях говорит 1.2 — это
        поле в новой версии протокола всегда говорит одно и то же.
        Настоящая версия лежит в расширении, и брать надо её.
        """
        rows = {r["case"]: r for r in load("downgrade.jsonl")}
        self.assertEqual(rows["negotiated_13"]["negotiated_version"], "TLS 1.3")
        self.assertEqual(rows["negotiated_12"]["negotiated_version"], "TLS 1.2")
        self.assertEqual(rows["negotiated_13"]["server_hello_version"],
                         rows["negotiated_12"]["server_hello_version"],
                         "поле версии в приветствии различается — значит "
                         "оно всё-таки что-то говорит, и вывод надо строить "
                         "заново")

    def test_mark_is_read_from_the_dump_again(self):
        # Не верим полю: перечитываем байты и разбираем заново.
        for row in load("downgrade.jsonl"):
            parsed = parse_server_hello(bytes.fromhex(row["hex"]))
            self.assertEqual(parsed["downgrade_to_12"], row["downgrade_mark"],
                             "%s: поле и байты расходятся" % row["case"])
            self.assertEqual(parsed["random"], row["random"], row["case"])


class Diversions(unittest.TestCase):
    def test_spoiled_mark_byte_is_noticed(self):
        rows = {r["case"]: r for r in load("downgrade.jsonl")}
        data = bytearray(bytes.fromhex(rows["negotiated_12"]["hex"]))
        parsed = parse_server_hello(bytes(data))
        # Портим последний байт случайного поля — там и лежит метка.
        last = parsed["random_at"] + 31
        data[last] = data[last] ^ 0xFF
        self.assertFalse(parse_server_hello(bytes(data))["downgrade_to_12"],
                         "метка уцелела после порчи своего же байта — "
                         "значит она берётся не из байтов")

    def test_parser_refuses_a_client_hello_as_a_server_hello(self):
        rows = load("wire.jsonl")
        with self.assertRaises(WireError):
            parse_server_hello(bytes.fromhex(rows[0]["hex"]))


if __name__ == "__main__":
    unittest.main()
