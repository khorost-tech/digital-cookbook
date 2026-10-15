"""Простой считается по паузам внутри соединения. Здесь — случаи, которые
роняют обе версии счёта: та, что считала «окно минус занятость», принимала
хвост слива за заминку и отбраковала 22 годных круга подряд.
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                "..", "bench"))

from load import idle_gaps  # noqa: E402


def lane(pairs):
    """(начало, длительность) → запись нагрузчика."""
    return [(t0, dur, True, b"") for t0, dur in pairs]


class TestIdleGaps(unittest.TestCase):
    def test_dense_lane_has_no_idle(self):
        gaps, spans = idle_gaps([lane([(0.0, 1.0), (1.0, 1.0), (2.0, 1.0)])])
        self.assertAlmostEqual(gaps, 0.0)
        self.assertAlmostEqual(spans, 3.0)

    def test_drain_tail_is_not_idle(self):
        # Одно соединение закончило раньше другого — так выглядит конец
        # пачки. Простоя здесь нет: незанятого времени МЕЖДУ запросами не
        # было ни у кого.
        gaps, spans = idle_gaps([
            lane([(0.0, 1.0), (1.0, 1.0), (2.0, 1.0)]),
            lane([(0.0, 1.0)]),
        ])
        self.assertAlmostEqual(gaps, 0.0)

    def test_stall_in_the_middle_is_counted(self):
        # Две секунды соединение не слало ничего — это и есть заминка.
        gaps, spans = idle_gaps([lane([(0.0, 1.0), (3.0, 1.0)])])
        self.assertAlmostEqual(gaps, 2.0)
        self.assertAlmostEqual(gaps / spans, 0.5)

    def test_single_request_lane_contributes_nothing(self):
        # Из одного запроса пауз не построить — ни числителя, ни знаменателя.
        self.assertEqual(idle_gaps([lane([(0.0, 1.0)])]), (0.0, 0.0))

    def test_out_of_order_records_do_not_invent_gaps(self):
        # Записи в списке могут идти не по времени; сортировка обязана быть,
        # иначе отрицательные разности дали бы нулевой простой на ровном месте.
        gaps, _ = idle_gaps([lane([(3.0, 1.0), (0.0, 1.0)])])
        self.assertAlmostEqual(gaps, 2.0)


if __name__ == "__main__":
    unittest.main(verbosity=2)
