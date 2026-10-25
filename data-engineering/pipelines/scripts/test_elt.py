"""Проверки оси 1."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_elt", os.path.join(HERE, "scripts", "analyze-elt.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Elt(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()
        self.by = {r["variant"]: r for r in self.rows}

    def test_controls(self):
        analyze.check(self.rows)

    def test_rule_change_cost(self):
        # ELT пересчитывает историю без источника, ETL выгружает каждый день заново
        self.assertEqual(self.by["elt/rule-change"]["source_queries"], 0)
        self.assertEqual(self.by["etl/rule-change"]["source_queries"], 2 * self.by["etl/rule-change"]["days"])
        for v in ("elt/rule-change", "etl/rule-change", "etl-facts/rule-change"):
            self.assertEqual(self.by[v]["outcome"], "match", v)

    def test_detail_not_place_decides(self):
        # ETL с подробными фактами тоже пересчитывает без источника: дело в
        # сохранённой детализации, а не в том, где идёт преобразование
        self.assertEqual(self.by["etl-facts/rule-change"]["source_queries"], 0)
        self.assertEqual(self.by["etl-facts/initial"]["source_queries"],
                         self.by["etl/initial"]["source_queries"])

    def test_day_count_mismatch_is_refused(self):
        # совпавших дней и расхождений вместе меньше, чем дней — счёт потерял день
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, matched_days=r["matched_days"] - 1) if r["variant"] == "elt/rule-change" else r
                           for r in self.rows])

    def test_broken_control_is_refused(self):
        with self.assertRaises(SystemExit):
            analyze.check([dict(r, outcome="mismatch") if r["variant"] == "elt/initial" else r for r in self.rows])


if __name__ == "__main__":
    unittest.main()
