"""Ось взаимного рукопожатия: чем подтверждается вывод.

Главное требование плана: у КАЖДОГО отказа должна быть заполнена
серверная сторона. Без неё утверждать что-либо об асимметрии нечем —
осталась бы половина события.

Отдельно проверяется сама мера. «Диагностика клиента менее конкретна» —
суждение, которое нечем проверить. Считается другое: сколько РАЗЛИЧИМЫХ
причин видно с каждой стороны. Мера обязана падать на подставных данных,
иначе она ничего не измеряет.
"""

import importlib.util
import os
import unittest

from scripts.outcome import read_fixture

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures/mutual.jsonl")
CONTROL = "mutual_valid"


def _analyzer():
    # Имя файла с дефисом обычным импортом не берётся.
    path = os.path.join(HERE, "scripts/analyze-mutual.py")
    spec = importlib.util.spec_from_file_location("analyze_mutual", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def rows():
    return read_fixture(FIXTURE)


class MutualFixture(unittest.TestCase):
    def test_control_connects_for_everyone(self):
        for row in rows():
            if row["case"] != CONTROL:
                continue
            self.assertEqual(row["outcome"], "connected",
                             "%s не прошёл контроль: остальные его строки "
                             "недействительны" % row["client"])

    def test_every_refusal_carries_the_server_side(self):
        """Требование плана: серверная сторона заполнена у всех отказов."""
        for row in rows():
            if row["outcome"] != "rejected":
                continue
            self.assertTrue(
                str(row.get("server_saw", "")).strip(),
                "%s/%s: отказ есть, серверной стороны нет — об асимметрии "
                "утверждать нечем" % (row["case"], row["client"]))

    def test_refusal_happens_after_the_handshake(self):
        """Отказ по клиентскому сертификату приходит ПОСЛЕ рукопожатия.

        Это отличает его от отказа в самом рукопожатии. Проба, которая не
        читает после рукопожатия, такого события просто не увидит — и
        объявит успех там, где соединение мертво. Один раз так и вышло.
        """
        seen = 0
        for row in rows():
            if row["case"] == CONTROL or row["outcome"] != "rejected":
                continue
            if row.get("handshake_ok") is True:
                seen += 1
        self.assertGreater(seen, 0,
                           "ни один отказ не отмечен как пришедший после "
                           "рукопожатия — значит пробы не дочитывают")


class Measure(unittest.TestCase):
    """Мера различимости обязана падать на подставных данных."""

    def test_identical_messages_count_as_one_cause(self):
        normalize = _analyzer().normalize
        same = {normalize("remote error: tls: certificate required"),
                normalize("remote error: tls: certificate required (42)")}
        self.assertEqual(len(same), 1,
                         "два одинаковых по сути сообщения посчитаны как "
                         "две различимые причины")

    def test_different_messages_stay_different(self):
        normalize = _analyzer().normalize
        pair = {normalize("remote error: tls: certificate required"),
                normalize("remote error: tls: expired certificate")}
        self.assertEqual(len(pair), 2,
                         "мера склеила разные причины — тогда она покажет "
                         "меньше различимого, чем есть на самом деле")


if __name__ == "__main__":
    unittest.main()
