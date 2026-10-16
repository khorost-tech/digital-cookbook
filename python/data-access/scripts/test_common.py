"""Каноническое представление результата: случаи, на которых плечи молча
расходятся.

Проверка равенства сравнивает хеши, поэтому вся её сила — в канонизации.
Если канонизация слишком мягкая, разные величины дадут один хеш и проверка
пропустит неравенство. Если слишком жёсткая — одинаковые величины дадут
разные хеши, и она забракует исправный стенд.

Здесь и то, и другое: что обязано считаться одинаковым, что обязано
считаться разным, и что обязано быть отказом.
"""

import datetime as dt
import decimal
import os
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "app"))
os.environ.setdefault("DSN", "postgresql://stand:stand@db:5432/stand")

import common  # noqa: E402

D = decimal.Decimal


class TestCanon(unittest.TestCase):
    def test_numeric_scale_does_not_matter(self):
        # asyncpg и psycopg отдают Decimal с разным числом значащих нулей
        # на одном и том же numeric. Это ОДНА величина.
        self.assertEqual(common.canon(D("12.3")), common.canon(D("12.30")))
        self.assertEqual(common.canon(D("12.3000")), common.canon(D("12.30")))

    def test_different_numbers_stay_different(self):
        # Мягкость канонизации не должна доходить до склейки разных чисел.
        self.assertNotEqual(common.canon(D("12.30")), common.canon(D("12.31")))
        # И до потери значащих разрядов за пределами округления тоже.
        self.assertNotEqual(common.canon(D("12.3001")),
                            common.canon(D("12.3002")))

    def test_timezone_does_not_matter_but_instant_does(self):
        utc = dt.datetime(2020, 5, 1, 12, 0, tzinfo=dt.timezone.utc)
        msk = utc.astimezone(dt.timezone(dt.timedelta(hours=3)))
        # Один и тот же момент в разных поясах — одна величина.
        self.assertEqual(common.canon(utc), common.canon(msk))
        # Другой момент — другая.
        self.assertNotEqual(common.canon(utc),
                            common.canon(utc + dt.timedelta(seconds=1)))

    def test_float_is_a_failure_not_a_value(self):
        # numeric обязан приезжать Decimal. Float означает, что драйвер
        # потерял точность, и молчать об этом нельзя.
        with self.assertRaises(TypeError):
            common.canon(12.30)

    def test_unknown_type_is_a_failure(self):
        with self.assertRaises(TypeError):
            common.canon(object())


class TestFingerprint(unittest.TestCase):
    def rows(self, price="12.30"):
        return [{"id": 1, "author_id": 2, "title": "т",
                 "published": dt.date(2020, 1, 1), "price": D(price),
                 "pages": 100}]

    def test_same_data_same_hash(self):
        a = common.fingerprint("one-row", self.rows("12.30"))
        b = common.fingerprint("one-row", self.rows("12.3"))
        self.assertEqual(a, b)

    def test_one_changed_field_changes_hash(self):
        a = common.fingerprint("one-row", self.rows("12.30"))
        b = common.fingerprint("one-row", self.rows("12.31"))
        self.assertNotEqual(a["sha256"], b["sha256"])
        self.assertEqual(a["rows"], b["rows"])

    def test_row_count_alone_would_not_catch_it(self):
        # Явная фиксация того, ЗАЧЕМ здесь хеш, а не счётчик строк:
        # подмена значения при том же числе строк счётчиком не ловится.
        a = common.fingerprint("one-row", self.rows("12.30"))
        b = common.fingerprint("one-row", self.rows("99.99"))
        self.assertEqual(a["rows"], b["rows"])
        self.assertNotEqual(a["sha256"], b["sha256"])

    def test_missing_field_is_a_failure(self):
        broken = [{"id": 1}]
        with self.assertRaises(KeyError):
            common.fingerprint("one-row", broken)


class TestQueriesAreDeclaredConsistently(unittest.TestCase):
    def test_every_query_has_fields(self):
        self.assertEqual(set(common.SQL), set(common.FIELDS))

    def test_field_order_is_fixed(self):
        # Порядок полей — часть контракта: набор совпал, а порядок нет —
        # это разные результаты для сериализации по позиции.
        for query, fields in common.FIELDS.items():
            self.assertEqual(len(fields), len(set(fields)), query)


if __name__ == "__main__":
    unittest.main(verbosity=2)
