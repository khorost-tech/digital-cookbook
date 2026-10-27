"""Проверки осей стенда dimensional."""

import importlib.util
import json
import os
import subprocess
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze", os.path.join(HERE, "scripts", "analyze.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


def by(axis):
    return {r["variant"]: r for r in analyze.read(axis)}


class Controls(unittest.TestCase):
    def test_controls(self):
        for axis in analyze.CONTROLS:
            analyze.check(axis, analyze.read(axis))

    def test_broken_control_is_refused(self):
        rows = [dict(r, outcome="mismatch") if r["variant"] == "asof-scd2" else r for r in analyze.read("scd")]
        with self.assertRaises(SystemExit):
            analyze.check("scd", rows)

    def test_unbalanced_keys_are_refused(self):
        rows = [dict(r, missing=0) if r["variant"] == "late-inner" else r for r in analyze.read("keys")]
        with self.assertRaises(SystemExit):
            analyze.check("keys", rows)


class Scd(unittest.TestCase):
    def setUp(self):
        self.d = by("scd")

    def test_overwrite_misattributes_history(self):
        self.assertGreater(self.d["asof-scd1"]["misattributed"], 0)
        self.assertEqual(self.d["asof-scd1"]["misattributed"], self.d["asof-scd3"]["misattributed"])

    def test_totals_hide_opposite_moves(self):
        # итоги по регионам гасят встречные переносы: по заказам ошибка больше
        for v in ("asof-scd1", "asof-scd3"):
            self.assertGreater(self.d[v]["wrong_amount"], self.d[v]["misattributed"], v)
        self.assertEqual((self.d["asof-scd2"]["wrong_amount"], self.d["asof-scd2"]["wrong_orders"]), (0, 0))
        for v in ("current-scd1", "current-scd2"):
            self.assertEqual(self.d[v]["wrong_amount"], self.d[v]["misattributed"], v)

    def test_business_key_merges_recycled_id(self):
        # по бизнес-ключу и SCD1, и SCD2 приписывают прежнего владельца id 77 новому
        self.assertEqual(self.d["current-scd1"]["misattributed"], self.d["current-scd2"]["misattributed"])
        self.assertGreater(self.d["current-scd1"]["misattributed"], 0)

    def test_scd3_keeps_one_step(self):
        f = self.d["first-scd3"]
        self.assertEqual((f["wrong_once"], f["wrong_twice"]), (0, f["moved_twice"]))


class Grain(unittest.TestCase):
    def test_fanout(self):
        d = by("grain")
        self.assertGreater(d["category-order"]["ratio"], d["orders-count"]["ratio"])
        self.assertGreater(d["orders-count"]["ratio"], 1)


class Additivity(unittest.TestCase):
    def test_semi_and_non_additive(self):
        d = by("additivity")
        self.assertGreater(d["stock-sum"]["ratio"], 6)
        self.assertNotEqual(d["cancel-avg"]["got"], d["cancel-avg"]["want"])


class Keys(unittest.TestCase):
    def test_late_member(self):
        d = by("keys")
        self.assertGreater(d["late-inner"]["missing"], 0)
        self.assertEqual(d["late-unknown"]["late_regions"], ["unknown"])

    def test_recycled(self):
        d = by("keys")["recycled-scd1"]
        self.assertEqual(d["got_orders"], d["want_orders"] + d["old_person_orders"])


if __name__ == "__main__":
    unittest.main()
