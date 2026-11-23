"""Проверки оси 2 (задача 3 плана 30.09)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location(
    "analyze_bench", os.path.join(HERE, "scripts", "analyze-bench.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)

HEAD = "0000000000001280 <body_x(benchmark::State&)>:\n"
# Цикл итераций 0x1290..0x12b0, внутри — цикл работы 0x1298..0x12a4 с madd.
NESTED = HEAD + (" 1290:\tadd\tx1, x1, #0x1\n"
                 " 1298:\tmadd\tw2, w3, w3, w2\n"
                 " 12a4:\tb.ne\t1298 <body_x(benchmark::State&)+0x18>\n"
                 " 12b0:\tb.ne\t1290 <body_x(benchmark::State&)+0x10>\n")
# Формула вместо цикла: умножение внутри единственного цикла итераций.
FORMULA = HEAD + (" 1400:\tumull\tx13, w12, w13\n"
                  " 1444:\tb.ne\t1400 <body_x(benchmark::State&)+0x40>\n")


class Bench(unittest.TestCase):
    def test_O0_keeps_work_everywhere(self):
        for row in analyze.rows_for(analyze.read(), opt="O0"):
            if row["writing"] != "empty":
                self.assertTrue(row["work_present"], row["probe"])

    def test_empty_function_is_always_empty(self):
        for row in analyze.rows_for(analyze.read(), writing="empty"):
            self.assertFalse(row["work_present"], row["probe"])

    def test_missing_symbol_is_error_not_empty(self):
        item = analyze.judge(disassembly="", symbol="body_naive")
        self.assertEqual(item["outcome"], "error")

    def test_nested_loop_with_multiply_is_kept(self):
        self.assertEqual(analyze.judge(NESTED, "body_x")["outcome"], "kept")

    def test_formula_is_replaced_not_kept(self):
        # Умножение есть, но цикла по нему нет: работа заменена формулой.
        self.assertEqual(analyze.judge(FORMULA, "body_x")["outcome"], "replaced")

    def test_every_writing_compiler_and_level_present(self):
        rows = analyze.read()
        self.assertEqual(len(rows), 5 * 2 * 3)


if __name__ == "__main__":
    unittest.main()
