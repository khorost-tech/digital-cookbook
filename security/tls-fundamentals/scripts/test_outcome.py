"""Контракт строки, которую печатает клиент.

Клиентов четыре, и сравнивать их можно только если они отвечают одним
протоколом. Здесь проверяется форма строки и — главное — то, что отказ
клиента и сбой самой пробы остаются РАЗНЫМИ исходами.

Почему это важно. В прошлом стенде смешение этих двух вещей однажды
выдало нашу собственную поломку за поведение проверяемой стороны:
недоступный файл схемы выглядел как «формат отказался читать». Здесь та
же ловушка ждёт на каждом шагу: клиент, который не запустился, не должен
попадать в таблицу как клиент, который не доверяет сертификату.
"""

import unittest


from scripts.outcome import parse_line


class OutcomeContract(unittest.TestCase):
    def test_accepts_wellformed_line(self):
        row = parse_line('{"kind":"chain","case":"valid","client":"openssl",'
                         '"outcome":"connected","detail":""}')
        self.assertEqual(row["case"], "valid")

    def test_rejects_missing_field(self):
        with self.assertRaises(ValueError):
            parse_line('{"kind":"chain","case":"valid","client":"openssl",'
                       '"outcome":"connected"}')

    def test_rejects_unknown_outcome(self):
        with self.assertRaises(ValueError):
            parse_line('{"kind":"chain","case":"valid","client":"openssl",'
                       '"outcome":"maybe","detail":"x"}')

    def test_refusal_must_explain_itself(self):
        """Отказ без объяснения нечем отличить от чужого отказа по другой
        причине — а вся матрица держится на том, ПОЧЕМУ клиент отказался."""
        with self.assertRaises(ValueError):
            parse_line('{"kind":"chain","case":"expired","client":"openssl",'
                       '"outcome":"rejected","detail":"  "}')

    def test_error_is_not_rejection(self):
        """Сбой пробы и отказ клиента — разные исходы, и смешивать их
        нельзя: первое говорит о нас, второе о клиенте."""
        broken = parse_line('{"kind":"chain","case":"valid","client":"go",'
                            '"outcome":"error","detail":"сервер не поднялся"}')
        refused = parse_line('{"kind":"chain","case":"expired","client":"go",'
                             '"outcome":"rejected","detail":"certificate has expired"}')
        self.assertNotEqual(broken["outcome"], refused["outcome"])


if __name__ == "__main__":
    unittest.main()
